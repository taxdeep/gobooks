// 遵循project_guide.md
package services

// fragment_builder.go — pure fragment-building functions for invoice and bill posting.
//
// These functions translate loaded business documents (with their lines, product/service
// associations, and tax codes) into a flat slice of PostingFragments. They have no
// database dependency and no side effects, making them directly unit-testable.
//
// Role in the posting pipeline:
//
//   Business document (Invoice / Bill)
//         │
//         ▼
//   BuildInvoiceFragments / BuildBillFragments   ← this file
//         │  []PostingFragment (one per line, one per tax line, one header)
//         ▼
//   AggregateJournalLines (journal_aggregate.go)
//         │  []PostingFragment (collapsed by account + side)
//         ▼
//   Journal entry + lines (DB insert, inside transaction)
//         │
//         ▼
//   ProjectToLedger (ledger.go)
//
// Relationship to existing invoice_post.go / bill_post.go:
// Those files currently embed equivalent logic inline. This file extracts the same
// logic into standalone functions so it can be shared, tested, and called by the
// PostingEngine coordinator (posting_engine.go). The existing posting functions
// are not modified in this phase; they continue to work independently.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

// ── Invoice fragments ─────────────────────────────────────────────────────────

// BuildInvoiceFragments builds the raw PostingFragments for a customer invoice.
//
// Fragment structure produced:
//
//	DR  Accounts Receivable   inv.Amount               (one line for the gross total)
//	CR  Revenue account       line.LineNet   per line   (from ProductService.RevenueAccountID)
//	CR  Sales Tax Payable     tax amount     per line   (from TaxCode.SalesTaxAccountID; omitted when no tax)
//
// Pre-conditions:
//   - inv.Lines must be preloaded with ProductService (including RevenueAccount) and TaxCode.
//   - arAccountID must be the ID of the active Accounts Receivable account for the company.
//
// This is a pure function: no database calls, no side effects.
// Call AggregateJournalLines on the result before writing journal lines.
func BuildInvoiceFragments(inv models.Invoice, arAccountID uint) ([]PostingFragment, error) {
	if arAccountID == 0 {
		return nil, errors.New("fragment builder: AR account ID is required")
	}
	if len(inv.Lines) == 0 {
		return nil, errors.New("fragment builder: invoice has no line items")
	}

	// Capacity: 1 AR debit + up to 2 credits per line (revenue + tax).
	frags := make([]PostingFragment, 0, 1+len(inv.Lines)*2)

	// Single AR debit for the invoice gross total.
	frags = append(frags, PostingFragment{
		AccountID: arAccountID,
		Debit:     inv.Amount,
		Credit:    decimal.Zero,
		Memo:      "Invoice " + inv.InvoiceNumber,
	})

	for i, l := range inv.Lines {
		lineNum := i + 1

		// Revenue credit: requires a resolved ProductService with a revenue account.
		if l.ProductService == nil {
			return nil, fmt.Errorf("fragment builder: line %d (%q) has no product/service loaded", lineNum, l.Description)
		}
		if l.ProductService.RevenueAccountID == 0 {
			return nil, fmt.Errorf("fragment builder: line %d (%q) product/service has no revenue account", lineNum, l.Description)
		}

		frags = append(frags, PostingFragment{
			AccountID: l.ProductService.RevenueAccountID,
			Debit:     decimal.Zero,
			Credit:    l.LineNet,
			Memo:      l.Description,
		})

		// Tax credit: only for tax codes scoped for sales or both.
		if l.TaxCodeID != nil && l.TaxCode != nil && l.TaxCode.Scope != models.TaxScopePurchase {
			lt := ComputeLineTax(l.LineNet, *l.TaxCode)
			if lt.TaxAmount.IsPositive() {
				posting := SalesTaxPostingLine(lt.AsTaxResult(), *l.TaxCode)
				frags = append(frags, PostingFragment{
					AccountID: posting.AccountID,
					Debit:     decimal.Zero,
					Credit:    posting.Amount,
					Memo:      "Tax on " + l.Description,
				})
			}
		}
	}

	return frags, nil
}

// ── Bill fragments ────────────────────────────────────────────────────────────

