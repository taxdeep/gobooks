// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// SettlementDocumentType distinguishes invoice from bill allocations.
type SettlementDocumentType string

const (
	SettlementDocInvoice SettlementDocumentType = "invoice"
	SettlementDocBill    SettlementDocumentType = "bill"
)

// SettlementAllocation records the application of one payment journal entry
// against a specific invoice or bill.
//
// For base-currency documents:
//   - AmountApplied == ARAPBaseReleased == BankBaseAmount
//   - RealizedFXGainLoss == 0
//   - SettlementRate == 1
//
// For foreign-currency documents:
//   - AmountApplied is in the document's currency (e.g. USD 600)
//   - ARAPBaseReleased is the pro-rated carrying value removed from AR/AP (base)
//   - BankBaseAmount is the actual base equivalent received/paid (AmountApplied × SettlementRate)
//   - RealizedFXGainLoss = BankBaseAmount − ARAPBaseReleased
//     (positive = gain, negative = loss)
//   - SettlementRate is documentCurrency→baseCurrency at the payment date
//
// The full journal entry for a foreign receipt looks like:
//
//	DR Bank (base)             BankBaseAmount
//	CR AR-Foreign (base)       ARAPBaseReleased
//	CR/DR FX Gain/Loss (base)  RealizedFXGainLoss
type SettlementAllocation struct {
	ID             uint                   `gorm:"primaryKey"`
	CompanyID      uint                   `gorm:"not null;index"`
	JournalEntryID uint                   `gorm:"not null;index"`

	// DocumentType + DocumentID identify the settled document.
	DocumentType SettlementDocumentType `gorm:"type:text;not null"`
	DocumentID   uint                   `gorm:"not null;index"`

	// AmountApplied is the document-currency amount settled this allocation.
	AmountApplied decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	// ARAPBaseReleased is the base-currency carrying value removed from AR or AP.
	// For base-currency documents this equals AmountApplied.
	ARAPBaseReleased decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	// BankBaseAmount is the base-currency equivalent received at the bank
	// (AmountApplied × SettlementRate, rounded to 2dp).
	// For base-currency documents this equals AmountApplied.
	BankBaseAmount decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	// RealizedFXGainLoss = BankBaseAmount − ARAPBaseReleased.
	// Positive means the company received more base currency than the carrying value → gain.
	// Negative means the company paid more than the carrying value → loss.
	RealizedFXGainLoss decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// SettlementRate is the documentCurrency→baseCurrency rate used. 1 for base-currency docs.
	SettlementRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`

	CreatedAt time.Time
}
