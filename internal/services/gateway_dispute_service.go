// 遵循project_guide.md
package services

// gateway_dispute_service.go — Batch 15: Gateway Dispute lifecycle management.
//
// ─── Responsibility ───────────────────────────────────────────────────────────
// Manages the lifecycle of payment disputes raised by cardholders.
//
// State machine:
//   dispute_opened → dispute_won   (no financial effect; original payment stands)
//   dispute_opened → dispute_lost  (chargeback PaymentTransaction created atomically)
//   Any other transition is rejected with ErrDisputeInvalidTransition.
//
// ─── What this service does NOT do ───────────────────────────────────────────
//   - Does NOT post JEs directly. The chargeback PaymentTransaction created on
//     LoseGatewayDispute must be explicitly posted + apply-chargebacked by an
//     operator (consistent with the manual posting pattern used throughout).
//   - Does NOT handle provider evidence, case notes, or document uploads.
//
// ─── Idempotency ─────────────────────────────────────────────────────────────
// Unique index on (company_id, gateway_account_id, provider_dispute_id) prevents
// duplicate dispute records. OpenGatewayDispute returns ErrDisputeDuplicate when
// the index fires.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrDisputeNotFound              = errors.New("dispute not found or does not belong to this company")
	ErrDisputeDuplicate             = errors.New("a dispute with this provider dispute ID already exists")
	ErrDisputeAlreadyResolved       = errors.New("dispute has already been resolved (won or lost)")
	ErrDisputeInvalidTransition     = errors.New("invalid dispute status transition")
	ErrDisputeChargeNotFound        = errors.New("original charge transaction not found or does not belong to this company")
	ErrDisputeChargeNotPosted       = errors.New("original charge transaction must be posted before a dispute can be opened")
	ErrDisputeProviderIDEmpty       = errors.New("provider dispute ID is required")
	ErrDisputeAmountInvalid         = errors.New("dispute amount must be positive")
	ErrDisputeGatewayMismatch       = errors.New("charge transaction belongs to a different gateway account")
	ErrDisputeWrongOriginalTxnType  = errors.New("only charge or capture transactions can be disputed")
)

// ── Input types ───────────────────────────────────────────────────────────────

// OpenDisputeInput carries all caller-supplied fields for opening a new dispute.
type OpenDisputeInput struct {
	CompanyID        uint
	GatewayAccountID uint

	// PaymentTransactionID is the original charge/capture being disputed.
	PaymentTransactionID uint

	// ProviderDisputeID is the processor-assigned dispute reference (e.g. Stripe dp_xxx).
	ProviderDisputeID string

	// Amount is the disputed amount. Must be > 0.
	Amount decimal.Decimal

	// CurrencyCode of the disputed amount. Empty defaults to charge's currency.
	CurrencyCode string

	// OpenedAt is when the dispute was raised. Zero defaults to time.Now().
	OpenedAt time.Time
}

// ── Service functions ─────────────────────────────────────────────────────────

