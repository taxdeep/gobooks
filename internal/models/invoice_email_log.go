// 遵循project_guide.md
package models

import (
	"time"

	"gorm.io/datatypes"
)

// EmailSendStatus tracks the result of an email send attempt.
type EmailSendStatus string

const (
	EmailSendStatusPending EmailSendStatus = "pending"
	EmailSendStatusSent    EmailSendStatus = "sent"
	EmailSendStatusFailed  EmailSendStatus = "failed"
)

// InvoiceEmailLog records a single email send attempt for an invoice.
// Company-scoped for audit and troubleshooting.
// Immutable once created: never updated, only read for history.
//
// SendStatus: pending (queued), sent (SMTP accepted), failed (SMTP rejected or error).
// ErrorMessage: SMTP error details (if SendStatus = failed).
// SMTPResponse: full SMTP server response (for debugging).
// MetadataJSON: flexible storage for retry counts, attachment sizes, etc.
type InvoiceEmailLog struct {
	ID        uint   `gorm:"primaryKey"`
	CompanyID uint   `gorm:"not null;index"`
	InvoiceID uint   `gorm:"not null;index"`

	// Recipient information
	ToEmail  string `gorm:"not null"`
	CCEmails string `gorm:"not null;default:''"` // comma-separated

	// Send attempt result
	SendStatus   EmailSendStatus `gorm:"type:text;not null;default:'pending';index:idx_invoices_email_logs_status,where:send_status"`
	ErrorMessage string          `gorm:"not null;default:''"`
	SMTPResponse string          `gorm:"not null;default:''"`

	// Message content reference
	Subject      string `gorm:"not null;default:''"`
	TemplateType string `gorm:"type:text;not null;default:'invoice';index:idx_invoices_email_logs_template"` // invoice|reminder|reminder2

	// Audit: who triggered the send
	// TriggeredByUserID: foreign key to users table (optional, NULL for automatic sends)
	TriggeredByUserID *uint `gorm:"index"`

	// Timestamps
	CreatedAt time.Time
	SentAt    *time.Time

	// MetadataJSON stores flexible data: retry_count, attachment_size, headers, etc.
	MetadataJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'`
}

// TableName returns the PostgreSQL table name for GORM.
func (InvoiceEmailLog) TableName() string {
	return "invoices_email_logs"
}

// AllEmailSendStatuses returns send statuses in logical order.
func AllEmailSendStatuses() []EmailSendStatus {
	return []EmailSendStatus{
		EmailSendStatusPending,
		EmailSendStatusSent,
		EmailSendStatusFailed,
	}
}

// EmailSendStatusLabel returns a human-readable label.
func EmailSendStatusLabel(s EmailSendStatus) string {
	switch s {
	case EmailSendStatusPending:
		return "Pending"
	case EmailSendStatusSent:
		return "Sent"
	case EmailSendStatusFailed:
		return "Failed"
	default:
		return string(s)
	}
}

// IsSuccessful returns true if the email was sent successfully.
func (log *InvoiceEmailLog) IsSuccessful() bool {
	return log.SendStatus == EmailSendStatusSent
}

// IsFailed returns true if the email send attempt failed.
func (log *InvoiceEmailLog) IsFailed() bool {
	return log.SendStatus == EmailSendStatusFailed
}
