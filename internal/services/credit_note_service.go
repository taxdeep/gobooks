// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
)

// ErrCreditNoteNotVoidable is returned when a void is attempted on a non-voidable credit note.
var ErrCreditNoteNotVoidable = errors.New("credit notes can only be voided from draft or issued state (before any application)")

// ── Number generation ─────────────────────────────────────────────────────────

// NextCreditNoteNumber generates the next available credit note number for the company.
// Format: CN-YYYY-NNNN (padded to 4 digits). Falls back to CN-YYYY-0001 on fresh install.
func NextCreditNoteNumber(db *gorm.DB, companyID uint, date time.Time) (string, error) {
	year := date.Year()
	prefix := fmt.Sprintf("CN-%d-", year)

	var count int64
	db.Model(&models.CreditNote{}).
		Where("company_id = ? AND credit_note_number LIKE ?", companyID, prefix+"%").
		Count(&count)

	return fmt.Sprintf("%s%04d", prefix, count+1), nil
}

// ── CreateCreditNoteDraftInput ─────────────────────────────────────────────────

// CreditNoteLineInput is one line in the credit note create/update request.
type CreditNoteLineInput struct {
	Description      string
	Qty              decimal.Decimal
	UnitPrice        decimal.Decimal
	RevenueAccountID uint
	TaxCodeID        *uint
	ProductServiceID *uint
	// OriginalInvoiceLineID (IN.5): trace back to the InvoiceLine
	// that originally sold this qty. Required at post time when
	// the line points at a stock item (IsStockItem=true). Used by
	// credit_note_post.go to reverse the original inventory
	// movement at its authoritative snapshot cost.
	OriginalInvoiceLineID *uint
	SortOrder             uint
}

// CreateCreditNoteDraftInput holds the parameters for creating a new draft credit note.
type CreateCreditNoteDraftInput struct {
	CompanyID    uint
	CustomerID   uint
	InvoiceID    *uint // optional: link to original invoice
	CreditNoteDate time.Time
	Reason       models.CreditNoteReason
	Memo         string
	CurrencyCode string
	ExchangeRate decimal.Decimal
	Lines        []CreditNoteLineInput
}

