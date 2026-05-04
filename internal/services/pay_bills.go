// 遵循project_guide.md
package services

import (
	"fmt"
	"time"

	"balanciz/internal/models"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// BillPayment is a single bill + the document-currency amount being paid.
//
// APAccountID overrides PayBillsInput.APAccountID for this specific bill.
// Pass 0 to use the top-level default.
type BillPayment struct {
	BillID      uint
	Amount      decimal.Decimal // document-currency amount to apply
	APAccountID uint            // 0 = use PayBillsInput.APAccountID
}

// PayBillsInput is the data needed to record a vendor payment across one or more bills.
//
// Phase 4: BillPayment.APAccountID allows per-bill AP account selection (required for
// foreign-currency bills that use the system-generated "ap_{code}" account).
// The top-level APAccountID serves as the default for bills where APAccountID == 0.
type PayBillsInput struct {
	CompanyID     uint
	EntryDate     time.Time
	BankAccountID uint
	APAccountID   uint          // default AP account; overridden per-bill when BillPayment.APAccountID != 0
	Bills         []BillPayment // at least one entry required
	Memo          string
	// ExchangeRateOverride, when non-zero, replaces the auto-looked-up rate for all
	// foreign-currency bills in this batch. Use when the user explicitly confirms or
	// overrides the rate (e.g. today's rate is not yet in the rate table).
	ExchangeRateOverride decimal.Decimal
}

// RecordPayBills posts a journal entry for a vendor payment and settles one or
// more bills, including automatic realized-FX posting for foreign-currency bills.
//
// Journal entry structure:
//
//	For each bill:
//	  DR AP / AP-Foreign (base)   arapBaseReleased
//	  DR/CR FX Gain/Loss (base)   ±realizedFXGainLoss  ← only when bill is foreign
//	CR Bank (base)                total bankBaseAmount   (one aggregated credit)
//
// For base-currency bills the FX gain/loss line is omitted and all amounts are equal.
//
// Returns the new journal entry ID.
func RecordPayBills(tx *gorm.DB, in PayBillsInput) (uint, error) {
	if in.CompanyID == 0 {
		return 0, fmt.Errorf("company is required")
	}
	if in.BankAccountID == 0 || in.APAccountID == 0 {
		return 0, fmt.Errorf("bank and A/P accounts are required")
	}
	if len(in.Bills) == 0 {
		return 0, fmt.Errorf("at least one bill must be selected")
	}

	// Validate bank account.
	var bank models.Account
	if err := tx.Where("id = ? AND company_id = ?", in.BankAccountID, in.CompanyID).First(&bank).Error; err != nil {
		return 0, fmt.Errorf("bank account not found")
	}
	if bank.ReportGroup() != models.AccountReportGroupAsset {
		return 0, fmt.Errorf("bank account must be an asset")
	}

	// Load company for base currency.
	var company models.Company
	if err := tx.Select("id", "base_currency_code").First(&company, in.CompanyID).Error; err != nil {
		return 0, fmt.Errorf("load company: %w", err)
	}
	baseCurrency := company.BaseCurrencyCode

	// ── Validate each bill and compute settlement amounts ─────────────────────
	type billRecord struct {
		bill            models.Bill
		apAccID         uint
		result          fxSettleResult
		overpaymentDoc  decimal.Decimal // > 0 when user paid more than balance
		overpaymentBase decimal.Decimal // base-currency equivalent of overpayment
	}
	records := make([]billRecord, 0, len(in.Bills))
	totalBankBase := decimal.Zero
	totalBankTx := decimal.Zero
	batchCurrency := ""
	cashRate := decimal.NewFromInt(1)
	mixedCurrency := false
	hasFX := false
	hasOverpayment := false

	for i, bp := range in.Bills {
		if bp.Amount.LessThanOrEqual(decimal.Zero) {
			return 0, fmt.Errorf("payment amount for bill %d must be > 0", bp.BillID)
		}

		var bill models.Bill
		if err := tx.Where("id = ? AND company_id = ?", bp.BillID, in.CompanyID).First(&bill).Error; err != nil {
			return 0, fmt.Errorf("bill %d not found", bp.BillID)
		}
		if bill.Status != models.BillStatusPosted && bill.Status != models.BillStatusPartiallyPaid {
			return 0, fmt.Errorf("bill %s is not open for payment (status: %s)", bill.BillNumber, bill.Status)
		}

		isForeign := bill.CurrencyCode != "" && bill.CurrencyCode != baseCurrency
		billCurrency := normalizeCurrencyCode(bill.CurrencyCode)
		if billCurrency == "" || billCurrency == normalizeCurrencyCode(baseCurrency) {
			billCurrency = normalizeCurrencyCode(baseCurrency)
		}
		if batchCurrency == "" {
			batchCurrency = billCurrency
		} else if batchCurrency != billCurrency {
			mixedCurrency = true
		}
		effBalance, effBalanceBase := effectiveBalances(
			bill.BalanceDue, bill.BalanceDueBase, bill.Amount, bill.AmountBase, isForeign,
		)

		// Detect overpayment: user paid more than the bill balance.
		// The excess becomes a vendor credit (DR Vendor Prepayments asset).
		overpaymentDoc := decimal.Zero
		payAmt := bp.Amount
		if payAmt.GreaterThan(effBalance) {
			overpaymentDoc = payAmt.Sub(effBalance)
			payAmt = effBalance // cap bill settlement at balance
		}

		settlementRate := decimal.NewFromInt(1)
		if isForeign {
			if in.ExchangeRateOverride.IsPositive() {
				settlementRate = in.ExchangeRateOverride
			} else {
				r, err := GetExchangeRate(tx, &in.CompanyID, bill.CurrencyCode, baseCurrency, in.EntryDate)
				if err != nil {
					return 0, fmt.Errorf("bill %s (allocation %d): exchange rate %s→%s not found for %s: %w",
						bill.BillNumber, i+1, bill.CurrencyCode, baseCurrency, in.EntryDate.Format("2006-01-02"), err)
				}
				settlementRate = r
			}
			hasFX = true
		}
		if isForeign && cashRate.Equal(decimal.NewFromInt(1)) {
			cashRate = settlementRate
		}

		result := computeAllocationAmounts(payAmt, effBalance, effBalanceBase, settlementRate)
		totalBankBase = totalBankBase.Add(result.bankBaseAmount)
		totalBankTx = totalBankTx.Add(payAmt)

		// Convert overpayment to base and add to bank credit.
		overpaymentBase := decimal.Zero
		if overpaymentDoc.IsPositive() {
			overpaymentBase = overpaymentDoc.Mul(settlementRate).Round(2)
			totalBankBase = totalBankBase.Add(overpaymentBase)
			totalBankTx = totalBankTx.Add(overpaymentDoc)
			hasOverpayment = true
		}

		apAccID := bp.APAccountID
		if apAccID == 0 {
			apAccID = in.APAccountID
		}
		// Validate AP account.
		var ap models.Account
		if err := tx.Where("id = ? AND company_id = ?", apAccID, in.CompanyID).First(&ap).Error; err != nil {
			return 0, fmt.Errorf("A/P account not found for bill %s", bill.BillNumber)
		}
		if ap.ReportGroup() != models.AccountReportGroupLiability {
			return 0, fmt.Errorf("A/P account must be a liability for bill %s", bill.BillNumber)
		}

		records = append(records, billRecord{
			bill:            bill,
			apAccID:         apAccID,
			result:          result,
			overpaymentDoc:  overpaymentDoc,
			overpaymentBase: overpaymentBase,
		})
	}

	if totalBankBase.LessThanOrEqual(decimal.Zero) {
		return 0, fmt.Errorf("total payment must be > 0")
	}
	if batchCurrency == "" {
		batchCurrency = baseCurrency
	}
	if mixedCurrency && bank.CurrencyMode == models.CurrencyModeFixedForeign {
		return 0, fmt.Errorf("foreign-currency bank payments must use bills in a single currency")
	}
	cash, err := resolveCashPostingCurrency(tx, in.CompanyID, in.BankAccountID, batchCurrency, cashRate)
	if err != nil {
		return 0, err
	}
	if !cash.BankIsForeign {
		totalBankTx = totalBankBase
	}

	// Ensure FX gain/loss account exists (only when needed).
	var fxAccountID uint
	if hasFX {
		id, err := EnsureFXGainLossAccount(tx, in.CompanyID)
		if err != nil {
			return 0, err
		}
		fxAccountID = id
	}

	// Ensure Vendor Prepayments asset account exists (only when needed).
	var vendorPrepayAccountID uint
	if hasOverpayment {
		id, err := EnsureVendorPrepaymentAccount(tx, in.CompanyID)
		if err != nil {
			return 0, err
		}
		vendorPrepayAccountID = id
	}

	// ── Build journal fragments ───────────────────────────────────────────────
	frags := make([]PostingFragment, 0, 1+len(records)*3)

	totalFXGainLoss := decimal.Zero
	for _, rec := range records {
		// AP debit at carrying value.
		frags = append(frags, PostingFragment{
			AccountID: rec.apAccID,
			Debit:     rec.result.arapBaseReleased,
			Credit:    decimal.Zero,
			Memo:      "Bill " + rec.bill.BillNumber,
		})
		totalFXGainLoss = totalFXGainLoss.Add(rec.result.realizedFXGainLoss)

		// Overpayment: DR Vendor Prepayments (excess amount → asset).
		if rec.overpaymentBase.IsPositive() {
			frags = append(frags, PostingFragment{
				AccountID: vendorPrepayAccountID,
				Debit:     rec.overpaymentBase,
				Credit:    decimal.Zero,
				Memo:      "Vendor prepayment – " + rec.bill.BillNumber,
			})
		}
	}

	// Single aggregated FX line (all allocations combined).
	//
	// AP sign convention: realizedFXGainLoss = bankBaseAmount − arapBaseReleased.
	//   Rate fell → bank < arap → result is NEGATIVE → company GAINED (paid less).
	//   Rate rose → bank > arap → result is POSITIVE → company LOST (paid more).
	// This is the inverse of the AR (invoice) sign, so the posting direction is reversed.
	if hasFX && !totalFXGainLoss.IsZero() {
		if totalFXGainLoss.IsNegative() {
			// Net gain: rate fell → paid less than carrying → credit FX income account.
			frags = append(frags, PostingFragment{
				AccountID: fxAccountID,
				Debit:     decimal.Zero,
				Credit:    totalFXGainLoss.Neg(),
				Memo:      "Realized FX gain/loss",
			})
		} else {
			// Net loss: rate rose → paid more than carrying → debit FX loss account.
			frags = append(frags, PostingFragment{
				AccountID: fxAccountID,
				Debit:     totalFXGainLoss,
				Credit:    decimal.Zero,
				Memo:      "Realized FX gain/loss",
			})
		}
	}

	// Aggregated bank credit (one line for the whole payment).
	frags = append(frags, PostingFragment{
		AccountID: in.BankAccountID,
		Debit:     decimal.Zero,
		Credit:    totalBankBase,
		Memo:      in.Memo,
	})

	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return 0, fmt.Errorf("aggregate journal lines: %w", err)
	}

	// ── Create journal entry ──────────────────────────────────────────────────
	je := models.JournalEntry{
		CompanyID:               in.CompanyID,
		EntryDate:               in.EntryDate,
		JournalNo:               "Pay Bills",
		Status:                  models.JournalEntryStatusPosted,
		TransactionCurrencyCode: cash.TransactionCurrencyCode,
		ExchangeRate:            cash.ExchangeRate,
		ExchangeRateDate:        in.EntryDate,
		ExchangeRateSource:      cashExchangeRateSource(cash),
		SourceType:              models.LedgerSourcePayment,
	}
	if err := tx.Create(&je).Error; err != nil {
		return 0, err
	}

	createdLines := make([]models.JournalLine, 0, len(jeLines))
	for _, frag := range jeLines {
		line := models.JournalLine{
			CompanyID:      in.CompanyID,
			JournalEntryID: je.ID,
			AccountID:      frag.AccountID,
			TxDebit:        frag.Debit,
			TxCredit:       frag.Credit,
			Debit:          frag.Debit,
			Credit:         frag.Credit,
			Memo:           frag.Memo,
		}
		if frag.AccountID == in.BankAccountID {
			line.TxDebit = decimal.Zero
			line.TxCredit = totalBankTx.Round(2)
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

	// ── Create settlement allocations + update bills ──────────────────────────
	for _, rec := range records {
		alloc := models.SettlementAllocation{
			CompanyID:          in.CompanyID,
			JournalEntryID:     je.ID,
			DocumentType:       models.SettlementDocBill,
			DocumentID:         rec.bill.ID,
			AmountApplied:      rec.result.amountApplied,
			ARAPBaseReleased:   rec.result.arapBaseReleased,
			BankBaseAmount:     rec.result.bankBaseAmount,
			RealizedFXGainLoss: rec.result.realizedFXGainLoss,
			SettlementRate:     rec.result.settlementRate,
		}
		if err := tx.Create(&alloc).Error; err != nil {
			return 0, fmt.Errorf("create settlement allocation: %w", err)
		}

		isForeign := rec.bill.CurrencyCode != "" && rec.bill.CurrencyCode != baseCurrency
		effBalance, effBalanceBase := effectiveBalances(
			rec.bill.BalanceDue, rec.bill.BalanceDueBase, rec.bill.Amount, rec.bill.AmountBase, isForeign,
		)
		newBalance := effBalance.Sub(rec.result.amountApplied)
		newBalanceBase := effBalanceBase.Sub(rec.result.arapBaseReleased)

		var newStatus models.BillStatus
		if newBalance.LessThanOrEqual(decimal.Zero) {
			newStatus = models.BillStatusPaid
			newBalance = decimal.Zero
			newBalanceBase = decimal.Zero
		} else {
			newStatus = models.BillStatusPartiallyPaid
		}
		if err := tx.Model(&rec.bill).Updates(map[string]any{
			"balance_due":      newBalance,
			"balance_due_base": newBalanceBase,
			"status":           newStatus,
		}).Error; err != nil {
			return 0, err
		}
	}

	// ── Create vendor credits for any overpayments ───────────────────────────
	for _, rec := range records {
		if !rec.overpaymentDoc.IsPositive() {
			continue
		}
		billID := rec.bill.ID
		vc := models.VendorCredit{
			CompanyID:            in.CompanyID,
			VendorID:             rec.bill.VendorID,
			SourceJournalEntryID: je.ID,
			SourceBillID:         &billID,
			OriginalAmount:       rec.overpaymentDoc,
			RemainingAmount:      rec.overpaymentDoc,
			CurrencyCode:         rec.bill.CurrencyCode,
			Status:               models.VendorCreditActive,
		}
		if err := tx.Create(&vc).Error; err != nil {
			return 0, fmt.Errorf("create vendor credit for bill %s: %w", rec.bill.BillNumber, err)
		}
	}

	return je.ID, nil
}
