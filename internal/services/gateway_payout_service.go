// 遵循project_guide.md
package services

// gateway_payout_service.go — Batch 14: Gateway Payout Bridge.
//
// ─── Responsibility ───────────────────────────────────────────────────────────
// CreateGatewayPayout bridges one or more GatewaySettlement (clearing) rows
// into a single GatewayPayout, posting the JE:
//
//	Dr  Bank Account        (net_amount)
//	Dr  Fee Expense Account (fee_amount, only when fee_amount > 0)
//	Cr  Gateway Clearing    (gross_amount = net + fee)
//
// This is the final accounting step in the hosted-payment collection chain:
//
//	customer pay → GatewaySettlement (Dr Clearing / Cr AR) → GatewayPayout (Dr Bank / Cr Clearing)
//
// ─── Idempotency ──────────────────────────────────────────────────────────────
// 1. Unique index on (company_id, gateway_account_id, provider_payout_id) prevents
//    duplicate payout bridges for the same external payout event.
// 2. Unique index on gateway_payout_settlements.gateway_settlement_id prevents a
//    clearing row from being bridged more than once. Acts as the last-line race guard.
// 3. Both checks are enforced inside the DB transaction after SELECT FOR UPDATE
//    on the settlement rows.
//
// ─── What this service does NOT do ───────────────────────────────────────────
// • It does not touch invoice status, AR balance, or any HostedPaymentAttempt field.
// • It does not handle refunds, disputes, or partial amounts.
// • Multi-currency payouts are rejected; all linked settlements must share one currency.

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	// Input / presence errors
	ErrPayoutNoSettlements      = errors.New("at least one settlement ID is required")
	ErrPayoutProviderIDEmpty    = errors.New("provider payout ID is required")
	ErrPayoutBankAccountEmpty   = errors.New("bank account ID is required")
	ErrPayoutFeeNegative        = errors.New("fee amount cannot be negative")
	ErrPayoutDateZero           = errors.New("payout date is required")

	// Duplicate
	ErrPayoutDuplicate = errors.New("a payout with this provider payout ID already exists for this gateway account")

	// Settlement validation
	ErrPayoutSettlementNotFound     = errors.New("one or more settlement IDs not found or not accessible")
	ErrPayoutSettlementAlreadyBridged = errors.New("one or more settlements are already linked to a payout")
	ErrPayoutSettlementGatewayMismatch = errors.New("all settlements must belong to the same gateway account as the payout")
	ErrPayoutSettlementCurrencyMismatch = errors.New("all settlements must share the same currency")

	// Amount invariants
	ErrPayoutGrossZero      = errors.New("gross amount must be positive")
	ErrPayoutFeeExceedsGross = errors.New("fee amount exceeds gross amount")

	// Gateway account ownership
	ErrPayoutGatewayAccountInvalid = errors.New("gateway account not found or does not belong to this company")

	// Account / mapping errors
	ErrPayoutBankAccountInvalid     = errors.New("bank account not found or does not belong to this company")
	ErrPayoutBankAccountInactive    = errors.New("bank account is inactive")
	ErrPayoutBankAccountNotAsset    = errors.New("bank account must be an asset account")
	ErrPayoutNoClearingAccount      = errors.New("gateway clearing account is not configured in the accounting mapping")
	ErrPayoutNoFeeExpenseAccount    = errors.New("gateway fee expense account is not configured (required when fee > 0)")
)

// ── Input / output types ──────────────────────────────────────────────────────

// CreateGatewayPayoutInput carries all caller-supplied fields for a new payout bridge.
// Backend recomputes gross and net from DB truth; caller-supplied amounts are not
// used as source-of-truth.
type CreateGatewayPayoutInput struct {
	CompanyID        uint
	GatewayAccountID uint

	// ProviderPayoutID is the processor-assigned payout reference (e.g. Stripe po_xxx).
	// Must be non-empty and unique per (company_id, gateway_account_id).
	ProviderPayoutID string

	// PayoutDate is the date the processor released funds (used as JE entry date).
	PayoutDate time.Time

	// FeeAmount is the processor fee deducted from gross. Zero is allowed.
	FeeAmount decimal.Decimal

	// BankAccountID is the GL asset account to debit for the net deposit.
	BankAccountID uint

	// SettlementIDs are the GatewaySettlement rows to bridge. Min 1.
	SettlementIDs []uint
}

// GatewayPayoutResult is returned on success.
type GatewayPayoutResult struct {
	Payout      *models.GatewayPayout
	Settlements []models.GatewaySettlement
}

// ── Query helpers ─────────────────────────────────────────────────────────────

// settlementWithGatewayAccountID extends GatewaySettlement with the
// gateway_account_id resolved via a JOIN on hosted_payment_attempts.
// Used internally for validation only.
type settlementWithGatewayAccountID struct {
	models.GatewaySettlement
	GatewayAccountID uint
}

