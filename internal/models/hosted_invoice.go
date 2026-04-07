// 遵循project_guide.md
package models

import "time"

// InvoiceHostedLinkStatus tracks whether a hosted invoice link is usable.
type InvoiceHostedLinkStatus string

const (
	// InvoiceHostedLinkStatusActive means the link is valid and can be accessed.
	InvoiceHostedLinkStatusActive InvoiceHostedLinkStatus = "active"
	// InvoiceHostedLinkStatusRevoked means the link was explicitly disabled.
	InvoiceHostedLinkStatusRevoked InvoiceHostedLinkStatus = "revoked"
)

// InvoiceHostedLink records a share link token for a specific invoice.
//
// Security design:
//   - TokenHash stores sha256(plaintext_token) as a hex string. The plaintext
//     is never persisted after the creation response.
//   - Only one active link per invoice is allowed (enforced by service layer;
//     PostgreSQL enforces via partial unique index on invoice_id WHERE status='active').
//   - ExpiresAt nil = no expiry (default). Revocation is the primary access control.
//
// Audit fields:
//   - CreatedBy is the internal user ID who generated the link (nullable for
//     future automated generation paths).
//   - LastViewedAt and ViewCount are updated on each successful public access.
type InvoiceHostedLink struct {
	ID        uint                    `gorm:"primaryKey"`
	CompanyID uint                    `gorm:"not null;index:idx_ihl_company"`
	InvoiceID uint                    `gorm:"not null;index:idx_ihl_invoice"`

	// TokenHash is sha256(plaintext token) encoded as lowercase hex (64 chars).
	// The plaintext is shown once at creation and never stored.
	TokenHash string                  `gorm:"type:text;not null;uniqueIndex:uk_ihl_token"`

	Status    InvoiceHostedLinkStatus `gorm:"type:text;not null;default:'active'"`

	// ExpiresAt nil = never expires. Checked on every access.
	ExpiresAt *time.Time

	// RevokedAt is set when the link is revoked.
	RevokedAt *time.Time

	// CreatedBy is the internal user who generated the link (nullable).
	CreatedBy *uint

	// Access audit: updated on each successful hosted page view.
	LastViewedAt *time.Time
	ViewCount    int `gorm:"not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the PostgreSQL table name for GORM.
func (InvoiceHostedLink) TableName() string {
	return "invoice_hosted_links"
}

// IsExpired returns true when the link has a non-nil expiry that has passed.
func (l *InvoiceHostedLink) IsExpired() bool {
	return l.ExpiresAt != nil && l.ExpiresAt.Before(time.Now())
}

// IsAccessible returns true when the link is active and not expired.
// Both conditions must hold for a hosted page request to succeed.
func (l *InvoiceHostedLink) IsAccessible() bool {
	return l.Status == InvoiceHostedLinkStatusActive && !l.IsExpired()
}
