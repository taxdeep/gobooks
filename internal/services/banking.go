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
)

// ReconcileCandidate is one unreconciled journal line for an account.
type ReconcileCandidate struct {
	LineID    uint
	EntryDate time.Time
	JournalNo string
	SourceType string
	PayeeName  string
	Memo       string
	Debit      decimal.Decimal
	Credit     decimal.Decimal

	// Amount is a convenience: for the bank account (asset),
	// amount = debit - credit.
	Amount decimal.Decimal

	// Payment is money leaving the bank account (credit side).
	// Deposit is money entering the bank account (debit side).
	Payment decimal.Decimal
	Deposit decimal.Decimal
}

// ListReconcileCandidates returns unreconciled lines for a given account
// up to (and including) statementDate. companyID must match the account's company.
func ListReconcileCandidates(db *gorm.DB, companyID, accountID uint, statementDate time.Time) ([]ReconcileCandidate, error) {
	var out []ReconcileCandidate
	err := db.Raw(
		`
SELECT
  jl.id AS line_id,
  je.entry_date AS entry_date,
  je.journal_no AS journal_no,
  COALESCE(je.source_type, '') AS source_type,
  COALESCE(
    CASE
      WHEN jl.party_type = 'customer' THEN (SELECT name FROM customers WHERE id = jl.party_id LIMIT 1)
      WHEN jl.party_type = 'vendor'   THEN (SELECT name FROM vendors   WHERE id = jl.party_id LIMIT 1)
      ELSE ''
    END,
    ''
  ) AS payee_name,
  jl.memo AS memo,
  jl.debit AS debit,
  jl.credit AS credit,
  (jl.debit - jl.credit) AS amount,
  jl.credit AS payment,
  jl.debit AS deposit
FROM journal_lines jl
JOIN journal_entries je ON je.id = jl.journal_entry_id
WHERE jl.account_id = ?
  AND jl.company_id = ?
  AND je.company_id = ?
  AND je.entry_date <= ?
  AND jl.reconciliation_id IS NULL
ORDER BY je.entry_date ASC, jl.id ASC
`,
		accountID, companyID, companyID, statementDate,
	).Scan(&out).Error
	return out, err
}

// ClearedBalance returns the sum of (debit - credit) for lines
// already reconciled for the account up to statementDate. companyID must match the account's company.
func ClearedBalance(db *gorm.DB, companyID, accountID uint, statementDate time.Time) (decimal.Decimal, error) {
	type row struct {
		Amount decimal.Decimal
	}
	var r row
	err := db.Raw(
		`
SELECT COALESCE(SUM(jl.debit - jl.credit), 0) AS amount
FROM journal_lines jl
JOIN journal_entries je ON je.id = jl.journal_entry_id
WHERE jl.account_id = ?
  AND jl.company_id = ?
  AND je.company_id = ?
  AND je.entry_date <= ?
  AND jl.reconciliation_id IS NOT NULL
`,
		accountID, companyID, companyID, statementDate,
	).Scan(&r).Error
	return r.Amount, err
}

