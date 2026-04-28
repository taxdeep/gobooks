// 遵循project_guide.md
package services

// settlement_review_service_test.go — Batch 13 settlement review list tests.
//
// Coverage:
//  TestSettlementReview_CompanyIsolation
//      list only returns rows for the queried company
//
//  TestSettlementReview_PendingFilter_ShowsPendingAndFailed
//      default filter returns pending_review + failed, not applied
//
//  TestSettlementReview_AllFilter_IncludesApplied
//      ?filter=all returns all non-empty settlement_status rows
//
//  TestSettlementReview_AllFilter_ExcludesNotAttempted
//      rows with empty settlement_status never appear in any filter
//
//  TestSettlementReview_FieldsArePopulatedFromPersistedTruth
//      returned row fields come from DB, not inferred from logs
//
//  TestSettlementReview_SortOrderDescendingByLastAttempted
//      most recently attempted row appears first
//
//  TestSettlementReview_EmptyWhenNoAttempts
//      returns empty slice when no matching attempts exist
//
//  TestSettlementReview_RetryViaRetryGatewaySettlement_ExactOnce
//      retry reuses same path, settlement applied exactly once
//
//  TestSettlementReview_RetryStillIneligible_StatusPreserved
//      retry that remains ineligible keeps pending_review with reason

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// reviewTestDB creates an in-memory SQLite DB with all necessary tables.
func reviewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:review_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceHostedLink{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		&models.HostedPaymentAttempt{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.GatewaySettlement{},
		&models.WebhookEvent{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// reviewSeedBase creates a company + gateway + AR account + clearing account.
// Returns the IDs needed to create invoices/attempts in individual tests.
type reviewBase struct {
	companyID   uint
	gatewayID   uint
	clearingID  uint
	arAccountID uint
}

func seedReviewBase(t *testing.T, db *gorm.DB) reviewBase {
	t.Helper()
	co := models.Company{Name: fmt.Sprintf("ReviewCo%d", time.Now().UnixNano()), BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)

	gw := models.PaymentGatewayAccount{
		CompanyID: co.ID, ProviderType: models.ProviderStripe,
		DisplayName: "Stripe", IsActive: true,
	}
	db.Create(&gw)

	clearing := models.Account{
		CompanyID: co.ID, Name: "GW Clearing", Code: "2100",
		DetailAccountType: models.DetailOtherCurrentLiability, IsActive: true,
	}
	db.Create(&clearing)

	ar := models.Account{
		CompanyID: co.ID, Name: "AR", Code: "1100",
		DetailAccountType: models.DetailAccountsReceivable, IsActive: true,
	}
	db.Create(&ar)

	mapping := models.PaymentAccountingMapping{
		CompanyID: co.ID, GatewayAccountID: gw.ID,
		ClearingAccountID: &clearing.ID,
	}
	db.Create(&mapping)

	return reviewBase{
		companyID:   co.ID,
		gatewayID:   gw.ID,
		clearingID:  clearing.ID,
		arAccountID: ar.ID,
	}
}

// seedReviewAttempt creates one full attempt chain: customer → invoice → link → attempt.
// Sets the settlement_status and settlement_reason on the attempt.
func seedReviewAttempt(t *testing.T, db *gorm.DB, base reviewBase,
	settlementStatus, settlementReason string) models.HostedPaymentAttempt {
	t.Helper()

	cust := models.Customer{CompanyID: base.companyID, Name: "Cust-" + settlementStatus}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID:            base.companyID,
		CustomerID:           cust.ID,
		InvoiceNumber:        fmt.Sprintf("INV-%d", time.Now().UnixNano()),
		InvoiceDate:          time.Now(),
		Status:               models.InvoiceStatusIssued,
		Amount:               decimal.NewFromInt(100),
		Subtotal:             decimal.NewFromInt(100),
		BalanceDue:           decimal.NewFromInt(100),
		BalanceDueBase:       decimal.NewFromInt(100),
		CurrencyCode:         "CAD",
		CustomerNameSnapshot: cust.Name,
	}
	db.Create(&inv)

	link := models.InvoiceHostedLink{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		TokenHash: fmt.Sprintf("thash%d", time.Now().UnixNano()),
		Status:    models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	now := time.Now()
	attempt := models.HostedPaymentAttempt{
		CompanyID:        base.companyID,
		InvoiceID:        inv.ID,
		HostedLinkID:     link.ID,
		GatewayAccountID: base.gatewayID,
		ProviderType:     models.ProviderStripe,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "CAD",
		Status:           models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef:      fmt.Sprintf("cs_%d", time.Now().UnixNano()),
		SettlementStatus: settlementStatus,
		SettlementReason: settlementReason,
		SettlementLastAttemptedAt: &now,
	}
	db.Create(&attempt)
	return attempt
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestSettlementReview_CompanyIsolation(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)
	seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "missing config")

	// Query with a different company ID — must return nothing.
	rows := ListSettlementReviewRows(db, base.companyID+9999, SettlementReviewPending)
	if len(rows) != 0 {
		t.Errorf("company isolation violated: got %d rows for wrong company", len(rows))
	}
}

func TestSettlementReview_PendingFilter_ShowsPendingAndFailed(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)
	seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "missing clearing")
	seedReviewAttempt(t, db, base, models.SettlementOutcomeFailed, "db error")
	seedReviewAttempt(t, db, base, models.SettlementOutcomeApplied, "")

	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	if len(rows) != 2 {
		t.Errorf("pending filter: want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.SettlementStatus == models.SettlementOutcomeApplied {
			t.Error("pending filter must not include applied rows")
		}
	}
}

