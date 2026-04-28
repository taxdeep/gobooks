// 遵循project_guide.md
package services

// gateway_settlement_service.go — Payment settlement bridge.
//
// Converts a verified hosted payment (gateway-side truth) into Balanciz
// invoice settlement truth (accounting-side truth) under strict eligibility rules.
//
// ─── Why this is not reconciliation ─────────────────────────────────────────
// This bridge handles exactly one scenario:
//   exact-match full settlement of a single hosted invoice via a verified gateway payment
//
// Not in scope: partial payments, multi-invoice allocation, overpayments, refunds,
// disputes, processor-fee settlement, bank payout matching, or FX settlement.
//
// ─── Settlement eligibility rules ───────────────────────────────────────────
//  1. Attempt status == payment_succeeded (webhook-verified; not browser-only)
//  2. Invoice company matches attempt company (isolation)
//  3. Invoice status is payable: issued | sent | overdue | partially_paid
//  4. attempt.Amount == invoice.BalanceDue (exact match — no partial auto-settlement)
//  5. Currency match: attempt.CurrencyCode == effective invoice currency
//  6. PaymentTransaction not already posted (PostedJournalEntryID == nil)
//  7. PaymentTransaction not already applied (AppliedInvoiceID == nil)
//  8. PaymentAccountingMapping.ClearingAccountID configured for the gateway
//  9. Active AR account exists in the company chart of accounts
// 10. No GatewaySettlement already exists for this attempt (idempotency check)
//
// If any rule fails, settlement is skipped with an explicit reason. No mutation occurs.
// The PaymentTransaction remains unposted/unapplied — visible to operators for
// manual post+apply via the existing payment gateway UI.
//
// ─── Accounting effect ───────────────────────────────────────────────────────
// On eligible settlement, one atomic transaction:
//   Dr  GW Clearing account (PaymentAccountingMapping.ClearingAccountID)
//   Cr  Accounts Receivable (first active AR account for company)
//   Update PaymentTransaction: PostedJournalEntryID, PostedAt, AppliedInvoiceID, AppliedAt
//   Update Invoice: BalanceDue=0, BalanceDueBase=0, Status=paid
//   Insert GatewaySettlement (idempotency anchor)
//
// JournalNo: "GWSETTLE-" + attempt.ProviderRef
// SourceType: LedgerSourcePaymentGateway
// SourceID: PaymentTransaction.ID
//
// ─── Idempotency guarantee ───────────────────────────────────────────────────
// Primary: GatewaySettlement uniqueIndex(hosted_attempt_id)
//   → inserting twice fires unique constraint → detect and return existing record
// Secondary: check PaymentTransaction.PostedJournalEntryID == nil inside transaction
// Tertiary: check PaymentTransaction.AppliedInvoiceID == nil inside transaction
// All three checks happen under a DB transaction with SELECT FOR UPDATE on the invoice.
//
// ─── Trigger ────────────────────────────────────────────────────────────────
// ExecuteGatewaySettlement is called from tryAutoSettleAfterIngestion after
// ingestCheckoutSessionCompleted commits. The ingestion transaction and the
// settlement transaction are separate — webhook ingestion always succeeds even if
// settlement is ineligible or config is missing.

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	// ErrSettlementAttemptNotFound is returned when the attempt ID does not exist
	// for the given company (e.g. wrong companyID passed).
	ErrSettlementAttemptNotFound = errors.New("hosted payment attempt not found for settlement")

	// ErrSettlementAlreadyDone is returned from ExecuteGatewaySettlement when
	// a GatewaySettlement already exists for the attempt.
	// This is the idempotency sentinel — callers should treat it as success.
	ErrSettlementAlreadyDone = errors.New("payment has already been settled (idempotent)")
)

// ── Eligibility ───────────────────────────────────────────────────────────────

// GatewaySettlementEligibility is the result of the eligibility check.
// Eligible=false with a non-empty Reason means auto-settlement is skipped.
// The verified payment remains traceable via HostedPaymentAttempt + PaymentTransaction.
type GatewaySettlementEligibility struct {
	Eligible bool
	Reason   string // non-empty when Eligible=false
}

func ineligible(reason string) GatewaySettlementEligibility {
	return GatewaySettlementEligibility{Eligible: false, Reason: reason}
}

var eligibleResult = GatewaySettlementEligibility{Eligible: true}

