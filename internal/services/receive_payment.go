// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Input types ───────────────────────────────────────────────────────────────

// InvoiceAllocation pairs an invoice with the document-currency amount being
// settled by this payment.
//
// ARAccountID overrides the top-level ReceivePaymentInput.ARAccountID for this
// specific invoice. Pass 0 to use the top-level default.
type InvoiceAllocation struct {
	InvoiceID   uint
	Amount      decimal.Decimal // document-currency amount to apply
	ARAccountID uint            // 0 = use ReceivePaymentInput.ARAccountID
}

// ReceivePaymentInput is the data needed to record a customer receipt.
//
// Two settlement modes:
//
//  1. Allocations (Phase 4): set Allocations with per-invoice amounts.
//     Supports partial settlement and automatic FX gain/loss posting for
//     foreign-currency invoices. Amount field is ignored in this mode.
//
//  2. Legacy (pre-Phase-4): set InvoiceID + Amount for a single full-settlement
//     link, or leave InvoiceID nil for an unlinked receipt. Only full settlement
//     is supported in this mode (partial returns an error).
type ReceivePaymentInput struct {
	CompanyID  uint
	CustomerID uint
	EntryDate  time.Time

	BankAccountID uint
	PaymentMethod models.PaymentMethod
	// ARAccountID is the default AR account to credit.
	// Can be overridden per-allocation via InvoiceAllocation.ARAccountID.
	ARAccountID uint

	// Allocations enables Phase-4 partial / FX settlement against one or more
	// invoices. When non-empty, InvoiceID and Amount are ignored.
	Allocations []InvoiceAllocation

	// InvoiceID (legacy): single full-settlement link.
	// Ignored when Allocations is non-empty.
	InvoiceID *uint

	// Amount: base-currency amount for unlinked/legacy receipts.
	// Ignored when Allocations is non-empty.
	Amount decimal.Decimal

	Memo string
}

// RecordReceivePayment posts a journal entry for a customer receipt and
// optionally settles one or more invoices.
//
// Phase 4 path (Allocations non-empty):
//
//	For each allocation:
//	  - Invoice must be posted (sent/overdue/partially_paid).
//	  - Partial settlement is allowed.
//	  - If the invoice is in a foreign currency, the settlement rate is looked up
//	    and a realized FX gain/loss line is auto-posted.
//
//	Journal entry structure (per allocation):
//	  DR Bank (base)              bankBaseAmount
//	  CR AR / AR-Foreign (base)   arapBaseReleased
//	  CR/DR FX Gain/Loss (base)   realizedFXGainLoss  ← only when invoice is foreign
//
//	The bank line is a single aggregated debit at the top.
//
// Legacy path (Allocations empty, InvoiceID set): full settlement only.
// Unlinked path (Allocations empty, InvoiceID nil): simple 2-line JE.
//
// Returns the new journal entry ID.
func RecordReceivePayment(tx *gorm.DB, in ReceivePaymentInput) (uint, error) {
	if in.CompanyID == 0 {
		return 0, fmt.Errorf("company is required")
	}
	if in.CustomerID == 0 || in.BankAccountID == 0 || in.ARAccountID == 0 {
		return 0, fmt.Errorf("missing required ids")
	}
	if _, err := models.ParsePaymentMethod(string(in.PaymentMethod)); err != nil {
		return 0, fmt.Errorf("payment method is required")
	}

	// Route to Phase-4 allocation path when Allocations are present.
	if len(in.Allocations) > 0 {
		return recordReceivePaymentAllocations(tx, in)
	}

	// Legacy / unlinked path — behaviour identical to pre-Phase-4.
	return recordReceivePaymentLegacy(tx, in)
}

// ── Phase-4 allocation path ───────────────────────────────────────────────────

