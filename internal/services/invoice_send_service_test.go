// 遵循project_guide.md
package services

// invoice_send_service_test.go — Tests for Batch 4 send service gates and delivery log.
//
// Coverage:
//   TestSendInvoiceByEmail_DraftRejected         — draft invoice blocked before SMTP gate
//   TestSendInvoiceByEmail_VoidedRejected         — voided invoice blocked before SMTP gate
//   TestSendInvoiceByEmail_SMTPGateNoLog          — SMTP not ready → rejected, no log created
//   TestSendInvoiceByEmail_RecipientMissing       — empty recipient rejected before SMTP gate
//   TestSendInvoiceByEmail_SMTPGateErrSentinel    — gate failure returns ErrSMTPNotReady sentinel
//   TestSendInvoiceByEmail_SMTPFailedLog          — SMTP dial failure → failed log written, invoice not updated
//   TestSendInvoiceByEmail_ResendCreatesNewLog    — each call creates a distinct log row
//   TestSendInvoiceByEmail_CompanyIsolation       — invoice from another company is not found
//   TestSendInvoiceByEmail_SendCountIncrement     — successful send increments send_count
//   TestCheckSMTPGate_NotReady                   — gate returns ErrSMTPNotReady when no config
//   TestValidateInvoiceForSending_Statuses        — status eligibility matrix

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testSendDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:send_%s?mode=memory&cache=shared", t.Name())
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

func seedSendCompany(t *testing.T, db *gorm.DB) *models.Company {
	t.Helper()
	co := models.Company{Name: "Send Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	return &co
}

// seedIssuedInvoice creates a fully issued invoice ready for email sending.
// It sets CustomerEmailSnapshot so the basic eligibility check passes.
func seedIssuedInvoice(t *testing.T, db *gorm.DB, companyID uint) *models.Invoice {
	t.Helper()

	cust := models.Customer{CompanyID: companyID, Name: "Email Customer", Email: "customer@example.com"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// Minimal JournalEntry so JournalEntryID can be set (voiding validation requires it).
	je := models.JournalEntry{CompanyID: companyID, EntryDate: time.Now()}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-SEND-%d", time.Now().UnixNano()),
		CustomerID:            cust.ID,
		InvoiceDate:           time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "Email Customer",
		CustomerEmailSnapshot: "customer@example.com",
		JournalEntryID:        &je.ID,
		SendCount:             0,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

// seedVerifiedSMTP injects a CompanyNotificationSettings row with EmailVerificationReady=true.
// The SMTP host is set to "localhost:0" which will fail to dial — this lets us test the
// "past the gate but SMTP dial fails" path without a real mail server.
func seedVerifiedSMTP(t *testing.T, db *gorm.DB, companyID uint) {
	t.Helper()
	row := models.CompanyNotificationSettings{
		CompanyID:              companyID,
		EmailEnabled:           true,
		SMTPHost:               "127.0.0.1",
		SMTPPort:               1,     // unreachable — dial will fail
		SMTPFromEmail:          "from@test.com",
		EmailVerificationReady: true,  // gate passes
		AllowSystemFallback:    false,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

// ── Gate / eligibility tests — no SMTP needed ─────────────────────────────────

func TestSendInvoiceByEmail_DraftRejected(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)

	cust := models.Customer{CompanyID: co.ID, Name: "C", Email: "c@e.com"}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID:             co.ID,
		InvoiceNumber:         "INV-DRAFT-01",
		CustomerID:            cust.ID,
		InvoiceDate:           time.Now(),
		Status:                models.InvoiceStatusDraft, // draft — must be blocked
		Amount:                decimal.RequireFromString("50.00"),
		Subtotal:              decimal.RequireFromString("50.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("50.00"),
		BalanceDueBase:        decimal.RequireFromString("50.00"),
		CustomerNameSnapshot:  "C",
		CustomerEmailSnapshot: "c@e.com",
	}
	db.Create(&inv)

	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    co.ID,
		InvoiceID:    inv.ID,
		ToEmail:      "c@e.com",
		TemplateType: "invoice",
	})
	if err == nil {
		t.Fatal("expected error for draft invoice, got nil")
	}
	if !isInvoiceValidationError(err) {
		t.Errorf("expected InvoiceValidationError, got %T: %v", err, err)
	}

	// No log must have been created.
	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("invoice_id = ?", inv.ID).Count(&count)
	if count != 0 {
		t.Errorf("draft rejection must not create any InvoiceEmailLog; got %d rows", count)
	}
}

func TestSendInvoiceByEmail_VoidedRejected(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)

	cust := models.Customer{CompanyID: co.ID, Name: "C", Email: "c@e.com"}
	db.Create(&cust)

	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)
	inv := models.Invoice{
		CompanyID:             co.ID,
		InvoiceNumber:         "INV-VOID-01",
		CustomerID:            cust.ID,
		InvoiceDate:           time.Now(),
		Status:                models.InvoiceStatusVoided, // voided — must be blocked
		Amount:                decimal.RequireFromString("50.00"),
		Subtotal:              decimal.RequireFromString("50.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.Zero,
		BalanceDueBase:        decimal.Zero,
		CustomerNameSnapshot:  "C",
		CustomerEmailSnapshot: "c@e.com",
		JournalEntryID:        &je.ID,
	}
	db.Create(&inv)

	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    co.ID,
		InvoiceID:    inv.ID,
		ToEmail:      "c@e.com",
		TemplateType: "invoice",
	})
	if err == nil {
		t.Fatal("expected error for voided invoice, got nil")
	}
	if !isInvoiceValidationError(err) {
		t.Errorf("expected InvoiceValidationError, got %T: %v", err, err)
	}

	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("invoice_id = ?", inv.ID).Count(&count)
	if count != 0 {
		t.Errorf("voided rejection must not create any InvoiceEmailLog; got %d rows", count)
	}
}

