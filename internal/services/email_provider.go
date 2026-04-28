// 遵循project_guide.md
package services

import (
	"errors"
	"strings"

	"balanciz/internal/models"
)

// EmailConfig holds the SMTP parameters required to send email.
type EmailConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string // plaintext, already decrypted by caller
	FromEmail  string
	FromName   string
	Encryption models.SMTPEncryption
}

// ValidateEmailConfig checks that the minimum required fields are present.
// Returns a non-nil error describing the first missing requirement.
func ValidateEmailConfig(cfg EmailConfig) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("SMTP host is required")
	}
	if cfg.Port <= 0 {
		return errors.New("SMTP port must be a positive integer")
	}
	if strings.TrimSpace(cfg.FromEmail) == "" {
		return errors.New("From email address is required")
	}
	return nil
}

// SendTestEmail validates cfg and, when valid, attempts a real SMTP delivery
// by sending a test message from and to cfg.FromEmail (a self-send).
// A successful return means the SMTP server was actually reachable and accepted
// the message — this is the signal that sets EmailVerificationReady in the DB.
//
// Returns (message, error). error is non-nil for both configuration problems
// and SMTP delivery failures.
func SendTestEmail(cfg EmailConfig) (string, error) {
	if err := ValidateEmailConfig(cfg); err != nil {
		return "", err
	}
	subject := "Balanciz – SMTP configuration test"
	body := "This is an automated test message from your Balanciz notification system.\n\n" +
		"If you received this message your SMTP configuration is working correctly.\n" +
		"You do not need to reply to this email."
	if err := SendEmail(cfg, cfg.FromEmail, subject, body); err != nil {
		return "", err
	}
	return "Test email sent to " + cfg.FromEmail + ". Check your inbox to confirm delivery.", nil
}
