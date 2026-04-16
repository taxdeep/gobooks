// 遵循project_guide.md
package models

// ap_credit_application.go — APCreditApplication: AP open-item matching record.
//
// APCreditApplication records the formal allocation of a VendorCreditNote balance
// against a Bill. It mirrors CreditNoteApplication on the AR side.
//
// Accounting was already settled when the VendorCreditNote was posted
// (Dr AP / Cr Purchase Returns). This record is purely an AP open-item allocation —
// it tracks which Bill's BalanceDue was reduced and by how much.

import (
	"time"

	"github.com/shopspring/decimal"
)

// APCreditApplication records one application of a VendorCreditNote amount against a Bill.
// A credit note may be split across multiple bills.
type APCreditApplication struct {
	ID                 uint             `gorm:"primaryKey"`
	CompanyID          uint             `gorm:"not null;index"`
	VendorCreditNoteID uint             `gorm:"not null;index"`
	VendorCreditNote   VendorCreditNote `gorm:"foreignKey:VendorCreditNoteID"`
	BillID             uint             `gorm:"not null;index"`
	Bill               Bill             `gorm:"foreignKey:BillID"`

	// AmountApplied is in the credit note's document currency.
	AmountApplied decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	// AmountAppliedBase is the base-currency equivalent at the credit note's exchange rate.
	AmountAppliedBase decimal.Decimal `gorm:"type:numeric(18,2);not null"`

	AppliedAt time.Time `gorm:"not null"`
	CreatedAt time.Time
}
