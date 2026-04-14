// 遵循project_guide.md
package models

// ar_write_off.go — ARWriteOff: bad-debt write-off of an AR balance.
//
// ARWriteOff records the formal write-off of an uncollectible receivable balance.
// It drives a journal entry through the Posting Engine.
//
// 会计规则：
//
//   Post (draft → posted):
//     Dr  ExpenseAccountID   Amount × ExchangeRate   (bad debt / write-off expense)
//     Cr  ARAccountID        Amount × ExchangeRate   (reduce AR)
//
// 状态机：
//
//   draft → posted → reversed
//          ↘ voided (from draft)

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ARWriteOffStatus tracks the lifecycle of a write-off.
type ARWriteOffStatus string

const (
	// ARWriteOffStatusDraft — write-off prepared but not yet posted.
	ARWriteOffStatusDraft ARWriteOffStatus = "draft"

	// ARWriteOffStatusPosted — JE created; AR balance reduced.
	ARWriteOffStatusPosted ARWriteOffStatus = "posted"

	// ARWriteOffStatusReversed — write-off reversed (rare recovery scenario).
	ARWriteOffStatusReversed ARWriteOffStatus = "reversed"

	// ARWriteOffStatusVoided — cancelled before posting.
	ARWriteOffStatusVoided ARWriteOffStatus = "voided"
)

// AllARWriteOffStatuses returns statuses in display order.
func AllARWriteOffStatuses() []ARWriteOffStatus {
	return []ARWriteOffStatus{
		ARWriteOffStatusDraft,
		ARWriteOffStatusPosted,
		ARWriteOffStatusReversed,
		ARWriteOffStatusVoided,
	}
}

// ARWriteOffStatusLabel returns a human-readable label.
func ARWriteOffStatusLabel(s ARWriteOffStatus) string {
	switch s {
	case ARWriteOffStatusDraft:
		return "Draft"
	case ARWriteOffStatusPosted:
		return "Posted"
	case ARWriteOffStatusReversed:
		return "Reversed"
	case ARWriteOffStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// ARWriteOff records the formal bad-debt / uncollectible write-off of an AR balance.
//
// Phase 1 establishes the model skeleton. Phase 6 implements posting.
type ARWriteOff struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// InvoiceID is the originating invoice being written off (optional but recommended).
	InvoiceID *uint   `gorm:"index"`
	Invoice   Invoice `gorm:"foreignKey:InvoiceID"`

	// JournalEntryID is set when the write-off is posted.
	JournalEntryID *uint         `gorm:"uniqueIndex"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// ARAccountID is the Accounts Receivable account being credited.
	ARAccountID *uint    `gorm:"index"`
	ARAccount   *Account `gorm:"foreignKey:ARAccountID"`

	// ExpenseAccountID is the Bad Debt Expense (or similar) account being debited.
	ExpenseAccountID *uint    `gorm:"index"`
	ExpenseAccount   *Account `gorm:"foreignKey:ExpenseAccountID"`

	WriteOffNumber string           `gorm:"type:varchar(50);not null;default:''"`
	Status         ARWriteOffStatus `gorm:"type:text;not null;default:'draft'"`
	WriteOffDate   time.Time        `gorm:"not null"`

	// CurrencyCode — currency of the write-off (matches the invoice currency).
	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(18,8);not null;default:1"`

	// Amount is the document-currency write-off total.
	Amount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// AmountBase is the base-currency equivalent at time of posting.
	AmountBase decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	Reason string `gorm:"type:text;not null;default:''"`
	Memo   string `gorm:"type:text;not null;default:''"`

	// PostedAt is set when the write-off is posted.
	PostedAt *time.Time
	// PostedBy is the actor who posted.
	PostedBy string `gorm:"type:varchar(200);not null;default:''"`
	// PostedByUserID links to the user who posted (optional).
	PostedByUserID *uuid.UUID `gorm:"type:uuid"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
