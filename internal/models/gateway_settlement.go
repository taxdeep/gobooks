// 遵循project_guide.md
package models

// gateway_settlement.go — Accounting-side bridge record for verified hosted payments.
//
// ─── Conceptual separation ────────────────────────────────────────────────────
// Payment-side truth:  HostedPaymentAttempt (status=payment_succeeded, set only by webhook)
// Accounting-side truth: GatewaySettlement + JournalEntry + invoice BalanceDue/Status update
//
// The existence of a payment_succeeded attempt is NOT the same as invoice settlement.
// A GatewaySettlement record is created only after all eligibility rules pass and the
// full accounting path (JE + apply) completes atomically.
//
// ─── Idempotency anchor ──────────────────────────────────────────────────────
// GatewaySettlement has a unique index on hosted_attempt_id.
// Exactly one settlement per verified attempt is guaranteed by this constraint.
// Repeated triggers (webhook re-delivery, admin retry, browser revisit) are safe:
// if a row already exists, the attempt is silently skipped.
//
// ─── Traceability links ───────────────────────────────────────────────────────
// GatewaySettlement.HostedAttemptID       → which external payment triggered settlement
// GatewaySettlement.PaymentTransactionID  → which charge transaction was posted+applied
// GatewaySettlement.JournalEntryID        → which accounting JE was created
// GatewaySettlement.InvoiceID             → which invoice was settled

import (
	"time"

	"github.com/shopspring/decimal"
)

// GatewaySettlement is the bridge record that links a verified hosted payment
// (gateway-side truth) to its accounting settlement (Balanciz-side truth).
//
// Created atomically alongside the journal entry and invoice balance update.
// One row per HostedPaymentAttempt (enforced by uniqueIndex:uq_gw_settle_attempt).
type GatewaySettlement struct {
	ID uint `gorm:"primaryKey"`

	// CompanyID for isolation. Never 0.
	CompanyID uint `gorm:"not null;index:idx_gw_settle_company"`

	// HostedAttemptID is the idempotency anchor — at most one settlement per attempt.
	// Unique constraint fires on duplicate settlement attempt.
	HostedAttemptID uint `gorm:"not null;uniqueIndex:uq_gw_settle_attempt"`

	// PaymentTransactionID is the charge PaymentTransaction that was posted+applied.
	PaymentTransactionID uint `gorm:"not null;index"`

	// InvoiceID is the invoice that was fully settled.
	InvoiceID uint `gorm:"not null;index"`

	// JournalEntryID is the JE created for this settlement (Dr GW Clearing / Cr AR).
	JournalEntryID uint `gorm:"not null;index"`

	// Amount and CurrencyCode mirror the settled amount from the attempt.
	// Stored for quick reporting without joins.
	Amount       decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	CurrencyCode string          `gorm:"type:text;not null;default:''"`

	// SettledAt is when this settlement bridge record was created.
	SettledAt time.Time `gorm:"not null"`
	CreatedAt time.Time
}

func (GatewaySettlement) TableName() string { return "gateway_settlements" }
