// 遵循project_guide.md
package services

// invoice_send_attachment_test.go — Batch 9 tests for PDF attachment in send pipeline.
//
// Coverage:
//   TestSendInvoiceByEmail_AttachPDFFalse_SendsWithoutAttachment
//     — AttachPDF=false succeeds (SMTP dial fails) without generating PDF; log records attachment_included=false
//   TestSendInvoiceByEmail_AttachPDFTrue_GeneratorUnavailable
//     — AttachPDF=true with no wkhtmltopdf → rejected with clear error; failed log written; no misleading success
//   TestSendInvoiceByEmail_AttachPDFDefault_NoAttachment
//     — Zero-value request (AttachPDF not set) behaves identically to AttachPDF=false
//   TestSendInvoiceByEmail_AttachPDFTrue_GeneratorAvailable
//     — Happy path: AttachPDF=true with wkhtmltopdf → PDF generated, attached, metadata recorded (skipped when absent)
//   TestSendInvoiceByEmail_ResendCreatesNewLogWithMetadata
//     — Two sends create two log rows each with attachment metadata
//   TestSendInvoiceByEmail_SharedFilenameLogic
//     — When PDF is attached, filename in metadata uses InvoicePDFSafeFilename rules
//   TestSendInvoiceByEmail_NoPDFBlobInLog
//     — MetadataJSON records filename only, never PDF bytes
//   TestGetInvoiceSendDefaults_PDFAvailableField
//     — InvoiceSendDefaults.PDFAvailable reflects PDFGeneratorAvailable()
//   TestPDFGeneratorAvailable_IsSharedTruth
//     — PDFGeneratorAvailable() returns a bool consistent with wkhtmltopdf presence

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func attachTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:attach_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.AuditLog{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.PaymentTerm{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.PaymentTransaction{},
		&models.TaskInvoiceSource{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceTemplate{},
		&models.InvoiceEmailLog{},
		&models.CompanyNotificationSettings{},
		&models.SystemNotificationSettings{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func attachSeedBase(t *testing.T, db *gorm.DB) (uint, *models.Invoice) {
	t.Helper()
	co := models.Company{Name: "Attach Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)

	cust := models.Customer{CompanyID: co.ID, Name: "Att Cust", Email: "att@test.com"}
	db.Create(&cust)

	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)

	invNo := fmt.Sprintf("INV-ATT-%d", time.Now().UnixNano())
	inv := models.Invoice{
		CompanyID:             co.ID,
		InvoiceNumber:         invNo,
		CustomerID:            cust.ID,
		InvoiceDate:           time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "Att Cust",
		CustomerEmailSnapshot: "att@test.com",
		JournalEntryID:        &je.ID,
	}
	db.Create(&inv)

	// SMTP: verified but will fail to dial (port 1 is unreachable).
	smtp := models.CompanyNotificationSettings{
		CompanyID:              co.ID,
		EmailEnabled:           true,
		SMTPHost:               "127.0.0.1",
		SMTPPort:               1,
		SMTPFromEmail:          "from@test.com",
		EmailVerificationReady: true,
		AllowSystemFallback:    false,
	}
	db.Create(&smtp)

	return co.ID, &inv
}

// parseLogMeta deserializes the MetadataJSON of a log entry into a map.
func parseLogMeta(t *testing.T, log *models.InvoiceEmailLog) map[string]any {
	t.Helper()
	var meta map[string]any
	if err := json.Unmarshal([]byte(log.MetadataJSON), &meta); err != nil {
		t.Fatalf("MetadataJSON parse failed: %v — raw: %s", err, log.MetadataJSON)
	}
	return meta
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestSendInvoiceByEmail_AttachPDFFalse_SendsWithoutAttachment(t *testing.T) {
	// AttachPDF=false: PDF generation must NOT be attempted.
	// The send still fails (SMTP dial) but the failure log must record attachment_included=false.
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	logEntry, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    inv.ID,
		ToEmail:      "att@test.com",
		TemplateType: "invoice",
		AttachPDF:    false,
	})
	// SMTP dial to port 1 will fail — that's expected.
	if err == nil {
		t.Fatal("expected SMTP dial error, got nil")
	}
	if logEntry == nil {
		t.Fatal("expected failed log entry, got nil")
	}
	if logEntry.SendStatus != models.EmailSendStatusFailed {
		t.Errorf("expected failed status, got %s", logEntry.SendStatus)
	}

	// Error must be SMTP-related, not PDF-related.
	if strings.Contains(err.Error(), "PDF") || strings.Contains(err.Error(), "wkhtmltopdf") {
		t.Errorf("AttachPDF=false must not attempt PDF generation; error was: %v", err)
	}

	meta := parseLogMeta(t, logEntry)
	if attached, _ := meta["attachment_included"].(bool); attached {
		t.Error("attachment_included must be false when AttachPDF=false")
	}
	if _, hasFile := meta["attachment_filename"]; hasFile {
		t.Error("attachment_filename must not be present when AttachPDF=false")
	}
}

func TestSendInvoiceByEmail_AttachPDFDefault_NoAttachment(t *testing.T) {
	// Zero-value AttachPDF (default false) must behave identically to AttachPDF=false.
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	logEntry, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    inv.ID,
		ToEmail:      "att@test.com",
		TemplateType: "invoice",
		// AttachPDF not set — zero value = false
	})
	if err == nil {
		t.Fatal("expected SMTP error")
	}
	if logEntry == nil {
		t.Fatal("expected log entry")
	}
	if strings.Contains(err.Error(), "PDF") || strings.Contains(err.Error(), "wkhtmltopdf") {
		t.Errorf("default AttachPDF must not attempt PDF; error: %v", err)
	}

	meta := parseLogMeta(t, logEntry)
	if attached, _ := meta["attachment_included"].(bool); attached {
		t.Error("attachment_included must be false for zero-value AttachPDF")
	}
}

func TestSendInvoiceByEmail_AttachPDFTrue_GeneratorUnavailable(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err == nil {
		t.Skip("wkhtmltopdf is installed — this test only runs when generator is absent")
	}
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	logEntry, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    inv.ID,
		ToEmail:      "att@test.com",
		TemplateType: "invoice",
		AttachPDF:    true,
	})
	if err == nil {
		t.Fatal("expected error when AttachPDF=true and generator absent, got nil")
	}
	// Error must mention the generator.
	if !strings.Contains(strings.ToLower(err.Error()), "wkhtmltopdf") &&
		!strings.Contains(strings.ToLower(err.Error()), "pdf") {
		t.Errorf("error should mention PDF/wkhtmltopdf unavailability; got: %v", err)
	}
	// A failed log MUST be written — no misleading success state.
	if logEntry == nil {
		t.Fatal("a failed log entry must be written when AttachPDF=true but generator absent")
	}
	if logEntry.SendStatus != models.EmailSendStatusFailed {
		t.Errorf("log status must be failed, got %s", logEntry.SendStatus)
	}
	// Log metadata must record attachment_included=false with an error.
	meta := parseLogMeta(t, logEntry)
	if attached, _ := meta["attachment_included"].(bool); attached {
		t.Error("attachment_included must be false when generator absent")
	}
	if _, hasErr := meta["attachment_error"]; !hasErr {
		t.Error("attachment_error must be present in metadata when generator absent")
	}
	// No email was sent — invoice status and send_count must be unchanged.
	var refreshed models.Invoice
	db.First(&refreshed, inv.ID)
	if refreshed.SendCount != 0 {
		t.Errorf("send_count must not increment on rejected send; got %d", refreshed.SendCount)
	}
}

