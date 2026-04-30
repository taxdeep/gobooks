// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
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

// DepositApplication consumes part of an existing CustomerDeposit to offset
// one or more invoices in the same Receive Payment session.
//
// Amount is the document-currency amount drawn from the deposit's
// BalanceRemaining. It is automatically distributed pro-rata across the
// invoice allocations in the same call so each consumed-deposit leaves a
// CustomerDepositApplication row per (deposit, invoice) pair for audit.
type DepositApplication struct {
	DepositID uint
	Amount    decimal.Decimal
}

// CreditNoteConsumption pulls part of an existing CreditNote's
// BalanceRemaining to offset invoices in the same session.
//
// Unlike Deposit, CN apply does not generate any JE line of its own —
// the original CN posting already credited AR by the full CN amount, so
// applying CN to an invoice is purely a sub-ledger move. The unified
// recipe handles this by *reducing* each invoice's CR-AR line by its
// pro-rated share of CN consumed (see recordReceivePaymentAllocations
// for the math). One CreditNoteApplication row is written per
// (cn, invoice) pair for audit.
type CreditNoteConsumption struct {
	CreditNoteID uint
	Amount       decimal.Decimal
}

// ReceivePaymentInput is the data needed to record a customer receipt.
//
// Three settlement modes:
//
//  1. Allocations (Phase 5 — multi-document): set any of Allocations / Deposits
//     / NewDepositAmount. Supports pure cash payment, pure offset (bank=0 when
//     deposit fully covers the invoices), mixed, or overpayment creating a new
//     Customer Deposit. See package doc for the unified JE recipe.
//
//  2. Legacy single-invoice: set InvoiceID + Amount; full settlement only.
//     Used by deep-link payment from /invoices/:id. Preserved unchanged so the
//     invoice-detail flow keeps working.
//
//  3. Unlinked (legacy): InvoiceID nil + Amount > 0. Simple 2-line JE.
type ReceivePaymentInput struct {
	CompanyID  uint
	CustomerID uint
	EntryDate  time.Time

	BankAccountID uint
	PaymentMethod models.PaymentMethod
	// ARAccountID is the default AR account to credit.
	// Can be overridden per-allocation via InvoiceAllocation.ARAccountID.
	ARAccountID uint

	// Allocations are invoice payments in this session (positive amounts).
	// Required when any of Deposits / NewDepositAmount are set.
	Allocations []InvoiceAllocation

	// Deposits consume existing CustomerDeposits (money we already hold on
	// behalf of the customer). The aggregated consumption must be ≤ total
	// invoice allocation amount — no Case where deposits exceed invoices
	// (that would require refunding the customer, which is a separate flow).
	Deposits []DepositApplication

	// CreditNotes consume existing CreditNote balances. Same constraint
	// as Deposits — combined CN+Deposit consumption can't exceed total
	// invoice allocations (the bank line would go negative).
	CreditNotes []CreditNoteConsumption

	// NewDepositAmount creates a new CustomerDeposit in the same JE — used
	// when the operator wants to record an overpayment as a future credit.
	// The bank debit absorbs this amount; Customer Deposits liability is
	// credited. Document currency must match the customer's currency.
	NewDepositAmount decimal.Decimal

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

	// Route to the unified allocation path when the receipt touches any
	// document-style AR object. A standalone NewDepositAmount is the
	// Receive Payment "customer paid, do not apply yet" path.
	if len(in.Allocations) > 0 || len(in.Deposits) > 0 || len(in.CreditNotes) > 0 || in.NewDepositAmount.IsPositive() {
		return recordReceivePaymentAllocations(tx, in)
	}

	// Legacy / unlinked path — behaviour identical to pre-Phase-4.
	return recordReceivePaymentLegacy(tx, in)
}

// invoiceProRata splits a single document-currency total (Σ CN or Σ deposit
// consumed in this session) across the session's invoices pro-rata by each
// invoice's amountApplied. Rounding drift lands on the last index so the
// slice always sums exactly to `total`.
//
// Returns a slice the same length as `invoiceAmounts`; all zeros when
// `total` is zero. Caller passes `[]decimal.Decimal` of the per-invoice
// `amountApplied` values in the same order as the records slice so the
// returned shares line up positionally.
func invoiceProRata(invoiceAmounts []decimal.Decimal, totalInvoiceDoc, total decimal.Decimal) []decimal.Decimal {
	out := make([]decimal.Decimal, len(invoiceAmounts))
	if !total.IsPositive() || !totalInvoiceDoc.IsPositive() {
		return out
	}
	remaining := total
	for i, amt := range invoiceAmounts {
		if i == len(invoiceAmounts)-1 {
			out[i] = remaining
			continue
		}
		share := total.Mul(amt).Div(totalInvoiceDoc).Round(2)
		if share.GreaterThan(remaining) {
			share = remaining
		}
		out[i] = share
		remaining = remaining.Sub(share)
	}
	return out
}