func recordReceivePaymentAllocations(tx *gorm.DB, in ReceivePaymentInput) (uint, error) {
	// Validate bank and AR accounts.
	var bank models.Account
	if err := tx.Where("id = ? AND company_id = ?", in.BankAccountID, in.CompanyID).First(&bank).Error; err != nil {
		return 0, fmt.Errorf("bank account not found")
	}
	if bank.ReportGroup() != models.AccountReportGroupAsset {
		return 0, fmt.Errorf("bank account must be an asset")
	}

	// Load company for base currency code.
	var company models.Company
	if err := tx.Select("id", "base_currency_code").First(&company, in.CompanyID).Error; err != nil {
		return 0, fmt.Errorf("load company: %w", err)
	}
	baseCurrency := company.BaseCurrencyCode

	// ── Validate each allocation and compute settlement amounts ───────────────
	type invoiceRecord struct {
		inv     models.Invoice
		arAccID uint
		result  fxSettleResult
	}
	records := make([]invoiceRecord, 0, len(in.Allocations))
	totalBankBase := decimal.Zero
	hasFX := false

	for i, alloc := range in.Allocations {
		if alloc.Amount.LessThanOrEqual(decimal.Zero) {
			return 0, fmt.Errorf("allocation %d: amount must be > 0", i+1)
		}

		var inv models.Invoice
		if err := tx.Where("id = ? AND company_id = ?", alloc.InvoiceID, in.CompanyID).First(&inv).Error; err != nil {
			return 0, fmt.Errorf("allocation %d: invoice not found", i+1)
		}
		if inv.CustomerID != in.CustomerID {
			return 0, fmt.Errorf("allocation %d: invoice does not belong to the selected customer", i+1)
		}
		switch inv.Status {
		case models.InvoiceStatusSent, models.InvoiceStatusOverdue, models.InvoiceStatusPartiallyPaid, models.InvoiceStatusIssued:
		default:
			return 0, fmt.Errorf("allocation %d: invoice is not open for payment (status: %s)", i+1, inv.Status)
		}

		isForeign := inv.CurrencyCode != "" && inv.CurrencyCode != baseCurrency
		effBalance, effBalanceBase := effectiveBalances(
			inv.BalanceDue, inv.BalanceDueBase, inv.Amount, inv.AmountBase, isForeign,
		)
		if alloc.Amount.GreaterThan(effBalance) {
			return 0, fmt.Errorf("allocation %d: payment %s exceeds balance %s for invoice %s",
				i+1, alloc.Amount.StringFixed(2), effBalance.StringFixed(2), inv.InvoiceNumber)
		}

		settlementRate := decimal.NewFromInt(1)
		if isForeign {
			r, err := GetExchangeRate(tx, &in.CompanyID, inv.CurrencyCode, baseCurrency, in.EntryDate)
			if err != nil {
				return 0, fmt.Errorf("allocation %d: exchange rate %s→%s not found for %s: %w",
					i+1, inv.CurrencyCode, baseCurrency, in.EntryDate.Format("2006-01-02"), err)
			}
			settlementRate = r
			hasFX = true
		}

		result := computeAllocationAmounts(alloc.Amount, effBalance, effBalanceBase, settlementRate)
		totalBankBase = totalBankBase.Add(result.bankBaseAmount)

		arAccID := alloc.ARAccountID
		if arAccID == 0 {
			arAccID = in.ARAccountID
		}

		records = append(records, invoiceRecord{inv: inv, arAccID: arAccID, result: result})
	}

	if totalBankBase.LessThanOrEqual(decimal.Zero) {
		return 0, fmt.Errorf("total bank amount must be > 0")
	}

	// Ensure FX gain/loss account exists (only if any allocation has FX).
	var fxAccountID uint
	if hasFX {
		id, err := EnsureFXGainLossAccount(tx, in.CompanyID)
		if err != nil {
			return 0, err
		}
		fxAccountID = id
	}

	// ── Build journal fragments ───────────────────────────────────────────────
	frags := make([]PostingFragment, 0, 1+len(records)*3)

	// Aggregated bank debit (one line for the whole receipt).
	frags = append(frags, PostingFragment{
		AccountID: in.BankAccountID,
		Debit:     totalBankBase,
		Credit:    decimal.Zero,
		Memo:      in.Memo,
	})

	totalFXGainLoss := decimal.Zero
	for _, rec := range records {
		// AR credit at carrying value.
		frags = append(frags, PostingFragment{
			AccountID: rec.arAccID,
			Debit:     decimal.Zero,
			Credit:    rec.result.arapBaseReleased,
			Memo:      "Invoice " + rec.inv.InvoiceNumber,
		})
		totalFXGainLoss = totalFXGainLoss.Add(rec.result.realizedFXGainLoss)
	}

	// Single aggregated FX line (collapses gains and losses from all allocations).
	if hasFX && !totalFXGainLoss.IsZero() {
		if totalFXGainLoss.IsPositive() {
			// Net gain → credit FX account.
			frags = append(frags, PostingFragment{
				AccountID: fxAccountID,
				Debit:     decimal.Zero,
				Credit:    totalFXGainLoss,
				Memo:      "Realized FX gain/loss",
			})
		} else {
			// Net loss → debit FX account.
			frags = append(frags, PostingFragment{
				AccountID: fxAccountID,
				Debit:     totalFXGainLoss.Neg(),
				Credit:    decimal.Zero,
				Memo:      "Realized FX gain/loss",
			})
		}
	}

	// Aggregate AR lines across invoices sharing the same AR account.
	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return 0, fmt.Errorf("aggregate journal lines: %w", err)
	}

	// ── Create journal entry header ───────────────────────────────────────────
	var cust models.Customer
	if err := tx.Where("id = ? AND company_id = ?", in.CustomerID, in.CompanyID).First(&cust).Error; err != nil {
		return 0, err
	}
	je := models.JournalEntry{
		CompanyID:  in.CompanyID,
		EntryDate:  in.EntryDate,
		JournalNo:  fmt.Sprintf("Receive Payment - %s", cust.Name),
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePayment,
	}
	if err := tx.Create(&je).Error; err != nil {
		return 0, err
	}

	// ── Insert journal lines ──────────────────────────────────────────────────
	createdLines := make([]models.JournalLine, 0, len(jeLines))
	for _, frag := range jeLines {
		line := models.JournalLine{
			CompanyID:      in.CompanyID,
			JournalEntryID: je.ID,
			AccountID:      frag.AccountID,
			Debit:          frag.Debit,
			Credit:         frag.Credit,
			Memo:           frag.Memo,
			PartyType:      models.PartyTypeCustomer,
			PartyID:        in.CustomerID,
		}
		if err := tx.Create(&line).Error; err != nil {
			return 0, fmt.Errorf("create journal line: %w", err)
		}
		createdLines = append(createdLines, line)
	}
	if err := ProjectToLedger(tx, in.CompanyID, LedgerPostInput{
		JournalEntry: je,
		Lines:        createdLines,
		SourceType:   models.LedgerSourcePayment,
	}); err != nil {
		return 0, fmt.Errorf("project payment to ledger: %w", err)
	}
	if err := createPaymentReceipt(tx, in, je.ID, totalBankBase); err != nil {
		return 0, fmt.Errorf("create payment receipt: %w", err)
	}

	// ── Create settlement allocations + update invoices ───────────────────────
	for _, rec := range records {
		alloc := models.SettlementAllocation{
			CompanyID:          in.CompanyID,
			JournalEntryID:     je.ID,
			DocumentType:       models.SettlementDocInvoice,
			DocumentID:         rec.inv.ID,
			AmountApplied:      rec.result.amountApplied,
			ARAPBaseReleased:   rec.result.arapBaseReleased,
			BankBaseAmount:     rec.result.bankBaseAmount,
			RealizedFXGainLoss: rec.result.realizedFXGainLoss,
			SettlementRate:     rec.result.settlementRate,
		}
		if err := tx.Create(&alloc).Error; err != nil {
			return 0, fmt.Errorf("create settlement allocation: %w", err)
		}

		isForeign := rec.inv.CurrencyCode != "" && rec.inv.CurrencyCode != company.BaseCurrencyCode
		effBalance, effBalanceBase := effectiveBalances(
			rec.inv.BalanceDue, rec.inv.BalanceDueBase, rec.inv.Amount, rec.inv.AmountBase, isForeign,
		)
		newBalance := effBalance.Sub(rec.result.amountApplied)
		newBalanceBase := effBalanceBase.Sub(rec.result.arapBaseReleased)

		var newStatus models.InvoiceStatus
		if newBalance.LessThanOrEqual(decimal.Zero) {
			newStatus = models.InvoiceStatusPaid
			newBalance = decimal.Zero
			newBalanceBase = decimal.Zero
		} else {
			newStatus = models.InvoiceStatusPartiallyPaid
		}
		if err := tx.Model(&rec.inv).Updates(map[string]any{
			"status":           newStatus,
			"balance_due":      newBalance,
			"balance_due_base": newBalanceBase,
		}).Error; err != nil {
			return 0, fmt.Errorf("update invoice %s: %w", rec.inv.InvoiceNumber, err)
		}
	}

	return je.ID, nil
}

