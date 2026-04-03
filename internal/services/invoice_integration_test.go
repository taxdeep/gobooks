// 遵循project_guide.md
package services

// invoice_integration_test.go — DB-backed integration tests for the invoice
// module (lifecycle, templates, email).
//
// All tests use isolated SQLite in-memory databases, fully independent.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func testInvoiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:invoice_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceTemplate{},
		&models.InvoiceEmailLog{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.PaymentTransaction{},   // required by VoidInvoice payment-transaction guard
		&models.SettlementAllocation{}, // required by VoidInvoice settlement-allocation guard
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

func seedCompanyForInvoice(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{
		Name:     "Test Company",
		Industry: "Other",
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedAccountForInvoice(t *testing.T, db *gorm.DB, companyID uint, code, name string, rootType models.RootAccountType, detailType models.DetailAccountType) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              name,
		RootAccountType:   rootType,
		DetailAccountType: detailType,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func seedCustomerForInvoice(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{
		CompanyID:   companyID,
		Name:        "Test Customer",
		AddrStreet1: "123 Main St",
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedInvoiceWithLines creates an invoice header only (no actual line items).
// Use seedFullInvoice for tests that need validation or posting.
func seedInvoiceWithLines(t *testing.T, db *gorm.DB, companyID, customerID uint, status models.InvoiceStatus) uint {
	t.Helper()
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-%d", time.Now().UnixNano()),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Now(),
		DueDate:               timePtr(time.Now().AddDate(0, 1, 0)),
		Status:                status,
		Amount:                toDecimal("1050.00"),
		Subtotal:              toDecimal("1000.00"),
		TaxTotal:              toDecimal("50.00"),
		BalanceDue:            toDecimal("1050.00"),
		Memo:                  "Test memo",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return inv.ID
}

// seedFullInvoice creates an invoice with a revenue account, product service, and
// a real line item — suitable for IssueInvoice, PostInvoice, and validation tests.
func seedFullInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, status models.InvoiceStatus) uint {
	t.Helper()

	// Revenue account
	revAcct := models.Account{
		CompanyID: companyID, Code: fmt.Sprintf("4%03d", time.Now().UnixNano()%1000),
		Name: "Revenue", RootAccountType: models.RootRevenue,
		DetailAccountType: "revenue", IsActive: true,
	}
	db.Create(&revAcct)

	// AR account (needed for posting)
	var arCount int64
	db.Model(&models.Account{}).Where("company_id = ? AND detail_account_type = ?",
		companyID, string(models.DetailAccountsReceivable)).Count(&arCount)
	if arCount == 0 {
		ar := models.Account{
			CompanyID: companyID, Code: "1100", Name: "Accounts Receivable",
			RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable,
			IsActive: true,
		}
		db.Create(&ar)
	}

	// Product service
	ps := models.ProductService{
		CompanyID: companyID, Name: "Test Service", Type: "service",
		RevenueAccountID: revAcct.ID, IsActive: true,
	}
	db.Create(&ps)

	// Invoice
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-%d", time.Now().UnixNano()),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Now(),
		DueDate:               timePtr(time.Now().AddDate(0, 1, 0)),
		Status:                status,
		Subtotal:              toDecimal("1000.00"),
		TaxTotal:              toDecimal("0"),
		Amount:                toDecimal("1000.00"),
		BalanceDue:            toDecimal("1000.00"),
		Memo:                  "Test memo",
	}
	db.Create(&inv)

	// Line item
	line := models.InvoiceLine{
		CompanyID: companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &ps.ID, Description: "Test Service",
		Qty: toDecimal("1"), UnitPrice: toDecimal("1000.00"),
		LineNet: toDecimal("1000.00"), LineTax: toDecimal("0"), LineTotal: toDecimal("1000.00"),
	}
	db.Create(&line)

	return inv.ID
}

// ── Test helpers ──────────────────────────────────────────────────────────────

func toDecimal(s string) decimal.Decimal {
	dec, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return dec
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// ── Tests: Template Management ─────────────────────────────────────────────────

func TestCreateInvoiceTemplate_Success(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	configJSON, _ := json.Marshal(models.TemplateConfig{
		DefaultTerms: "Net 30",
	})

	tmpl, err := CreateInvoiceTemplate(db, companyID, "Default Template", "Standard template", configJSON, true)
	if err != nil {
		t.Fatalf("CreateInvoiceTemplate failed: %v", err)
	}
	if tmpl.ID == 0 {
		t.Fatal("Template ID not set")
	}
	if !tmpl.IsDefault {
		t.Fatal("IsDefault not set")
	}

	// Verify in DB
	var retrieved models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ?", tmpl.ID, companyID).First(&retrieved).Error; err != nil {
		t.Fatalf("Template not found in DB: %v", err)
	}
}

func TestCreateInvoiceTemplate_UniqueDefaultOnly(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	configJSON, _ := json.Marshal(models.TemplateConfig{})

	// Create first default template
	_, err := CreateInvoiceTemplate(db, companyID, "Template 1", "First", configJSON, true)
	if err != nil {
		t.Fatalf("First template creation failed: %v", err)
	}

	// Try to create second default — should fail
	_, err = CreateInvoiceTemplate(db, companyID, "Template 2", "Second", configJSON, true)
	if err == nil {
		t.Fatal("Expected error for duplicate default template, got nil")
	}
}

func TestGetDefaultInvoiceTemplate_Success(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	configJSON, _ := json.Marshal(models.TemplateConfig{})
	created, _ := CreateInvoiceTemplate(db, companyID, "Default", "desc", configJSON, true)

	tmpl, err := GetDefaultInvoiceTemplate(db, companyID)
	if err != nil {
		t.Fatalf("GetDefaultInvoiceTemplate failed: %v", err)
	}
	if tmpl.ID != created.ID {
		t.Fatalf("Retrieved template ID %d does not match created %d", tmpl.ID, created.ID)
	}
}

// ── Tests: Invoice Lifecycle ───────────────────────────────────────────────────

func TestIssueInvoice_CapturesSnapshots(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	updated, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	if updated.Status != models.InvoiceStatusIssued {
		t.Fatalf("Status not updated to issued, got %v", updated.Status)
	}
	if updated.CustomerNameSnapshot == "" {
		t.Fatal("CustomerNameSnapshot not captured")
	}
	if updated.IssuedAt == nil {
		t.Fatal("IssuedAt not set")
	}
	if updated.JournalEntryID == nil {
		t.Fatal("JournalEntryID not set — posting should happen on issue")
	}
}

func TestIssueInvoice_InvalidTransitionRejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// Issue once
	_, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("First issue failed: %v", err)
	}

	// Try to issue again — should fail
	_, err = IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error on duplicate issue, got nil")
	}
}