// CreateCreditNoteDraft creates a new draft credit note. Totals are computed from lines.
// Returns the created CreditNote with ID populated.
func CreateCreditNoteDraft(db *gorm.DB, in CreateCreditNoteDraftInput) (*models.CreditNote, error) {
	if len(in.Lines) == 0 {
		return nil, errors.New("credit note must have at least one line item")
	}

	// Generate number.
	cnNumber, err := NextCreditNoteNumber(db, in.CompanyID, in.CreditNoteDate)
	if err != nil {
		return nil, fmt.Errorf("generate credit note number: %w", err)
	}

	// Compute totals from lines.
	subtotal := decimal.Zero
	taxTotal := decimal.Zero
	for i, l := range in.Lines {
		if l.Description == "" {
			return nil, fmt.Errorf("line %d: description is required", i+1)
		}
		if l.RevenueAccountID == 0 {
			return nil, fmt.Errorf("line %d (%q): revenue account is required", i+1, l.Description)
		}
		if err := validateStockItemQty(db, in.CompanyID, l.ProductServiceID, l.Qty, i+1); err != nil {
			return nil, err
		}
		lineNet := l.Qty.Mul(l.UnitPrice).Round(2)
		subtotal = subtotal.Add(lineNet)

		// Tax calculation.
		lineTax := decimal.Zero
		if l.TaxCodeID != nil {
			var tc models.TaxCode
			if err := db.Where("id = ? AND company_id = ?", *l.TaxCodeID, in.CompanyID).
				First(&tc).Error; err == nil {
				lineTax = computeLineTax(lineNet, tc).Round(2)
			}
		}
		taxTotal = taxTotal.Add(lineTax)
	}
	amount := subtotal.Add(taxTotal)

	reason := in.Reason
	if reason == "" {
		reason = models.CreditNoteReasonOther
	}

	cn := models.CreditNote{
		CompanyID:        in.CompanyID,
		CreditNoteNumber: cnNumber,
		CustomerID:       in.CustomerID,
		InvoiceID:        in.InvoiceID,
		CreditNoteDate:   in.CreditNoteDate,
		Status:           models.CreditNoteStatusDraft,
		Reason:           reason,
		Memo:             in.Memo,
		Subtotal:         subtotal,
		TaxTotal:         taxTotal,
		Amount:           amount,
		BalanceRemaining: amount,
		CurrencyCode:     normalizeCurrencyCode(in.CurrencyCode),
		ExchangeRate:     in.ExchangeRate,
	}
	if cn.ExchangeRate.IsZero() {
		cn.ExchangeRate = decimal.NewFromInt(1)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&cn).Error; err != nil {
			return fmt.Errorf("create credit note: %w", err)
		}
		// Create lines.
		for i, l := range in.Lines {
			lineNet := l.Qty.Mul(l.UnitPrice).Round(2)
			lineTax := decimal.Zero
			if l.TaxCodeID != nil {
				var tc models.TaxCode
				if tx.Where("id = ? AND company_id = ?", *l.TaxCodeID, in.CompanyID).
					First(&tc).Error == nil {
					lineTax = computeLineTax(lineNet, tc).Round(2)
				}
			}
			sortOrder := l.SortOrder
			if sortOrder == 0 {
				sortOrder = uint(i + 1)
			}
			line := models.CreditNoteLine{
				CompanyID:             in.CompanyID,
				CreditNoteID:          cn.ID,
				SortOrder:             sortOrder,
				ProductServiceID:      l.ProductServiceID,
				OriginalInvoiceLineID: l.OriginalInvoiceLineID,
				RevenueAccountID:      l.RevenueAccountID,
				Description:           l.Description,
				Qty:                   l.Qty,
				UnitPrice:             l.UnitPrice,
				TaxCodeID:             l.TaxCodeID,
				LineNet:          lineNet,
				LineTax:          lineTax,
				LineTotal:        lineNet.Add(lineTax),
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create credit note line %d: %w", i+1, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return &cn, nil
}

// VoidCreditNote voids a credit note. Only draft and issued (unapplied) credit notes
// may be voided. Creates a reversal JE if the credit note was already posted.
func VoidCreditNote(db *gorm.DB, companyID, cnID uint, actor string, userID *uuid.UUID) error {
	var cn models.CreditNote
	if err := db.Where("id = ? AND company_id = ?", cnID, companyID).First(&cn).Error; err != nil {
		return fmt.Errorf("load credit note: %w", err)
	}

	if cn.Status != models.CreditNoteStatusDraft && cn.Status != models.CreditNoteStatusIssued {
		return ErrCreditNoteNotVoidable
	}

	// Check no applications exist.
	var appCount int64
	db.Model(&models.CreditNoteApplication{}).
		Where("credit_note_id = ? AND company_id = ?", cnID, companyID).
		Count(&appCount)
	if appCount > 0 {
		return errors.New("cannot void a credit note that has already been applied to an invoice")
	}

	voidedAt := time.Now()

	return db.Transaction(func(tx *gorm.DB) error {
		// If issued, create reversal JE.
		if cn.Status == models.CreditNoteStatusIssued && cn.JournalEntryID != nil {
			var origJE models.JournalEntry
			if err := tx.First(&origJE, *cn.JournalEntryID).Error; err != nil {
				return fmt.Errorf("load original JE: %w", err)
			}

			// Load original lines.
			var origLines []models.JournalLine
			if err := tx.Where("journal_entry_id = ? AND company_id = ?",
				origJE.ID, companyID).Find(&origLines).Error; err != nil {
				return fmt.Errorf("load original JE lines: %w", err)
			}

			// Reversal JE.
			reversalJE := models.JournalEntry{
				CompanyID:               companyID,
				EntryDate:               voidedAt,
				JournalNo:               cn.CreditNoteNumber + "-VOID",
				Status:                  models.JournalEntryStatusPosted,
				SourceType:              models.LedgerSourceReversal,
				SourceID:                origJE.ID,
				TransactionCurrencyCode: origJE.TransactionCurrencyCode,
				ExchangeRate:            origJE.ExchangeRate,
				ReversedFromID:          &origJE.ID,
			}
			if err := tx.Create(&reversalJE).Error; err != nil {
				return fmt.Errorf("create reversal JE: %w", err)
			}

			revLines := make([]models.JournalLine, 0, len(origLines))
			for _, l := range origLines {
				rev := models.JournalLine{
					CompanyID:      companyID,
					JournalEntryID: reversalJE.ID,
					AccountID:      l.AccountID,
					TxDebit:        l.TxCredit,
					TxCredit:       l.TxDebit,
					Debit:          l.Credit,
					Credit:         l.Debit,
					Memo:           "Void: " + l.Memo,
					PartyType:      l.PartyType,
					PartyID:        l.PartyID,
				}
				if err := tx.Create(&rev).Error; err != nil {
					return fmt.Errorf("create reversal line: %w", err)
				}
				revLines = append(revLines, rev)
			}

			if err := WriteSecondaryBookAmounts(tx, companyID, revLines,
				reversalJE.TransactionCurrencyCode, voidedAt,
				models.FXPostingReasonTransaction); err != nil {
				return fmt.Errorf("write secondary book amounts (void): %w", err)
			}

			if err := ProjectToLedger(tx, companyID, LedgerPostInput{
				JournalEntry: reversalJE,
				Lines:        revLines,
				SourceType:   models.LedgerSourceReversal,
				SourceID:     origJE.ID,
			}); err != nil {
				return fmt.Errorf("project reversal to ledger: %w", err)
			}

			// Reverse invoice BalanceDue change (if CN was auto-applied to invoice at posting).
			if cn.InvoiceID != nil {
				var app models.CreditNoteApplication
				err := tx.Where("credit_note_id = ? AND invoice_id = ? AND company_id = ?",
					cnID, *cn.InvoiceID, companyID).First(&app).Error
				if err == nil {
					tx.Model(&models.Invoice{}).Where("id = ? AND company_id = ?", *cn.InvoiceID, companyID).
						Updates(map[string]any{
							"balance_due": gorm.Expr("balance_due + ?", app.AmountApplied),
							"status":      string(models.InvoiceStatusIssued),
						})
				}
			}

			// IN.5 void symmetry: undo the inventory restoration that
			// credit_note_post formed for stock-item lines. Reverses
			// every source_type='credit_note' movement attached to
			// this CN. Reaches inventory back to the state it was in
			// right after the original invoice post, which is exactly
			// where it should be when a credit note is voided — the
			// sale stands, the return is cancelled, goods go back out.
			if err := reverseDocumentMovements(tx, companyID, reverseDocumentScope{
				sourceType:         string(models.LedgerSourceCreditNote),
				sourceID:           cn.ID,
				reversalSourceType: "credit_note_reversal",
				movementDate:       voidedAt,
				memo:               "Void: " + cn.CreditNoteNumber,
				reason:             inventory.ReversalReasonErrorCorrection,
			}); err != nil {
				return fmt.Errorf("reverse credit note inventory return on void: %w", err)
			}
		}

		// Mark voided.
		if err := tx.Model(&cn).Updates(map[string]any{
			"status":    string(models.CreditNoteStatusVoided),
			"voided_at": voidedAt,
		}).Error; err != nil {
			return fmt.Errorf("mark credit note voided: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "credit_note.voided", "credit_note", cn.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{"credit_note_number": cn.CreditNoteNumber},
		)
	})
}

// ApplyCreditNoteToInvoice applies an issued credit note against an open invoice.
// Reduces Invoice.BalanceDue and CreditNote.BalanceRemaining; creates a
// CreditNoteApplication record. No JE is created (accounting happened at CN posting).
func ApplyCreditNoteToInvoice(db *gorm.DB, companyID, cnID, invoiceID uint,
	amountToApply decimal.Decimal, actor string, userID *uuid.UUID) error {

	if !amountToApply.IsPositive() {
		return errors.New("amount to apply must be positive")
	}

	// Validate CN.
	var cn models.CreditNote
	if err := db.Where("id = ? AND company_id = ?", cnID, companyID).First(&cn).Error; err != nil {
		return fmt.Errorf("load credit note: %w", err)
	}
	if cn.Status != models.CreditNoteStatusIssued && cn.Status != models.CreditNoteStatusPartiallyApplied {
		return fmt.Errorf("credit note must be issued or partially applied to apply; status is %s", cn.Status)
	}
	if amountToApply.GreaterThan(cn.BalanceRemaining) {
		return fmt.Errorf("amount to apply (%s) exceeds credit note balance (%s)",
			amountToApply.StringFixed(2), cn.BalanceRemaining.StringFixed(2))
	}

	// Compute base amount proportionally.
	amountBase := amountToApply
	if cn.Amount.GreaterThan(decimal.Zero) && cn.AmountBase.GreaterThan(decimal.Zero) {
		ratio := amountToApply.Div(cn.Amount)
		amountBase = cn.AmountBase.Mul(ratio).Round(2)
	}

	appliedAt := time.Now()

	return db.Transaction(func(tx *gorm.DB) error {
		if err := applyCreditNoteToInvoiceTx(tx, companyID, cnID, invoiceID,
			amountToApply, amountBase, appliedAt); err != nil {
			return err
		}
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "credit_note.applied", "credit_note", cnID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"invoice_id":     invoiceID,
				"amount_applied": amountToApply.StringFixed(2),
			},
		)
	})
}

// ListCreditNotes returns all credit notes for the company, newest first.
// Preloads Customer for display.
func ListCreditNotes(db *gorm.DB, companyID uint) ([]models.CreditNote, error) {
	var cns []models.CreditNote
	if err := db.
		Preload("Customer").
		Where("company_id = ?", companyID).
		Order("credit_note_date desc, id desc").
		Find(&cns).Error; err != nil {
		return nil, fmt.Errorf("list credit notes: %w", err)
	}
	return cns, nil
}

// GetCreditNote loads a single credit note with full detail (lines + customer + invoice).
func GetCreditNote(db *gorm.DB, companyID, cnID uint) (*models.CreditNote, error) {
	var cn models.CreditNote
	if err := db.
		Preload("Lines.TaxCode").
		Preload("Lines.RevenueAccount").
		Preload("Customer").
		Preload("Invoice").
		Preload("Applications").Preload("Applications.Invoice").
		Where("id = ? AND company_id = ?", cnID, companyID).
		First(&cn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("credit note %d not found", cnID)
		}
		return nil, fmt.Errorf("get credit note: %w", err)
	}
	return &cn, nil
}

