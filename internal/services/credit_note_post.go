// 遵循project_guide.md
package services

// credit_note_post.go — PostCreditNote: posting pipeline for customer credit notes.
//
// Posting pipeline:
//
//  1. Load credit note + lines (RevenueAccount + TaxCode preloaded)
//  2. Pre-flight validation
//  3. Resolve Accounts Receivable account
//  4. BuildCreditNoteFragments → raw []PostingFragment (fragment_builder.go)
//  5. AggregateJournalLines   → collapse by account + side
//  6. Validate double-entry balance
//  7. Transaction:
//       a. Lock credit note row; re-validate status
//       b. INSERT journal_entries header (SourceType=credit_note, SourceID=cn.ID)
//       c. INSERT journal_lines
//       d. WriteSecondaryBookAmounts
//       e. ProjectToLedger
//       f. UPDATE credit_notes → status='issued', amount, balance_remaining, journal_entry_id
//       g. UPDATE invoices → decrement balance_due (if InvoiceID set)
//       h. WriteAuditLog
//
// Journal shape — example credit note $100 net, 5% GST:
//
//   Line 1: Widget A  $100.00 net, GST $5.00 → revenue account 4000
//
//   After aggregation:
//     DR  4000 Revenue        100.00
//     DR  2300 GST Payable      5.00
//     CR  1100 AR             105.00

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ErrCreditNoteNotDraft is returned when posting is attempted on a non-draft credit note.
var ErrCreditNoteNotDraft = errors.New("only draft credit notes can be posted")

// IN.5 sentinels for Rule #4 on the Credit Note path.
var (
	// ErrCreditNoteStockItemRequiresInvoice — a stock-item line on a
	// credit note requires the header to link back to the original
	// invoice. Standalone (InvoiceID=nil) credit notes cannot form a
	// stock return — there is no originating inventory movement to
	// reverse against the authoritative snapshot cost.
	ErrCreditNoteStockItemRequiresInvoice = errors.New(
		"credit note: stock-item line requires the credit note to link to an originating invoice — cannot reverse inventory without a source")

	// ErrCreditNoteStockItemRequiresOriginalLine — the line carries a
	// stock item but does not point at a specific InvoiceLine it is
	// reversing. Operator must pick which invoice line this return
	// applies to so authoritative cost is traceable.
	ErrCreditNoteStockItemRequiresOriginalLine = errors.New(
		"credit note: stock-item line requires original_invoice_line_id — pick which invoice line this return applies to")

	// ErrCreditNoteStockItemRequiresReturnReceipt — Rule #4 Q2 for
	// the AR side. Under shipment_required=true (controlled mode),
	// the Credit Note path is NOT the outbound-return owner;
	// Phase I.6 Return Receipt is the intended owner. Until I.6
	// ships, stock-item credit notes on controlled-mode companies
	// fail loud rather than silently book partial (revenue-only)
	// reversal.
	ErrCreditNoteStockItemRequiresReturnReceipt = errors.New(
		"credit note: stock-item line not allowed when shipment_required=true — route outbound returns through a Return Receipt instead (Phase I.6)")

	// ErrCreditNoteOriginalLineMismatch — the OriginalInvoiceLineID
	// on a credit-note line points at an invoice line that doesn't
	// belong to this credit note's linked Invoice, or at a line
	// whose product differs from the credit note's product, or at
	// an invoice that isn't in this company.
	ErrCreditNoteOriginalLineMismatch = errors.New(
		"credit note: original_invoice_line_id does not match a valid sale line on the linked invoice")
)