// BuildBillFragments builds the raw PostingFragments for a purchase bill.
//
// Fragment structure produced per line:
//
//	DR  Expense account        lineNet + nonRecoverableTax   (cost of purchase + embedded non-ITC tax)
//	DR  ITC Receivable         recoverableTax                (only when TaxCode has a recoverable account)
//	CR  Accounts Payable       lineTotal                     (single AP credit summed across all lines)
//
// Recovery mode handling (from TaxCode.RecoveryMode):
//   - full:    entire tax amount goes to ITC Receivable (DR); expense debit = lineNet only.
//   - partial: TaxCode.RecoveryRate % goes to ITC Receivable; remainder embedded in expense debit.
//   - none:    no ITC line; full tax amount embedded in expense debit.
//
// Pre-conditions:
//   - bill.Lines must be preloaded with TaxCode.
//   - Every line must have ExpenseAccountID set.
//   - apAccountID must be the ID of the active Accounts Payable account for the company.
//
// This is a pure function: no database calls, no side effects.
// Call AggregateJournalLines on the result before writing journal lines.
func BuildBillFragments(bill models.Bill, apAccountID uint) ([]PostingFragment, error) {
	if apAccountID == 0 {
		return nil, errors.New("fragment builder: AP account ID is required")
	}
	if len(bill.Lines) == 0 {
		return nil, errors.New("fragment builder: bill has no line items")
	}

	// Capacity: 1 AP credit + up to 2 debits per line (expense + optional ITC).
	frags := make([]PostingFragment, 0, 1+len(bill.Lines)*2)
	apCreditTotal := decimal.Zero

	for i, l := range bill.Lines {
		lineNum := i + 1

		if l.ExpenseAccountID == nil {
			return nil, fmt.Errorf("fragment builder: line %d (%q) has no expense account", lineNum, l.Description)
		}

		lineNet := l.LineNet

		// Compute tax split (recoverable vs non-recoverable).
		// Only applies when the tax code is scoped for purchases.
		var lt LineTaxAmounts
		if l.TaxCode != nil && l.TaxCode.Scope != models.TaxScopeSales {
			lt = ComputeLineTax(lineNet, *l.TaxCode)
		}

		// Expense debit = net cost + any non-recoverable tax (embedded in the cost of purchase).
		expenseDebit := lineNet.Add(lt.NonRecoverableTaxAmount)
		frags = append(frags, PostingFragment{
			AccountID: *l.ExpenseAccountID,
			Debit:     expenseDebit,
			Credit:    decimal.Zero,
			Memo:      l.Description,
		})

		// Recoverable ITC debit: only when the tax code specifies a recoverable account.
		if lt.RecoverableTaxAmount.IsPositive() &&
			l.TaxCode != nil &&
			l.TaxCode.PurchaseRecoverableAccountID != nil {
			frags = append(frags, PostingFragment{
				AccountID: *l.TaxCode.PurchaseRecoverableAccountID,
				Debit:     lt.RecoverableTaxAmount,
				Credit:    decimal.Zero,
				Memo:      "ITC: " + l.Description,
			})
		}

		// AP credit grows by the full line total (net + tax).
		lineTotal := lineNet.Add(lt.TaxAmount)
		apCreditTotal = apCreditTotal.Add(lineTotal)
	}

	// Single aggregated AP credit for the vendor's total payable.
	frags = append(frags, PostingFragment{
		AccountID: apAccountID,
		Debit:     decimal.Zero,
		Credit:    apCreditTotal,
		Memo:      "Bill " + bill.BillNumber,
	})

	return frags, nil
}

// applyFXScaling converts fragment amounts from document currency to base currency.
// exchangeRate is "how many base units per 1 document-currency unit" (e.g. 1.37 for USD→CAD).
// The anchor line (AR for invoices, AP for bills) absorbs any rounding residual so that
// ΣDebit == ΣCredit after scaling.
//
// anchorIsDebit == true  → anchor line is on the debit side (invoice AR)
// anchorIsDebit == false → anchor line is on the credit side (bill AP)
//
// Returns frags unmodified when exchangeRate == 1 (base-currency document).
func applyFXScaling(frags []PostingFragment, exchangeRate decimal.Decimal, anchorAccountID uint, anchorIsDebit bool) []PostingFragment {
	if exchangeRate.Equal(decimal.NewFromInt(1)) {
		return frags
	}
	otherSum := decimal.Zero
	for i := range frags {
		if frags[i].AccountID == anchorAccountID {
			continue
		}
		frags[i].Debit = frags[i].Debit.Mul(exchangeRate).Round(2)
		frags[i].Credit = frags[i].Credit.Mul(exchangeRate).Round(2)
		if anchorIsDebit {
			otherSum = otherSum.Add(frags[i].Credit)
		} else {
			otherSum = otherSum.Add(frags[i].Debit)
		}
	}
	for i := range frags {
		if frags[i].AccountID == anchorAccountID {
			if anchorIsDebit {
				frags[i].Debit = otherSum
			} else {
				frags[i].Credit = otherSum
			}
			break
		}
	}
	return frags
}