// OpenGatewayDispute registers a new dispute for an existing charge transaction.
//
// Validates:
//   - ProviderDisputeID non-empty
//   - Amount > 0
//   - Original charge exists and belongs to this company
//   - Original charge has been posted to a JE (PostedJournalEntryID != nil)
//   - Charge's GatewayAccountID matches input.GatewayAccountID
//
// Returns ErrDisputeDuplicate if provider_dispute_id already exists for this
// (company_id, gateway_account_id).
func OpenGatewayDispute(db *gorm.DB, input OpenDisputeInput) (*models.GatewayDispute, error) {
	// ── Input validation ──────────────────────────────────────────────────────
	if strings.TrimSpace(input.ProviderDisputeID) == "" {
		return nil, ErrDisputeProviderIDEmpty
	}
	if !input.Amount.IsPositive() {
		return nil, ErrDisputeAmountInvalid
	}

	openedAt := input.OpenedAt
	if openedAt.IsZero() {
		openedAt = time.Now()
	}

	// ── Validate gateway account belongs to company ───────────────────────────
	if _, err := GetGatewayAccount(db, input.CompanyID, input.GatewayAccountID); err != nil {
		return nil, ErrPayoutGatewayAccountInvalid
	}

	// ── Load + validate original charge ──────────────────────────────────────
	var charge models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", input.PaymentTransactionID, input.CompanyID).
		First(&charge).Error; err != nil {
		return nil, ErrDisputeChargeNotFound
	}
	if charge.PostedJournalEntryID == nil {
		return nil, ErrDisputeChargeNotPosted
	}
	// Only charge / capture can be disputed. Disputing a refund, fee, payout,
	// or another chargeback is not a valid business operation.
	if charge.TransactionType != models.TxnTypeCharge && charge.TransactionType != models.TxnTypeCapture {
		return nil, ErrDisputeWrongOriginalTxnType
	}
	if charge.GatewayAccountID != input.GatewayAccountID {
		return nil, ErrDisputeGatewayMismatch
	}

	// ── Idempotency pre-check ─────────────────────────────────────────────────
	var dup models.GatewayDispute
	if err := db.Where(
		"company_id = ? AND gateway_account_id = ? AND provider_dispute_id = ?",
		input.CompanyID, input.GatewayAccountID, input.ProviderDisputeID,
	).First(&dup).Error; err == nil {
		return nil, ErrDisputeDuplicate
	}

	// ── Resolve optional settlement linkage (best-effort) ────────────────────
	var settlementID *uint
	var gs models.GatewaySettlement
	if err := db.Where("company_id = ? AND payment_transaction_id = ?",
		input.CompanyID, charge.ID).First(&gs).Error; err == nil {
		settlementID = &gs.ID
	}

	// ── Currency default ──────────────────────────────────────────────────────
	currency := strings.TrimSpace(input.CurrencyCode)
	if currency == "" {
		currency = charge.CurrencyCode
	}

	// ── Insert dispute ────────────────────────────────────────────────────────
	dispute := models.GatewayDispute{
		CompanyID:            input.CompanyID,
		GatewayAccountID:     input.GatewayAccountID,
		ProviderDisputeID:    input.ProviderDisputeID,
		PaymentTransactionID: input.PaymentTransactionID,
		GatewaySettlementID:  settlementID,
		Amount:               input.Amount,
		CurrencyCode:         currency,
		Status:               models.DisputeStatusOpened,
		OpenedAt:             openedAt,
	}
	if err := db.Create(&dispute).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") ||
			strings.Contains(err.Error(), "unique") ||
			strings.Contains(err.Error(), "duplicate") {
			return nil, ErrDisputeDuplicate
		}
		return nil, fmt.Errorf("open dispute: insert: %w", err)
	}

	slog.Info("gateway dispute opened",
		"dispute_id", dispute.ID,
		"provider_dispute_id", input.ProviderDisputeID,
		"charge_id", input.PaymentTransactionID,
		"amount", input.Amount.StringFixed(2),
	)
	return &dispute, nil
}

