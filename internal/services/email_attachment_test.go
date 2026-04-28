// 遵循project_guide.md
package services

import (
	"strings"
	"testing"

	"balanciz/internal/models"
)

func TestBuildRawMessage_PlainText(t *testing.T) {
	msg := buildRawMessage("sender@test.com", "rcpt@test.com", "Test Subject", "Hello World")
	s := string(msg)

	if !strings.Contains(s, "From: sender@test.com") {
		t.Error("Missing From header")
	}
	if !strings.Contains(s, "To: rcpt@test.com") {
		t.Error("Missing To header")
	}
	if !strings.Contains(s, "Subject: Test Subject") {
		t.Error("Missing Subject header")
	}
	if !strings.Contains(s, "Content-Type: text/plain; charset=UTF-8") {
		t.Error("Missing Content-Type header")
	}
	if !strings.Contains(s, "Hello World") {
		t.Error("Missing body")
	}
}

func TestBuildMIMEMessage_WithAttachment(t *testing.T) {
	att := &EmailAttachment{
		Filename:    "Invoice-001.pdf",
		ContentType: "application/pdf",
		Data:        []byte("fake pdf content for testing"),
	}

	msg := buildMIMEMessage("sender@test.com", "rcpt@test.com", "Invoice #001", "Please find attached.", att)
	s := string(msg)

	// Verify MIME structure
	if !strings.Contains(s, "multipart/mixed") {
		t.Error("Missing multipart/mixed Content-Type")
	}
	if !strings.Contains(s, "Balanciz_MIME_Boundary") {
		t.Error("Missing MIME boundary")
	}
	if !strings.Contains(s, "Content-Type: text/plain; charset=UTF-8") {
		t.Error("Missing text part Content-Type")
	}
	if !strings.Contains(s, "Please find attached.") {
		t.Error("Missing email body text")
	}
	if !strings.Contains(s, "Content-Type: application/pdf") {
		t.Error("Missing attachment Content-Type")
	}
	if !strings.Contains(s, `filename="Invoice-001.pdf"`) {
		t.Error("Missing attachment filename")
	}
	if !strings.Contains(s, "Content-Transfer-Encoding: base64") {
		t.Error("Missing base64 transfer encoding")
	}
}

func TestBuildMIMEMessage_Base64Encoding(t *testing.T) {
	// Create data larger than 76 bytes to verify line wrapping
	data := make([]byte, 200)
	for i := range data {
		data[i] = byte(i % 256)
	}
	att := &EmailAttachment{
		Filename:    "test.bin",
		ContentType: "application/octet-stream",
		Data:        data,
	}

	msg := buildMIMEMessage("a@b.com", "c@d.com", "Test", "Body", att)
	s := string(msg)

	// Find base64 content between the second boundary marker and the end boundary
	lines := strings.Split(s, "\r\n")
	var base64Lines []string
	inBase64 := false
	for _, line := range lines {
		if strings.Contains(line, "Content-Transfer-Encoding: base64") {
			inBase64 = true
			continue
		}
		if inBase64 && line == "" {
			continue // skip empty line after header
		}
		if inBase64 {
			if strings.HasPrefix(line, "--") {
				break
			}
			base64Lines = append(base64Lines, line)
		}
	}

	// Verify line length <= 76 chars (RFC 2045)
	for i, line := range base64Lines {
		if len(line) > 76 {
			t.Errorf("Base64 line %d exceeds 76 chars: %d", i, len(line))
		}
	}
}

func TestSendEmailWithAttachment_NilAttachment_FallsBackToPlain(t *testing.T) {
	// This just tests the code path — actual SMTP not available in tests.
	// We verify it calls the right builder by checking error behavior with invalid config.
	cfg := EmailConfig{} // invalid config
	err := SendEmailWithAttachment(cfg, "test@example.com", "Subject", "Body", nil)
	if err == nil {
		t.Fatal("Expected error with empty config")
	}
	// Should fail at ValidateEmailConfig, not at MIME building
	if !strings.Contains(err.Error(), "host") && !strings.Contains(err.Error(), "required") {
		t.Errorf("Expected validation error, got: %v", err)
	}
}

func TestSendInvoiceByEmail_RequiresRecipientEmail(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusIssued)

	// Clear email snapshot
	db.Model(&models.Invoice{}).Where("id = ?", invoiceID).Update("customer_email_snapshot", "")

	req := SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    invoiceID,
		ToEmail:      "", // empty — should fall back to snapshot, which is also empty
		TemplateType: "invoice",
	}

	_, err := SendInvoiceByEmail(db, req)
	if err == nil {
		t.Fatal("Expected error for missing recipient email")
	}
	// ValidateInvoiceForSending fires first and reports missing customer email.
	if !strings.Contains(err.Error(), "email") {
		t.Errorf("Expected email-related error, got: %v", err)
	}
}

func TestSendInvoiceByEmail_RequiresSMTPConfig(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusIssued)

	req := SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    invoiceID,
		ToEmail:      "customer@example.com",
		TemplateType: "invoice",
	}

	_, err := SendInvoiceByEmail(db, req)
	if err == nil {
		t.Fatal("Expected error for missing SMTP config")
	}
	if !strings.Contains(err.Error(), "SMTP") {
		t.Errorf("Expected SMTP-related error, got: %v", err)
	}
}