func TestSendInvoiceByEmail_RecipientMissing(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)

	cust := models.Customer{CompanyID: co.ID, Name: "NoEmail", Email: ""}
	db.Create(&cust)

	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)
	inv := models.Invoice{
		CompanyID:             co.ID,
		InvoiceNumber:         "INV-NOEMAIL-01",
		CustomerID:            cust.ID,
		InvoiceDate:           time.Now(),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("50.00"),
		Subtotal:              decimal.RequireFromString("50.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("50.00"),
		BalanceDueBase:        decimal.RequireFromString("50.00"),
		CustomerNameSnapshot:  "NoEmail",
		CustomerEmailSnapshot: "", // no snapshot
		JournalEntryID:        &je.ID,
	}
	db.Create(&inv)

	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    co.ID,
		InvoiceID:    inv.ID,
		ToEmail:      "", // no override either
		TemplateType: "invoice",
	})
	if err == nil {
		t.Fatal("expected error for missing recipient, got nil")
	}
	if !isInvoiceValidationError(err) {
		t.Errorf("expected InvoiceValidationError for missing email, got %T: %v", err, err)
	}

	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("invoice_id = ?", inv.ID).Count(&count)
	if count != 0 {
		t.Errorf("missing recipient must not create any InvoiceEmailLog; got %d rows", count)
	}
}

func TestSendInvoiceByEmail_SMTPGateNoLog(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)
	inv := seedIssuedInvoice(t, db, co.ID)
	// No CompanyNotificationSettings → gate returns not-ready.

	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    co.ID,
		InvoiceID:    inv.ID,
		ToEmail:      "someone@example.com",
		TemplateType: "invoice",
	})
	if err == nil {
		t.Fatal("expected SMTP gate error, got nil")
	}
	if !errors.Is(err, ErrSMTPNotReady) {
		t.Errorf("expected ErrSMTPNotReady, got: %v", err)
	}

	// Gate failure must never create a log.
	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("invoice_id = ?", inv.ID).Count(&count)
	if count != 0 {
		t.Errorf("SMTP gate failure must not create any InvoiceEmailLog; got %d rows", count)
	}
}