// EvaluateGatewaySettlementEligibility checks all settlement rules synchronously.
//
// Parameters:
//   - attempt: the HostedPaymentAttempt to evaluate
//   - inv: the Invoice linked to the attempt
//   - txn: the PaymentTransaction (charge) linked via PaymentRequest; nil = not found
//   - mapping: PaymentAccountingMapping for the gateway; nil = not configured
//   - arAccountID: the company's active AR account ID; 0 = not found
//   - companyBaseCurrency: the company's base currency code (for empty invoice.CurrencyCode)
func EvaluateGatewaySettlementEligibility(
	attempt models.HostedPaymentAttempt,
	inv models.Invoice,
	txn *models.PaymentTransaction,
	mapping *models.PaymentAccountingMapping,
	arAccountID uint,
	companyBaseCurrency string,
) GatewaySettlementEligibility {
	// Rule 1: webhook-verified success.
	if attempt.Status != models.HostedPaymentAttemptPaymentSucceeded {
		return ineligible("attempt is not in payment_succeeded status")
	}

	// Rule 2: company isolation.
	if inv.CompanyID != attempt.CompanyID {
		return ineligible("invoice company does not match attempt company")
	}

	// Rule 3: invoice must be in a payable state.
	if !IsInvoicePayable(inv.Status) {
		return ineligible(fmt.Sprintf("invoice status %q is not payable", inv.Status))
	}

	// Rule 4: exact-match full settlement only.
	if !attempt.Amount.Equal(inv.BalanceDue) {
		return ineligible(fmt.Sprintf(
			"amount mismatch: attempt=%s, balance_due=%s — partial auto-settlement not supported",
			attempt.Amount.StringFixed(2), inv.BalanceDue.StringFixed(2)))
	}

	// Rule 5: currency match (empty invoice.CurrencyCode = company base currency).
	invoiceCurrency := inv.CurrencyCode
	if invoiceCurrency == "" {
		invoiceCurrency = companyBaseCurrency
	}
	if !strings.EqualFold(attempt.CurrencyCode, invoiceCurrency) {
		return ineligible(fmt.Sprintf(
			"currency mismatch: attempt=%s, invoice=%s", attempt.CurrencyCode, invoiceCurrency))
	}

	// Rule 6: PaymentTransaction must exist and not be posted yet.
	if txn == nil {
		return ineligible("no charge PaymentTransaction found for this attempt")
	}
	if txn.PostedJournalEntryID != nil {
		return ineligible("PaymentTransaction already posted to journal")
	}

	// Rule 7: PaymentTransaction must not already be applied.
	if txn.AppliedInvoiceID != nil {
		return ineligible("PaymentTransaction already applied to invoice")
	}

	// Rule 8: clearing account must be configured.
	if mapping == nil || mapping.ClearingAccountID == nil {
		return ineligible("gateway clearing account not configured in payment accounting mappings")
	}

	// Rule 9: active AR account must exist.
	if arAccountID == 0 {
		return ineligible("no active Accounts Receivable account found in chart of accounts")
	}

	return eligibleResult
}

// ── Settlement execution ──────────────────────────────────────────────────────

// GatewaySettlementResult wraps the outcome of ExecuteGatewaySettlement.
type GatewaySettlementResult struct {
	// Settlement is non-nil when settlement completed (including idempotent re-execution).
	Settlement *models.GatewaySettlement
	// Eligibility is the evaluated eligibility result.
	Eligibility GatewaySettlementEligibility
}

