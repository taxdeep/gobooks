// 遵循project_guide.md
package services

// payout_reconciliation_service.go — Batch 18: Payout ↔ bank entry matching.
//
// Provides:
//   - CreateBankEntry:                  manually record a bank-side deposit
//   - MatchGatewayPayoutToBankEntry:    1:1 match a payout to a bank entry
//   - ListUnmatchedGatewayPayouts:      payouts with no PayoutReconciliation record
//   - ListUnmatchedBankEntries:         bank entries with no PayoutReconciliation record
//   - ListMatchedPayoutReconciliations: completed match records
//   - GetPayoutReconciliation:          load a match record by payout ID
//   - GetBankEntry:                     load a bank entry by ID
//   - ListBankEntriesForAccount:        list all entries for a bank account
//
// Invariants:
//   - Company isolation: all objects must share companyID.
//   - Account match: payout.BankAccountID == bankEntry.BankAccountID.
//   - Strict amount equality (NetAmount == BankEntry.Amount; no tolerance).
//   - 1:1 via unique indexes: uq_payout_recon_payout + uq_payout_recon_bank_entry.
//   - No JE created or modified by any reconciliation action.
//   - Lock order: payout row (lower table) first, bank entry row second — consistent
//     to prevent deadlocks under concurrent matching attempts.
//
// Future scope (not in Batch 18):
//   - Multi-payout batch vs single bank credit
//   - Amount tolerance / partial matching
//   - Bank statement CSV import
//   - Automatic match suggestions

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrReconPayoutNotFound          = errors.New("gateway payout not found or does not belong to this company")
	ErrReconBankEntryNotFound       = errors.New("bank entry not found or does not belong to this company")
	ErrReconPayoutAlreadyMatched    = errors.New("gateway payout is already matched to a bank entry")
	ErrReconBankEntryAlreadyMatched = errors.New("bank entry is already matched to a gateway payout")
	ErrReconAmountMismatch          = errors.New("payout net amount does not match bank entry amount")
	ErrReconAccountMismatch         = errors.New("payout bank account does not match bank entry bank account")
	ErrReconCurrencyMismatch        = errors.New("payout currency does not match bank entry currency")
	ErrReconBankEntryInvalid        = errors.New("bank entry amount must be positive")
	ErrReconBankAccountInvalid      = errors.New("bank account not found or does not belong to this company")
)

// ── Bank entry CRUD ───────────────────────────────────────────────────────────

// CreateBankEntryInput holds the fields needed to record a bank-side deposit.
type CreateBankEntryInput struct {
	CompanyID     uint
	BankAccountID uint
	EntryDate     time.Time
	Amount        decimal.Decimal
	CurrencyCode  string
	Description   string
}

// CreateBankEntry records a manually-entered bank deposit.
// Validates that the bank account exists, is active, and is an asset account
// belonging to the company.
func CreateBankEntry(db *gorm.DB, input CreateBankEntryInput) (*models.BankEntry, error) {
	if !input.Amount.IsPositive() {
		return nil, ErrReconBankEntryInvalid
	}

	// Validate bank account.
	var acct models.Account
	if err := db.Where("id = ? AND company_id = ?", input.BankAccountID, input.CompanyID).
		First(&acct).Error; err != nil {
		return nil, ErrReconBankAccountInvalid
	}
	if !acct.IsActive {
		return nil, fmt.Errorf("%w: account is inactive", ErrReconBankAccountInvalid)
	}
	if acct.RootAccountType != models.RootAsset {
		return nil, fmt.Errorf("%w: account must be an asset account", ErrReconBankAccountInvalid)
	}

	entry := &models.BankEntry{
		CompanyID:     input.CompanyID,
		BankAccountID: input.BankAccountID,
		EntryDate:     input.EntryDate,
		Amount:        input.Amount,
		CurrencyCode:  input.CurrencyCode,
		Description:   input.Description,
	}
	if err := db.Create(entry).Error; err != nil {
		return nil, fmt.Errorf("create bank entry: %w", err)
	}
	return entry, nil
}

// GetBankEntry loads a bank entry scoped to a company.
func GetBankEntry(db *gorm.DB, companyID, entryID uint) (*models.BankEntry, error) {
	var e models.BankEntry
	if err := db.Where("id = ? AND company_id = ?", entryID, companyID).First(&e).Error; err != nil {
		return nil, ErrReconBankEntryNotFound
	}
	return &e, nil
}

