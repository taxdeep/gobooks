// 遵循project_guide.md
package models

// ar_return.go — ARReturn: AR 退货 / 退单业务事实对象。
//
// ARReturn 记录客户退货或退单的业务事实。它本身不自动产生会计结果。
// 会计影响取决于后续操作：
//   - 若退货对应退款 → 创建 ARRefund
//   - 若退货对应信用调整 → 创建 CreditNote
//   - 两者皆有可能，不强制自动触发
//
// 这是 AR 模块中非常重要的设计原则：
//   Return ≠ CreditNote  (Return 是业务事实，CreditNote 是会计事实)
//   Return ≠ Refund      (Return 是退货，Refund 是资金流出)
//
// 会计规则：ARReturn 本身不产生 JE。
//
// 状态机：
//
//	draft → submitted → approved → processed
//	               ↘ rejected
//	      ↘ cancelled

import (
	"time"

	"github.com/shopspring/decimal"
)

// ARReturnStatus tracks the lifecycle of a customer return.
type ARReturnStatus string

const (
	// ARReturnStatusDraft — return request created; not yet submitted.
	ARReturnStatusDraft ARReturnStatus = "draft"

	// ARReturnStatusSubmitted — return request submitted; awaiting approval.
	ARReturnStatusSubmitted ARReturnStatus = "submitted"

	// ARReturnStatusApproved — return approved; goods/services accepted back.
	// A CreditNote or Refund may now be issued.
	ARReturnStatusApproved ARReturnStatus = "approved"

	// ARReturnStatusRejected — return rejected.
	ARReturnStatusRejected ARReturnStatus = "rejected"

	// ARReturnStatusProcessed — return fully resolved (credit/refund issued).
	ARReturnStatusProcessed ARReturnStatus = "processed"

	// ARReturnStatusCancelled — return cancelled before approval.
	ARReturnStatusCancelled ARReturnStatus = "cancelled"
)

// AllARReturnStatuses returns statuses in display order.
func AllARReturnStatuses() []ARReturnStatus {
	return []ARReturnStatus{
		ARReturnStatusDraft,
		ARReturnStatusSubmitted,
		ARReturnStatusApproved,
		ARReturnStatusRejected,
		ARReturnStatusProcessed,
		ARReturnStatusCancelled,
	}
}

// ARReturnStatusLabel returns a human-readable label.
func ARReturnStatusLabel(s ARReturnStatus) string {
	switch s {
	case ARReturnStatusDraft:
		return "Draft"
	case ARReturnStatusSubmitted:
		return "Submitted"
	case ARReturnStatusApproved:
		return "Approved"
	case ARReturnStatusRejected:
		return "Rejected"
	case ARReturnStatusProcessed:
		return "Processed"
	case ARReturnStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// ARReturnReason classifies why the return was requested.
type ARReturnReason string

const (
	ARReturnReasonDefective    ARReturnReason = "defective"     // goods defective/damaged
	ARReturnReasonWrongItem    ARReturnReason = "wrong_item"    // wrong item shipped
	ARReturnReasonNotRequired  ARReturnReason = "not_required"  // customer no longer needs
	ARReturnReasonQuality      ARReturnReason = "quality"       // quality not as expected
	ARReturnReasonOther        ARReturnReason = "other"
)

// ARReturnReasonLabel returns a human-readable label.
func ARReturnReasonLabel(r ARReturnReason) string {
	switch r {
	case ARReturnReasonDefective:
		return "Defective / Damaged"
	case ARReturnReasonWrongItem:
		return "Wrong Item"
	case ARReturnReasonNotRequired:
		return "No Longer Required"
	case ARReturnReasonQuality:
		return "Quality Issue"
	case ARReturnReasonOther:
		return "Other"
	default:
		return string(r)
	}
}

// ARReturn records a customer return or service cancellation request.
//
// An ARReturn is a business-layer fact. It does NOT automatically generate a CreditNote
// or Refund — those must be created explicitly by the user after approval.
//
// The link to the originating Invoice is required. The link to resulting
// CreditNote or Refund is set when those objects are created.
//
// Phase 1 establishes the model skeleton. Phase 5 implements the full flow.
type ARReturn struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// InvoiceID is the originating invoice (required).
	InvoiceID uint    `gorm:"not null;index"`
	Invoice   Invoice `gorm:"foreignKey:InvoiceID"`

	// CreditNoteID is set if a CreditNote was issued as a result of this return.
	CreditNoteID *uint       `gorm:"index"`
	CreditNote   *CreditNote `gorm:"foreignKey:CreditNoteID"`

	ReturnNumber string         `gorm:"type:varchar(50);not null;default:''"`
	Status       ARReturnStatus `gorm:"type:text;not null;default:'draft'"`
	ReturnDate   time.Time      `gorm:"not null"`

	Reason      ARReturnReason `gorm:"type:text;not null;default:'other'"`
	Description string         `gorm:"type:text;not null;default:''"`

	// CurrencyCode — inherited from the originating Invoice.
	CurrencyCode string `gorm:"type:varchar(3);not null;default:''"`

	// ReturnAmount is the amount being claimed for return (may be partial).
	ReturnAmount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// ApprovedAt / ApprovedBy record the approval decision.
	ApprovedAt *time.Time
	ApprovedBy string `gorm:"type:varchar(200);not null;default:''"`

	// ProcessedAt is set when CreditNote or Refund is issued.
	ProcessedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}