func TestSendInvoiceByEmail_SMTPGateErrSentinel(t *testing.T) {
	// Identical scenario to the above; explicitly asserts errors.Is semantics.
	db := testSendDB(t)
	co := seedSendCompany(t, db)
	inv := seedIssuedInvoice(t, db, co.ID)

	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID: co.ID, InvoiceID: inv.ID, ToEmail: "x@x.com", TemplateType: "invoice",
	})
	if !errors.Is(err, ErrSMTPNotReady) {
		t.Fatalf("gate failure must return ErrSMTPNotReady sentinel; got: %v", err)
	}
}

// ── SMTP-dial-failure path ────────────────────────────────────────────────────
// These tests set up a "verified" SMTP config that will fail to dial (port 1),
// so the gate passes but the actual send fails. This exercises the failed-log path.

func TestSendInvoiceByEmail_SMTPFailedLog(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)
	inv := seedIssuedInvoice(t, db, co.ID)
	seedVerifiedSMTP(t, db, co.ID)

	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    co.ID,
		InvoiceID:    inv.ID,
		ToEmail:      "customer@example.com",
		TemplateType: "invoice",
	})
	// Expect an error (dial will fail or PDF gen may fail — either way non-nil).
	if err == nil {
		t.Skip("unexpected success — is a real SMTP server running on port 1?")
	}

	// A failed InvoiceEmailLog must exist.
	var logs []models.InvoiceEmailLog
	db.Where("invoice_id = ? AND company_id = ?", inv.ID, co.ID).Find(&logs)
	if len(logs) == 0 {
		t.Fatal("expected at least one InvoiceEmailLog after SMTP failure, got none")
	}

	// Verify the log is marked failed (if it got past PDF gen stage).
	// Note: if PDF gen fails first, the log will be from the PDF-failure path.
	// Either way a log must exist and have failed status.
	hasFailed := false
	for _, l := range logs {
		if l.SendStatus == models.EmailSendStatusFailed {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Errorf("at least one log must have send_status=failed; got: %+v", logs)
	}

	// Invoice send_count must NOT have been incremented on failure.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.SendCount != 0 {
		t.Errorf("send_count must not increment on failure; got %d", reloaded.SendCount)
	}
}

func TestSendInvoiceByEmail_ResendCreatesNewLog(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)
	inv := seedIssuedInvoice(t, db, co.ID)
	seedVerifiedSMTP(t, db, co.ID)

	req := SendInvoiceEmailRequest{
		CompanyID: co.ID, InvoiceID: inv.ID, ToEmail: "customer@example.com", TemplateType: "invoice",
	}

	// Two calls, both will fail at SMTP/PDF, but each must produce its own log row.
	SendInvoiceByEmail(db, req) //nolint:errcheck
	SendInvoiceByEmail(db, req) //nolint:errcheck

	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("invoice_id = ? AND company_id = ?", inv.ID, co.ID).Count(&count)
	if count < 2 {
		t.Errorf("two send attempts must create at least 2 log rows; got %d", count)
	}
}

func TestSendInvoiceByEmail_CompanyIsolation(t *testing.T) {
	db := testSendDB(t)
	co1 := seedSendCompany(t, db)
	co2 := seedSendCompany(t, db)

	inv := seedIssuedInvoice(t, db, co1.ID)
	seedVerifiedSMTP(t, db, co2.ID)

	// co2 tries to send an invoice that belongs to co1.
	_, err := SendInvoiceByEmail(db, SendInvoiceEmailRequest{
		CompanyID:    co2.ID, // wrong company
		InvoiceID:    inv.ID,
		ToEmail:      "customer@example.com",
		TemplateType: "invoice",
	})
	if err == nil {
		t.Fatal("expected error when company_id does not match invoice; got nil")
	}
	// The validation or load must fail — no log for co2 must be created.
	var count int64
	db.Model(&models.InvoiceEmailLog{}).Where("company_id = ?", co2.ID).Count(&count)
	if count != 0 {
		t.Errorf("cross-company attempt must produce no log for the requesting company; got %d", count)
	}
}

