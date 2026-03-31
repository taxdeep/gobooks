// 遵循project_guide.md
package services

// invoice_hardening_test.go — Phase 8 hardening tests for invoice module.
// Covers edge cases, state machine boundaries, validation failures,
// cross-company isolation, and previously untested functions.

import (
	"strings"
	"testing"
	"time"

	"gobooks/internal/models"
)

// ── State Machine Edge Cases ─────────────────────────────────────────────────

func TestMarkInvoicePaid_FromDraft_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusDraft)

	_, err := MarkInvoicePaid(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error marking draft invoice as paid")
	}
	if !strings.Contains(err.Error(), "cannot mark") {
		t.Errorf("Expected state machine error, got: %v", err)
	}
}

func TestMarkInvoicePaid_FromVoided_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusVoided)

	_, err := MarkInvoicePaid(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error marking voided invoice as paid")
	}
}

func TestMarkInvoicePaid_FromOverdue_Allowed(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusOverdue)

	updated, err := MarkInvoicePaid(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("MarkInvoicePaid from overdue failed: %v", err)
	}
	if updated.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid, got %s", updated.Status)
	}
}

func TestMarkInvoicePaid_FromPartiallyPaid_Allowed(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusPartiallyPaid)

	updated, err := MarkInvoicePaid(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("MarkInvoicePaid from partially_paid failed: %v", err)
	}
	if updated.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid, got %s", updated.Status)
	}
}

func TestMarkInvoicePaid_NotFound(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	_, err := MarkInvoicePaid(db, companyID, 99999)
	if err == nil {
		t.Fatal("Expected not-found error")
	}
}

// ── Void Edge Cases ──────────────────────────────────────────────────────────

func TestVoidInvoice_Draft_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	err := VoidInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error voiding draft invoice")
	}
	if !strings.Contains(err.Error(), "posted") {
		t.Errorf("Expected posted-only error, got: %v", err)
	}
}

func TestVoidInvoice_AlreadyVoided_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// Issue (posts) then void
	_, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}
	err = VoidInvoice(db, companyID, invoiceID, "test", nil)
	if err != nil {
		t.Fatalf("First void failed: %v", err)
	}

	// Try to void again
	err = VoidInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error voiding already-voided invoice")
	}
}

func TestVoidInvoice_NotFound(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	err := VoidInvoice(db, companyID, 99999, "test", nil)
	if err == nil {
		t.Fatal("Expected not-found error")
	}
}

// ── Send Edge Cases ──────────────────────────────────────────────────────────

func TestSendInvoice_FromDraft_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusDraft)

	_, err := SendInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error sending draft invoice")
	}
	if !strings.Contains(err.Error(), "only issued") {
		t.Errorf("Expected status error, got: %v", err)
	}
}

func TestSendInvoice_FromVoided_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusVoided)

	_, err := SendInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error sending voided invoice")
	}
}

func TestSendInvoice_NotFound(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	_, err := SendInvoice(db, companyID, 99999)
	if err == nil {
		t.Fatal("Expected not-found error")
	}
}

// ── Delete Edge Cases ────────────────────────────────────────────────────────

func TestDeleteInvoice_CrossCompany_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, company1ID)
	invoiceID := seedFullInvoice(t, db, company1ID, customerID, models.InvoiceStatusDraft)

	err := DeleteInvoice(db, company2ID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error for cross-company delete")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected not-found error, got: %v", err)
	}
}

func TestDeleteInvoice_SentStatus_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusSent)

	err := DeleteInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error deleting sent invoice")
	}
}

func TestDeleteInvoice_VoidedStatus_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusVoided)

	err := DeleteInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error deleting voided invoice")
	}
}

func TestDeleteInvoice_NotFound(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	err := DeleteInvoice(db, companyID, 99999, "test", nil)
	if err == nil {
		t.Fatal("Expected not-found error")
	}
}

// ── IssueInvoice Validation Failures ─────────────────────────────────────────

func TestIssueInvoice_NoLines_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)

	// Create invoice without lines (using seedInvoiceWithLines which doesn't create actual lines)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusDraft)

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for invoice with no lines")
	}
	if !strings.Contains(err.Error(), "line item") {
		t.Errorf("Expected line item error, got: %v", err)
	}
}

