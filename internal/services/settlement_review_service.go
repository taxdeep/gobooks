// 遵循project_guide.md
package services

// settlement_review_service.go — Minimal read model for the settlement review list.
//
// Purpose: surface HostedPaymentAttempt rows whose settlement outcome is
// "pending_review" or "failed" (the two actionable states) so operators can
// see what went wrong and retry from one place rather than opening invoices blindly.
//
// Scope boundary:
//   - Read-only query layer. No mutation here.
//   - Mutation (retry) is delegated to RetryGatewaySettlement in gateway_settlement_service.go.
//   - This is not a reconciliation engine. One query, one page, company-scoped.
//
// Filter options:
//   - "pending" (default): pending_review + failed attempts only
//   - "all": all attempts with a non-empty settlement_status (includes applied)
//
// Sort: settlement_last_attempted_at DESC, then id DESC as tiebreaker.

import (
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// SettlementReviewFilter controls which outcome states appear in the list.
type SettlementReviewFilter string

const (
	// SettlementReviewPending is the default: pending_review + failed rows only.
	SettlementReviewPending SettlementReviewFilter = "pending"
	// SettlementReviewAll includes applied rows too (traceability/history).
	SettlementReviewAll SettlementReviewFilter = "all"
)

// SettlementReviewRow is the minimal read model for one row in the settlement
// review list. All fields are populated by a single joined query; no N+1 loads.
type SettlementReviewRow struct {
	// Attempt identity
	AttemptID uint
	InvoiceID uint

	// Invoice display
	InvoiceNumber string
	CustomerName  string

	// Payment amounts
	Amount       decimal.Decimal
	CurrencyCode string

	// Payment timing
	PaymentVerifiedAt time.Time // attempt.created_at (when webhook set payment_succeeded)

	// Settlement outcome
	SettlementStatus            string
	SettlementReason            string
	SettlementLastAttemptedAt   *time.Time
}

// settlementReviewJoin is the raw SQL result shape from the joined query.
type settlementReviewJoin struct {
	AttemptID                 uint
	InvoiceID                 uint
	InvoiceNumber             string
	CustomerName              string
	Amount                    decimal.Decimal
	CurrencyCode              string
	PaymentVerifiedAt         time.Time
	SettlementStatus          string
	SettlementReason          string
	SettlementLastAttemptedAt *time.Time
}

// ListSettlementReviewRows returns settlement outcome rows for the company.
//
// Default filter (SettlementReviewPending): pending_review + failed only.
// SettlementReviewAll: all attempts that have ever had a settlement outcome recorded.
//
// Only payment_succeeded attempts are included (the only ones that ever get a
// settlement_status written). Company isolation is enforced via company_id on the
// hosted_payment_attempts table.
//
// Returns at most 200 rows (sufficient for any reasonable ops queue; this is not
// a paginated reporting engine).
func ListSettlementReviewRows(db *gorm.DB, companyID uint, filter SettlementReviewFilter) []SettlementReviewRow {
	q := db.Table("hosted_payment_attempts hpa").
		Select(`
			hpa.id                         AS attempt_id,
			hpa.invoice_id                 AS invoice_id,
			inv.invoice_number             AS invoice_number,
			COALESCE(c.name, '')           AS customer_name,
			hpa.amount                     AS amount,
			hpa.currency_code              AS currency_code,
			hpa.created_at                 AS payment_verified_at,
			hpa.settlement_status          AS settlement_status,
			hpa.settlement_reason          AS settlement_reason,
			hpa.settlement_last_attempted_at AS settlement_last_attempted_at
		`).
		Joins("JOIN invoices inv ON inv.id = hpa.invoice_id").
		Joins("LEFT JOIN customers c ON c.id = inv.customer_id").
		Where("hpa.company_id = ?", companyID).
		Where("hpa.status = ?", string(models.HostedPaymentAttemptPaymentSucceeded)).
		Order("hpa.settlement_last_attempted_at DESC, hpa.id DESC").
		Limit(200)

	if filter == SettlementReviewAll {
		// All attempts that have had any settlement outcome recorded.
		q = q.Where("hpa.settlement_status != ''")
	} else {
		// Default: actionable states only.
		q = q.Where("hpa.settlement_status IN ?", []string{
			models.SettlementOutcomePendingReview,
			models.SettlementOutcomeFailed,
		})
	}

	var raw []settlementReviewJoin
	q.Scan(&raw)

	rows := make([]SettlementReviewRow, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, SettlementReviewRow{
			AttemptID:                 r.AttemptID,
			InvoiceID:                 r.InvoiceID,
			InvoiceNumber:             r.InvoiceNumber,
			CustomerName:              r.CustomerName,
			Amount:                    r.Amount,
			CurrencyCode:              r.CurrencyCode,
			PaymentVerifiedAt:         r.PaymentVerifiedAt,
			SettlementStatus:          r.SettlementStatus,
			SettlementReason:          r.SettlementReason,
			SettlementLastAttemptedAt: r.SettlementLastAttemptedAt,
		})
	}
	return rows
}