// ── Legacy path (pre-Phase-4 behaviour, unchanged) ────────────────────────────

func recordReceivePaymentLegacy(tx *gorm.DB, in ReceivePaymentInput) (uint, error) {
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return 0, fmt.Errorf("amount must be > 0")
	}

	var cust models.Customer
	if err := tx.Where("id = ? AND company_id = ?", in.CustomerID, in.CompanyID).First(&cust).Error; err != nil {
		return 0, err
	}

	var bank models.Account
	if err := tx.Where("id = ? AND company_id = ?", in.BankAccountID, in.CompanyID).First(&bank).Error; err != nil {
		return 0, err
	}
	var ar models.Account
	if err := tx.Where("id = ? AND company_id = ?", in.ARAccountID, in.CompanyID).First(&ar).Error; err != nil {
		return 0, err
	}
	if bank.ReportGroup() != models.AccountReportGroupAsset {
		return 0, fmt.Errorf("bank account must be an asset")
	}
	if ar.ReportGroup() != models.AccountReportGroupAsset {
		return 0, fmt.Errorf("A/R account must be an asset")
	}
	if cust.CompanyID != bank.CompanyID || cust.CompanyID != ar.CompanyID || cust.CompanyID != in.CompanyID {
		return 0, fmt.Errorf("customer and accounts must belong to the same company")
	}

	companyID := in.CompanyID
	desc := fmt.Sprintf("Receive Payment - %s", cust.Name)

	je := models.JournalEntry{
		CompanyID:  companyID,
		EntryDate:  in.EntryDate,
		JournalNo:  desc,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePayment,
	}
	if err := tx.Create(&je).Error; err != nil {
		return 0, err
	}

	lines := []models.JournalLine{
		{
			CompanyID:      companyID,
			JournalEntryID: je.ID,
			AccountID:      in.BankAccountID,
			Debit:          in.Amount,
			Credit:         decimal.Zero,
			Memo:           in.Memo,
			PartyType:      models.PartyTypeNone,
			PartyID:        0,
		},
		{
			CompanyID:      companyID,
			JournalEntryID: je.ID,
			AccountID:      in.ARAccountID,
			Debit:          decimal.Zero,
			Credit:         in.Amount,
			Memo:           in.Memo,
			PartyType:      models.PartyTypeCustomer,
			PartyID:        in.CustomerID,
		},
	}
	if err := tx.Create(&lines).Error; err != nil {
		return 0, err
	}
	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: je,
		Lines:        lines,
		SourceType:   models.LedgerSourcePayment,
	}); err != nil {
		return 0, fmt.Errorf("project payment to ledger: %w", err)
	}
	if err := createPaymentReceipt(tx, in, je.ID, in.Amount); err != nil {
		return 0, fmt.Errorf("create payment receipt: %w", err)
	}

	// Legacy invoice full-settlement check (preserved from pre-Phase-4).
	if in.InvoiceID != nil && *in.InvoiceID != 0 {
		var inv models.Invoice
		if err := tx.Where("id = ? AND company_id = ?", *in.InvoiceID, in.CompanyID).First(&inv).Error; err != nil {
			return 0, fmt.Errorf("linked invoice not found")
		}
		if inv.CustomerID != in.CustomerID {
			return 0, fmt.Errorf("invoice does not belong to the selected customer")
		}
		switch inv.Status {
		case models.InvoiceStatusSent, models.InvoiceStatusOverdue, models.InvoiceStatusPartiallyPaid:
		default:
			return 0, fmt.Errorf("invoice is not open for payment (status: %s)", inv.Status)
		}
		outstanding := inv.BalanceDue
		if outstanding.LessThanOrEqual(decimal.Zero) {
			outstanding = inv.Amount
		}
		if !outstanding.Equal(in.Amount) {
			return 0, fmt.Errorf(
				"linked invoice payments currently support full settlement only: payment amount (%s) must equal the remaining balance due (%s); leave the invoice blank to record a partial or unapplied receipt",
				in.Amount.StringFixed(2), outstanding.StringFixed(2),
			)
		}
		if err := tx.Model(&inv).Updates(map[string]any{
			"status":           models.InvoiceStatusPaid,
			"balance_due":      decimal.Zero,
			"balance_due_base": decimal.Zero,
		}).Error; err != nil {
			return 0, err
		}
	}

	return je.ID, nil
}

// errors referenced from other packages — kept for backward compat
var _ = errors.New

func createPaymentReceipt(tx *gorm.DB, in ReceivePaymentInput, journalEntryID uint, amountBase decimal.Decimal) error {
	receipt := models.PaymentReceipt{
		CompanyID:      in.CompanyID,
		CustomerID:     in.CustomerID,
		InvoiceID:      primaryReceiptInvoiceID(in),
		JournalEntryID: journalEntryID,
		BankAccountID:  in.BankAccountID,
		PaymentMethod:  in.PaymentMethod,
		AmountBase:     amountBase,
		Memo:           in.Memo,
		EntryDate:      in.EntryDate,
	}
	return tx.Create(&receipt).Error
}

func primaryReceiptInvoiceID(in ReceivePaymentInput) *uint {
	if len(in.Allocations) == 1 {
		id := in.Allocations[0].InvoiceID
		if id != 0 {
			return &id
		}
	}
	if in.InvoiceID != nil && *in.InvoiceID != 0 {
		id := *in.InvoiceID
		return &id
	}
	return nil
}