func TestIssueInvoice_NoARAccount_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)

	// Create revenue account and product but NO AR account
	revAcct := models.Account{
		CompanyID: companyID, Code: "4000", Name: "Revenue",
		RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true,
	}
	db.Create(&revAcct)

	ps := models.ProductService{
		CompanyID: companyID, Name: "Test", Type: "service",
		RevenueAccountID: revAcct.ID, IsActive: true,
	}
	db.Create(&ps)

	inv := models.Invoice{
		CompanyID: companyID, InvoiceNumber: "INV-NOAR", CustomerID: customerID,
		InvoiceDate: timePtr(timeNow()).UTC(), Status: models.InvoiceStatusDraft,
		Subtotal: toDecimal("100"), TaxTotal: toDecimal("0"), Amount: toDecimal("100"),
		BalanceDue: toDecimal("100"), CustomerNameSnapshot: "Test",
	}
	db.Create(&inv)

	line := models.InvoiceLine{
		CompanyID: companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &ps.ID, Description: "Test",
		Qty: toDecimal("1"), UnitPrice: toDecimal("100"),
		LineNet: toDecimal("100"), LineTax: toDecimal("0"), LineTotal: toDecimal("100"),
	}
	db.Create(&line)

	_, err := IssueInvoice(db, companyID, inv.ID)
	if err == nil {
		t.Fatal("Expected error for missing AR account")
	}
	if !strings.Contains(err.Error(), "Accounts Receivable") {
		t.Errorf("Expected AR account error, got: %v", err)
	}
}

func TestIssueInvoice_ZeroAmount_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)

	inv := models.Invoice{
		CompanyID: companyID, InvoiceNumber: "INV-ZERO", CustomerID: customerID,
		InvoiceDate: timeNow(), Status: models.InvoiceStatusDraft,
		Subtotal: toDecimal("0"), TaxTotal: toDecimal("0"), Amount: toDecimal("0"),
		BalanceDue: toDecimal("0"), CustomerNameSnapshot: "Test",
	}
	db.Create(&inv)

	_, err := IssueInvoice(db, companyID, inv.ID)
	if err == nil {
		t.Fatal("Expected error for zero-amount invoice")
	}
}

// ── RecalculateInvoiceBalance ────────────────────────────────────────────────

func TestRecalculateInvoiceBalance_SetsToAmount(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusIssued)

	// Manually set balance_due to 0
	db.Model(&models.Invoice{}).Where("id = ?", invoiceID).Update("balance_due", "0")

	updated, err := RecalculateInvoiceBalance(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("RecalculateInvoiceBalance failed: %v", err)
	}

	if !updated.BalanceDue.Equal(updated.Amount) {
		t.Fatalf("BalanceDue %s != Amount %s", updated.BalanceDue, updated.Amount)
	}
}

func TestRecalculateInvoiceBalance_NotFound(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	_, err := RecalculateInvoiceBalance(db, companyID, 99999)
	if err == nil {
		t.Fatal("Expected not-found error")
	}
}

// ── UpdateInvoiceStatus ──────────────────────────────────────────────────────

func TestUpdateInvoiceStatus_DraftToIssued(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusDraft)

	updated, err := UpdateInvoiceStatus(db, companyID, invoiceID, models.InvoiceStatusIssued)
	if err != nil {
		t.Fatalf("UpdateInvoiceStatus failed: %v", err)
	}
	if updated.Status != models.InvoiceStatusIssued {
		t.Fatalf("Expected issued, got %s", updated.Status)
	}
	if updated.IssuedAt == nil {
		t.Fatal("IssuedAt not set")
	}
}

func TestUpdateInvoiceStatus_InvalidTransition_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusDraft)

	_, err := UpdateInvoiceStatus(db, companyID, invoiceID, models.InvoiceStatusPaid)
	if err == nil {
		t.Fatal("Expected error for invalid transition draft → paid")
	}
	if !strings.Contains(err.Error(), "invalid status transition") {
		t.Errorf("Expected transition error, got: %v", err)
	}
}

func TestUpdateInvoiceStatus_NotFound(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)

	_, err := UpdateInvoiceStatus(db, companyID, 99999, models.InvoiceStatusIssued)
	if err == nil {
		t.Fatal("Expected not-found error")
	}
}

// ── Validation Service Direct Tests ──────────────────────────────────────────

func TestValidateInvoiceForVoiding_AlreadyVoided(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedInvoiceWithLines(t, db, companyID, customerID, models.InvoiceStatusVoided)

	err := ValidateInvoiceForVoiding(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for already-voided invoice")
	}
}

func TestValidateInvoiceForPosting_AlreadyPosted(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// Issue (which posts)
	_, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	// Validate for posting should reject (already has JE)
	err = ValidateInvoiceForPosting(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for already-posted invoice")
	}
	if !strings.Contains(err.Error(), "already posted") {
		t.Errorf("Expected already-posted error, got: %v", err)
	}
}

// ── Cross-Company Isolation ──────────────────────────────────────────────────

func TestSendInvoice_CrossCompany_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, company1ID)
	invoiceID := seedInvoiceWithLines(t, db, company1ID, customerID, models.InvoiceStatusIssued)

	_, err := SendInvoice(db, company2ID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for cross-company send")
	}
}