// ListBankEntriesForAccount returns all bank entries for a bank account, newest first.
func ListBankEntriesForAccount(db *gorm.DB, companyID, bankAccountID uint) ([]models.BankEntry, error) {
	var entries []models.BankEntry
	err := db.
		Where("company_id = ? AND bank_account_id = ?", companyID, bankAccountID).
		Order("entry_date DESC, id DESC").
		Find(&entries).Error
	return entries, err
}

// ListAllBankEntries returns all bank entries for a company, newest first.
func ListAllBankEntries(db *gorm.DB, companyID uint) ([]models.BankEntry, error) {
	var entries []models.BankEntry
	err := db.
		Where("company_id = ?", companyID).
		Order("entry_date DESC, id DESC").
		Find(&entries).Error
	return entries, err
}

// ── Unmatched queries ─────────────────────────────────────────────────────────

// ListUnmatchedGatewayPayouts returns payouts that have no PayoutReconciliation
// record — i.e. not yet matched to a bank entry.
func ListUnmatchedGatewayPayouts(db *gorm.DB, companyID uint) ([]models.GatewayPayout, error) {
	var payouts []models.GatewayPayout
	err := db.
		Where("company_id = ?", companyID).
		Where("id NOT IN (?)",
			db.Model(&models.PayoutReconciliation{}).
				Select("gateway_payout_id").
				Where("company_id = ?", companyID),
		).
		Order("payout_date DESC, id DESC").
		Find(&payouts).Error
	return payouts, err
}

// ListUnmatchedBankEntries returns bank entries that have no PayoutReconciliation
// record — i.e. not yet matched to a payout.
func ListUnmatchedBankEntries(db *gorm.DB, companyID uint) ([]models.BankEntry, error) {
	var entries []models.BankEntry
	err := db.
		Where("company_id = ?", companyID).
		Where("id NOT IN (?)",
			db.Model(&models.PayoutReconciliation{}).
				Select("bank_entry_id").
				Where("company_id = ?", companyID),
		).
		Order("entry_date DESC, id DESC").
		Find(&entries).Error
	return entries, err
}

// ListMatchedPayoutReconciliations returns all completed reconciliation records
// for a company, newest first.
func ListMatchedPayoutReconciliations(db *gorm.DB, companyID uint) ([]models.PayoutReconciliation, error) {
	var recs []models.PayoutReconciliation
	err := db.
		Where("company_id = ?", companyID).
		Order("matched_at DESC, id DESC").
		Find(&recs).Error
	return recs, err
}

// GetPayoutReconciliation loads the reconciliation record for a payout, if any.
// Returns nil, nil when no match exists (unmatched payout).
func GetPayoutReconciliation(db *gorm.DB, companyID, gatewayPayoutID uint) (*models.PayoutReconciliation, error) {
	var rec models.PayoutReconciliation
	err := db.
		Where("company_id = ? AND gateway_payout_id = ?", companyID, gatewayPayoutID).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &rec, err
}

// ── Core matching ─────────────────────────────────────────────────────────────

// MatchGatewayPayoutToBankEntry creates a 1:1 reconciliation record linking a
// GatewayPayout to a BankEntry.
//
// Business rules enforced transactionally:
//   - Both objects must belong to companyID (company isolation).
//   - payout.BankAccountID == bankEntry.BankAccountID (account match).
//   - ComputeGatewayPayoutExpectedNet(payout) == bankEntry.Amount (strict equality).
//     When no components exist, ExpectedNet == payout.NetAmount (Batch 18 behaviour
//     is preserved exactly).  When components exist, the expected bank deposit after
//     fee/reserve/adjustment components is used as the matching basis.
//   - currencyCodesMatch(payout.CurrencyCode, bankEntry.CurrencyCode).
//   - Neither the payout nor the bank entry may already be matched.
//
// On success a PayoutReconciliation record is inserted and an audit log entry
// is written.  No JE is created or modified.
//
// Locking order: payout row first, bank entry row second (both by table-PK
// ascending within each table) to prevent deadlocks on concurrent match
// attempts involving the same pair.
func MatchGatewayPayoutToBankEntry(
	db *gorm.DB,
	companyID uint,
	gatewayPayoutID uint,
	bankEntryID uint,
	actor string,
) error {
	// Pre-transaction existence checks (cheap, non-locking).
	if err := validatePayoutMatchCandidates(db, companyID, gatewayPayoutID, bankEntryID); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		return matchGatewayPayoutToBankEntryTx(tx, companyID, gatewayPayoutID, bankEntryID, actor)
	})
}