// ExecuteGatewaySettlement attempts auto-settlement for a verified hosted payment attempt.
//
// Exact-once semantics:
//   - If a GatewaySettlement already exists for attemptID, returns the existing record
//     with ErrSettlementAlreadyDone (idempotent; not a failure).
//   - If ineligible, returns result with Eligibility.Eligible=false and nil error.
//   - If eligible, creates the JE + applies to invoice + creates GatewaySettlement atomically.
//
// companyID is required for isolation — attemptID must belong to this company.
func ExecuteGatewaySettlement(db *gorm.DB, companyID, attemptID uint) (GatewaySettlementResult, error) {
	// ── Pre-flight: load attempt ──────────────────────────────────────────────
	var attempt models.HostedPaymentAttempt
	if err := db.Where("id = ? AND company_id = ?", attemptID, companyID).
		First(&attempt).Error; err != nil {
		return GatewaySettlementResult{}, ErrSettlementAttemptNotFound
	}

	// ── Idempotency: pre-check outside transaction ────────────────────────────
	// Fast path: if already settled, return immediately.
	var existing models.GatewaySettlement
	if err := db.Where("hosted_attempt_id = ? AND company_id = ?", attemptID, companyID).
		First(&existing).Error; err == nil {
		return GatewaySettlementResult{
			Settlement:  &existing,
			Eligibility: eligibleResult,
		}, ErrSettlementAlreadyDone
	}

	// ── Load supporting data for eligibility check ────────────────────────────
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", attempt.InvoiceID, companyID).
		First(&inv).Error; err != nil {
		return GatewaySettlementResult{}, fmt.Errorf("settlement: load invoice %d: %w", attempt.InvoiceID, err)
	}

	var company models.Company
	if err := db.Select("base_currency_code").Where("id = ?", companyID).
		First(&company).Error; err != nil {
		return GatewaySettlementResult{}, fmt.Errorf("settlement: load company: %w", err)
	}

	// Find the charge PaymentTransaction via PaymentRequest.ExternalRef = attempt.ProviderRef.
	txn := findChargeTransaction(db, companyID, attempt)

	// Load accounting mapping.
	mapping, _ := GetPaymentAccountingMapping(db, companyID, attempt.GatewayAccountID)

	// Find AR account.
	arAccountID := findARAccountID(db, companyID)

	// ── Eligibility check ─────────────────────────────────────────────────────
	elig := EvaluateGatewaySettlementEligibility(attempt, inv, txn, mapping, arAccountID, company.BaseCurrencyCode)
	if !elig.Eligible {
		return GatewaySettlementResult{Eligibility: elig}, nil
	}

	// ── Atomic settlement: JE + invoice update + bridge record ───────────────
	var settlement models.GatewaySettlement
	err := db.Transaction(func(tx *gorm.DB) error {
		// Re-check idempotency inside transaction (race protection).
		var dup models.GatewaySettlement
		if err := tx.Where("hosted_attempt_id = ?", attemptID).First(&dup).Error; err == nil {
			settlement = dup
			return ErrSettlementAlreadyDone
		}

		// Lock invoice for update and re-validate under lock.
		var lockedInv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", inv.ID, companyID),
		).First(&lockedInv).Error; err != nil {
			return fmt.Errorf("settlement: lock invoice: %w", err)
		}

		// Re-check amount under lock (guard against concurrent payment).
		if !attempt.Amount.Equal(lockedInv.BalanceDue) {
			return ineligibleErr(fmt.Sprintf(
				"balance changed under lock: attempt=%s, balance_due=%s",
				attempt.Amount.StringFixed(2), lockedInv.BalanceDue.StringFixed(2)))
		}
		if !IsInvoicePayable(lockedInv.Status) {
			return ineligibleErr(fmt.Sprintf("invoice status %q no longer payable", lockedInv.Status))
		}

		// Re-check transaction flags under lock.
		if err := tx.First(txn, txn.ID).Error; err != nil {
			return fmt.Errorf("settlement: reload txn: %w", err)
		}
		if txn.PostedJournalEntryID != nil {
			return ineligibleErr("PaymentTransaction was posted concurrently")
		}
		if txn.AppliedInvoiceID != nil {
			return ineligibleErr("PaymentTransaction was applied concurrently")
		}

		// ── Create Journal Entry: Dr GW Clearing / Cr AR ─────────────────────
		amount := attempt.Amount.Abs()
		now := time.Now()

		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  now,
			JournalNo:  "GWSETTLE-" + attempt.ProviderRef,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourcePaymentGateway,
			SourceID:   txn.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("settlement: create journal entry: %w", err)
		}

		memo := fmt.Sprintf("Gateway settlement — %s", attempt.ProviderRef)
		debitLine := models.JournalLine{
			CompanyID:      companyID,
			JournalEntryID: je.ID,
			AccountID:      *mapping.ClearingAccountID,
			Debit:          amount,
			Credit:         decimal.Zero,
			Memo:           memo,
		}
		creditLine := models.JournalLine{
			CompanyID:      companyID,
			JournalEntryID: je.ID,
			AccountID:      arAccountID,
			Debit:          decimal.Zero,
			Credit:         amount,
			Memo:           memo,
		}
		if err := tx.Create(&debitLine).Error; err != nil {
			return fmt.Errorf("settlement: create debit line: %w", err)
		}
		if err := tx.Create(&creditLine).Error; err != nil {
			return fmt.Errorf("settlement: create credit line: %w", err)
		}

		// Project to ledger.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        []models.JournalLine{debitLine, creditLine},
			SourceType:   models.LedgerSourcePaymentGateway,
			SourceID:     txn.ID,
		}); err != nil {
			return fmt.Errorf("settlement: project to ledger: %w", err)
		}

		// ── Mark PaymentTransaction posted ────────────────────────────────────
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txn.ID, companyID).
			Updates(map[string]any{
				"posted_journal_entry_id": je.ID,
				"posted_at":              now,
			}).Error; err != nil {
			return fmt.Errorf("settlement: mark txn posted: %w", err)
		}

		// ── Reduce invoice balance and mark paid ──────────────────────────────
		// FX invoices are blocked at the hosted pay layer so balance_due_base == balance_due
		// for every invoice that reaches this path. Mirror both fields to zero.
		// Intentional: invoice is updated directly here (not via ApplyPaymentTransactionToInvoice)
		// to keep the entire gateway-settlement operation atomic inside a single transaction.
		// If ApplyPaymentTransactionToInvoice gains audit/event side effects in the future,
		// replicate them here explicitly rather than routing this path through that helper.
		if err := tx.Model(&lockedInv).Updates(map[string]any{
			"balance_due":      decimal.Zero,
			"balance_due_base": decimal.Zero,
			"status":           string(models.InvoiceStatusPaid),
		}).Error; err != nil {
			return fmt.Errorf("settlement: update invoice: %w", err)
		}

		// ── Mark PaymentTransaction applied ───────────────────────────────────
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txn.ID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": inv.ID,
				"applied_at":        now,
			}).Error; err != nil {
			return fmt.Errorf("settlement: mark txn applied: %w", err)
		}

		// ── Insert GatewaySettlement (idempotency anchor) ─────────────────────
		settlement = models.GatewaySettlement{
			CompanyID:            companyID,
			HostedAttemptID:      attemptID,
			PaymentTransactionID: txn.ID,
			InvoiceID:            inv.ID,
			JournalEntryID:       je.ID,
			Amount:               amount,
			CurrencyCode:         attempt.CurrencyCode,
			SettledAt:            now,
		}
		if err := tx.Create(&settlement).Error; err != nil {
			// Unique constraint = concurrent settlement won the race → idempotent.
			if strings.Contains(err.Error(), "UNIQUE") ||
				strings.Contains(err.Error(), "unique") ||
				strings.Contains(err.Error(), "duplicate") {
				// Re-load the winner.
				tx.Where("hosted_attempt_id = ?", attemptID).First(&settlement)
				return ErrSettlementAlreadyDone
			}
			return fmt.Errorf("settlement: insert gateway_settlement: %w", err)
		}

		slog.Info("gateway settlement completed",
			"settlement_id", settlement.ID,
			"attempt_id", attemptID,
			"invoice_id", inv.ID,
			"amount", amount.StringFixed(2),
			"currency", attempt.CurrencyCode,
			"journal_entry_id", je.ID,
		)
		return nil
	})

	if err != nil {
		if errors.Is(err, ErrSettlementAlreadyDone) {
			return GatewaySettlementResult{
				Settlement:  &settlement,
				Eligibility: eligibleResult,
			}, ErrSettlementAlreadyDone
		}
		// ineligibleErr is wrapped inside an error — unwrap and surface as ineligible result.
		var ie *ineligibleError
		if errors.As(err, &ie) {
			return GatewaySettlementResult{
				Eligibility: ineligible(ie.reason),
			}, nil
		}
		return GatewaySettlementResult{Eligibility: elig}, err
	}

	return GatewaySettlementResult{
		Settlement:  &settlement,
		Eligibility: eligibleResult,
	}, nil
}