func TestMarkInvoicePaid_CrossCompany_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, company1ID)
	invoiceID := seedInvoiceWithLines(t, db, company1ID, customerID, models.InvoiceStatusSent)

	_, err := MarkInvoicePaid(db, company2ID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for cross-company mark paid")
	}
}

func TestVoidInvoice_CrossCompany_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, company1ID)
	invoiceID := seedFullInvoice(t, db, company1ID, customerID, models.InvoiceStatusDraft)

	// Issue first (so it can be voided)
	_, _ = IssueInvoice(db, company1ID, invoiceID)

	err := VoidInvoice(db, company2ID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error for cross-company void")
	}
}

func TestRecalculateInvoiceBalance_CrossCompany_Rejected(t *testing.T) {
	db := testInvoiceDB(t)
	company1ID := seedCompanyForInvoice(t, db)
	company2ID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, company1ID)
	invoiceID := seedInvoiceWithLines(t, db, company1ID, customerID, models.InvoiceStatusIssued)

	_, err := RecalculateInvoiceBalance(db, company2ID, invoiceID)
	if err == nil {
		t.Fatal("Expected error for cross-company balance recalculation")
	}
}

// ── Full Lifecycle Integration ───────────────────────────────────────────────

func TestInvoiceFullLifecycle_DraftToIssueToSendToPaid(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// 1. Issue
	inv, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if inv.Status != models.InvoiceStatusIssued {
		t.Fatalf("Expected issued, got %s", inv.Status)
	}
	if inv.JournalEntryID == nil {
		t.Fatal("JournalEntryID not set after issue")
	}
	if inv.IssuedAt == nil {
		t.Fatal("IssuedAt not set after issue")
	}

	// 2. Send
	inv, err = SendInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if inv.Status != models.InvoiceStatusSent {
		t.Fatalf("Expected sent, got %s", inv.Status)
	}
	if inv.SentAt == nil {
		t.Fatal("SentAt not set after send")
	}

	// 3. Mark Paid
	inv, err = MarkInvoicePaid(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("MarkPaid failed: %v", err)
	}
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Fatalf("BalanceDue not zero: %s", inv.BalanceDue)
	}

	// 4. Verify JE still exists
	var je models.JournalEntry
	if err := db.Where("id = ?", *inv.JournalEntryID).First(&je).Error; err != nil {
		t.Fatal("Journal entry not found after full lifecycle")
	}
	if je.Status != models.JournalEntryStatusPosted {
		t.Fatalf("JE status expected posted, got %s", je.Status)
	}
}

func TestInvoiceFullLifecycle_DraftToIssueToVoid(t *testing.T) {
	db := testInvoiceDB(t)
	companyID := seedCompanyForInvoice(t, db)
	customerID := seedCustomerForInvoice(t, db, companyID)
	invoiceID := seedFullInvoice(t, db, companyID, customerID, models.InvoiceStatusDraft)

	// 1. Issue
	_, err := IssueInvoice(db, companyID, invoiceID)
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	// 2. Void
	err = VoidInvoice(db, companyID, invoiceID, "test-void", nil)
	if err != nil {
		t.Fatalf("Void failed: %v", err)
	}

	// 3. Verify invoice is voided
	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.Status != models.InvoiceStatusVoided {
		t.Fatalf("Expected voided, got %s", inv.Status)
	}

	// 4. Verify original JE is reversed
	var origJE models.JournalEntry
	db.First(&origJE, *inv.JournalEntryID)
	if origJE.Status != models.JournalEntryStatusReversed {
		t.Fatalf("Original JE expected reversed, got %s", origJE.Status)
	}

	// 5. Verify reversal JE exists
	var reversalJE models.JournalEntry
	if err := db.Where("reversed_from_id = ?", origJE.ID).First(&reversalJE).Error; err != nil {
		t.Fatal("Reversal JE not found")
	}
	if reversalJE.Status != models.JournalEntryStatusPosted {
		t.Fatalf("Reversal JE expected posted, got %s", reversalJE.Status)
	}

	// 6. Cannot void again
	err = VoidInvoice(db, companyID, invoiceID, "test-void-again", nil)
	if err == nil {
		t.Fatal("Expected error voiding already-voided invoice")
	}

	// 7. Cannot mark paid
	_, err = MarkInvoicePaid(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("Expected error marking voided invoice as paid")
	}

	// 8. Cannot delete
	err = DeleteInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("Expected error deleting voided invoice")
	}
}

// ── Helper ───────────────────────────────────────────────────────────────────

func timeNow() time.Time {
	return time.Now()
}