func matchGatewayPayoutToBankEntryTx(
	tx *gorm.DB,
	companyID uint,
	gatewayPayoutID uint,
	bankEntryID uint,
	actor string,
) error {
	// 1. Lock payout row first (lower-ID table first per lock ordering convention).
	var payout models.GatewayPayout
	if err := applyLockForUpdate(
		tx.Where("id = ? AND company_id = ?", gatewayPayoutID, companyID),
	).First(&payout).Error; err != nil {
		return ErrReconPayoutNotFound
	}

	// 2. Lock bank entry row.
	var entry models.BankEntry
	if err := applyLockForUpdate(
		tx.Where("id = ? AND company_id = ?", bankEntryID, companyID),
	).First(&entry).Error; err != nil {
		return ErrReconBankEntryNotFound
	}

	// 3. Re-check matched state under lock (concurrent submission guard).
	var existingPayoutMatch int64
	tx.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", companyID, gatewayPayoutID).
		Count(&existingPayoutMatch)
	if existingPayoutMatch > 0 {
		return ErrReconPayoutAlreadyMatched
	}

	var existingEntryMatch int64
	tx.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND bank_entry_id = ?", companyID, bankEntryID).
		Count(&existingEntryMatch)
	if existingEntryMatch > 0 {
		return ErrReconBankEntryAlreadyMatched
	}

	// 4. Business-rule validations under lock.
	if payout.BankAccountID != entry.BankAccountID {
		return ErrReconAccountMismatch
	}

	// Compute expected net (payout.NetAmount adjusted by components) inside
	// the transaction so the component set is consistent with the locked payout.
	expectedNet, err := ComputeGatewayPayoutExpectedNet(tx, companyID, &payout)
	if err != nil {
		return fmt.Errorf("compute expected net: %w", err)
	}
	if !expectedNet.Equal(entry.Amount) {
		return fmt.Errorf("%w: expected net=%s (payout net=%s + components), bank entry=%s",
			ErrReconAmountMismatch,
			expectedNet.StringFixed(2),
			payout.NetAmount.StringFixed(2),
			entry.Amount.StringFixed(2))
	}
	if !currencyCodesMatch(payout.CurrencyCode, entry.CurrencyCode) {
		return fmt.Errorf("%w: payout=%q, entry=%q",
			ErrReconCurrencyMismatch, payout.CurrencyCode, entry.CurrencyCode)
	}

	// 5. Create reconciliation record.
	rec := models.PayoutReconciliation{
		CompanyID:       companyID,
		GatewayPayoutID: gatewayPayoutID,
		BankEntryID:     bankEntryID,
		MatchedAt:       time.Now(),
		Actor:           actor,
	}
	if err := tx.Create(&rec).Error; err != nil {
		if conflictErr := classifyPayoutReconUniqueConflict(err); conflictErr != nil {
			return conflictErr
		}
		return fmt.Errorf("create payout reconciliation: %w", err)
	}

	// 6. Audit log.
	cid := companyID
	if err := WriteAuditLogWithContextDetails(tx,
		"payout.reconciled",
		"gateway_payout", gatewayPayoutID, actor,
		map[string]any{"company_id": companyID},
		&cid, nil, nil,
		map[string]any{
			"bank_entry_id":     bankEntryID,
			"payout_net_amount": payout.NetAmount.StringFixed(2),
			"expected_net":      expectedNet.StringFixed(2),
			"bank_entry_amount": entry.Amount.StringFixed(2),
			"bank_account_id":   payout.BankAccountID,
		},
	); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}

	slog.Info("payout reconciled",
		"payout_id", gatewayPayoutID,
		"bank_entry_id", bankEntryID,
		"expected_net", expectedNet.StringFixed(2),
		"payout_net", payout.NetAmount.StringFixed(2),
	)
	return nil
}