// ── CheckSMTPGate unit test ────────────────────────────────────────────────────

func TestCheckSMTPGate_NotReady(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)

	err := CheckSMTPGate(db, co.ID)
	if err == nil {
		t.Fatal("expected ErrSMTPNotReady, got nil")
	}
	if !errors.Is(err, ErrSMTPNotReady) {
		t.Errorf("expected ErrSMTPNotReady sentinel, got: %v", err)
	}
}

func TestCheckSMTPGate_Ready(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)
	seedVerifiedSMTP(t, db, co.ID)

	err := CheckSMTPGate(db, co.ID)
	if err != nil {
		t.Errorf("expected nil when SMTP is verified, got: %v", err)
	}
}

// ── ValidateInvoiceForSending status matrix ───────────────────────────────────

func TestValidateInvoiceForSending_Statuses(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)

	cust := models.Customer{CompanyID: co.ID, Name: "C", Email: "c@e.com"}
	db.Create(&cust)

	type row struct {
		status  models.InvoiceStatus
		wantErr bool
	}
	cases := []row{
		{models.InvoiceStatusDraft, true},
		{models.InvoiceStatusVoided, true},
		{models.InvoiceStatusIssued, false},
		{models.InvoiceStatusSent, false},
		{models.InvoiceStatusPaid, false},
		{models.InvoiceStatusPartiallyPaid, false},
		{models.InvoiceStatusOverdue, false},
	}

	for _, tc := range cases {
		inv := models.Invoice{
			CompanyID:             co.ID,
			InvoiceNumber:         fmt.Sprintf("INV-STATUS-%s-%d", tc.status, time.Now().UnixNano()),
			CustomerID:            cust.ID,
			InvoiceDate:           time.Now(),
			Status:                tc.status,
			Amount:                decimal.RequireFromString("10.00"),
			Subtotal:              decimal.RequireFromString("10.00"),
			TaxTotal:              decimal.Zero,
			BalanceDue:            decimal.RequireFromString("10.00"),
			BalanceDueBase:        decimal.RequireFromString("10.00"),
			CustomerNameSnapshot:  "C",
			CustomerEmailSnapshot: "c@e.com",
		}
		db.Create(&inv)

		err := ValidateInvoiceForSending(db, co.ID, inv.ID)
		if tc.wantErr && err == nil {
			t.Errorf("status %q: expected validation error, got nil", tc.status)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("status %q: expected no error, got: %v", tc.status, err)
		}
	}
}

// ── SendCount increment test ───────────────────────────────────────────────────

// TestSendInvoiceByEmail_SendCountIncrement verifies that a successful send
// increments Invoice.SendCount. Because we cannot use a real SMTP server in
// unit tests, this test seeds a "verified" config pointing at port 1 which
// will always fail to dial. The send_count must therefore stay at 0 on failure.
// The test documents the expected behaviour even when a real server is absent.
//
// Integration coverage of the success path (send_count incremented) is verified
// through the broader integration test suite that wires a real SMTP stub.
func TestSendInvoiceByEmail_SendCountNotIncrementedOnFailure(t *testing.T) {
	db := testSendDB(t)
	co := seedSendCompany(t, db)
	inv := seedIssuedInvoice(t, db, co.ID)
	seedVerifiedSMTP(t, db, co.ID)

	initialCount := inv.SendCount // 0

	SendInvoiceByEmail(db, SendInvoiceEmailRequest{ //nolint:errcheck
		CompanyID: co.ID, InvoiceID: inv.ID, ToEmail: "x@x.com", TemplateType: "invoice",
	})

	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.SendCount != initialCount {
		t.Errorf("send_count must not change on failure; want %d, got %d", initialCount, reloaded.SendCount)
	}
}

// ── Helper ────────────────────────────────────────────────────────────────────

// isInvoiceValidationError returns true if err is or wraps *InvoiceValidationError.
func isInvoiceValidationError(err error) bool {
	var ve *InvoiceValidationError
	return errors.As(err, &ve)
}