func TestSendInvoice_TransitionsToSent(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// Issue first (this also posts)
	_, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	// Send
	updated, err := SendInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("SendInvoice failed: %v", err)
	}

	if updated.Status != models.InvoiceStatusSent {
		t.Fatalf("Status not sent, got %v", updated.Status)
	}
	if updated.SentAt == nil {
		t.Fatal("SentAt not set")
	}
}

func TestMarkInvoicePaid_ClearsBalanceDue(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusSent)

	updated, err := MarkInvoicePaid(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("MarkInvoicePaid failed: %v", err)
	}

	if updated.Status != models.InvoiceStatusPaid {
		t.Fatalf("Status not paid, got %v", updated.Status)
	}
	if updated.BalanceDue.GreaterThan(decimal.Zero) {
		t.Fatalf("BalanceDue not cleared, got %v", updated.BalanceDue)
	}
}

// ── Tests: Validation ──────────────────────────────────────────────────────────

func TestValidateInvoiceForSending_NoCustomerEmail(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusIssued)

	// Clear email snapshot
	db.Model(&models.Invoice{}).Where("id = ?", invoiceID).Update("customer_email_snapshot", "")

	err := ValidateInvoiceForSending(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected validation to fail for missing customer email")
	}
}

func TestValidateInvoiceForVoiding_NotPosted(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusDraft)

	err := ValidateInvoiceForVoiding(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected validation to fail for unposted invoice")
	}
}

// ── Tests: Delete ──────────────────────────────────────────────────────────────

func TestDeleteInvoice_DraftOK(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	err := DeleteInvoice(db, companyID, invoiceID, "test", nil)
	if err != nil {
		t.Fatalf("DeleteInvoice failed: %v", err)
	}

	// Verify deleted
	var count int64
	db.Model(&models.Invoice{}).Where("id = ?", invoiceID).Count(&count)
	if count != 0 {
		t.Fatal("Invoice not deleted")
	}

	// Verify lines deleted
	db.Model(&models.InvoiceLine{}).Where("invoice_id = ?", invoiceID).Count(&count)
	if count != 0 {
		t.Fatal("Invoice lines not deleted")
	}
}

