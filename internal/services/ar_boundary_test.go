// 遵循project_guide.md
package services

// ar_boundary_test.go — AR Phase 1: company isolation + status constant tests.
//
// Tests verify:
//   1. Each AR object enforces company isolation (cannot load rows from a different company).
//   2. Status enum constants are complete (no accidental omissions).
//   3. Basic record creation succeeds within correct company scope.
//
// These tests do NOT test posting or JE generation (those are Phase 3-5).

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func arBoundaryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Quote{},
		&models.QuoteLine{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.CustomerReceipt{},
		&models.PaymentApplication{},
		&models.ARReturn{},
		&models.ARRefund{},
		// Dependencies referenced by FK
		&models.Invoice{},
		&models.CreditNote{},
		&models.JournalEntry{},
		&models.Account{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedARCompany creates a company and returns its ID.
func seedARCompany(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	c := models.Company{Name: name, BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedARCustomer creates a customer for the given company.
func seedARCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// ── Quote isolation ───────────────────────────────────────────────────────────

func TestARBoundary_QuoteCompanyIsolation(t *testing.T) {
	db := arBoundaryDB(t)
	cid1 := seedARCompany(t, db, "Company A")
	cid2 := seedARCompany(t, db, "Company B")
	custID1 := seedARCustomer(t, db, cid1)
	seedARCustomer(t, db, cid2)

	q := models.Quote{
		CompanyID:  cid1,
		CustomerID: custID1,
		QuoteDate:  time.Now(),
		Status:     models.QuoteStatusDraft,
	}
	if err := db.Create(&q).Error; err != nil {
		t.Fatal(err)
	}

	// cid2 must not see cid1's quote.
	var found models.Quote
	err := db.Where("id = ? AND company_id = ?", q.ID, cid2).First(&found).Error
	if err == nil {
		t.Error("expected error loading cid1 quote under cid2; got nil")
	}

	// cid1 must see it.
	if err := db.Where("id = ? AND company_id = ?", q.ID, cid1).First(&found).Error; err != nil {
		t.Errorf("expected to load quote under cid1; got: %v", err)
	}
}

// ── SalesOrder isolation ──────────────────────────────────────────────────────

func TestARBoundary_SalesOrderCompanyIsolation(t *testing.T) {
	db := arBoundaryDB(t)
	cid1 := seedARCompany(t, db, "Company A")
	cid2 := seedARCompany(t, db, "Company B")
	custID1 := seedARCustomer(t, db, cid1)

	so := models.SalesOrder{
		CompanyID:  cid1,
		CustomerID: custID1,
		OrderDate:  time.Now(),
		Status:     models.SalesOrderStatusDraft,
	}
	if err := db.Create(&so).Error; err != nil {
		t.Fatal(err)
	}

	var found models.SalesOrder
	if db.Where("id = ? AND company_id = ?", so.ID, cid2).First(&found).Error == nil {
		t.Error("expected isolation error; cid2 should not see cid1 sales order")
	}
	if err := db.Where("id = ? AND company_id = ?", so.ID, cid1).First(&found).Error; err != nil {
		t.Errorf("cid1 should see own sales order: %v", err)
	}
}

// ── CustomerDeposit isolation ─────────────────────────────────────────────────

func TestARBoundary_CustomerDepositCompanyIsolation(t *testing.T) {
	db := arBoundaryDB(t)
	cid1 := seedARCompany(t, db, "Company A")
	cid2 := seedARCompany(t, db, "Company B")
	custID1 := seedARCustomer(t, db, cid1)

	dep := models.CustomerDeposit{
		CompanyID:   cid1,
		CustomerID:  custID1,
		DepositDate: time.Now(),
		Status:      models.CustomerDepositStatusDraft,
	}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatal(err)
	}

	var found models.CustomerDeposit
	if db.Where("id = ? AND company_id = ?", dep.ID, cid2).First(&found).Error == nil {
		t.Error("expected isolation error; cid2 should not see cid1 deposit")
	}
	if err := db.Where("id = ? AND company_id = ?", dep.ID, cid1).First(&found).Error; err != nil {
		t.Errorf("cid1 should see own deposit: %v", err)
	}
}

// ── CustomerReceipt isolation ─────────────────────────────────────────────────

func TestARBoundary_CustomerReceiptCompanyIsolation(t *testing.T) {
	db := arBoundaryDB(t)
	cid1 := seedARCompany(t, db, "Company A")
	cid2 := seedARCompany(t, db, "Company B")
	custID1 := seedARCustomer(t, db, cid1)

	rec := models.CustomerReceipt{
		CompanyID:   cid1,
		CustomerID:  custID1,
		ReceiptDate: time.Now(),
		Status:      models.CustomerReceiptStatusDraft,
	}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}

	var found models.CustomerReceipt
	if db.Where("id = ? AND company_id = ?", rec.ID, cid2).First(&found).Error == nil {
		t.Error("expected isolation error; cid2 should not see cid1 receipt")
	}
	if err := db.Where("id = ? AND company_id = ?", rec.ID, cid1).First(&found).Error; err != nil {
		t.Errorf("cid1 should see own receipt: %v", err)
	}
}

// ── ARReturn isolation ────────────────────────────────────────────────────────

func TestARBoundary_ARReturnCompanyIsolation(t *testing.T) {
	db := arBoundaryDB(t)
	cid1 := seedARCompany(t, db, "Company A")
	cid2 := seedARCompany(t, db, "Company B")
	custID1 := seedARCustomer(t, db, cid1)

	// Minimal invoice for FK.
	inv := models.Invoice{CompanyID: cid1, CustomerID: custID1, InvoiceDate: time.Now(), Status: models.InvoiceStatusDraft}
	db.Create(&inv)

	ret := models.ARReturn{
		CompanyID:  cid1,
		CustomerID: custID1,
		InvoiceID:  inv.ID,
		ReturnDate: time.Now(),
		Status:     models.ARReturnStatusDraft,
	}
	if err := db.Create(&ret).Error; err != nil {
		t.Fatal(err)
	}

	var found models.ARReturn
	if db.Where("id = ? AND company_id = ?", ret.ID, cid2).First(&found).Error == nil {
		t.Error("expected isolation error; cid2 should not see cid1 return")
	}
	if err := db.Where("id = ? AND company_id = ?", ret.ID, cid1).First(&found).Error; err != nil {
		t.Errorf("cid1 should see own return: %v", err)
	}
}

// ── ARRefund isolation ────────────────────────────────────────────────────────

func TestARBoundary_ARRefundCompanyIsolation(t *testing.T) {
	db := arBoundaryDB(t)
	cid1 := seedARCompany(t, db, "Company A")
	cid2 := seedARCompany(t, db, "Company B")
	custID1 := seedARCustomer(t, db, cid1)

	ref := models.ARRefund{
		CompanyID:  cid1,
		CustomerID: custID1,
		RefundDate: time.Now(),
		Status:     models.ARRefundStatusDraft,
		SourceType: models.ARRefundSourceOther,
	}
	if err := db.Create(&ref).Error; err != nil {
		t.Fatal(err)
	}

	var found models.ARRefund
	if db.Where("id = ? AND company_id = ?", ref.ID, cid2).First(&found).Error == nil {
		t.Error("expected isolation error; cid2 should not see cid1 refund")
	}
	if err := db.Where("id = ? AND company_id = ?", ref.ID, cid1).First(&found).Error; err != nil {
		t.Errorf("cid1 should see own refund: %v", err)
	}
}

// ── Status constant completeness ──────────────────────────────────────────────

func TestARBoundary_QuoteStatusConstants(t *testing.T) {
	all := models.AllQuoteStatuses()
	want := map[models.QuoteStatus]bool{
		models.QuoteStatusDraft:     true,
		models.QuoteStatusSent:      true,
		models.QuoteStatusAccepted:  true,
		models.QuoteStatusRejected:  true,
		models.QuoteStatusConverted: true,
		models.QuoteStatusCancelled: true,
	}
	for _, s := range all {
		if !want[s] {
			t.Errorf("unexpected QuoteStatus %q in AllQuoteStatuses", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("QuoteStatus %q missing from AllQuoteStatuses", s)
	}
}

func TestARBoundary_SalesOrderStatusConstants(t *testing.T) {
	all := models.AllSalesOrderStatuses()
	want := map[models.SalesOrderStatus]bool{
		models.SalesOrderStatusDraft:             true,
		models.SalesOrderStatusConfirmed:         true,
		models.SalesOrderStatusPartiallyInvoiced: true,
		models.SalesOrderStatusFullyInvoiced:     true,
		models.SalesOrderStatusCancelled:         true,
	}
	for _, s := range all {
		if !want[s] {
			t.Errorf("unexpected SalesOrderStatus %q", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("SalesOrderStatus %q missing from AllSalesOrderStatuses", s)
	}
}

func TestARBoundary_CustomerDepositStatusConstants(t *testing.T) {
	all := models.AllCustomerDepositStatuses()
	want := map[models.CustomerDepositStatus]bool{
		models.CustomerDepositStatusDraft:            true,
		models.CustomerDepositStatusPosted:           true,
		models.CustomerDepositStatusPartiallyApplied: true,
		models.CustomerDepositStatusFullyApplied:     true,
		models.CustomerDepositStatusRefunded:         true,
		models.CustomerDepositStatusVoided:           true,
	}
	for _, s := range all {
		if !want[s] {
			t.Errorf("unexpected CustomerDepositStatus %q", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("CustomerDepositStatus %q missing", s)
	}
}

func TestARBoundary_CustomerReceiptStatusConstants(t *testing.T) {
	all := models.AllCustomerReceiptStatuses()
	want := map[models.CustomerReceiptStatus]bool{
		models.CustomerReceiptStatusDraft:            true,
		models.CustomerReceiptStatusConfirmed:        true,
		models.CustomerReceiptStatusPartiallyApplied: true,
		models.CustomerReceiptStatusFullyApplied:     true,
		models.CustomerReceiptStatusReversed:         true,
		models.CustomerReceiptStatusVoided:           true,
	}
	for _, s := range all {
		if !want[s] {
			t.Errorf("unexpected CustomerReceiptStatus %q", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("CustomerReceiptStatus %q missing", s)
	}
}

func TestARBoundary_ARReturnStatusConstants(t *testing.T) {
	all := models.AllARReturnStatuses()
	want := map[models.ARReturnStatus]bool{
		models.ARReturnStatusDraft:     true,
		models.ARReturnStatusSubmitted: true,
		models.ARReturnStatusApproved:  true,
		models.ARReturnStatusRejected:  true,
		models.ARReturnStatusProcessed: true,
		models.ARReturnStatusCancelled: true,
	}
	for _, s := range all {
		if !want[s] {
			t.Errorf("unexpected ARReturnStatus %q", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("ARReturnStatus %q missing", s)
	}
}

func TestARBoundary_ARRefundStatusConstants(t *testing.T) {
	all := models.AllARRefundStatuses()
	want := map[models.ARRefundStatus]bool{
		models.ARRefundStatusDraft:    true,
		models.ARRefundStatusPosted:   true,
		models.ARRefundStatusReversed: true,
		models.ARRefundStatusVoided:   true,
	}
	for _, s := range all {
		if !want[s] {
			t.Errorf("unexpected ARRefundStatus %q", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("ARRefundStatus %q missing", s)
	}
}

// ── No JE generated at creation (Phase 1 boundary) ───────────────────────────

// TestARBoundary_NoJEOnCreation verifies that creating Quote/SalesOrder/Receipt/Return/Refund
// draft records does not touch the journal_entries table.
func TestARBoundary_NoJEOnCreation(t *testing.T) {
	db := arBoundaryDB(t)
	cid := seedARCompany(t, db, "Test Co")
	custID := seedARCustomer(t, db, cid)

	inv := models.Invoice{CompanyID: cid, CustomerID: custID, InvoiceDate: time.Now(), Status: models.InvoiceStatusDraft}
	db.Create(&inv)

	// Create one of each AR object.
	db.Create(&models.Quote{CompanyID: cid, CustomerID: custID, QuoteDate: time.Now(), Status: models.QuoteStatusDraft})
	db.Create(&models.SalesOrder{CompanyID: cid, CustomerID: custID, OrderDate: time.Now(), Status: models.SalesOrderStatusDraft})
	db.Create(&models.CustomerDeposit{CompanyID: cid, CustomerID: custID, DepositDate: time.Now(), Status: models.CustomerDepositStatusDraft})
	db.Create(&models.CustomerReceipt{CompanyID: cid, CustomerID: custID, ReceiptDate: time.Now(), Status: models.CustomerReceiptStatusDraft})
	db.Create(&models.ARReturn{CompanyID: cid, CustomerID: custID, InvoiceID: inv.ID, ReturnDate: time.Now(), Status: models.ARReturnStatusDraft})
	db.Create(&models.ARRefund{CompanyID: cid, CustomerID: custID, RefundDate: time.Now(), Status: models.ARRefundStatusDraft, SourceType: models.ARRefundSourceOther})

	// No JournalEntry should exist — creation must not trigger any posting.
	var count int64
	db.Model(&models.JournalEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 journal entries after AR object creation; got %d", count)
	}
}