// ── Phase-5 allocation path (unified invoice + deposit + credit-note + new-deposit) ──
//
// JE recipe (see design note 2026-04-24, updated 2026-04-25 for auto-overage):
//
//	Let:
//	  I_raw = Σ raw invoice Payment values entered by the operator
//	  I     = Σ capped invoice settlement (each row capped at its balance)
//	  O     = Σ row overage = I_raw − I (excess auto-rolled into new deposit)
//	  C     = Σ credit-note consumption
//	  D     = Σ deposit consumption
//	  N_in  = explicit Extra → New Deposit field
//	  N     = N_in + O                            (total new deposit liability)
//	  B     = I + O − C − D + N_in = I_raw − C − D + N_in   (bank received)
//
//	Lines:
//	  DR Bank                  B                          (B > 0)
//	  DR Customer Deposits     D_i                        (one per consumed deposit)
//	    CR AR                    I_i − cn_share_i         (per invoice, net of CN absorbed)
//	    CR Customer Deposits     N                        (new deposit from overpayment)
//
// CN apply does NOT post a DR line of its own — the original CN posting
// already CR'd AR by the full CN amount, so applying CN to an invoice is a
// pure sub-ledger move. We bake that into the JE by *shrinking* each
// invoice's CR-AR by its pro-rated share of CN consumed in this session.
//
// Balance: DR = B + ΣD = (I − C − D + N) + D = I − C + N
//          CR = Σ(I_i − cn_share_i) + N = (I − C) + N = I − C + N ✓
//
// Foreign currency support mirrors the pre-Phase-5 path — FX gain/loss
// posts to a single aggregated line. CN / Deposit consumption and new
// deposit must match the customer's currency (cross-currency apply is
// rejected).