// PostCreditNote transitions a draft credit note to "issued" and generates a
// double-entry journal entry in a single database transaction.
//
// Returns ErrCreditNoteNotDraft, ErrNoARAccount, or a descriptive error on failure.
func PostCreditNote(db *gorm.DB, companyID, creditNoteID uint, actor string, userID *uuid.UUID) error {
	// ── 1. Load credit note with full line detail ─────────────────────────────
	var cn models.CreditNote
	if err := db.
		Preload("Lines.RevenueAccount").
		Preload("Lines.TaxCode").
		Preload("Lines.ProductService"). // IN.5: needed to detect stock items
		Where("id = ? AND company_id = ?", creditNoteID, companyID).
		First(&cn).Error; err != nil {
		return fmt.Errorf("load credit note: %w", err)
	}

	// ── 2. Pre-flight checks ──────────────────────────────────────────────────
	if cn.Status != models.CreditNoteStatusDraft {
		return ErrCreditNoteNotDraft
	}
	if len(cn.Lines) == 0 {
		return errors.New("credit note has no line items")
	}
	for i, l := range cn.Lines {
		if l.RevenueAccountID == 0 {
			return fmt.Errorf("line %d (%q): revenue account is required before posting", i+1, l.Description)
		}
	}

	// ── 2b. Load company (for base currency + Phase I rail state) ────────────
	// IN.5 reads shipment_required to gate stock-item credit notes
	// off controlled-mode companies (Q2: defer to Phase I.6).
	var company models.Company
	if err := db.Select("id", "base_currency_code", "shipment_required").
		First(&company, companyID).Error; err != nil {
		return fmt.Errorf("load company: %w", err)
	}

	// ── 2b.IN.5. Rule #4 pre-flight for stock-item lines ─────────────────────
	stockLineCount := 0
	for i, l := range cn.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		stockLineCount++
		// Q2: controlled mode rejects stock-item credit notes.
		if company.ShipmentRequired {
			return fmt.Errorf("%w: line[%d]", ErrCreditNoteStockItemRequiresReturnReceipt, i)
		}
		// Legacy mode: stock line must trace back to a specific
		// invoice line so authoritative cost is resolvable.
		if cn.InvoiceID == nil || *cn.InvoiceID == 0 {
			return fmt.Errorf("%w: line[%d] item=%d", ErrCreditNoteStockItemRequiresInvoice, i, *l.ProductServiceID)
		}
		if l.OriginalInvoiceLineID == nil || *l.OriginalInvoiceLineID == 0 {
			return fmt.Errorf("%w: line[%d] item=%d", ErrCreditNoteStockItemRequiresOriginalLine, i, *l.ProductServiceID)
		}
	}

	// ── 2c. Exchange rate ─────────────────────────────────────────────────────
	exchangeRate := decimal.NewFromInt(1)
	txCurrencyCode := company.BaseCurrencyCode
	if normalizeCurrencyCode(cn.CurrencyCode) != "" {
		txCurrencyCode = normalizeCurrencyCode(cn.CurrencyCode)
	}
	isForeignCurrency := txCurrencyCode != company.BaseCurrencyCode
	if isForeignCurrency {
		if cn.ExchangeRate.GreaterThan(decimal.Zero) && !cn.ExchangeRate.Equal(decimal.NewFromInt(1)) {
			exchangeRate = cn.ExchangeRate
		} else {
			row, found, err := lookupExchangeRateRow(db, companyID, txCurrencyCode, company.BaseCurrencyCode, cn.CreditNoteDate)
			if err != nil || !found {
				if !found {
					err = ErrNoRate
				}
				return fmt.Errorf("exchange rate %s→%s not found for %s: %w",
					txCurrencyCode, company.BaseCurrencyCode, cn.CreditNoteDate.Format("2006-01-02"), err)
			}
			exchangeRate = snapshotFromExchangeRateRow(row, companyID).ExchangeRate
		}
	}

	// ── 2b. Validate customer currency policy (Phase 12) ─────────────────────
	if err := ValidateDocumentCurrency(db, companyID, cn.CustomerID,
		models.PartyTypeCustomer, txCurrencyCode, company.BaseCurrencyCode); err != nil {
		return err
	}

	// ── 3. Resolve AR account (Phase 11: ARAPControlMapping) ─────────────────
	arAccount, err := ResolveControlAccount(db, companyID, 0,
		models.ARAPDocTypeCreditNote, txCurrencyCode, isForeignCurrency,
		models.DetailAccountsReceivable, ErrNoARAccount)
	if err != nil {
		return err
	}

	// ── 4. Build posting fragments ────────────────────────────────────────────
	frags, err := BuildCreditNoteFragments(cn, arAccount.ID)
	if err != nil {
		return fmt.Errorf("build credit note fragments: %w", err)
	}

	// ── 5. Aggregate by account + side ───────────────────────────────────────
	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return fmt.Errorf("aggregate journal lines: %w", err)
	}
	txJournalLines := make([]PostingFragment, len(jeLines))
	copy(txJournalLines, jeLines)

	// ── 5b. FX scaling ───────────────────────────────────────────────────────
	docCreditSum := sumPostingCredits(jeLines)
	if isForeignCurrency {
		// anchorIsDebit=false: for a credit note, AR is on the credit side (CR AR).
		jeLines = applyFXScaling(jeLines, exchangeRate, arAccount.ID, false)
	}

	// ── 6. Double-entry balance check ─────────────────────────────────────────
	debitSum := sumPostingDebits(jeLines)
	creditSum := sumPostingCredits(jeLines)
	if !debitSum.Equal(creditSum) {
		return fmt.Errorf("journal entry imbalance: debit %s, credit %s",
			debitSum.StringFixed(2), creditSum.StringFixed(2))
	}

	// ── 7. Transaction ────────────────────────────────────────────────────────
	return db.Transaction(func(tx *gorm.DB) error {
		// a. Lock + re-validate.
		var locked models.CreditNote
		if err := applyLockForUpdate(
			tx.Select("id", "company_id", "status").
				Where("id = ? AND company_id = ?", creditNoteID, companyID),
		).First(&locked).Error; err != nil {
			return fmt.Errorf("lock credit note: %w", err)
		}
		if locked.Status != models.CreditNoteStatusDraft {
			return ErrAlreadyPosted
		}

		// b. Journal entry header.
		issuedAt := time.Now()
		je := models.JournalEntry{
			CompanyID:               companyID,
			EntryDate:               cn.CreditNoteDate,
			JournalNo:               cn.CreditNoteNumber,
			Status:                  models.JournalEntryStatusPosted,
			SourceType:              models.LedgerSourceCreditNote,
			SourceID:                cn.ID,
			TransactionCurrencyCode: txCurrencyCode,
			ExchangeRate:            exchangeRate.RoundBank(8),
		}
		if err := wrapUniqueViolation(tx.Create(&je).Error, "create credit note JE"); err != nil {
			return err
		}

		// c. Journal lines (revenue reversal + AR side — pre-tx built).
		createdLines := make([]models.JournalLine, 0, len(jeLines))
		for i, jl := range jeLines {
			txLine := txJournalLines[i]
			line := models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      jl.AccountID,
				TxDebit:        txLine.Debit,
				TxCredit:       txLine.Credit,
				Debit:          jl.Debit,
				Credit:         jl.Credit,
				Memo:           jl.Memo,
				PartyType:      models.PartyTypeCustomer,
				PartyID:        cn.CustomerID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create journal line: %w", err)
			}
			createdLines = append(createdLines, line)
		}

		// c.IN.5 Inventory returns + Dr Inventory / Cr COGS fragments.
		// Pre-flight already rejected stock lines under controlled
		// mode; reaching this point means legacy mode with valid
		// stock lines (or no stock lines at all, in which case the
		// helpers no-op).
		returns, retErr := CreateCreditNoteInventoryReturns(tx, cn)
		if retErr != nil {
			return retErr
		}
		if len(returns) > 0 {
			invFrags := buildCreditNoteInventoryFragments(returns, cn.CreditNoteNumber)
			// Each (Dr Inventory / Cr COGS) pair is self-balancing,
			// so no additional aggregation or balance check is
			// needed — the parent JE stays balanced.
			for _, f := range invFrags {
				line := models.JournalLine{
					CompanyID:      companyID,
					JournalEntryID: je.ID,
					AccountID:      f.AccountID,
					TxDebit:        f.Debit,
					TxCredit:       f.Credit,
					Debit:          f.Debit,
					Credit:         f.Credit,
					Memo:           f.Memo,
					PartyType:      models.PartyTypeCustomer,
					PartyID:        cn.CustomerID,
				}
				if err := tx.Create(&line).Error; err != nil {
					return fmt.Errorf("create IN.5 inventory journal line: %w", err)
				}
				createdLines = append(createdLines, line)
			}
		}

		// d. Secondary book amounts.
		if err := WriteSecondaryBookAmounts(tx, companyID, createdLines,
			txCurrencyCode, cn.CreditNoteDate,
			models.FXPostingReasonTransaction); err != nil {
			return fmt.Errorf("write secondary book amounts: %w", err)
		}

		// e. Ledger projection.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourceCreditNote,
			SourceID:     cn.ID,
		}); err != nil {
			return fmt.Errorf("project to ledger: %w", err)
		}

		// f. Update credit note.
		amountBase := creditSum
		var customer models.Customer
		tx.Select("name").First(&customer, cn.CustomerID)
		cnUpdates := map[string]any{
			"status":                 string(models.CreditNoteStatusIssued),
			"journal_entry_id":       je.ID,
			"issued_at":              issuedAt,
			"amount":                 docCreditSum,
			"balance_remaining":      docCreditSum,
			"subtotal":               cn.Subtotal,
			"tax_total":              cn.TaxTotal,
			"amount_base":            amountBase,
			"customer_name_snapshot": customer.Name,
		}
		if isForeignCurrency {
			cnUpdates["exchange_rate"] = exchangeRate
		}
		if err := tx.Model(&cn).Updates(cnUpdates).Error; err != nil {
			return fmt.Errorf("update credit note: %w", err)
		}

		// g. If linked to a specific invoice, apply immediately (reduce BalanceDue).
		if cn.InvoiceID != nil {
			if err := applyCreditNoteToInvoiceTx(tx, companyID, cn.ID, *cn.InvoiceID,
				docCreditSum, amountBase, issuedAt); err != nil {
				return err
			}
		}

		// g.IN.3. Rule #4 post-time invariant. Credit note under
		// legacy mode owns its return movements; controlled mode
		// rejected stock lines pre-post, so this assertion only
		// meaningfully fires on the legacy path with stock lines.
		if err := AssertRule4PostTimeInvariant(tx, companyID,
			Rule4DocCreditNote, cn.ID, stockLineCount,
			Rule4WorkflowState{
				ShipmentRequired: company.ShipmentRequired,
			},
		); err != nil {
			return err
		}

		// h. Audit log.
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "credit_note.issued", "credit_note", cn.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"credit_note_number": cn.CreditNoteNumber,
				"journal_entry_id":   je.ID,
				"amount":             creditSum.StringFixed(2),
			},
		)
	})
}