// ── Auto-settlement trigger ───────────────────────────────────────────────────

// TryAutoSettleAfterIngestion is called after ingestCheckoutSessionCompleted
// commits its transaction. It looks up the newly succeeded attempt by provider_ref
// and gateway_account_id, then calls ExecuteGatewaySettlement.
//
// This function is deliberately non-blocking: ineligibility and missing config are
// logged as INFO/WARN but do not return an error. Failures (DB errors) are logged
// as ERROR. In all cases, the webhook handler has already responded 200.
//
// Batch 12: settlement outcome is persisted to HostedPaymentAttempt.SettlementStatus
// so operators can see why auto-settlement did not happen, even when this function
// returns without error (non-blocking path).
func TryAutoSettleAfterIngestion(db *gorm.DB, gatewayAccountID uint, sessionID string) {
	var attempt models.HostedPaymentAttempt
	if err := db.Where(
		"provider_ref = ? AND gateway_account_id = ? AND status = ?",
		sessionID, gatewayAccountID, models.HostedPaymentAttemptPaymentSucceeded,
	).First(&attempt).Error; err != nil {
		// Attempt not found or not in payment_succeeded — unexpected but not fatal.
		slog.Warn("auto-settlement: attempt not found after verified ingestion",
			"gateway_account_id", gatewayAccountID, "session_id", sessionID, "error", err.Error())
		return
	}

	result, err := ExecuteGatewaySettlement(db, attempt.CompanyID, attempt.ID)
	if err != nil {
		if errors.Is(err, ErrSettlementAlreadyDone) {
			// Already settled — idempotent write of applied status in case the
			// original persistSettlementOutcome was lost (e.g. crash between commit
			// and persist). Safe to overwrite: status is the same value.
			persistSettlementOutcome(db, attempt.ID, models.SettlementOutcomeApplied, "")
			slog.Info("auto-settlement: already settled (idempotent)",
				"attempt_id", attempt.ID, "settlement_id", result.Settlement.ID)
			return
		}
		// Unexpected execution error — persist so operator can see and retry.
		persistSettlementOutcome(db, attempt.ID,
			models.SettlementOutcomeFailed, "execution error: "+err.Error())
		slog.Error("auto-settlement: execution error",
			"attempt_id", attempt.ID, "invoice_id", attempt.InvoiceID, "error", err.Error())
		return
	}

	if !result.Eligibility.Eligible {
		// Ineligible: persist the reason so operator can act without reading logs.
		persistSettlementOutcome(db, attempt.ID,
			models.SettlementOutcomePendingReview, result.Eligibility.Reason)
		slog.Info("auto-settlement: ineligible — payment verified but settlement pending manual review",
			"attempt_id", attempt.ID,
			"invoice_id", attempt.InvoiceID,
			"reason", result.Eligibility.Reason,
		)
		return
	}

	// Success: persist applied outcome.
	persistSettlementOutcome(db, attempt.ID, models.SettlementOutcomeApplied, "")
	slog.Info("auto-settlement: success",
		"settlement_id", result.Settlement.ID,
		"attempt_id", attempt.ID,
		"invoice_id", attempt.InvoiceID,
		"amount", result.Settlement.Amount.StringFixed(2),
	)
}