func TestSendInvoiceByEmail_AttachPDFTrue_GeneratorAvailable(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err != nil {
		t.Skip("wkhtmltopdf not installed — skipped; run on CI with apt-get install wkhtmltopdf")
	}
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	logEntry, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    inv.ID,
		ToEmail:      "att@test.com",
		TemplateType: "invoice",
		AttachPDF:    true,
	})
	// SMTP dial will fail (port 1), but PDF must have been generated before the SMTP call.
	if err == nil {
		t.Fatal("expected SMTP dial error")
	}
	if logEntry == nil {
		t.Fatal("expected failed log entry")
	}
	// Error must be SMTP-related, not PDF-related.
	if strings.Contains(strings.ToLower(err.Error()), "pdf generation failed") {
		t.Errorf("PDF generation should succeed when wkhtmltopdf is present; error: %v", err)
	}
	// Metadata: attachment_included=true, attachment_filename present.
	meta := parseLogMeta(t, logEntry)
	if attached, _ := meta["attachment_included"].(bool); !attached {
		t.Error("attachment_included must be true when PDF was generated")
	}
	filename, _ := meta["attachment_filename"].(string)
	if !strings.HasPrefix(filename, "Invoice-") || !strings.HasSuffix(filename, ".pdf") {
		t.Errorf("attachment_filename format unexpected: %q", filename)
	}
	// Filename must not contain unsafe chars (spot-check for common bad chars).
	for _, bad := range []string{`"`, `;`, "\r", "\n"} {
		if strings.Contains(filename, bad) {
			t.Errorf("attachment_filename contains unsafe char %q: %s", bad, filename)
		}
	}
}

func TestSendInvoiceByEmail_ResendCreatesNewLogWithMetadata(t *testing.T) {
	// Each send call creates a NEW log entry — history is never overwritten.
	// Both entries have attachment metadata.
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	req := SendInvoiceEmailRequest{
		CompanyID:    companyID,
		InvoiceID:    inv.ID,
		ToEmail:      "att@test.com",
		TemplateType: "invoice",
		AttachPDF:    false,
	}

	log1, err1 := SendInvoiceByEmail(db, req)
	log2, err2 := SendInvoiceByEmail(db, req)

	// Both fail at SMTP (expected).
	if err1 == nil || err2 == nil {
		t.Fatal("expected SMTP errors for both sends")
	}
	if log1 == nil || log2 == nil {
		t.Fatal("both sends must produce log entries")
	}
	if log1.ID == log2.ID {
		t.Error("resend must create a new log row, not reuse the first")
	}

	// Both entries must have attachment metadata.
	meta1 := parseLogMeta(t, log1)
	meta2 := parseLogMeta(t, log2)
	if _, ok := meta1["attachment_included"]; !ok {
		t.Error("first log entry missing attachment_included")
	}
	if _, ok := meta2["attachment_included"]; !ok {
		t.Error("second log entry missing attachment_included")
	}

	// Verify both rows are in the DB.
	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("invoice_id = ?", inv.ID).Count(&count)
	if count != 2 {
		t.Errorf("expected 2 log rows after 2 sends, got %d", count)
	}
}