// WinGatewayDispute transitions a dispute to dispute_won.
//
// The original payment stands — no financial reversal is made and no chargeback
// transaction is created. Only the dispute status and resolved_at are updated.
//
// Returns ErrDisputeAlreadyResolved if the dispute is already won or lost.
func WinGatewayDispute(db *gorm.DB, companyID, disputeID uint) (*models.GatewayDispute, error) {
	var dispute models.GatewayDispute

	txErr := db.Transaction(func(tx *gorm.DB) error {
		// ── 1. Lock dispute row inside the transaction ────────────────────────
		// applyLockForUpdate issues SELECT ... FOR UPDATE on PostgreSQL so that
		// a concurrent LoseGatewayDispute (or duplicate WinGatewayDispute) call
		// blocks here until this transaction commits or rolls back.
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", disputeID, companyID),
		).First(&dispute).Error; err != nil {
			return ErrDisputeNotFound
		}

		// ── 2. Re-check status under lock ─────────────────────────────────────
		if err := requireDisputeOpenedState(dispute); err != nil {
			return err
		}

		// ── 3. Transition to won ──────────────────────────────────────────────
		now := time.Now()
		if err := tx.Model(&dispute).Updates(map[string]any{
			"status":      string(models.DisputeStatusWon),
			"resolved_at": now,
		}).Error; err != nil {
			return fmt.Errorf("win dispute: %w", err)
		}
		dispute.Status = models.DisputeStatusWon
		dispute.ResolvedAt = &now
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	slog.Info("gateway dispute won — no financial reversal", "dispute_id", dispute.ID)
	return &dispute, nil
}

// LoseGatewayDispute transitions a dispute to dispute_lost and atomically
// creates a PaymentTransaction of type TxnTypeChargeback.
//
// The chargeback transaction is linked back to the original charge via
// OriginalTransactionID, and its PaymentRequestID is inherited from the charge
// (preserving the invoice linkage for the apply-chargeback step).
//
// After this call, the operator must explicitly:
//   1. POST /transactions/:id/post         → creates JE (Dr Chargeback / Cr Clearing)
//   2. POST /transactions/:id/apply-chargeback → restores invoice BalanceDue
//
// Returns ErrDisputeAlreadyResolved if the dispute is already won or lost.
func LoseGatewayDispute(db *gorm.DB, companyID, disputeID uint) (*models.GatewayDispute, *models.PaymentTransaction, error) {
	var dispute models.GatewayDispute
	var chargeback models.PaymentTransaction

	txErr := db.Transaction(func(tx *gorm.DB) error {
		// ── 1. Lock dispute row inside the transaction ────────────────────────
		// applyLockForUpdate issues SELECT ... FOR UPDATE on PostgreSQL so that
		// a concurrent LoseGatewayDispute call blocks here until this transaction
		// commits or rolls back. On SQLite (tests only) it is a no-op; SQLite
		// serialises writes at the connection level, so concurrent goroutines
		// racing on the same DB file will be serialised naturally.
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", disputeID, companyID),
		).First(&dispute).Error; err != nil {
			return ErrDisputeNotFound
		}

		// ── 2. Re-check status under lock ─────────────────────────────────────
		// Any concurrent LoseGatewayDispute that reached this point after us will
		// see dispute_lost here and return ErrDisputeAlreadyResolved.
		if err := requireDisputeOpenedState(dispute); err != nil {
			return err
		}

		// ── 3. Load original charge (inside tx for consistency) ───────────────
		var charge models.PaymentTransaction
		if err := tx.Where("id = ? AND company_id = ?", dispute.PaymentTransactionID, companyID).
			First(&charge).Error; err != nil {
			return fmt.Errorf("lose dispute: load original charge %d: %w", dispute.PaymentTransactionID, err)
		}

		// ── 4. Create chargeback PaymentTransaction ───────────────────────────
		now := time.Now()
		rawPayload, _ := json.Marshal(map[string]any{
			"source":      "dispute_lost",
			"dispute_id":  dispute.ID,
			"provider_id": dispute.ProviderDisputeID,
		})
		chargeID := charge.ID
		chargeback = models.PaymentTransaction{
			CompanyID:             companyID,
			GatewayAccountID:      dispute.GatewayAccountID,
			PaymentRequestID:      charge.PaymentRequestID, // inherit invoice linkage
			TransactionType:       models.TxnTypeChargeback,
			Amount:                dispute.Amount,
			CurrencyCode:          dispute.CurrencyCode,
			Status:                "completed",
			ExternalTxnRef:        dispute.ProviderDisputeID,
			OriginalTransactionID: &chargeID,
			RawPayload:            datatypes.JSON(rawPayload),
		}
		if err := tx.Create(&chargeback).Error; err != nil {
			return fmt.Errorf("create chargeback transaction: %w", err)
		}

		// ── 5. Transition dispute to lost and link chargeback ─────────────────
		cbID := chargeback.ID
		if err := tx.Model(&dispute).Updates(map[string]any{
			"status":                    string(models.DisputeStatusLost),
			"resolved_at":               now,
			"chargeback_transaction_id": cbID,
		}).Error; err != nil {
			return fmt.Errorf("update dispute to lost: %w", err)
		}
		dispute.Status = models.DisputeStatusLost
		dispute.ResolvedAt = &now
		dispute.ChargebackTransactionID = &cbID

		return nil
	})
	if txErr != nil {
		return nil, nil, txErr
	}

	slog.Info("gateway dispute lost — chargeback transaction created",
		"dispute_id", dispute.ID,
		"chargeback_txn_id", chargeback.ID,
		"amount", dispute.Amount.StringFixed(2),
	)
	return &dispute, &chargeback, nil
}

// ── Query helpers ─────────────────────────────────────────────────────────────

// GetGatewayDisputeByID loads a dispute scoped to a company.
func GetGatewayDisputeByID(db *gorm.DB, companyID, disputeID uint) (*models.GatewayDispute, error) {
	var d models.GatewayDispute
	if err := db.Where("id = ? AND company_id = ?", disputeID, companyID).First(&d).Error; err != nil {
		return nil, ErrDisputeNotFound
	}
	return &d, nil
}

// ListGatewayDisputes returns all disputes for a company, newest first.
func ListGatewayDisputes(db *gorm.DB, companyID uint) ([]models.GatewayDispute, error) {
	var disputes []models.GatewayDispute
	err := db.Where("company_id = ?", companyID).
		Order("opened_at DESC, id DESC").
		Limit(500).
		Find(&disputes).Error
	return disputes, err
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// requireDisputeOpenedState returns an error if the dispute is not in
// dispute_opened status, with a clear reason.
func requireDisputeOpenedState(d models.GatewayDispute) error {
	switch d.Status {
	case models.DisputeStatusOpened:
		return nil
	case models.DisputeStatusWon, models.DisputeStatusLost:
		return ErrDisputeAlreadyResolved
	default:
		return ErrDisputeInvalidTransition
	}
}