// RetryGatewaySettlement is the manual retry path for operators.
//
// It finds the latest payment_succeeded HostedPaymentAttempt for the invoice,
// re-evaluates eligibility, and re-runs ExecuteGatewaySettlement if eligible.
// The settlement outcome fields on the attempt are updated regardless of outcome.
//
// Exact-once safety: inherited from ExecuteGatewaySettlement (unique constraint
// + SELECT FOR UPDATE + transaction). Repeated retry after success returns
// ErrSettlementAlreadyDone without any mutation.
//
// Returns:
//   - (result, nil)                  on success
//   - (result, ErrSettlementAlreadyDone) when already settled (idempotent)
//   - (result{ineligible}, nil)      when still ineligible after retry
//   - ({}, ErrNoSucceededAttempt)    when no payment_succeeded attempt exists
//   - ({}, err)                      on unexpected DB error
var ErrNoSucceededAttempt = errors.New("no payment_succeeded attempt found for this invoice")

func RetryGatewaySettlement(db *gorm.DB, companyID, invoiceID uint) (GatewaySettlementResult, error) {
	// Find the latest payment_succeeded attempt for this invoice + company.
	var attempt models.HostedPaymentAttempt
	if err := db.Where(
		"invoice_id = ? AND company_id = ? AND status = ?",
		invoiceID, companyID, models.HostedPaymentAttemptPaymentSucceeded,
	).Order("created_at desc").First(&attempt).Error; err != nil {
		return GatewaySettlementResult{}, ErrNoSucceededAttempt
	}

	result, err := ExecuteGatewaySettlement(db, companyID, attempt.ID)
	if err != nil {
		if errors.Is(err, ErrSettlementAlreadyDone) {
			// Already done — ensure outcome field reflects this (idempotent write).
			persistSettlementOutcome(db, attempt.ID, models.SettlementOutcomeApplied, "")
			return result, ErrSettlementAlreadyDone
		}
		// Unexpected error — persist and surface.
		persistSettlementOutcome(db, attempt.ID,
			models.SettlementOutcomeFailed, "execution error: "+err.Error())
		return result, err
	}

	if !result.Eligibility.Eligible {
		// Still ineligible — update reason so operator sees the current condition.
		persistSettlementOutcome(db, attempt.ID,
			models.SettlementOutcomePendingReview, result.Eligibility.Reason)
		return result, nil
	}

	// Success.
	persistSettlementOutcome(db, attempt.ID, models.SettlementOutcomeApplied, "")
	return result, nil
}