func TestSendInvoiceByEmail_SharedFilenameLogic(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err != nil {
		t.Skip("wkhtmltopdf not installed — skipped")
	}
	// Seed an invoice with a dangerous number to verify safe filename appears in log.
	db := attachTestDB(t)
	co := models.Company{Name: "Fn Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C", Email: "c@t.com"}
	db.Create(&cust)
	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID,
		InvoiceNumber:         "2024/001",   // slash → sanitized to dash
		InvoiceDate:           time.Now(),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("50.00"),
		Subtotal:              decimal.RequireFromString("50.00"),
		BalanceDue:            decimal.RequireFromString("50.00"),
		BalanceDueBase:        decimal.RequireFromString("50.00"),
		CustomerNameSnapshot:  "C",
		CustomerEmailSnapshot: "c@t.com",
		JournalEntryID:        &je.ID,
	}
	db.Create(&inv)
	smtp := models.CompanyNotificationSettings{
		CompanyID: co.ID, EmailEnabled: true, SMTPHost: "127.0.0.1", SMTPPort: 1,
		SMTPFromEmail: "from@t.com", EmailVerificationReady: true,
	}
	db.Create(&smtp)

	logEntry, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID: co.ID, InvoiceID: inv.ID,
		ToEmail: "c@t.com", TemplateType: "invoice", AttachPDF: true,
	})
	if err == nil {
		t.Fatal("expected SMTP error")
	}
	if logEntry == nil {
		t.Fatal("expected log entry")
	}
	meta := parseLogMeta(t, logEntry)
	filename, _ := meta["attachment_filename"].(string)
	// slash in "2024/001" must have been sanitized to dash.
	if strings.Contains(filename, "/") {
		t.Errorf("attachment_filename must not contain slash; got %q", filename)
	}
	// Must match InvoicePDFSafeFilename output.
	want := InvoicePDFSafeFilename("2024/001")
	if filename != want {
		t.Errorf("attachment_filename = %q, want %q (from InvoicePDFSafeFilename)", filename, want)
	}
}

func TestSendInvoiceByEmail_NoPDFBlobInLog(t *testing.T) {
	// MetadataJSON must contain only metadata (bool + string), never PDF bytes.
	// This verifies the "no archive scope drift" invariant.
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	logEntry, _ := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID: companyID, InvoiceID: inv.ID,
		ToEmail: "att@test.com", TemplateType: "invoice", AttachPDF: false,
	})
	if logEntry == nil {
		t.Fatal("expected log entry")
	}

	// MetadataJSON must be valid JSON and must not be suspiciously large.
	// A PDF blob would be hundreds of kilobytes — metadata should be tiny.
	rawMeta := []byte(logEntry.MetadataJSON)
	if len(rawMeta) > 512 {
		t.Errorf("MetadataJSON suspiciously large (%d bytes) — PDF blob may have been stored", len(rawMeta))
	}
	// Must be parseable as a flat key/value map, not a complex structure.
	var meta map[string]any
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		t.Errorf("MetadataJSON is not valid JSON: %v", err)
	}
}

func TestGetInvoiceSendDefaults_PDFAvailableField(t *testing.T) {
	// InvoiceSendDefaults.PDFAvailable must match PDFGeneratorAvailable().
	db := attachTestDB(t)
	companyID, inv := attachSeedBase(t, db)

	defaults, err := GetInvoiceSendDefaults(db, companyID, inv.ID)
	if err != nil {
		t.Fatalf("GetInvoiceSendDefaults: %v", err)
	}
	if defaults == nil {
		t.Fatal("expected non-nil defaults")
	}
	// PDFAvailable must match the shared truth function.
	want := PDFGeneratorAvailable()
	if defaults.PDFAvailable != want {
		t.Errorf("InvoiceSendDefaults.PDFAvailable = %v, want %v (from PDFGeneratorAvailable())",
			defaults.PDFAvailable, want)
	}
}

func TestPDFGeneratorAvailable_IsSharedTruth(t *testing.T) {
	// PDFGeneratorAvailable() must return a bool consistent with exec.LookPath.
	// This test locks the function to its documented contract.
	_, lookErr := exec.LookPath("wkhtmltopdf")
	got := PDFGeneratorAvailable()
	want := lookErr == nil
	if got != want {
		t.Errorf("PDFGeneratorAvailable() = %v but exec.LookPath returned err=%v (want consistent)", got, lookErr)
	}
}