// ReverseARCreditNoteApplication removes a single credit note application,
// restoring the credit note's BalanceRemaining and the invoice's BalanceDue.
// Only valid when neither the credit note nor the invoice is voided.
func ReverseARCreditNoteApplication(db *gorm.DB, companyID, applicationID uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var app models.CreditNoteApplication
		if err := tx.Where("id = ? AND company_id = ?", applicationID, companyID).
			First(&app).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("credit note application not found")
			}
			return fmt.Errorf("load application: %w", err)
		}

		var cn models.CreditNote
		if err := tx.Where("id = ? AND company_id = ?", app.CreditNoteID, companyID).
			First(&cn).Error; err != nil {
			return fmt.Errorf("load credit note: %w", err)
		}
		if cn.Status == models.CreditNoteStatusVoided {
			return errors.New("cannot reverse: credit note is voided")
		}

		var inv models.Invoice
		if err := tx.Where("id = ? AND company_id = ?", app.InvoiceID, companyID).
			First(&inv).Error; err != nil {
			return fmt.Errorf("load invoice: %w", err)
		}
		if inv.Status == models.InvoiceStatusVoided {
			return errors.New("cannot reverse: invoice is voided")
		}

		// Restore credit note balance.
		newBalance := cn.BalanceRemaining.Add(app.AmountApplied)
		newCNStatus := models.CreditNoteStatusPartiallyApplied
		if newBalance.Equal(cn.Amount) {
			newCNStatus = models.CreditNoteStatusIssued
		}
		if err := tx.Model(&cn).Updates(map[string]any{
			"balance_remaining": newBalance,
			"status":            string(newCNStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore credit note: %w", err)
		}

		// Restore invoice balance.
		newInvBalance := inv.BalanceDue.Add(app.AmountApplied)
		newInvStatus := models.InvoiceStatusPartiallyPaid
		if newInvBalance.Equal(inv.Amount) {
			newInvStatus = models.InvoiceStatusSent
		}
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due": newInvBalance,
			"status":      string(newInvStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore invoice: %w", err)
		}

		// Delete application.
		if err := tx.Delete(&app).Error; err != nil {
			return fmt.Errorf("delete application: %w", err)
		}
		return nil
	})
}

// computeLineTax is a helper that computes tax for a line given a TaxCode.
// This is a simplified calculation using the code's effective rate.
func computeLineTax(lineNet decimal.Decimal, tc models.TaxCode) decimal.Decimal {
	if tc.Rate.IsZero() {
		return decimal.Zero
	}
	return lineNet.Mul(tc.Rate).Div(decimal.NewFromInt(100)).Round(2)
}