func TestSettlementReview_AllFilter_IncludesApplied(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)
	seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "missing clearing")
	seedReviewAttempt(t, db, base, models.SettlementOutcomeApplied, "")

	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewAll)
	if len(rows) != 2 {
		t.Errorf("all filter: want 2 rows, got %d", len(rows))
	}
}

func TestSettlementReview_AllFilter_ExcludesNotAttempted(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)
	// Attempt with empty settlement_status = not yet attempted.
	cust := models.Customer{CompanyID: base.companyID, Name: "NotAttempted"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: base.companyID, CustomerID: cust.ID,
		InvoiceNumber: "INV-NA", InvoiceDate: time.Now(),
		Status: models.InvoiceStatusIssued, Amount: decimal.NewFromInt(50),
		Subtotal: decimal.NewFromInt(50), BalanceDue: decimal.NewFromInt(50),
		BalanceDueBase: decimal.NewFromInt(50), CurrencyCode: "CAD",
		CustomerNameSnapshot: "NotAttempted",
	}
	db.Create(&inv)
	link := models.InvoiceHostedLink{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		TokenHash: "tna", Status: models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)
	attempt := models.HostedPaymentAttempt{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		HostedLinkID: link.ID, GatewayAccountID: base.gatewayID,
		ProviderType: models.ProviderStripe, Amount: decimal.NewFromInt(50),
		CurrencyCode: "CAD", Status: models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef: "cs_na",
		// SettlementStatus intentionally empty
	}
	db.Create(&attempt)

	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewAll)
	for _, r := range rows {
		if r.AttemptID == attempt.ID {
			t.Error("all filter must not include rows with empty settlement_status")
		}
	}
}

func TestSettlementReview_FieldsArePopulatedFromPersistedTruth(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)
	attempt := seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "test reason persisted")

	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.AttemptID != attempt.ID {
		t.Errorf("AttemptID: want %d, got %d", attempt.ID, r.AttemptID)
	}
	if r.SettlementStatus != models.SettlementOutcomePendingReview {
		t.Errorf("SettlementStatus: want pending_review, got %q", r.SettlementStatus)
	}
	if r.SettlementReason != "test reason persisted" {
		t.Errorf("SettlementReason: want %q, got %q", "test reason persisted", r.SettlementReason)
	}
	if r.InvoiceID != attempt.InvoiceID {
		t.Errorf("InvoiceID: want %d, got %d", attempt.InvoiceID, r.InvoiceID)
	}
	if r.Amount.IsZero() {
		t.Error("Amount must be non-zero")
	}
	if r.CurrencyCode != "CAD" {
		t.Errorf("CurrencyCode: want CAD, got %q", r.CurrencyCode)
	}
	if r.SettlementLastAttemptedAt == nil {
		t.Error("SettlementLastAttemptedAt must be non-nil")
	}
	if r.InvoiceNumber == "" {
		t.Error("InvoiceNumber must be populated from join")
	}
}

func TestSettlementReview_SortOrderDescendingByLastAttempted(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)

	// Create two attempts with different last-attempted timestamps.
	older := seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "older")
	time.Sleep(2 * time.Millisecond) // ensure clock difference on fast machines
	newer := seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "newer")

	// Backdate the older one to ensure ordering.
	oldTime := time.Now().Add(-1 * time.Hour)
	db.Model(&models.HostedPaymentAttempt{}).Where("id = ?", older.ID).
		Update("settlement_last_attempted_at", oldTime)

	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 rows, got %d", len(rows))
	}
	// Most recently attempted should be first.
	if rows[0].AttemptID != newer.ID {
		t.Errorf("first row should be the newer attempt (id=%d), got id=%d", newer.ID, rows[0].AttemptID)
	}
}

func TestSettlementReview_EmptyWhenNoAttempts(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)

	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	if len(rows) != 0 {
		t.Errorf("expected empty result, got %d rows", len(rows))
	}
}

