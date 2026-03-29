// 遵循产品需求 v1.0
package services

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ErrBillNotDraft is returned when posting is attempted on a non-draft bill.
var ErrBillNotDraft = errors.New("only draft bills can be posted")

// ErrNoAPAccount is returned when no active Accounts Payable account exists for the company.
var ErrNoAPAccount = errors.New("no active Accounts Payable account found — create one in your Chart of Accounts first")

// PostBill transitions a draft bill to "posted" and generates a double-entry
// journal entry in a single database transaction.
//
// Accounting entries produced per line:
//
//	Dr  Expense / Inventory account   lineNet + nonRecoverableTax   (cost of purchase)
//	Dr  Recoverable Tax account       recoverableTax                (ITC receivable, if any)
//	Cr  Accounts Payable              lineTotal                     (= lineNet + taxAmount)
//
// The AP credit equals the gross amount owed to the vendor.
// Non-recoverable tax is rolled into the expense debit because it increases the
// true cost of the purchase; recoverable tax goes to a separate asset/receivable
// account so it can later be offset against tax filings.
//
// Pre-conditions:
//   - Bill must be in "draft" status.
//   - Every line must have an ExpenseAccountID set.
//   - Tax codes on purchase lines must have Scope "purchase" or "both".
//   - An active Accounts Payable account must exist for the company.
//
// Returns ErrBillNotDraft, ErrNoAPAccount, or a descriptive error on failure.
func PostBill(db *gorm.DB, companyID, billID uint, actor string, userID *uuid.UUID) error {
	// ── Load bill with full line detail ──────────────────────────────────────
	var bill models.Bill
	err := db.
		Preload("Lines.TaxCode").
		Preload("Lines.ProductService").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&bill).Error
	if err != nil {
		return fmt.Errorf("load bill: %w", err)
	}

	// ── Pre-flight checks ─────────────────────────────────────────────────────
	if bill.Status != models.BillStatusDraft {
		return ErrBillNotDraft
	}
	if len(bill.Lines) == 0 {
		return errors.New("bill has no line items")
	}
	for i, l := range bill.Lines {
		if l.ExpenseAccountID == nil {
			return fmt.Errorf("line %d (%q): expense account is required before posting", i+1, l.Description)
		}
	}

	// ── Find AP account ───────────────────────────────────────────────────────
	var apAccount models.Account
	if err := db.
		Where("company_id = ? AND detail_account_type = ? AND is_active = true", companyID, string(models.DetailAccountsPayable)).
		Order("code asc").
		First(&apAccount).Error; err != nil {
		return ErrNoAPAccount
	}

	// ── Build posting fragments (line-level tax) then aggregate ───────────────
	var frags []PostingFragment
	apCreditTotal := decimal.Zero

	for _, l := range bill.Lines {
		lineNet := l.LineNet

		var lt LineTaxAmounts
		if l.TaxCode != nil && l.TaxCode.Scope != models.TaxScopeSales {
			lt = ComputeLineTax(lineNet, *l.TaxCode)
		}

		// Expense / asset debit = net + non-recoverable tax (embedded in cost when not recoverable).
		expenseDebit := lineNet.Add(lt.NonRecoverableTaxAmount)
		frags = append(frags, PostingFragment{
			AccountID: *l.ExpenseAccountID,
			Debit:     expenseDebit,
			Credit:    decimal.Zero,
			Memo:      l.Description,
		})

		if lt.RecoverableTaxAmount.IsPositive() && l.TaxCode != nil &&
			l.TaxCode.PurchaseRecoverableAccountID != nil {
			frags = append(frags, PostingFragment{
				AccountID: *l.TaxCode.PurchaseRecoverableAccountID,
				Debit:     lt.RecoverableTaxAmount,
				Credit:    decimal.Zero,
				Memo:      "ITC: " + l.Description,
			})
		}

		lineTotal := lineNet.Add(lt.TaxAmount)
		apCreditTotal = apCreditTotal.Add(lineTotal)
	}

	frags = append(frags, PostingFragment{
		AccountID: apAccount.ID,
		Debit:     decimal.Zero,
		Credit:    apCreditTotal,
		Memo:      "Bill " + bill.BillNumber,
	})

	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return fmt.Errorf("aggregate journal lines: %w", err)
	}

	debitSum := sumPostingDebits(jeLines)
	creditSum := sumPostingCredits(jeLines)
	if !debitSum.Equal(creditSum) || !creditSum.Equal(apCreditTotal) {
		return fmt.Errorf("journal entry imbalance: debit sum %s, credit sum %s, AP total %s — check line totals",
			debitSum.StringFixed(2), creditSum.StringFixed(2), apCreditTotal.StringFixed(2))
	}

	// ── Transaction ───────────────────────────────────────────────────────────
	return db.Transaction(func(tx *gorm.DB) error {
		je := models.JournalEntry{
			CompanyID: companyID,
			EntryDate: bill.BillDate,
			JournalNo: bill.BillNumber,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create journal entry: %w", err)
		}

		for _, jl := range jeLines {
			line := models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      jl.AccountID,
				Debit:          jl.Debit,
				Credit:         jl.Credit,
				Memo:           jl.Memo,
				PartyType:      models.PartyTypeVendor,
				PartyID:        bill.VendorID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create journal line: %w", err)
			}
		}

		// Update bill: mark posted, cache grand total, link journal entry.
		if err := tx.Model(&bill).Updates(map[string]any{
			"status":           string(models.BillStatusPosted),
			"amount":           apCreditTotal,
			"journal_entry_id": je.ID,
		}).Error; err != nil {
			return fmt.Errorf("update bill status: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "bill.posted", "bill", bill.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"bill_number":      bill.BillNumber,
				"journal_entry_id": je.ID,
				"total":            apCreditTotal.StringFixed(2),
			},
		)
	})
}
