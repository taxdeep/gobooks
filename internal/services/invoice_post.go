// 遵循project_guide.md
package services

// invoice_post.go — PostInvoice: posting pipeline for customer invoices.
//
// Posting pipeline (Phase 4 + Phase 6 concurrency controls):
//
//   1. Load invoice + lines  (ProductService.RevenueAccount + TaxCode preloaded)
//   2. Pre-flight validation  (fast reject before acquiring DB resources)
//   3. Resolve Accounts Receivable account
//   4. BuildInvoiceFragments  → raw []PostingFragment per line (fragment_builder.go)
//   5. AggregateJournalLines  → collapse by account + side (journal_aggregate.go)
//   6. Validate double-entry balance (ΣDebit == ΣCredit == invoice.Amount)
//   7. Transaction:
//        a. SELECT FOR UPDATE on invoice row; re-validate status inside lock
//           (prevents concurrent double-posting; second caller blocks then sees
//           status='sent' and returns ErrAlreadyPosted)
//        b. INSERT journal_entries header (SourceType=invoice, SourceID=inv.ID)
//           wrapUniqueViolation converts 23505 → ErrConcurrentPostingConflict
//        c. INSERT journal_lines (one per aggregated fragment)
//        d. ProjectToLedger   → INSERT ledger_entries (ledger.go)
//        e. UPDATE invoices   → status='sent', journal_entry_id
//        f. WriteAuditLog
//
// Before vs after journal shape — example invoice $1 000 net, 5% GST:
//
//   Line 1: Widget A  $800.00 net, GST $40.00 → revenue account 4000
//   Line 2: Widget B  $200.00 net, GST $10.00 → revenue account 4000  (same acct)
//
//   Raw fragments (pre-aggregation):
//     DR  1100 AR                  1 050.00
//     CR  4000 Revenue               800.00   (Widget A net)
//     CR  4000 Revenue               200.00   (Widget B net)
//     CR  2300 GST Payable            40.00   (Widget A tax)
//     CR  2300 GST Payable            10.00   (Widget B tax)
//
//   After AggregateJournalLines (merged by account + side):
//     DR  1100 AR                  1 050.00
//     CR  4000 Revenue             1 000.00   ← two lines merged
//     CR  2300 GST Payable            50.00   ← two lines merged
//
//   Ledger entries (one per journal line, status=active):
//     company 1, account 1100, debit  1 050.00, credit      0
//     company 1, account 4000, debit      0,    credit  1 000.00
//     company 1, account 2300, debit      0,    credit     50.00

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ErrInvoiceNotDraft is returned when posting is attempted on a non-draft invoice.
var ErrInvoiceNotDraft = errors.New("only draft invoices can be posted")

// ErrNoARAccount is returned when no active Accounts Receivable account exists for the company.
var ErrNoARAccount = errors.New("no active Accounts Receivable account found — create one in your Chart of Accounts first")

