// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ── Journal entry status ──────────────────────────────────────────────────────

// JournalEntryStatus describes the lifecycle state of a journal entry.
//
// Storage rule: status is stored independently from the source document's own
// status field. The posting engine synchronises both inside a single transaction.
// Neither field drives the other directly — only the engine coordinates them.
type JournalEntryStatus string

const (
	// JournalEntryStatusDraft means the entry has been created but not committed
	// to books. Lines may still change. No active ledger entries exist.
	// The current engine does not produce draft JEs; reserved for future
	// approval workflows.
	JournalEntryStatusDraft JournalEntryStatus = "draft"

	// JournalEntryStatusPosted means the entry is committed to books.
	// Lines are immutable. Ledger entries are active (status=active).
	JournalEntryStatusPosted JournalEntryStatus = "posted"

	// JournalEntryStatusVoided means the entry was cancelled before posting.
	// No ledger entries exist for a voided JE.
	JournalEntryStatusVoided JournalEntryStatus = "voided"

	// JournalEntryStatusReversed is a legacy/non-reporting state retained for
	// audit compatibility. Current posted-document voids keep the original JE
	// posted and link a posted reversal JE through ReversedFromID.
	JournalEntryStatusReversed JournalEntryStatus = "reversed"
)

// ── Journal entry ─────────────────────────────────────────────────────────────

// JournalEntry is the header for a double-entry transaction.
//
// Lifecycle synchronisation (enforced by the posting engine, not DB triggers):
//
//	PostInvoice / PostBill       → Status = posted
//	VoidInvoice / VoidBill       → original JE Status = posted
//	                               reversal JE Status = posted
//	ReverseJournalEntry          → legacy reversal helper for manual flows
//
// ReversedFromID links a reversal JE back to the original it cancels.
// Report readers can use that relationship to hide reversal pairs from
// ordinary activity while audit views can show the full chain.
//
// Concurrency / uniqueness:
//
//	SourceType + SourceID identify the originating business document.
//	A unique partial index (status='posted', source_type != '', source_id > 0)
//	enforces that at most one active JE exists per (company, source, document).
//	Manual entries (SourceType = '') are excluded from the index.
type JournalEntry struct {
	ID        uint               `gorm:"primaryKey"`
	CompanyID uint               `gorm:"not null;index"`
	EntryDate time.Time          `gorm:"not null"`
	JournalNo string             `gorm:"column:journal_no;not null;default:''"`
	Status    JournalEntryStatus `gorm:"type:text;not null;default:'posted'"`
	// TransactionCurrencyCode always stores the explicit transaction currency ISO code.
	// Base-currency entries persist the company base currency, never an empty string.
	TransactionCurrencyCode string `gorm:"column:transaction_currency_code;size:3;not null;default:''"`
	// ExchangeRate stores the accepted immutable transaction-currency -> base-currency snapshot.
	ExchangeRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`
	// ExchangeRateDate stores the effective date of the accepted snapshot.
	ExchangeRateDate time.Time `gorm:"type:date"`
	// ExchangeRateSource stores JE snapshot semantics, not exchange-rate-row origin semantics.
	ExchangeRateSource string `gorm:"type:text;not null;default:'identity'"`

	// SourceType identifies the originating business document.
	// Empty string for manual journal entries; excluded from the uniqueness index.
	SourceType LedgerSourceType `gorm:"type:text;not null;default:''"`
	// SourceID is the PK of the originating document. 0 for manual entries.
	SourceID uint `gorm:"not null;default:0"`

	// ReversedFromID is set on the reversal JE; nil on normal entries.
	ReversedFromID *uint `gorm:"index"`

	// FXSnapshotID links to the structured FX rate snapshot used when this entry
	// was posted. Nil for base-currency entries and for entries posted before Phase 1.
	FXSnapshotID *uint `gorm:"index"`

	CreatedAt time.Time

	Lines []JournalLine `gorm:"foreignKey:JournalEntryID"`
}

// ── Journal line ──────────────────────────────────────────────────────────────

// JournalLine is a single debit OR credit line in a journal entry.
//
// PROJECT_GUIDE rules (enforced in handlers/services):
//   - Debit and Credit cannot both have values.
//   - A saved Journal Entry must have at least 2 valid lines.
//   - Total Debits must equal Total Credits.
type JournalLine struct {
	ID             uint `gorm:"primaryKey"`
	CompanyID      uint `gorm:"not null;index"`
	JournalEntryID uint `gorm:"not null;index"`

	AccountID uint    `gorm:"not null;index"`
	Account   Account `gorm:"foreignKey:AccountID"`

	// TxDebit / TxCredit preserve the original transaction-currency source amounts.
	TxDebit  decimal.Decimal `gorm:"column:tx_debit;type:numeric(18,2);not null"`
	TxCredit decimal.Decimal `gorm:"column:tx_credit;type:numeric(18,2);not null"`
	// Debit / Credit remain ledger truth in company base currency.
	Debit  decimal.Decimal `gorm:"type:numeric(18,2);not null"`
	Credit decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	Memo string `gorm:"not null;default:''"`

	PartyType PartyType `gorm:"type:text;not null;default:''"`
	PartyID   uint      `gorm:"not null;default:0"`

	// Banking: reconciliation markers (optional).
	ReconciliationID *uint      `gorm:"index"`
	ReconciledAt     *time.Time `gorm:""`
}

func (j *JournalEntry) BeforeCreate(tx *gorm.DB) error {
	if j.TransactionCurrencyCode == "" && j.CompanyID != 0 {
		var company Company
		if err := tx.Select("id", "base_currency_code").First(&company, j.CompanyID).Error; err == nil {
			j.TransactionCurrencyCode = company.BaseCurrencyCode
		}
	}
	if j.ExchangeRate.IsZero() {
		j.ExchangeRate = decimal.NewFromInt(1)
	}
	if j.ExchangeRateDate.IsZero() {
		j.ExchangeRateDate = j.EntryDate
	}
	if j.ExchangeRateSource == "" {
		j.ExchangeRateSource = "identity"
	}
	return nil
}

func (l *JournalLine) BeforeCreate(tx *gorm.DB) error {
	if l.TxDebit.IsZero() && !l.Debit.IsZero() {
		l.TxDebit = l.Debit
	}
	if l.TxCredit.IsZero() && !l.Credit.IsZero() {
		l.TxCredit = l.Credit
	}
	return nil
}
