// 遵循project_guide.md
package services

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"gobooks/internal/models"

	"gorm.io/gorm"
)

func base64StdEncoding(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// ── Real SMTP sender ──────────────────────────────────────────────────────────

// SendEmail dials the configured SMTP server and sends a single-recipient
// plain-text email. It replaces the stub in email_provider.go for flows that
// must actually deliver (e.g. verification codes).
//
// The caller is responsible for decrypting cfg.Password before passing it here.
func SendEmail(cfg EmailConfig, toAddr, subject, body string) error {
	if err := ValidateEmailConfig(cfg); err != nil {
		return err
	}
	if strings.TrimSpace(toAddr) == "" {
		return errors.New("recipient address is required")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
	}

	msg := buildRawMessage(from, toAddr, subject, body)

	switch cfg.Encryption {
	case models.SMTPEncryptionSSLTLS:
		return sendViaSSL(addr, cfg, toAddr, msg)
	default:
		// STARTTLS (default) and none both start with a plain connection.
		return sendViaSTARTTLS(addr, cfg, toAddr, msg, cfg.Encryption == models.SMTPEncryptionSTARTTLS)
	}
}

// EmailAttachment represents a file to attach to an email.
type EmailAttachment struct {
	Filename    string // e.g. "Invoice-INV001.pdf"
	ContentType string // e.g. "application/pdf"
	Data        []byte
}

// SendEmailWithAttachment sends an email with optional file attachment.
// If attachment is nil, sends plain text only.
func SendEmailWithAttachment(cfg EmailConfig, toAddr, subject, body string, attachment *EmailAttachment) error {
	if err := ValidateEmailConfig(cfg); err != nil {
		return err
	}
	if strings.TrimSpace(toAddr) == "" {
		return errors.New("recipient address is required")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
	}

	var msg []byte
	if attachment != nil && len(attachment.Data) > 0 {
		msg = buildMIMEMessage(from, toAddr, subject, body, attachment)
	} else {
		msg = buildRawMessage(from, toAddr, subject, body)
	}

	switch cfg.Encryption {
	case models.SMTPEncryptionSSLTLS:
		return sendViaSSL(addr, cfg, toAddr, msg)
	default:
		return sendViaSTARTTLS(addr, cfg, toAddr, msg, cfg.Encryption == models.SMTPEncryptionSTARTTLS)
	}
}

func buildRawMessage(from, to, subject, body string) []byte {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

func buildMIMEMessage(from, to, subject, body string, att *EmailAttachment) []byte {
	boundary := "==GoBooks_MIME_Boundary=="

	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
	sb.WriteString("\r\n")

	// Text body part
	sb.WriteString("--" + boundary + "\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: 7bit\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	sb.WriteString("\r\n")

	// Attachment part
	sb.WriteString("--" + boundary + "\r\n")
	ct := att.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	sb.WriteString("Content-Type: " + ct + "\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Content-Disposition: attachment; filename=\"" + att.Filename + "\"\r\n")
	sb.WriteString("\r\n")

	// Base64 encode attachment with line breaks every 76 chars
	encoded := base64Encode(att.Data)
	sb.WriteString(encoded)
	sb.WriteString("\r\n")

	// End boundary
	sb.WriteString("--" + boundary + "--\r\n")

	return []byte(sb.String())
}

func base64Encode(data []byte) string {
	encoded := base64StdEncoding(data)
	// Insert line breaks every 76 chars per RFC 2045
	var result strings.Builder
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		result.WriteString(encoded[i:end])
		result.WriteString("\r\n")
	}
	return result.String()
}

func smtpAuth(cfg EmailConfig) smtp.Auth {
	if cfg.Username == "" {
		return nil
	}
	// PLAIN auth; most providers require TLS before accepting auth.
	return smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
}

func sendViaSTARTTLS(addr string, cfg EmailConfig, to string, msg []byte, doStartTLS bool) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP dial: %w", err)
	}
	defer c.Close()

	if doStartTLS {
		tlsCfg := &tls.Config{ServerName: cfg.Host} //nolint:gosec
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}

	if auth := smtpAuth(cfg); auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := c.Mail(cfg.FromEmail); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write: %w", err)
	}
	return w.Close()
}

func sendViaSSL(addr string, cfg EmailConfig, to string, msg []byte) error {
	host, _, _ := net.SplitHostPort(addr)
	tlsCfg := &tls.Config{ServerName: host} //nolint:gosec
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("SSL/TLS dial: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer c.Close()

	if auth := smtpAuth(cfg); auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := c.Mail(cfg.FromEmail); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write: %w", err)
	}
	return w.Close()
}

// ── Effective SMTP resolver ───────────────────────────────────────────────────

// EffectiveSMTPForCompany returns the SMTP configuration and readiness verdict
// for a given company. Resolution order:
//
//  1. Company override: if enabled and email is verification-ready → use it.
//  2. System default: if email is verification-ready → use it.
//  3. Neither ready → (zero EmailConfig, false).
//
// The returned EmailConfig has Password already decrypted (via Load* functions).
// ready==false means no suitable config exists; the caller must block the flow.
func EffectiveSMTPForCompany(db *gorm.DB, companyID uint) (cfg EmailConfig, ready bool, err error) {
	// Try company override first.
	compRow, err := LoadCompanyNotificationSettings(db, companyID)
	if err != nil {
		return EmailConfig{}, false, err
	}
	if compRow.EmailVerificationReady {
		return EmailConfig{
			Host:       compRow.SMTPHost,
			Port:       compRow.SMTPPort,
			Username:   compRow.SMTPUsername,
			Password:   compRow.SMTPPasswordEncrypted, // already decrypted by Load*
			FromEmail:  compRow.SMTPFromEmail,
			FromName:   compRow.SMTPFromName,
			Encryption: compRow.SMTPEncryption,
		}, true, nil
	}

	// Fall through to system default (honoring AllowSystemFallback).
	if !compRow.AllowSystemFallback && compRow.ID != 0 {
		return EmailConfig{}, false, nil
	}

	sysRow, err := LoadSystemNotificationSettings(db)
	if err != nil {
		return EmailConfig{}, false, err
	}
	if sysRow.EmailVerificationReady {
		return EmailConfig{
			Host:       sysRow.SMTPHost,
			Port:       sysRow.SMTPPort,
			Username:   sysRow.SMTPUsername,
			Password:   sysRow.SMTPPasswordEncrypted,
			FromEmail:  sysRow.SMTPFromEmail,
			FromName:   sysRow.SMTPFromName,
			Encryption: sysRow.SMTPEncryption,
		}, true, nil
	}

	return EmailConfig{}, false, nil
}

// EffectiveSMTPSystem returns the system SMTP config if it is verification-ready.
// Used for user profile flows when there is no active company context.
func EffectiveSMTPSystem(db *gorm.DB) (cfg EmailConfig, ready bool, err error) {
	sysRow, err := LoadSystemNotificationSettings(db)
	if err != nil {
		return EmailConfig{}, false, err
	}
	if sysRow.EmailVerificationReady {
		return EmailConfig{
			Host:       sysRow.SMTPHost,
			Port:       sysRow.SMTPPort,
			Username:   sysRow.SMTPUsername,
			Password:   sysRow.SMTPPasswordEncrypted,
			FromEmail:  sysRow.SMTPFromEmail,
			FromName:   sysRow.SMTPFromName,
			Encryption: sysRow.SMTPEncryption,
		}, true, nil
	}
	return EmailConfig{}, false, nil
}