func TestDeleteInvoice_IssuedBlocked(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// Issue the invoice (posts it)
	_, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	// Try to delete — should fail
	err = DeleteInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error deleting issued invoice, got nil")
	}
}

// ── Tests: Email History ──────────────────────────────────────────────────────

func TestGetInvoiceEmailHistory_Success(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	invoiceID := uint(1)

	log1 := models.InvoiceEmailLog{
		CompanyID:    companyID,
		InvoiceID:    invoiceID,
		ToEmail:      "test1@example.com",
		SendStatus:   models.EmailSendStatusSent,
		TemplateType: "invoice",
		CreatedAt:    time.Now().Add(-1 * time.Hour),
	}
	log2 := models.InvoiceEmailLog{
		CompanyID:    companyID,
		InvoiceID:    invoiceID,
		ToEmail:      "test2@example.com",
		SendStatus:   models.EmailSendStatusFailed,
		TemplateType: "reminder",
		CreatedAt:    time.Now(),
	}
	db.Create(&log1)
	db.Create(&log2)

	logs, err := GetInvoiceEmailHistory(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("GetInvoiceEmailHistory failed: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("Expected 2 logs, got %d", len(logs))
	}

	// Verify ordering (newest first)
	if logs[0].ToEmail != "test2@example.com" {
		t.Fatal("Expected newest log first")
	}
}

func TestGetInvoiceEmailHistory_CrossCompanyIsolated(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)

	log1 := models.InvoiceEmailLog{
		CompanyID:  company1ID,
		InvoiceID:  1,
		ToEmail:    "c1@example.com",
		SendStatus: models.EmailSendStatusSent,
		CreatedAt:  time.Now(),
	}
	log2 := models.InvoiceEmailLog{
		CompanyID:  company2ID,
		InvoiceID:  1,
		ToEmail:    "c2@example.com",
		SendStatus: models.EmailSendStatusSent,
		CreatedAt:  time.Now(),
	}
	db.Create(&log1)
	db.Create(&log2)

	logs, _ := GetInvoiceEmailHistory(db, company1ID, 1)
	if len(logs) != 1 {
		t.Fatalf("Expected 1 log for company1, got %d", len(logs))
	}
	if logs[0].ToEmail != "c1@example.com" {
		t.Fatal("Got wrong company's email log")
	}
}

// ── Tests: Company Isolation ───────────────────────────────────────────────────

func TestInvoiceLifecycle_CrossCompanyAccess(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)

	customerID := seedCustomerForInvoice(t, db, company1ID)
	invoiceID := seedFullInvoice(t, db, company1ID, customerID, models.InvoiceStatusDraft)

	// Try to issue with different company — should fail
	_, err := IssueInvoice(db, company2ID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for cross-company access, got nil")
	}
}

// ── Tests: State Machine ──────────────────────────────────────────────────────

func TestIsValidInvoiceTransition(t *testing.T) {
	tests := []struct {
		from    models.InvoiceStatus
		to      models.InvoiceStatus
		allowed bool
	}{
		{models.InvoiceStatusDraft, models.InvoiceStatusIssued, true},
		{models.InvoiceStatusDraft, models.InvoiceStatusSent, false},
		{models.InvoiceStatusIssued, models.InvoiceStatusSent, true},
		{models.InvoiceStatusIssued, models.InvoiceStatusPaid, true},
		{models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid, true},
		{models.InvoiceStatusSent, models.InvoiceStatusPaid, true},
		{models.InvoiceStatusSent, models.InvoiceStatusOverdue, true},
		{models.InvoiceStatusPartiallyPaid, models.InvoiceStatusPaid, true},
		{models.InvoiceStatusOverdue, models.InvoiceStatusPaid, true},
		{models.InvoiceStatusDraft, models.InvoiceStatusVoided, true},
		{models.InvoiceStatusIssued, models.InvoiceStatusVoided, true},
		{models.InvoiceStatusSent, models.InvoiceStatusVoided, true},
		{models.InvoiceStatusVoided, models.InvoiceStatusVoided, false},
		{models.InvoiceStatusVoided, models.InvoiceStatusDraft, false},
		{models.InvoiceStatusPaid, models.InvoiceStatusDraft, false},
	}

	for _, tt := range tests {
		name := fmt.Sprintf("%s->%s", tt.from, tt.to)
		t.Run(name, func(t *testing.T) {
			got := isValidInvoiceTransition(tt.from, tt.to)
			if got != tt.allowed {
				t.Errorf("isValidInvoiceTransition(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.allowed)
			}
		})
	}
}