// ListCandidateBankEntries returns unmatched bank entries that could match a
// given payout: same company, same bank account, same expected net, same currency.
//
// Expected net = payout.NetAmount adjusted by all recorded components.
// When no components exist, this equals payout.NetAmount (Batch 18 behaviour).
func ListCandidateBankEntries(db *gorm.DB, companyID uint, payout *models.GatewayPayout) ([]models.BankEntry, error) {
	// Compute expected net to use as the amount filter.
	expectedNet, err := ComputeGatewayPayoutExpectedNet(db, companyID, payout)
	if err != nil {
		return nil, fmt.Errorf("list candidates: compute expected net: %w", err)
	}

	var entries []models.BankEntry
	err = db.
		Where("company_id = ? AND bank_account_id = ? AND amount = ? AND currency_code = ?",
			companyID, payout.BankAccountID, expectedNet, payout.CurrencyCode).
		Where("id NOT IN (?)",
			db.Model(&models.PayoutReconciliation{}).
				Select("bank_entry_id").
				Where("company_id = ?", companyID),
		).
		Order("entry_date DESC, id DESC").
		Find(&entries).Error
	return entries, err
}

// ── Validation helpers ────────────────────────────────────────────────────────

// validatePayoutMatchCandidates does a lightweight pre-transaction check that
// both objects exist and have matching currency + account before acquiring locks.
// The full transactional re-check in MatchGatewayPayoutToBankEntry is the
// authoritative truth.
func validatePayoutMatchCandidates(db *gorm.DB, companyID, gatewayPayoutID, bankEntryID uint) error {
	var payout models.GatewayPayout
	if err := db.Where("id = ? AND company_id = ?", gatewayPayoutID, companyID).
		First(&payout).Error; err != nil {
		return ErrReconPayoutNotFound
	}

	var entry models.BankEntry
	if err := db.Where("id = ? AND company_id = ?", bankEntryID, companyID).
		First(&entry).Error; err != nil {
		return ErrReconBankEntryNotFound
	}

	if payout.BankAccountID != entry.BankAccountID {
		return ErrReconAccountMismatch
	}
	// Use expected net (payout.NetAmount adjusted by components) as the match basis.
	expectedNet, err := ComputeGatewayPayoutExpectedNet(db, companyID, &payout)
	if err != nil {
		return fmt.Errorf("compute expected net: %w", err)
	}
	if !expectedNet.Equal(entry.Amount) {
		return fmt.Errorf("%w: expected net=%s (payout net=%s + components), bank entry=%s",
			ErrReconAmountMismatch,
			expectedNet.StringFixed(2),
			payout.NetAmount.StringFixed(2),
			entry.Amount.StringFixed(2))
	}
	if !currencyCodesMatch(payout.CurrencyCode, entry.CurrencyCode) {
		return fmt.Errorf("%w: payout=%q, entry=%q",
			ErrReconCurrencyMismatch, payout.CurrencyCode, entry.CurrencyCode)
	}
	return nil
}

// classifyPayoutReconUniqueConflict maps the DB unique-index backstop to the
// exact matched-side sentinel. Returning nil means the unique error was not
// recognized well enough to safely translate.
func classifyPayoutReconUniqueConflict(err error) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "uq_payout_recon_bank_entry":
			return ErrReconBankEntryAlreadyMatched
		case "uq_payout_recon_payout":
			return ErrReconPayoutAlreadyMatched
		}
		msg := strings.ToLower(pgErr.ConstraintName + " " + pgErr.Detail + " " + pgErr.Message)
		switch {
		case strings.Contains(msg, "uq_payout_recon_bank_entry"), strings.Contains(msg, "bank_entry_id"):
			return ErrReconBankEntryAlreadyMatched
		case strings.Contains(msg, "uq_payout_recon_payout"), strings.Contains(msg, "gateway_payout_id"):
			return ErrReconPayoutAlreadyMatched
		default:
			return nil
		}
	}

	if !isUniqueConstraintError(err) {
		return nil
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "uq_payout_recon_bank_entry"), strings.Contains(msg, "bank_entry_id"):
		return ErrReconBankEntryAlreadyMatched
	case strings.Contains(msg, "uq_payout_recon_payout"), strings.Contains(msg, "gateway_payout_id"):
		return ErrReconPayoutAlreadyMatched
	default:
		return nil
	}
}