// PostInvoice transitions a draft invoice to "sent" and generates a double-entry
// journal entry in a single transaction.
//
// Returns ErrInvoiceNotDraft, ErrNoARAccount, or a descriptive error on failure.
func PostInvoice(db *gorm.DB, companyID, invoiceID uint, actor string, userID *uuid.UUID) error {
	// ── 1. Load invoice with full line detail ─────────────────────────────────
	var inv models.Invoice
	if err := db.
		Preload("Lines.ProductService.RevenueAccount").
		Preload("Lines.TaxCode").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return fmt.Errorf("load invoice: %w", err)
	}

	// ── 2. Pre-flight checks ──────────────────────────────────────────────────
	if inv.Status != models.InvoiceStatusDraft && inv.Status != models.InvoiceStatusIssued {
		return ErrInvoiceNotDraft
	}
	if inv.JournalEntryID != nil {
		return ErrAlreadyPosted
	}
	if len(inv.Lines) == 0 {
		return errors.New("invoice has no line items")
	}
	for i, l := range inv.Lines {
		if l.ProductServiceID == nil {
			return fmt.Errorf("line %d (%q) has no product/service — assign one before posting", i+1, l.Description)
		}
		if l.ProductService == nil || l.ProductService.CompanyID != companyID {
			return fmt.Errorf("line %d (%q): product/service is not valid for this company", i+1, l.Description)
		}
		if l.ProductService.RevenueAccountID == 0 {
			return fmt.Errorf("line %d (%q): product/service has no revenue account configured", i+1, l.Description)
		}
		if !l.ProductService.IsActive {
			return fmt.Errorf("line %d (%q): product/service is inactive", i+1, l.Description)
		}
		if l.ProductService.RevenueAccount.CompanyID != companyID {
			return fmt.Errorf("line %d (%q): revenue account does not belong to this company", i+1, l.Description)
		}
		if l.TaxCodeID != nil && (l.TaxCode == nil || l.TaxCode.CompanyID != companyID) {
			return fmt.Errorf("line %d (%q): tax code is not valid for this company", i+1, l.Description)
		}
	}

	// ── 2b. Load company (needed for base currency code) ─────────────────────
	var company models.Company
	if err := db.Select("id", "base_currency_code").First(&company, companyID).Error; err != nil {
		return fmt.Errorf("load company: %w", err)
	}

	// ── 2c. Determine exchange rate ───────────────────────────────────────────
	// isForeignCurrency is true when the invoice has an explicit currency code
	// that differs from the company base currency.
	exchangeRate := decimal.NewFromInt(1)
	isForeignCurrency := inv.CurrencyCode != "" && inv.CurrencyCode != company.BaseCurrencyCode
	if isForeignCurrency {
		if inv.ExchangeRate.GreaterThan(decimal.Zero) && !inv.ExchangeRate.Equal(decimal.NewFromInt(1)) {
			exchangeRate = inv.ExchangeRate
		} else {
			r, err := GetExchangeRate(db, &companyID, inv.CurrencyCode, company.BaseCurrencyCode, inv.InvoiceDate)
			if err != nil {
				return fmt.Errorf("exchange rate %s→%s not found for %s: %w",
					inv.CurrencyCode, company.BaseCurrencyCode, inv.InvoiceDate.Format("2006-01-02"), err)
			}
			exchangeRate = r
		}
	}

	// ── 3. Resolve AR account ─────────────────────────────────────────────────
	// For foreign-currency invoices, prefer the system-generated AR account for
	// that currency (system_key = "ar_{code}", created by AddCompanyCurrency).
	// Fall back to the first active AR account if not found.
	var arAccount models.Account
	if isForeignCurrency {
		sysKey := "ar_" + inv.CurrencyCode
		err := db.Where("company_id = ? AND system_key = ? AND is_active = true", companyID, sysKey).
			First(&arAccount).Error
		if err != nil {
			if err := db.
				Where("company_id = ? AND detail_account_type = ? AND is_active = true",
					companyID, string(models.DetailAccountsReceivable)).
				Order("code asc").First(&arAccount).Error; err != nil {
				return ErrNoARAccount
			}
		}
	} else {
		if err := db.
			Where("company_id = ? AND detail_account_type = ? AND is_active = true",
				companyID, string(models.DetailAccountsReceivable)).
			Order("code asc").
			First(&arAccount).Error; err != nil {
			return ErrNoARAccount
		}
	}

	// ── 4. Build posting fragments ────────────────────────────────────────────
	// Pure function: one DR (AR) + one CR (revenue) per line + one CR (tax) per
	// taxable line. No DB calls; validated against company above.
	frags, err := BuildInvoiceFragments(inv, arAccount.ID)
	if err != nil {
		return fmt.Errorf("build invoice fragments: %w", err)
	}

	// ── 5. Aggregate by account + side ────────────────────────────────────────
	// Multiple lines pointing to the same revenue or tax account are merged into
	// a single journal line. Debit and credit sides are never merged together.
	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return fmt.Errorf("aggregate journal lines: %w", err)
	}
	// ── 5b. Apply FX scaling (foreign-currency invoices only) ────────────────
	// Scale all non-AR lines to base currency. The AR anchor absorbs rounding so
	// that ΣDebit == ΣCredit after conversion.
	if isForeignCurrency {
		jeLines = applyFXScaling(jeLines, exchangeRate, arAccount.ID, true)
	}

	companyCheckLines := make([]models.JournalLine, 0, len(jeLines))
	for _, jl := range jeLines {
		companyCheckLines = append(companyCheckLines, models.JournalLine{AccountID: jl.AccountID})
	}
	if err := EnsureJournalLineAccountsBelongToCompany(db, companyID, companyCheckLines); err != nil {
		return fmt.Errorf("journal line account validation: %w", err)
	}

	// ── 6. Double-entry balance check ─────────────────────────────────────────
	creditSum := sumPostingCredits(jeLines)
	debitSum := sumPostingDebits(jeLines)
	if !debitSum.Equal(creditSum) {
		return fmt.Errorf(
			"journal entry imbalance: debit sum %s ≠ credit sum %s — check line totals",
			debitSum.StringFixed(2), creditSum.StringFixed(2),
		)
	}

	// ── 7. Transaction ────────────────────────────────────────────────────────
	return db.Transaction(func(tx *gorm.DB) error {
		// a. Lock invoice row and re-validate status inside the lock.
		//    applyLockForUpdate issues SELECT FOR UPDATE on Postgres so that a
		//    concurrent PostInvoice call blocks here until this transaction
		//    commits or rolls back, then re-reads and sees status='sent'.
		var locked models.Invoice
		if err := applyLockForUpdate(
			tx.Select("id", "company_id", "status").
				Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&locked).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}
		if locked.Status != models.InvoiceStatusDraft && locked.Status != models.InvoiceStatusIssued {
			return ErrAlreadyPosted
		}

		// b. Journal entry header.
		//    SourceType + SourceID enable the unique partial index backstop:
		//    (company_id, source_type='invoice', source_id=inv.ID) WHERE status='posted'.
		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  inv.InvoiceDate,
			JournalNo:  inv.InvoiceNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceInvoice,
			SourceID:   inv.ID,
		}
		if err := wrapUniqueViolation(tx.Create(&je).Error, "create journal entry"); err != nil {
			return fmt.Errorf("create journal entry: %w", err)
		}

		// c. Journal lines — one per aggregated fragment.
		//    Collect created rows: ProjectToLedger needs AccountID + Debit/Credit
		//    from the persisted rows (company_id cross-check inside ProjectToLedger).
		createdLines := make([]models.JournalLine, 0, len(jeLines))
		for _, jl := range jeLines {
			line := models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      jl.AccountID,
				Debit:          jl.Debit,
				Credit:         jl.Credit,
				Memo:           jl.Memo,
				PartyType:      models.PartyTypeCustomer,
				PartyID:        inv.CustomerID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create journal line: %w", err)
			}
			createdLines = append(createdLines, line)
		}

		// d. Ledger projection — one ledger_entry per journal_line, status=active.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourceInvoice,
			SourceID:     inv.ID,
		}); err != nil {
			return fmt.Errorf("project to ledger: %w", err)
		}

		// e. Update invoice: mark issued (posted), link journal entry, snapshot base amounts.
		// Phase 4: also set balance_due = Amount and balance_due_base = amountBase so
		// FX settlement can pro-rate the carrying value correctly across partial payments.
		amountBase := debitSum
		invUpdates := map[string]any{
			"status":           string(models.InvoiceStatusIssued),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"subtotal_base":    inv.Subtotal.Mul(exchangeRate).Round(2),
			"tax_total_base":   inv.TaxTotal.Mul(exchangeRate).Round(2),
			"balance_due":      inv.Amount,
			"balance_due_base": amountBase,
		}
		if isForeignCurrency {
			invUpdates["exchange_rate"] = exchangeRate
		}
		if err := tx.Model(&inv).Updates(invUpdates).Error; err != nil {
			return fmt.Errorf("update invoice status: %w", err)
		}

		// f. Audit log.
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "invoice.posted", "invoice", inv.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"invoice_number":   inv.InvoiceNumber,
				"journal_entry_id": je.ID,
				"total":            inv.Amount.StringFixed(2),
			},
		)
	})
}

// PostInvoiceAndReturn is a wrapper around PostInvoice that returns the updated invoice.
// It's used by HTTP handlers and the invoice lifecycle service where the posted invoice
// object is needed for the response. actor is set to "system" and userID is nil.
func PostInvoiceAndReturn(db *gorm.DB, companyID, invoiceID uint) (*models.Invoice, error) {
	// Call the core PostInvoice function with system actor
	if err := PostInvoice(db, companyID, invoiceID, "system", nil); err != nil {
		return nil, err
	}

	// Load and return the updated invoice
	var invoice models.Invoice
	if err := db.
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&invoice).Error; err != nil {
		return nil, fmt.Errorf("load posted invoice: %w", err)
	}

	return &invoice, nil
}
