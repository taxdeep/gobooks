// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// FXRateType classifies the economic nature of the exchange rate captured in
// a snapshot. This maps to the rate-type taxonomy required by IAS 21 (2025/2027)
// and ASPE Section 1651.
type FXRateType string

const (
	// FXRateTypeSpot is the rate at the transaction date (most common for postings).
	FXRateTypeSpot FXRateType = "spot"

	// FXRateTypeAverage is a period average rate (used for P&L translation).
	FXRateTypeAverage FXRateType = "average"

	// FXRateTypeClosing is the period-end (balance sheet date) rate, used for
	// monetary item remeasurement and balance sheet translation.
	FXRateTypeClosing FXRateType = "closing"

	// FXRateTypeHistorical is the rate at the original transaction date, used for
	// non-monetary items measured at historical cost.
	FXRateTypeHistorical FXRateType = "historical"
)

// FXQuoteBasis indicates whether the rate expresses units of the quote currency
// per unit of the base currency (direct) or the inverse (indirect).
// Balanciz always stores rates as direct (1 foreign unit = X base units).
type FXQuoteBasis string

const (
	// FXQuoteBasisDirect: rate = base units per 1 foreign unit (e.g. 1 USD = 1.35 CAD).
	FXQuoteBasisDirect FXQuoteBasis = "direct"

	// FXQuoteBasisIndirect: rate = foreign units per 1 base unit (e.g. 1 CAD = 0.74 USD).
	// Reserved; Balanciz normalises all rates to direct on ingestion.
	FXQuoteBasisIndirect FXQuoteBasis = "indirect"
)

// FXPostingReason identifies the business event that triggered rate capture.
// This determines which accounting standard rules govern how the rate is applied
// and how the resulting exchange difference is classified.
type FXPostingReason string

const (
	// FXPostingReasonTransaction — rate used when initially recording a transaction
	// (invoice, bill, expense, manual JE).
	FXPostingReasonTransaction FXPostingReason = "transaction"

	// FXPostingReasonSettlement — rate used when settling an open item (payment applied
	// to an invoice or bill). Exchange difference = realized FX gain/loss.
	FXPostingReasonSettlement FXPostingReason = "settlement"

	// FXPostingReasonRemeasurement — period-end rate used to remeasure monetary items.
	// Exchange difference = unrealized FX gain/loss (auto-reversed next period).
	FXPostingReasonRemeasurement FXPostingReason = "remeasurement"

	// FXPostingReasonTranslation — rate used to translate a foreign operation's
	// financial statements into the presentation currency.
	// Exchange difference goes to OCI / CTA. Phase 3+.
	FXPostingReasonTranslation FXPostingReason = "translation"
)

// FXRateCategory maps to the specific rate terminology used in accounting standards.
// IAS 21 (2027 amendments) requires enhanced disclosure that distinguishes these.
type FXRateCategory string

const (
	// FXRateCategoryTransaction — the spot rate at the date of the transaction.
	FXRateCategoryTransaction FXRateCategory = "transaction_rate"

	// FXRateCategorySettlement — the spot rate at the date of settlement.
	FXRateCategorySettlement FXRateCategory = "settlement_rate"

	// FXRateCategoryPeriodClosing — the closing rate at the end of the reporting period.
	FXRateCategoryPeriodClosing FXRateCategory = "period_closing_rate"

	// FXRateCategoryAverage — the average rate for the reporting period.
	FXRateCategoryAverage FXRateCategory = "average_rate"
)

// FXSnapshot is an immutable point-in-time record of the foreign-exchange rate
// used in a specific accounting posting event.
//
// Immutability contract:
//   - IsImmutable is always true on persisted rows.
//   - The service layer (fx_snapshot_service.go) is the sole creation path.
//   - No UPDATE path exists; the column exists as an explicit audit signal.
//   - Rows are never deleted; they are retained indefinitely for audit.
//
// Relationship to JournalEntry and SettlementAllocation:
//   - JournalEntry.FXSnapshotID links to the snapshot used at posting time.
//   - SettlementAllocation.FXSnapshotID links to the snapshot used at settlement.
//   - Both are nullable for rows created before Phase 1.
type FXSnapshot struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// FromCurrency is the transaction/source currency (3-char ISO 4217).
	FromCurrency string `gorm:"type:varchar(3);not null"`
	// ToCurrency is the base/functional currency of the book (3-char ISO 4217).
	ToCurrency string `gorm:"type:varchar(3);not null"`

	// Rate is the number of ToCurrency units per 1 FromCurrency unit.
	// Always stored as direct quote; always > 0.
	Rate decimal.Decimal `gorm:"type:numeric(20,8);not null"`

	// EffectiveDate is the market date the rate corresponds to.
	EffectiveDate time.Time `gorm:"type:date;not null"`

	RateType      FXRateType      `gorm:"type:text;not null;default:'spot'"`
	QuoteBasis    FXQuoteBasis    `gorm:"type:text;not null;default:'direct'"`
	PostingReason FXPostingReason `gorm:"type:text;not null"`
	RateCategory  FXRateCategory  `gorm:"type:text;not null"`

	// Source identifies how the rate was obtained.
	// Examples: "manual", "frankfurter", "company_override", "system_stored".
	Source string `gorm:"type:text;not null;default:''"`

	// IsImmutable is always true. It is stored explicitly so that any accidental
	// mutation attempt is visible in the audit trail.
	IsImmutable bool `gorm:"not null;default:true"`

	CreatedAt time.Time
}