// applyCreditNoteToInvoiceTx applies a credit note amount to an invoice within
// an existing transaction. It reduces Invoice.BalanceDue and CreditNote.BalanceRemaining,
// creates a CreditNoteApplication record, and updates invoice status if fully paid.
func applyCreditNoteToInvoiceTx(tx *gorm.DB, companyID, cnID, invoiceID uint,
	amountDoc, amountBase decimal.Decimal, appliedAt time.Time) error {

	// Load invoice with a row lock.
	var inv models.Invoice
	if err := applyLockForUpdate(
		tx.Select("id", "company_id", "status", "balance_due", "amount").
			Where("id = ? AND company_id = ?", invoiceID, companyID),
	).First(&inv).Error; err != nil {
		return fmt.Errorf("lock invoice for CN application: %w", err)
	}

	// Clamp applied amount to the invoice's open balance.
	apply := amountDoc
	if apply.GreaterThan(inv.BalanceDue) {
		apply = inv.BalanceDue
	}
	applyBase := amountBase
	if amountDoc.GreaterThan(decimal.Zero) {
		ratio := apply.Div(amountDoc)
		applyBase = amountBase.Mul(ratio).Round(2)
	}

	newBalanceDue := inv.BalanceDue.Sub(apply)

	// Determine new invoice status.
	newStatus := inv.Status
	if newBalanceDue.IsZero() || newBalanceDue.IsNegative() {
		newStatus = models.InvoiceStatusPaid
		newBalanceDue = decimal.Zero
	} else if apply.GreaterThan(decimal.Zero) && inv.Status == models.InvoiceStatusIssued {
		newStatus = models.InvoiceStatusPartiallyPaid
	}

	if err := tx.Model(&inv).Updates(map[string]any{
		"balance_due": newBalanceDue,
		"status":      string(newStatus),
	}).Error; err != nil {
		return fmt.Errorf("update invoice balance after CN application: %w", err)
	}

	// Create application record.
	app := models.CreditNoteApplication{
		CompanyID:         companyID,
		CreditNoteID:      cnID,
		InvoiceID:         invoiceID,
		AmountApplied:     apply,
		AmountAppliedBase: applyBase,
		AppliedAt:         appliedAt,
	}
	if err := tx.Create(&app).Error; err != nil {
		return fmt.Errorf("create credit note application: %w", err)
	}

	// Update credit note balance remaining.
	var cn models.CreditNote
	if err := tx.Select("id", "balance_remaining", "amount").First(&cn, cnID).Error; err != nil {
		return fmt.Errorf("reload credit note for balance update: %w", err)
	}
	newBalance := cn.BalanceRemaining.Sub(apply)
	newCNStatus := models.CreditNoteStatusIssued
	if newBalance.IsZero() || newBalance.IsNegative() {
		newCNStatus = models.CreditNoteStatusFullyApplied
		newBalance = decimal.Zero
	} else if apply.GreaterThan(decimal.Zero) {
		newCNStatus = models.CreditNoteStatusPartiallyApplied
	}
	if err := tx.Model(&cn).Updates(map[string]any{
		"balance_remaining": newBalance,
		"status":            string(newCNStatus),
	}).Error; err != nil {
		return fmt.Errorf("update credit note balance: %w", err)
	}

	return nil
}