// GetSettlementForAttempt returns the GatewaySettlement for a hosted attempt,
// or nil if no settlement exists. Used for tracing and operator visibility.
func GetSettlementForAttempt(db *gorm.DB, companyID, attemptID uint) *models.GatewaySettlement {
	var s models.GatewaySettlement
	if err := db.Where("hosted_attempt_id = ? AND company_id = ?", attemptID, companyID).
		First(&s).Error; err != nil {
		return nil
	}
	return &s
}

// GetSettlementForInvoice returns the GatewaySettlement for a given invoice,
// or nil if no gateway settlement exists. A nil result means either no payment
// has been confirmed or settlement is pending manual review.
func GetSettlementForInvoice(db *gorm.DB, companyID, invoiceID uint) *models.GatewaySettlement {
	var s models.GatewaySettlement
	if err := db.Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
		First(&s).Error; err != nil {
		return nil
	}
	return &s
}

// ── Settlement outcome persistence ───────────────────────────────────────────

// persistSettlementOutcome writes SettlementStatus, SettlementReason, and
// SettlementLastAttemptedAt to the HostedPaymentAttempt row identified by attemptID.
//
// This is a best-effort write: failure is logged but never returned to the caller.
// The settlement transaction itself has already committed (or rolled back) before
// this function is called; outcome persistence is a separate, non-blocking update.
func persistSettlementOutcome(db *gorm.DB, attemptID uint, status, reason string) {
	now := time.Now()
	if err := db.Model(&models.HostedPaymentAttempt{}).
		Where("id = ?", attemptID).
		Updates(map[string]any{
			"settlement_status":             status,
			"settlement_reason":             reason,
			"settlement_last_attempted_at":  now,
		}).Error; err != nil {
		slog.Error("settlement: failed to persist outcome to attempt",
			"attempt_id", attemptID, "status", status, "error", err.Error())
	}
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// findChargeTransaction returns the charge PaymentTransaction linked to the attempt
// via PaymentRequest.ExternalRef = attempt.ProviderRef.
// Returns nil if not found.
func findChargeTransaction(db *gorm.DB, companyID uint, attempt models.HostedPaymentAttempt) *models.PaymentTransaction {
	var pr models.PaymentRequest
	if err := db.Where(
		"external_ref = ? AND gateway_account_id = ? AND company_id = ?",
		attempt.ProviderRef, attempt.GatewayAccountID, companyID,
	).First(&pr).Error; err != nil {
		return nil
	}
	var txn models.PaymentTransaction
	if err := db.Where(
		"payment_request_id = ? AND company_id = ? AND transaction_type = ?",
		pr.ID, companyID, models.TxnTypeCharge,
	).First(&txn).Error; err != nil {
		return nil
	}
	return &txn
}

// findARAccountID returns the ID of the first active AR account for the company,
// or 0 if none found.
func findARAccountID(db *gorm.DB, companyID uint) uint {
	var arAcct models.Account
	if err := db.Where(
		"company_id = ? AND detail_account_type = ? AND is_active = true",
		companyID, string(models.DetailAccountsReceivable),
	).Order("code asc").First(&arAcct).Error; err != nil {
		return 0
	}
	return arAcct.ID
}

// ── ineligibleError: internal sentinel for lock-check failures ───────────────

// ineligibleError wraps a condition that was valid at eligibility-check time but
// failed under the transaction lock. Treated as ineligible (not a hard error).
type ineligibleError struct{ reason string }

func (e *ineligibleError) Error() string { return "ineligible under lock: " + e.reason }

func ineligibleErr(reason string) error { return &ineligibleError{reason: reason} }
