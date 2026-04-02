// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// RevaluationRunStatus tracks the lifecycle of a revaluation run.
type RevaluationRunStatus string

const (
	// RevaluationRunStatusPosted means the revaluation JE has been posted and the
	// auto-reversal JE has been created (but is dated in the future).
	RevaluationRunStatusPosted RevaluationRunStatus = "posted"

	// RevaluationRunStatusReversed means the reversal JE date has passed.
	// (This status is set externally; Phase 5 always creates runs as "posted".)
	RevaluationRunStatusReversed RevaluationRunStatus = "reversed"
)

// RevaluationRun records one period-end unrealized-FX revaluation pass.
//
// Two journal entries are created atomically:
//   - JournalEntryID  — the revaluation JE (date = RunDate)
//   - ReversalJEID    — the auto-reversal JE (date = ReversalDate)
//
// Revaluation does NOT update BalanceDue or BalanceDueBase on source documents.
// Carrying values stay at original posting rates; the JEs are the only record
// of the unrealized adjustment.
type RevaluationRun struct {
	ID           uint                 `gorm:"primaryKey"`
	CompanyID    uint                 `gorm:"not null;index"`
	RunDate      time.Time            `gorm:"not null"`
	ReversalDate time.Time            `gorm:"not null"`
	Status       RevaluationRunStatus `gorm:"type:text;not null;default:'posted'"`
	// JournalEntryID is the revaluation JE (0 until the transaction commits).
	JournalEntryID uint  `gorm:"not null;default:0"`
	// ReversalJEID is the auto-reversal JE (nil until the transaction commits).
	ReversalJEID *uint
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RevaluationLine is one row per open foreign-currency document in a revaluation run.
//
// Adjustment = NewBase − OldBase.
//   Positive: rate rose   → AR unrealized gain / AP unrealized loss.
//   Negative: rate fell   → AR unrealized loss / AP unrealized gain.
type RevaluationLine struct {
	ID               uint            `gorm:"primaryKey"`
	RevaluationRunID uint            `gorm:"not null;index"`
	CompanyID        uint            `gorm:"not null"`
	// DocumentType is "invoice" or "bill".
	DocumentType string `gorm:"type:text;not null"`
	DocumentID   uint   `gorm:"not null"`
	// AccountID is the AR (for invoices) or AP (for bills) account affected.
	AccountID    uint   `gorm:"not null"`
	CurrencyCode string `gorm:"type:varchar(3);not null"`
	// BalanceDue is the remaining document-currency balance at run time.
	BalanceDue decimal.Decimal `gorm:"type:numeric(18,2);not null"`
	// RevaluationRate is the exchange rate used (foreign → base).
	RevaluationRate decimal.Decimal `gorm:"type:numeric(20,8);not null"`
	// OldBase is the carrying value in base currency before revaluation.
	OldBase decimal.Decimal `gorm:"type:numeric(18,2);not null"`
	// NewBase = BalanceDue × RevaluationRate (rounded to 2 dp).
	NewBase decimal.Decimal `gorm:"type:numeric(18,2);not null"`
	// Adjustment = NewBase − OldBase.
	Adjustment decimal.Decimal `gorm:"type:numeric(18,2);not null"`
	CreatedAt  time.Time
}