// TestSettlementReview_RetryViaRetryGatewaySettlement_ExactOnce seeds a full
// settlement-eligible attempt in pending_review state and verifies that
// RetryGatewaySettlement settles it exactly once and the row disappears from
// the pending filter afterward.
func TestSettlementReview_RetryViaRetryGatewaySettlement_ExactOnce(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)

	// Build a fully wired attempt via seedSettlementBase logic inline.
	cust := models.Customer{CompanyID: base.companyID, Name: "RetryCust"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: base.companyID, CustomerID: cust.ID,
		InvoiceNumber: "INV-RETRY", InvoiceDate: time.Now(),
		Status: models.InvoiceStatusIssued, Amount: decimal.NewFromInt(100),
		Subtotal: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
		BalanceDueBase: decimal.NewFromInt(100), CurrencyCode: "CAD",
		CustomerNameSnapshot: "RetryCust",
	}
	db.Create(&inv)
	link := models.InvoiceHostedLink{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		TokenHash: "thr", Status: models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)
	provRef := "cs_retry_001"
	now := time.Now()
	attempt := models.HostedPaymentAttempt{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		HostedLinkID: link.ID, GatewayAccountID: base.gatewayID,
		ProviderType: models.ProviderStripe, Amount: decimal.NewFromInt(100),
		CurrencyCode: "CAD", Status: models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef: provRef,
		// Start in pending_review state (as if auto-settle had failed).
		SettlementStatus:          models.SettlementOutcomePendingReview,
		SettlementReason:          "simulated initial failure",
		SettlementLastAttemptedAt: &now,
	}
	db.Create(&attempt)
	pr := models.PaymentRequest{
		CompanyID: base.companyID, GatewayAccountID: base.gatewayID,
		InvoiceID: &inv.ID, Amount: decimal.NewFromInt(100), CurrencyCode: "CAD",
		Status: models.PaymentRequestPaid, ExternalRef: provRef,
	}
	db.Create(&pr)
	txn := models.PaymentTransaction{
		CompanyID: base.companyID, GatewayAccountID: base.gatewayID,
		PaymentRequestID: &pr.ID, TransactionType: models.TxnTypeCharge,
		Amount: decimal.NewFromInt(100), CurrencyCode: "CAD",
		Status: "completed", ExternalTxnRef: "pi_retry_001",
		RawPayload: datatypes.JSON(`{"source":"test"}`),
	}
	if err := db.Create(&txn).Error; err != nil {
		t.Fatalf("create PaymentTransaction: %v", err)
	}

	// Before retry: row appears in pending list.
	before := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	if len(before) == 0 {
		t.Fatal("expected pending row before retry")
	}

	// Retry via exact same path as Batch 12.
	result, err := RetryGatewaySettlement(db, base.companyID, inv.ID)
	if err != nil {
		t.Fatalf("RetryGatewaySettlement: %v", err)
	}
	if !result.Eligibility.Eligible {
		t.Fatalf("expected eligible after retry, reason: %q", result.Eligibility.Reason)
	}

	// After retry: row no longer in pending list (status = applied).
	after := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	for _, r := range after {
		if r.AttemptID == attempt.ID {
			t.Error("attempt still shows as pending_review after successful settlement")
		}
	}

	// Row appears in "all" filter with applied status.
	allRows := ListSettlementReviewRows(db, base.companyID, SettlementReviewAll)
	var found bool
	for _, r := range allRows {
		if r.AttemptID == attempt.ID {
			found = true
			if r.SettlementStatus != models.SettlementOutcomeApplied {
				t.Errorf("applied row: want status=applied, got %q", r.SettlementStatus)
			}
		}
	}
	if !found {
		t.Error("attempt not found in all-filter after settlement")
	}

	// Second retry → ErrSettlementAlreadyDone; no duplicate JE.
	_, err2 := RetryGatewaySettlement(db, base.companyID, inv.ID)
	if !errors.Is(err2, ErrSettlementAlreadyDone) {
		t.Errorf("second retry: want ErrSettlementAlreadyDone, got %v", err2)
	}
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("source_type = ? AND source_id = ?", models.LedgerSourcePaymentGateway, txn.ID).
		Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("expected exactly 1 JournalEntry, got %d", jeCount)
	}
}

func TestSettlementReview_RetryStillIneligible_StatusPreserved(t *testing.T) {
	db := reviewTestDB(t)
	base := seedReviewBase(t, db)

	// Remove clearing account so settlement is ineligible.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("gateway_account_id = ?", base.gatewayID).
		Update("clearing_account_id", nil)

	attempt := seedReviewAttempt(t, db, base, models.SettlementOutcomePendingReview, "initial reason")

	// Retry while still ineligible.
	result, err := RetryGatewaySettlement(db, base.companyID, attempt.InvoiceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Fatal("expected ineligible result")
	}

	// Row must still appear in pending list with updated reason.
	rows := ListSettlementReviewRows(db, base.companyID, SettlementReviewPending)
	var found bool
	for _, r := range rows {
		if r.AttemptID == attempt.ID {
			found = true
			if r.SettlementStatus != models.SettlementOutcomePendingReview {
				t.Errorf("want pending_review, got %q", r.SettlementStatus)
			}
			if r.SettlementReason == "" {
				t.Error("reason must be non-empty after still-ineligible retry")
			}
		}
	}
	if !found {
		t.Error("attempt not found in pending list after still-ineligible retry")
	}
}