func recordReceivePaymentAllocations(tx *gorm.DB, in ReceivePaymentInput) (uint, error) {
	// Validate bank account.
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

	// ── Validate invoice allocations ──────────────────────────────────────────
	//
	// Row-level overpayment (alloc.Amount > effBalance) is auto-split: the
	// invoice is settled at its balance and the excess is routed into the
	// session's new-deposit total. The operator can also use the explicit
	// NewDepositAmount field — both feed the same Customer Deposits row.
	// Foreign-currency invoices still reject overpayment (FX semantics on
	// the over-portion are out of scope for v1).
	type invoiceRecord struct {
		inv     models.Invoice
		arAccID uint
		result  fxSettleResult
		// rowOverageDoc is the (alloc.Amount − effBalance) excess that gets
		// routed into the session's new Customer Deposit. Document currency.
		rowOverageDoc decimal.Decimal
	}
	records := make([]invoiceRecord, 0, len(in.Allocations))
	totalInvoiceDoc := decimal.Zero    // capped — used for invoice retirement math
	totalInvoiceBase := decimal.Zero   // capped — used for AR-credit math
	totalRowOverageDoc := decimal.Zero // sum of auto-overage across rows; rolls into new deposit
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

		// Row-level overpayment: cap invoice settlement at balance, route the
		// excess into the session's new-deposit total. FX overpayment is out
		// of scope for v1.
		appliedDoc := alloc.Amount
		rowOverageDoc := decimal.Zero
		if alloc.Amount.GreaterThan(effBalance) {
			if isForeign {
				return 0, fmt.Errorf("allocation %d: overpayment on foreign-currency invoice %s is not supported — cap payment at balance %s",
					i+1, inv.InvoiceNumber, effBalance.StringFixed(2))
			}
			appliedDoc = effBalance
			rowOverageDoc = alloc.Amount.Sub(effBalance)
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

		// Drive computeAllocationAmounts with the *capped* amount so the
		// invoice settles cleanly. The row overage is tracked separately
		// below and rolled into the new-deposit total.
		result := computeAllocationAmounts(appliedDoc, effBalance, effBalanceBase, settlementRate)

		arAccID := alloc.ARAccountID
		if arAccID == 0 {
			arAccID = in.ARAccountID
		}

		records = append(records, invoiceRecord{
			inv:           inv,
			arAccID:       arAccID,
			result:        result,
			rowOverageDoc: rowOverageDoc,
		})
		totalInvoiceDoc = totalInvoiceDoc.Add(result.amountApplied)
		totalInvoiceBase = totalInvoiceBase.Add(result.arapBaseReleased)
		totalRowOverageDoc = totalRowOverageDoc.Add(rowOverageDoc)
	}

	// ── Validate deposit applications ─────────────────────────────────────────
	type depositRecord struct {
		dep        models.CustomerDeposit
		appliedAmt decimal.Decimal // document currency; consumed from this deposit
	}
	depositRecords := make([]depositRecord, 0, len(in.Deposits))
	totalDepositDoc := decimal.Zero

	for i, da := range in.Deposits {
		if da.Amount.LessThanOrEqual(decimal.Zero) {
			return 0, fmt.Errorf("deposit application %d: amount must be > 0", i+1)
		}
		var dep models.CustomerDeposit
		if err := tx.Where("id = ? AND company_id = ?", da.DepositID, in.CompanyID).First(&dep).Error; err != nil {
			return 0, fmt.Errorf("deposit application %d: deposit not found", i+1)
		}
		if dep.CustomerID != in.CustomerID {
			return 0, fmt.Errorf("deposit application %d: deposit belongs to a different customer", i+1)
		}
		switch dep.Status {
		case models.CustomerDepositStatusPosted, models.CustomerDepositStatusPartiallyApplied:
		default:
			return 0, fmt.Errorf("deposit application %d: deposit %s is not available for apply (status: %s)",
				i+1, dep.DepositNumber, dep.Status)
		}
		if da.Amount.GreaterThan(dep.BalanceRemaining) {
			return 0, fmt.Errorf("deposit application %d: amount %s exceeds deposit %s remaining balance %s",
				i+1, da.Amount.StringFixed(2), dep.DepositNumber, dep.BalanceRemaining.StringFixed(2))
		}
		// v1 constraint: deposit and invoices must share the customer's currency.
		// Mixed-currency apply is a separate slice (needs FX rate decision).
		for _, rec := range records {
			if rec.inv.CurrencyCode != dep.CurrencyCode {
				return 0, fmt.Errorf("deposit application %d: deposit %s currency %q does not match invoice %s currency %q — cross-currency apply not supported in v1",
					i+1, dep.DepositNumber, dep.CurrencyCode, rec.inv.InvoiceNumber, rec.inv.CurrencyCode)
			}
		}
		depositRecords = append(depositRecords, depositRecord{dep: dep, appliedAmt: da.Amount})
		totalDepositDoc = totalDepositDoc.Add(da.Amount)
	}

	// Deposit consumption cannot exceed invoice applications — that would
	// mean "I want to refund the customer" which is the Refund flow, not
	// Receive Payment.
	if totalDepositDoc.GreaterThan(totalInvoiceDoc) {
		return 0, fmt.Errorf("deposit consumption %s exceeds invoice allocations %s — excess deposit credit cannot be refunded from this form",
			totalDepositDoc.StringFixed(2), totalInvoiceDoc.StringFixed(2))
	}

	// ── Validate credit-note consumption ──────────────────────────────────────
	type cnRecord struct {
		cn       models.CreditNote
		consumed decimal.Decimal // document currency consumed in this session
	}
	cnRecords := make([]cnRecord, 0, len(in.CreditNotes))
	totalCNDoc := decimal.Zero
	for i, cnApp := range in.CreditNotes {
		if cnApp.Amount.LessThanOrEqual(decimal.Zero) {
			return 0, fmt.Errorf("credit note application %d: amount must be > 0", i+1)
		}
		var cn models.CreditNote
		if err := tx.Where("id = ? AND company_id = ?", cnApp.CreditNoteID, in.CompanyID).First(&cn).Error; err != nil {
			return 0, fmt.Errorf("credit note application %d: credit note not found", i+1)
		}
		if cn.CustomerID != in.CustomerID {
			return 0, fmt.Errorf("credit note application %d: credit note belongs to a different customer", i+1)
		}
		switch cn.Status {
		case models.CreditNoteStatusIssued, models.CreditNoteStatusPartiallyApplied:
		default:
			return 0, fmt.Errorf("credit note application %d: credit note %s is not available for apply (status: %s)",
				i+1, cn.CreditNoteNumber, cn.Status)
		}
		if cnApp.Amount.GreaterThan(cn.BalanceRemaining) {
			return 0, fmt.Errorf("credit note application %d: amount %s exceeds CN %s remaining balance %s",
				i+1, cnApp.Amount.StringFixed(2), cn.CreditNoteNumber, cn.BalanceRemaining.StringFixed(2))
		}
		// Same currency-match guard as deposit: cross-currency apply is
		// out of scope for v1 (see existing CN apply behaviour).
		for _, rec := range records {
			if rec.inv.CurrencyCode != cn.CurrencyCode {
				return 0, fmt.Errorf("credit note application %d: CN %s currency %q does not match invoice %s currency %q — cross-currency apply not supported",
					i+1, cn.CreditNoteNumber, cn.CurrencyCode, rec.inv.InvoiceNumber, rec.inv.CurrencyCode)
			}
		}
		cnRecords = append(cnRecords, cnRecord{cn: cn, consumed: cnApp.Amount})
		totalCNDoc = totalCNDoc.Add(cnApp.Amount)
	}

	// CN consumption + deposit consumption can't combined exceed invoices
	// (would push bank negative — covered by the bank-amount check below
	// but surface a clearer message here for the common single-input mistake).
	if totalCNDoc.GreaterThan(totalInvoiceDoc) {
		return 0, fmt.Errorf("credit note consumption %s exceeds invoice allocations %s — apply only what the invoices can absorb",
			totalCNDoc.StringFixed(2), totalInvoiceDoc.StringFixed(2))
	}

	// ── Validate new-deposit amount ───────────────────────────────────────────
	if in.NewDepositAmount.IsNegative() {
		return 0, fmt.Errorf("new deposit amount must be ≥ 0")
	}
	// effectiveNewDepositDoc combines the operator's explicit Extra → New
	// Deposit field with any auto-overage from invoice rows (when the user
	// types Payment > balance, the excess folds into the same deposit).
	effectiveNewDepositDoc := in.NewDepositAmount.Add(totalRowOverageDoc)

	// New deposit currency follows the customer — check consistency with
	// any invoices in the session (base-only is fine since Customer.CurrencyCode
	// governs downstream).
	newDepositCurrency := ""
	if len(records) > 0 {
		newDepositCurrency = records[0].inv.CurrencyCode
	} else if len(depositRecords) > 0 {
		newDepositCurrency = depositRecords[0].dep.CurrencyCode
	}
	// For FX consistency: v1 forces N to be base currency only — see
	// design note. Mixed FX + overpayment is too many knobs at once.
	if effectiveNewDepositDoc.IsPositive() && newDepositCurrency != "" && newDepositCurrency != baseCurrency {
		return 0, fmt.Errorf("creating a new deposit on a foreign-currency receive payment is not supported in v1")
	}

	// ── Compute bank amount (base currency) ───────────────────────────────────
	//
	// Bank receives the customer's full cash hand-over:
	//   = Σ invoice.bankBaseAmount (capped) + Σ row-overage − CN − Deposit + explicit-N
	// which equals raw Σ alloc.Amount − CN − Deposit + explicit-N. Adding the
	// auto-overage back recovers the user's original Payment-column intent.
	sumInvoiceBankBase := decimal.Zero
	totalFXGainLoss := decimal.Zero
	for _, rec := range records {
		sumInvoiceBankBase = sumInvoiceBankBase.Add(rec.result.bankBaseAmount)
		totalFXGainLoss = totalFXGainLoss.Add(rec.result.realizedFXGainLoss)
	}
	// Deposits + CN + new deposit are base currency in v1 (guarded above).
	bankBase := sumInvoiceBankBase.
		Add(totalRowOverageDoc).
		Sub(totalCNDoc).
		Sub(totalDepositDoc).
		Add(in.NewDepositAmount)
	if bankBase.IsNegative() {
		return 0, fmt.Errorf("bank amount is negative (%s) — credit-note + deposit consumption cannot exceed invoice allocations", bankBase.StringFixed(2))
	}

	// Sanity: at least one document side must have value. Pure-zero all
	// around means the caller sent an empty form.
	if totalInvoiceDoc.IsZero() && effectiveNewDepositDoc.IsZero() {
		return 0, fmt.Errorf("receive payment must include at least one invoice allocation or a new deposit amount")
	}

	// ── Ensure system accounts needed by the JE ───────────────────────────────
	var fxAccountID uint
	if hasFX {
		id, err := EnsureFXGainLossAccount(tx, in.CompanyID)
		if err != nil {
			return 0, err
		}
		fxAccountID = id
	}
	var customerDepositsAccID uint
	if len(depositRecords) > 0 || effectiveNewDepositDoc.IsPositive() {
		id, err := EnsureCustomerDepositsAccount(tx, in.CompanyID)
		if err != nil {
			return 0, err
		}
		customerDepositsAccID = id
	}

	// ── Build journal fragments ───────────────────────────────────────────────
	frags := make([]PostingFragment, 0, 2+len(records)*2+len(depositRecords))

	// Bank DR (only if there's cash movement; Case B pure offset has B=0).
	if bankBase.IsPositive() {
		frags = append(frags, PostingFragment{
			AccountID: in.BankAccountID,
			Debit:     bankBase,
			Credit:    decimal.Zero,
			Memo:      in.Memo,
		})
	}

	// Customer Deposits DR per consumed deposit — releases the liability.
	for _, dr := range depositRecords {
		frags = append(frags, PostingFragment{
			AccountID: customerDepositsAccID,
			Debit:     dr.appliedAmt,
			Credit:    decimal.Zero,
			Memo:      "Deposit " + dr.dep.DepositNumber,
		})
	}

	// AR CR per invoice (base currency), shrunk by this invoice's pro-rated
	// share of CN consumed in the session. The CN's original posting
	// already CR'd AR for the full CN amount, so the apply is a sub-ledger
	// reshuffle — we don't post a DR-AR line for the CN release; instead
	// we just CR less AR for the invoice.
	invoiceAmts := make([]decimal.Decimal, len(records))
	for idx, rec := range records {
		invoiceAmts[idx] = rec.result.amountApplied
	}
	cnSharePerInvoice := invoiceProRata(invoiceAmts, totalInvoiceDoc, totalCNDoc)
	for idx, rec := range records {
		credit := rec.result.arapBaseReleased.Sub(cnSharePerInvoice[idx])
		if credit.IsNegative() {
			credit = decimal.Zero
		}
		if credit.IsPositive() {
			frags = append(frags, PostingFragment{
				AccountID: rec.arAccID,
				Debit:     decimal.Zero,
				Credit:    credit,
				Memo:      "Invoice " + rec.inv.InvoiceNumber,
			})
		}
	}

	// New deposit CR — liability for the newly-held customer money.
	// Combines the explicit Extra → New Deposit field with any auto-overage
	// rolled in from rows where Payment > balance.
	if effectiveNewDepositDoc.IsPositive() {
		frags = append(frags, PostingFragment{
			AccountID: customerDepositsAccID,
			Debit:     decimal.Zero,
			Credit:    effectiveNewDepositDoc,
			Memo:      "Customer deposit (overpayment)",
		})
	}

	// Single aggregated FX line.
	if hasFX && !totalFXGainLoss.IsZero() {
		if totalFXGainLoss.IsPositive() {
			frags = append(frags, PostingFragment{
				AccountID: fxAccountID,
				Debit:     decimal.Zero,
				Credit:    totalFXGainLoss,
				Memo:      "Realized FX gain/loss",
			})
		} else {
			frags = append(frags, PostingFragment{
				AccountID: fxAccountID,
				Debit:     totalFXGainLoss.Neg(),
				Credit:    decimal.Zero,
				Memo:      "Realized FX gain/loss",
			})
		}
	}

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
	//
	// Edge case: pure CN-offset (Case E) results in zero JE lines because
	// the CN's prior posting already moved AR. The header JE still gets
	// created so SettlementAllocation / CreditNoteApplication rows have
	// something to anchor to, but lines + ledger projection are skipped
	// (the projector rejects empty journal entries).
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
	if len(createdLines) > 0 {
		if err := ProjectToLedger(tx, in.CompanyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourcePayment,
		}); err != nil {
			return 0, fmt.Errorf("project payment to ledger: %w", err)
		}
	}
	if err := createPaymentReceipt(tx, in, je.ID, bankBase); err != nil {
		return 0, fmt.Errorf("create payment receipt: %w", err)
	}

	// ── Settlement allocations + invoice updates ──────────────────────────────
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

	// ── Consume deposits: pro-rata split across invoices, write apps ──────────
	//
	// Each deposit application row ties (deposit, invoice) with the share
	// of that deposit that went to that invoice. Pro-rata by invoice amount
	// applied; rounding drift flows into the last row so the sum matches
	// the deposit consumption exactly.
	appliedAt := time.Now()
	for _, dr := range depositRecords {
		if totalInvoiceDoc.IsZero() {
			break
		}
		shares := invoiceProRata(invoiceAmts, totalInvoiceDoc, dr.appliedAmt)
		for idx, rec := range records {
			share := shares[idx]
			if !share.IsPositive() {
				continue
			}
			app := models.CustomerDepositApplication{
				CompanyID:         in.CompanyID,
				CustomerDepositID: dr.dep.ID,
				InvoiceID:         rec.inv.ID,
				AmountApplied:     share,
				AmountAppliedBase: share,
				JournalEntryID:    &je.ID,
				AppliedAt:         appliedAt,
			}
			if err := tx.Create(&app).Error; err != nil {
				return 0, fmt.Errorf("create deposit application (deposit %s → invoice %s): %w",
					dr.dep.DepositNumber, rec.inv.InvoiceNumber, err)
			}
		}

		newRemaining := dr.dep.BalanceRemaining.Sub(dr.appliedAmt)
		var newStatus models.CustomerDepositStatus
		if newRemaining.LessThanOrEqual(decimal.Zero) {
			newStatus = models.CustomerDepositStatusFullyApplied
			newRemaining = decimal.Zero
		} else {
			newStatus = models.CustomerDepositStatusPartiallyApplied
		}
		if err := tx.Model(&dr.dep).Updates(map[string]any{
			"balance_remaining": newRemaining,
			"status":            newStatus,
		}).Error; err != nil {
			return 0, fmt.Errorf("update deposit %s: %w", dr.dep.DepositNumber, err)
		}
	}

	// ── Consume credit notes: pro-rata + apps + decrement CN balance ──────────
	for _, cnr := range cnRecords {
		if totalInvoiceDoc.IsZero() {
			break
		}
		shares := invoiceProRata(invoiceAmts, totalInvoiceDoc, cnr.consumed)
		for idx, rec := range records {
			share := shares[idx]
			if !share.IsPositive() {
				continue
			}
			app := models.CreditNoteApplication{
				CompanyID:         in.CompanyID,
				CreditNoteID:      cnr.cn.ID,
				InvoiceID:         rec.inv.ID,
				AmountApplied:     share,
				AmountAppliedBase: share,
				AppliedAt:         appliedAt,
			}
			if err := tx.Create(&app).Error; err != nil {
				return 0, fmt.Errorf("create credit note application (CN %s → invoice %s): %w",
					cnr.cn.CreditNoteNumber, rec.inv.InvoiceNumber, err)
			}
		}

		newRemaining := cnr.cn.BalanceRemaining.Sub(cnr.consumed)
		var newStatus models.CreditNoteStatus
		if newRemaining.LessThanOrEqual(decimal.Zero) {
			newStatus = models.CreditNoteStatusFullyApplied
			newRemaining = decimal.Zero
		} else {
			newStatus = models.CreditNoteStatusPartiallyApplied
		}
		if err := tx.Model(&cnr.cn).Updates(map[string]any{
			"balance_remaining": newRemaining,
			"status":            newStatus,
		}).Error; err != nil {
			return 0, fmt.Errorf("update credit note %s: %w", cnr.cn.CreditNoteNumber, err)
		}
	}

	// ── New deposit creation (overpayment) ───────────────────────────────────
	if effectiveNewDepositDoc.IsPositive() {
		depNumber, err := SuggestNextCustomerDepositNumber(tx, in.CompanyID)
		if err != nil {
			return 0, fmt.Errorf("suggest deposit number: %w", err)
		}
		dep := models.CustomerDeposit{
			CompanyID:                 in.CompanyID,
			CustomerID:                in.CustomerID,
			JournalEntryID:            &je.ID,
			BankAccountID:             &in.BankAccountID,
			DepositLiabilityAccountID: &customerDepositsAccID,
			DepositNumber:             depNumber,
			Status:                    models.CustomerDepositStatusPosted,
			Source:                    models.DepositSourceOverpayment,
			DepositDate:               in.EntryDate,
			CurrencyCode:              "", // base currency v1
			ExchangeRate:              decimal.NewFromInt(1),
			Amount:                    effectiveNewDepositDoc,
			AmountBase:                effectiveNewDepositDoc,
			BalanceRemaining:          effectiveNewDepositDoc,
			PaymentMethod:             in.PaymentMethod,
			Memo:                      in.Memo,
		}
		if err := tx.Create(&dep).Error; err != nil {
			return 0, fmt.Errorf("create overpayment deposit: %w", err)
		}
		if err := BumpCustomerDepositNextNumberAfterCreate(tx, in.CompanyID); err != nil {
			return 0, fmt.Errorf("bump deposit counter: %w", err)
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