// LatestActiveReconciliation returns the most recent non-voided reconciliation
// for the given account, or nil if none exists.
func LatestActiveReconciliation(db *gorm.DB, companyID, accountID uint) (*models.Reconciliation, error) {
	var rec models.Reconciliation
	err := db.Where("company_id = ? AND account_id = ? AND is_voided = FALSE", companyID, accountID).
		Order("id DESC").
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ReconcileDraftResult is a lightweight view of a draft used by the handler
// to restore UI state without exposing the full model.
type ReconcileDraftResult struct {
	SelectedLineIDs string // JSON array of line ID strings
}

// ── Reconciliation default-value decision engine ──────────────────────────────

// ReconcileDefaultSource describes which rule produced the defaults.
type ReconcileDefaultSource int

const (
	// ReconcileDefaultsBlank: first-time reconciliation — no data for this account.
	// Handler must leave StatementDate and EndingBalance empty.
	ReconcileDefaultsBlank ReconcileDefaultSource = iota
	// ReconcileDefaultsDraft: an in-progress draft was found. All fields are restored.
	ReconcileDefaultsDraft
	// ReconcileDefaultsInferred: no draft, but a prior completed reconciliation exists.
	// StatementDate is set to the next month-end; EndingBalance is left empty.
	ReconcileDefaultsInferred
)

// ReconcileDefaults holds the computed default values for the reconciliation form
// when the user selects a bank account and has not yet supplied URL parameters.
type ReconcileDefaults struct {
	Source        ReconcileDefaultSource
	StatementDate string // "2006-01-02" or ""
	EndingBalance string // decimal string or ""
	// SelectedLineIDs is non-empty only when Source == ReconcileDefaultsDraft.
	SelectedLineIDs string
	// LastStatementDate is the most recent completed reconciliation's statement date,
	// formatted for display (DD/MM/YYYY). Empty if no prior reconciliation exists.
	LastStatementDate string
}

// ComputeReconcileDefaults decides what to pre-fill in the reconciliation form
// for a given (company, account) when the user has not yet provided URL params.
//
// Priority:
//  1. In-progress draft (ReconcileDefaultsDraft) — highest
//  2. Completed reconciliation → infer next month-end (ReconcileDefaultsInferred)
//  3. No data → blank form (ReconcileDefaultsBlank)
//
// Next-month-end rule:
//
//	Given last statement date D, the next default is the last calendar day of
//	the month following D's month. This is true regardless of what day D falls on.
//	Examples:
//	  2026-03-31 → 2026-04-30
//	  2026-01-15 → 2026-02-28
//	  2025-12-31 → 2026-01-31
func ComputeReconcileDefaults(db *gorm.DB, companyID, accountID uint) (ReconcileDefaults, error) {
	// 1. Check for an in-progress draft.
	draft, err := GetReconcileDraft(db, companyID, accountID)
	if err != nil {
		return ReconcileDefaults{}, err
	}
	if draft != nil {
		return ReconcileDefaults{
			Source:          ReconcileDefaultsDraft,
			StatementDate:   draft.StatementDate,
			EndingBalance:   draft.EndingBalance.StringFixed(2),
			SelectedLineIDs: draft.SelectedLineIDs,
		}, nil
	}

	// 2. Check for the most recent completed reconciliation.
	latest, err := LatestActiveReconciliation(db, companyID, accountID)
	if err != nil {
		return ReconcileDefaults{}, err
	}
	if latest != nil {
		next := nextMonthEnd(latest.StatementDate)
		return ReconcileDefaults{
			Source:            ReconcileDefaultsInferred,
			StatementDate:     next.Format("2006-01-02"),
			EndingBalance:     "", // do not guess; user must enter
			LastStatementDate: latest.StatementDate.Format("02/01/2006"),
		}, nil
	}

	// 3. First reconciliation for this account.
	return ReconcileDefaults{Source: ReconcileDefaultsBlank}, nil
}

// nextMonthEnd returns the last calendar day of the month following t.
// It works by going to the first day of t's month + 2 months, then subtracting 1 day.
func nextMonthEnd(t time.Time) time.Time {
	// Advance to the first day of the month after next month, then subtract 1 day.
	y, m, _ := t.Date()
	firstOfMonthAfterNext := time.Date(y, m+2, 1, 0, 0, 0, 0, time.UTC)
	return firstOfMonthAfterNext.AddDate(0, 0, -1)
}

// ── Reconciliation draft (save-progress) ─────────────────────────────────────

// UpsertReconcileDraft saves or overwrites the in-progress reconcile state for
// a (company, account) pair. One draft per pair is enforced by the unique index.
func UpsertReconcileDraft(db *gorm.DB, companyID, accountID uint, statementDate, endingBalance, selectedLineIDsJSON string) error {
	eb, _ := decimal.NewFromString(endingBalance)
	var existing models.ReconciliationDraft
	err := db.Where("company_id = ? AND account_id = ?", companyID, accountID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Create(&models.ReconciliationDraft{
			CompanyID:       companyID,
			AccountID:       accountID,
			StatementDate:   statementDate,
			EndingBalance:   eb,
			SelectedLineIDs: selectedLineIDsJSON,
		}).Error
	}
	if err != nil {
		return err
	}
	return db.Model(&existing).Updates(map[string]any{
		"statement_date":    statementDate,
		"ending_balance":    eb,
		"selected_line_ids": selectedLineIDsJSON,
	}).Error
}

// GetReconcileDraft returns the in-progress draft for the account, or nil if none.
func GetReconcileDraft(db *gorm.DB, companyID, accountID uint) (*models.ReconciliationDraft, error) {
	var d models.ReconciliationDraft
	err := db.Where("company_id = ? AND account_id = ?", companyID, accountID).First(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteReconcileDraft removes the draft after a successful reconciliation finish.
// Best-effort: callers should not fail the main flow if this errors.
func DeleteReconcileDraft(db *gorm.DB, companyID, accountID uint) error {
	return db.Where("company_id = ? AND account_id = ?", companyID, accountID).
		Delete(&models.ReconciliationDraft{}).Error
}

// VoidReconciliation voids the given reconciliation inside a transaction.
// Only the latest active reconciliation for an account may be voided.
// All journal lines linked to it are unreconciled (reconciliation_id = NULL).
func VoidReconciliation(db *gorm.DB, companyID uint, recID uint, userID uuid.UUID, reason string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var rec models.Reconciliation
		if err := tx.Where("id = ? AND company_id = ? AND is_voided = FALSE", recID, companyID).
			First(&rec).Error; err != nil {
			return err
		}

		// Verify no newer active reconciliation exists for this account.
		var newer int64
		if err := tx.Model(&models.Reconciliation{}).
			Where("account_id = ? AND company_id = ? AND id > ? AND is_voided = FALSE", rec.AccountID, companyID, recID).
			Count(&newer).Error; err != nil {
			return err
		}
		if newer > 0 {
			return errors.New("only the latest reconciliation can be voided")
		}

		// Unreconcile all journal lines linked to this reconciliation.
		if err := tx.Model(&models.JournalLine{}).
			Where("reconciliation_id = ?", recID).
			Updates(map[string]any{
				"reconciliation_id": nil,
				"reconciled_at":     nil,
			}).Error; err != nil {
			return err
		}

		// Mark the reconciliation as voided.
		now := time.Now()
		if err := tx.Model(&rec).Updates(map[string]any{
			"is_voided":           true,
			"void_reason":         reason,
			"voided_at":           &now,
			"voided_by_user_id":   userID,
		}).Error; err != nil {
			return err
		}

		return nil
	})
}

// ── Reconciliation setup adjustments ─────────────────────────────────────────

// ReconcileSetupEntriesInput carries optional service-charge and interest-earned
// amounts to be posted as journal entries before entering work mode.
// Any field whose amount is zero (or negative) is skipped.
type ReconcileSetupEntriesInput struct {
	CompanyID     uint
	BankAccountID uint

	// Service charge: bank fee (DR expense, CR bank).
	ServiceCharge          decimal.Decimal
	ServiceChargeDate      time.Time
	ServiceChargeAccountID uint

	// Interest earned: bank interest income (DR bank, CR income).
	InterestEarned          decimal.Decimal
	InterestEarnedDate      time.Time
	InterestEarnedAccountID uint
}

// CreateReconcileSetupEntries posts service-charge and/or interest-earned journal
// entries atomically. Each non-zero amount produces one two-line journal entry.
// Entries are immediately cleared (added to the reconciliation candidate pool).
func CreateReconcileSetupEntries(db *gorm.DB, in ReconcileSetupEntriesInput) error {
	if !in.ServiceCharge.IsPositive() && !in.InterestEarned.IsPositive() {
		return nil // nothing to do
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if in.ServiceCharge.IsPositive() {
			if in.ServiceChargeAccountID == 0 {
				return errors.New("service charge account is required when a service charge amount is provided")
			}
			je := models.JournalEntry{
				CompanyID:  in.CompanyID,
				EntryDate:  in.ServiceChargeDate,
				JournalNo:  "Bank Service Charge",
				Status:     models.JournalEntryStatusPosted,
				SourceType: models.LedgerSourceBankCharge,
			}
			if err := tx.Create(&je).Error; err != nil {
				return fmt.Errorf("create bank charge journal entry: %w", err)
			}
			lines := []models.JournalLine{
				// DR expense account
				{CompanyID: in.CompanyID, JournalEntryID: je.ID, AccountID: in.ServiceChargeAccountID, Debit: in.ServiceCharge, Credit: decimal.Zero, Memo: "Bank service charge"},
				// CR bank account
				{CompanyID: in.CompanyID, JournalEntryID: je.ID, AccountID: in.BankAccountID, Debit: decimal.Zero, Credit: in.ServiceCharge, Memo: "Bank service charge"},
			}
			for i := range lines {
				if err := tx.Create(&lines[i]).Error; err != nil {
					return fmt.Errorf("create bank charge journal line: %w", err)
				}
			}
			if err := ProjectToLedger(tx, in.CompanyID, LedgerPostInput{
				JournalEntry: je,
				Lines:        lines,
				SourceType:   models.LedgerSourceBankCharge,
			}); err != nil {
				return fmt.Errorf("project bank charge to ledger: %w", err)
			}
		}

		if in.InterestEarned.IsPositive() {
			if in.InterestEarnedAccountID == 0 {
				return errors.New("interest earned account is required when an interest earned amount is provided")
			}
			je := models.JournalEntry{
				CompanyID:  in.CompanyID,
				EntryDate:  in.InterestEarnedDate,
				JournalNo:  "Bank Interest Earned",
				Status:     models.JournalEntryStatusPosted,
				SourceType: models.LedgerSourceBankInterest,
			}
			if err := tx.Create(&je).Error; err != nil {
				return fmt.Errorf("create bank interest journal entry: %w", err)
			}
			lines := []models.JournalLine{
				// DR bank account
				{CompanyID: in.CompanyID, JournalEntryID: je.ID, AccountID: in.BankAccountID, Debit: in.InterestEarned, Credit: decimal.Zero, Memo: "Bank interest earned"},
				// CR income account
				{CompanyID: in.CompanyID, JournalEntryID: je.ID, AccountID: in.InterestEarnedAccountID, Debit: decimal.Zero, Credit: in.InterestEarned, Memo: "Bank interest earned"},
			}
			for i := range lines {
				if err := tx.Create(&lines[i]).Error; err != nil {
					return fmt.Errorf("create bank interest journal line: %w", err)
				}
			}
			if err := ProjectToLedger(tx, in.CompanyID, LedgerPostInput{
				JournalEntry: je,
				Lines:        lines,
				SourceType:   models.LedgerSourceBankInterest,
			}); err != nil {
				return fmt.Errorf("project bank interest to ledger: %w", err)
			}
		}

		return nil
	})
}