// loadSettlementsWithGateway returns settlements together with their gateway
// account ID, joined from hosted_payment_attempts.
func loadSettlementsWithGateway(db *gorm.DB, companyID uint, ids []uint) ([]settlementWithGatewayAccountID, error) {
	var rows []settlementWithGatewayAccountID
	err := db.Table("gateway_settlements gs").
		Select("gs.*, hpa.gateway_account_id").
		Joins("JOIN hosted_payment_attempts hpa ON hpa.id = gs.hosted_attempt_id").
		Where("gs.id IN ? AND gs.company_id = ?", ids, companyID).
		Scan(&rows).Error
	return rows, err
}

// loadSettlementsForUpdate locks the given settlement rows inside a transaction
// using SELECT ... FOR UPDATE (or ROWID lock on SQLite).
// Rows are ordered by id ASC to prevent deadlock when multiple concurrent
// transactions lock overlapping sets.
func loadSettlementsForUpdate(tx *gorm.DB, companyID uint, ids []uint) ([]models.GatewaySettlement, error) {
	var rows []models.GatewaySettlement
	q := applyLockForUpdate(
		tx.Where("id IN ? AND company_id = ?", ids, companyID).Order("id ASC"),
	)
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetGatewayPayoutByID loads a payout by ID scoped to company.
func GetGatewayPayoutByID(db *gorm.DB, companyID, payoutID uint) (*models.GatewayPayout, error) {
	var p models.GatewayPayout
	if err := db.Where("id = ? AND company_id = ?", payoutID, companyID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListGatewayPayouts returns all payouts for a company, newest first.
func ListGatewayPayouts(db *gorm.DB, companyID uint) ([]models.GatewayPayout, error) {
	var payouts []models.GatewayPayout
	err := db.Where("company_id = ?", companyID).
		Order("payout_date DESC, id DESC").
		Limit(500).
		Find(&payouts).Error
	return payouts, err
}

// ListGatewayPayoutSettlements returns the GatewaySettlement rows linked to a payout.
func ListGatewayPayoutSettlements(db *gorm.DB, companyID, payoutID uint) ([]models.GatewaySettlement, error) {
	var rows []models.GatewaySettlement
	err := db.Table("gateway_settlements gs").
		Joins("JOIN gateway_payout_settlements gps ON gps.gateway_settlement_id = gs.id AND gps.company_id = ?", companyID).
		Where("gps.gateway_payout_id = ? AND gs.company_id = ?", payoutID, companyID).
		Find(&rows).Error
	return rows, err
}

// UnbridgedSettlements returns GatewaySettlement rows for a company that have
// not yet been linked to any GatewayPayout. Optionally filtered by gateway account.
// Used to populate the "new payout" form.
func UnbridgedSettlements(db *gorm.DB, companyID uint, gatewayAccountID uint) ([]models.GatewaySettlement, error) {
	q := db.Table("gateway_settlements gs").
		Select("gs.*").
		Where("gs.company_id = ?", companyID).
		Where("NOT EXISTS (SELECT 1 FROM gateway_payout_settlements gps WHERE gps.gateway_settlement_id = gs.id)")

	if gatewayAccountID > 0 {
		q = q.Joins("JOIN hosted_payment_attempts hpa ON hpa.id = gs.hosted_attempt_id").
			Where("hpa.gateway_account_id = ?", gatewayAccountID)
	}

	var rows []models.GatewaySettlement
	err := q.Order("gs.settled_at DESC").Limit(300).Find(&rows).Error
	return rows, err
}

// ── Core service ──────────────────────────────────────────────────────────────

// CreateGatewayPayout validates, creates, and posts a payout bridge atomically.
//
// All validation is done before and re-verified inside the transaction under
// SELECT FOR UPDATE locks. A failure at any step rolls back the entire transaction —
// no partial writes are possible.
func CreateGatewayPayout(db *gorm.DB, input CreateGatewayPayoutInput) (*GatewayPayoutResult, error) {
	// ── 1. Basic input validation ─────────────────────────────────────────────
	if len(input.SettlementIDs) == 0 {
		return nil, ErrPayoutNoSettlements
	}
	if strings.TrimSpace(input.ProviderPayoutID) == "" {
		return nil, ErrPayoutProviderIDEmpty
	}
	if input.BankAccountID == 0 {
		return nil, ErrPayoutBankAccountEmpty
	}
	if input.PayoutDate.IsZero() {
		return nil, ErrPayoutDateZero
	}
	if input.FeeAmount.IsNegative() {
		return nil, ErrPayoutFeeNegative
	}

	// ── 1.5. Validate gateway account ownership ───────────────────────────────
	// Explicit check before any downstream queries so callers receive a clear
	// error rather than an indirect "clearing mapping not found" when a wrong
	// or cross-company gatewayAccountID is supplied.
	if _, err := GetGatewayAccount(db, input.CompanyID, input.GatewayAccountID); err != nil {
		return nil, ErrPayoutGatewayAccountInvalid
	}

	// ── 2. Duplicate check (fast path outside transaction) ────────────────────
	var dup models.GatewayPayout
	if err := db.Where(
		"company_id = ? AND gateway_account_id = ? AND provider_payout_id = ?",
		input.CompanyID, input.GatewayAccountID, input.ProviderPayoutID,
	).First(&dup).Error; err == nil {
		return nil, ErrPayoutDuplicate
	}

	// ── 3. Load settlements with gateway account (pre-transaction validation) ─
	settlements, err := loadSettlementsWithGateway(db, input.CompanyID, input.SettlementIDs)
	if err != nil {
		return nil, fmt.Errorf("payout: load settlements: %w", err)
	}
	if len(settlements) != len(input.SettlementIDs) {
		return nil, ErrPayoutSettlementNotFound
	}

	// Validate gateway account, currency, and bridge status.
	var currency string
	for _, s := range settlements {
		if s.GatewayAccountID != input.GatewayAccountID {
			return nil, ErrPayoutSettlementGatewayMismatch
		}
		if currency == "" {
			currency = s.CurrencyCode
		} else if s.CurrencyCode != currency {
			return nil, ErrPayoutSettlementCurrencyMismatch
		}
	}

	// Check none are already bridged.
	settleIDs := make([]uint, len(settlements))
	for i, s := range settlements {
		settleIDs[i] = s.ID
	}
	var alreadyBridged int64
	db.Model(&models.GatewayPayoutSettlement{}).
		Where("gateway_settlement_id IN ?", settleIDs).
		Count(&alreadyBridged)
	if alreadyBridged > 0 {
		return nil, ErrPayoutSettlementAlreadyBridged
	}

	// ── 4. Validate bank account ──────────────────────────────────────────────
	var bankAccount models.Account
	if err := db.Where("id = ? AND company_id = ?", input.BankAccountID, input.CompanyID).
		First(&bankAccount).Error; err != nil {
		return nil, ErrPayoutBankAccountInvalid
	}
	if !bankAccount.IsActive {
		return nil, ErrPayoutBankAccountInactive
	}
	if bankAccount.RootAccountType != models.RootAsset {
		return nil, ErrPayoutBankAccountNotAsset
	}

	// ── 5. Load accounting mapping ────────────────────────────────────────────
	mapping, err := GetPaymentAccountingMapping(db, input.CompanyID, input.GatewayAccountID)
	if err != nil {
		return nil, fmt.Errorf("payout: load accounting mapping: %w", err)
	}
	if mapping == nil || mapping.ClearingAccountID == nil {
		return nil, ErrPayoutNoClearingAccount
	}
	if input.FeeAmount.IsPositive() && mapping.FeeExpenseAccountID == nil {
		return nil, ErrPayoutNoFeeExpenseAccount
	}

	// ── 6. Compute and validate amounts (backend authority) ───────────────────
	gross := decimal.Zero
	for _, s := range settlements {
		gross = gross.Add(s.Amount)
	}
	if !gross.IsPositive() {
		return nil, ErrPayoutGrossZero
	}
	if input.FeeAmount.GreaterThan(gross) {
		return nil, ErrPayoutFeeExceedsGross
	}
	net := gross.Sub(input.FeeAmount)

	// ── 7. Atomic transaction ─────────────────────────────────────────────────
	var payout models.GatewayPayout
	var linkedSettlements []models.GatewaySettlement

	err = db.Transaction(func(tx *gorm.DB) error {
		// ── 7a. Re-check duplicate inside transaction ─────────────────────────
		var dupInTx models.GatewayPayout
		if err := tx.Where(
			"company_id = ? AND gateway_account_id = ? AND provider_payout_id = ?",
			input.CompanyID, input.GatewayAccountID, input.ProviderPayoutID,
		).First(&dupInTx).Error; err == nil {
			return ErrPayoutDuplicate
		}

		// ── 7b. Lock settlements FOR UPDATE ───────────────────────────────────
		lockedSettlements, err := loadSettlementsForUpdate(tx, input.CompanyID, settleIDs)
		if err != nil {
			return fmt.Errorf("payout: lock settlements: %w", err)
		}
		if len(lockedSettlements) != len(settleIDs) {
			return ErrPayoutSettlementNotFound
		}

		// ── 7c. Re-validate not bridged under lock ────────────────────────────
		var bridgedCount int64
		if err := tx.Model(&models.GatewayPayoutSettlement{}).
			Where("gateway_settlement_id IN ?", settleIDs).
			Count(&bridgedCount).Error; err != nil {
			return fmt.Errorf("payout: bridge check: %w", err)
		}
		if bridgedCount > 0 {
			return ErrPayoutSettlementAlreadyBridged
		}

		// ── 7d. Re-sum gross under lock (guard against tampered input) ────────
		lockedGross := decimal.Zero
		for _, s := range lockedSettlements {
			lockedGross = lockedGross.Add(s.Amount)
		}
		if !lockedGross.Equal(gross) {
			return fmt.Errorf("payout: gross mismatch under lock (pre=%s locked=%s)",
				gross.StringFixed(2), lockedGross.StringFixed(2))
		}

		// ── 7e. Insert GatewayPayout (JournalEntryID still nil) ───────────────
		payout = models.GatewayPayout{
			CompanyID:        input.CompanyID,
			GatewayAccountID: input.GatewayAccountID,
			ProviderPayoutID: input.ProviderPayoutID,
			PayoutDate:       input.PayoutDate,
			CurrencyCode:     currency,
			GrossAmount:      gross,
			FeeAmount:        input.FeeAmount,
			NetAmount:        net,
			BankAccountID:    input.BankAccountID,
		}
		if err := tx.Create(&payout).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") ||
				strings.Contains(err.Error(), "unique") ||
				strings.Contains(err.Error(), "duplicate") {
				return ErrPayoutDuplicate
			}
			return fmt.Errorf("payout: insert payout: %w", err)
		}

		// ── 7f. Insert join rows ──────────────────────────────────────────────
		joinRows := make([]models.GatewayPayoutSettlement, len(lockedSettlements))
		for i, s := range lockedSettlements {
			joinRows[i] = models.GatewayPayoutSettlement{
				CompanyID:           input.CompanyID,
				GatewayPayoutID:     payout.ID,
				GatewaySettlementID: s.ID,
			}
		}
		if err := tx.Create(&joinRows).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") ||
				strings.Contains(err.Error(), "unique") ||
				strings.Contains(err.Error(), "duplicate") {
				return ErrPayoutSettlementAlreadyBridged
			}
			return fmt.Errorf("payout: insert join rows: %w", err)
		}

		// ── 7g. Create Journal Entry ──────────────────────────────────────────
		memo := "Gateway payout — " + input.ProviderPayoutID
		je := models.JournalEntry{
			CompanyID:  input.CompanyID,
			EntryDate:  input.PayoutDate,
			JournalNo:  "GWPAYOUT-" + input.ProviderPayoutID,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceGatewayPayout,
			SourceID:   payout.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("payout: create journal entry: %w", err)
		}

		// Build lines.
		lines := make([]models.JournalLine, 0, 3)

		// Dr Bank = net
		lines = append(lines, models.JournalLine{
			CompanyID:      input.CompanyID,
			JournalEntryID: je.ID,
			AccountID:      input.BankAccountID,
			Debit:          net,
			Credit:         decimal.Zero,
			Memo:           memo,
		})

		// Dr Fee Expense = fee (only when fee > 0)
		if input.FeeAmount.IsPositive() {
			lines = append(lines, models.JournalLine{
				CompanyID:      input.CompanyID,
				JournalEntryID: je.ID,
				AccountID:      *mapping.FeeExpenseAccountID,
				Debit:          input.FeeAmount,
				Credit:         decimal.Zero,
				Memo:           "Gateway fee — " + input.ProviderPayoutID,
			})
		}

		// Cr Clearing = gross
		lines = append(lines, models.JournalLine{
			CompanyID:      input.CompanyID,
			JournalEntryID: je.ID,
			AccountID:      *mapping.ClearingAccountID,
			Debit:          decimal.Zero,
			Credit:         gross,
			Memo:           memo,
		})

		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("payout: create journal lines: %w", err)
		}

		// ── 7h. Project to ledger ─────────────────────────────────────────────
		if err := ProjectToLedger(tx, input.CompanyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceGatewayPayout,
			SourceID:     payout.ID,
		}); err != nil {
			return fmt.Errorf("payout: project to ledger: %w", err)
		}

		// ── 7i. Back-fill JournalEntryID on payout ────────────────────────────
		if err := tx.Model(&payout).Update("journal_entry_id", je.ID).Error; err != nil {
			return fmt.Errorf("payout: update journal_entry_id: %w", err)
		}

		linkedSettlements = lockedSettlements

		slog.Info("gateway payout bridge created",
			"payout_id", payout.ID,
			"provider_payout_id", input.ProviderPayoutID,
			"settlements", len(lockedSettlements),
			"gross", gross.StringFixed(2),
			"fee", input.FeeAmount.StringFixed(2),
			"net", net.StringFixed(2),
			"journal_entry_id", je.ID,
		)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &GatewayPayoutResult{
		Payout:      &payout,
		Settlements: linkedSettlements,
	}, nil
}
