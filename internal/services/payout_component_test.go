// 遵循project_guide.md
package services

// payout_component_test.go — Batch 19: Payout component / expected-net / reconciliation-extension tests.
//
// Uses the same payoutTestDB / seedPayoutBase / seedOneSettlement / makeGatewayPayout
// helpers from gateway_payout_service_test.go (same package).
// Also shares reconTestDB / makeBankEntry from payout_reconciliation_test.go.

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Test DB helper ────────────────────────────────────────────────────────────

// componentTestDB extends reconTestDB with the new GatewayPayoutComponent table.
func componentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := reconTestDB(t)
	if err := db.AutoMigrate(&models.GatewayPayoutComponent{}); err != nil {
		t.Fatalf("migrate GatewayPayoutComponent: %v", err)
	}
	return db
}

// ── A. Payout component truth ─────────────────────────────────────────────────

// TestComponent_AddFeeHappyPath adds a fee component and verifies it is stored.
func TestComponent_AddFeeHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	comp, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromFloat(5.00),
		Description:     "monthly processor fee",
	})
	if err != nil {
		t.Fatalf("AddGatewayPayoutComponent: %v", err)
	}
	if comp.ID == 0 {
		t.Error("expected non-zero component ID")
	}
	if comp.ComponentType != models.PayoutComponentFee {
		t.Errorf("ComponentType: want fee got %s", comp.ComponentType)
	}
	if comp.Direction != models.PayoutComponentDebit {
		t.Errorf("Direction: want debit got %s", comp.Direction)
	}
	if !comp.Amount.Equal(decimal.NewFromFloat(5.00)) {
		t.Errorf("Amount: want 5.00 got %s", comp.Amount)
	}
}

// TestComponent_AddReserveHoldHappyPath adds a reserve_hold and verifies storage.
func TestComponent_AddReserveHoldHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentReserveHold,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(20),
	})
	if err != nil {
		t.Fatalf("reserve_hold add: %v", err)
	}
}

// TestComponent_AddReserveReleaseHappyPath adds a reserve_release and verifies storage.
func TestComponent_AddReserveReleaseHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentReserveRelease,
		Direction:       models.PayoutComponentCredit,
		Amount:          decimal.NewFromInt(15),
	})
	if err != nil {
		t.Fatalf("reserve_release add: %v", err)
	}
}

// TestComponent_AddAdjustmentCreditHappyPath adds a credit adjustment.
func TestComponent_AddAdjustmentCreditHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentAdjustment,
		Direction:       models.PayoutComponentCredit,
		Amount:          decimal.NewFromInt(3),
		Description:     "dispute won",
	})
	if err != nil {
		t.Fatalf("adjustment credit add: %v", err)
	}
}

// TestComponent_AddAdjustmentDebitHappyPath adds a debit adjustment.
func TestComponent_AddAdjustmentDebitHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentAdjustment,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(7),
		Description:     "fee correction",
	})
	if err != nil {
		t.Fatalf("adjustment debit add: %v", err)
	}
}

func TestComponent_AddWritesAuditLog(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	comp, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentAdjustment,
		Direction:       models.PayoutComponentCredit,
		Amount:          decimal.NewFromInt(3),
		Description:     "manual adjustment",
		Actor:           "auditor@example.com",
	})
	if err != nil {
		t.Fatalf("add component: %v", err)
	}

	row := loadAuditLogByAction(t, db, "payout.component_added", comp.ID)
	if row.Actor != "auditor@example.com" {
		t.Fatalf("expected audit actor auditor@example.com, got %s", row.Actor)
	}
	details := auditDetails(t, row.DetailsJSON)
	if uintFromJSON(t, details["gateway_payout_id"]) != payout.ID {
		t.Fatalf("expected gateway_payout_id %d, got %#v", payout.ID, details["gateway_payout_id"])
	}
	after, ok := details["after"].(map[string]any)
	if !ok {
		t.Fatalf("expected after snapshot in audit details, got %#v", details)
	}
	if after["component_type"] != string(models.PayoutComponentAdjustment) {
		t.Fatalf("expected component_type adjustment, got %#v", after["component_type"])
	}
	if after["direction"] != string(models.PayoutComponentCredit) {
		t.Fatalf("expected direction credit, got %#v", after["direction"])
	}
	if after["amount"] != "3.00" {
		t.Fatalf("expected amount 3.00, got %#v", after["amount"])
	}
}

// TestComponent_UnsupportedTypeReject rejects an unknown component type.
func TestComponent_UnsupportedTypeReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentType("unknown_type"),
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(5),
	})
	if err == nil {
		t.Fatal("expected error for unsupported component type")
	}
	if err != ErrComponentTypeInvalid {
		t.Errorf("expected ErrComponentTypeInvalid, got %v", err)
	}
}

// TestComponent_CrossCompanyReject rejects a component targeting another company's payout.
func TestComponent_CrossCompanyReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	// Use a different (non-existent) company ID.
	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID + 999,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(5),
	})
	if err == nil {
		t.Fatal("expected error for cross-company component add")
	}
	if err != ErrComponentPayoutNotFound {
		t.Errorf("expected ErrComponentPayoutNotFound, got %v", err)
	}
}

func TestComponent_DuplicateExactLineReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	in := AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(5),
		Description:     "monthly fee",
	}
	if _, err := AddGatewayPayoutComponent(db, in); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := AddGatewayPayoutComponent(db, in); err != ErrComponentDuplicate {
		t.Fatalf("expected ErrComponentDuplicate, got %v", err)
	}

	comps, err := ListGatewayPayoutComponents(db, base.companyID, payout.ID)
	if err != nil {
		t.Fatalf("ListGatewayPayoutComponents: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("expected exactly 1 component after duplicate submit, got %d", len(comps))
	}
}

// TestComponent_MatchedPayoutModifyReject rejects adding a component to a matched payout.
func TestComponent_MatchedPayoutModifyReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	// Match the payout first.
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("match: %v", err)
	}

	// Now attempt to add a component.
	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(5),
	})
	if err == nil {
		t.Fatal("expected error when adding component to matched payout")
	}
	if err != ErrComponentPayoutAlreadyMatched {
		t.Errorf("expected ErrComponentPayoutAlreadyMatched, got %v", err)
	}
}

// TestComponent_DeleteReject_MatchedPayout rejects deleting a component from a matched payout.
func TestComponent_DeleteReject_MatchedPayout(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	// Add component before matching.
	comp, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentReserveHold,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(10),
	})
	if err != nil {
		t.Fatalf("add component: %v", err)
	}

	// Match with expected net = 100 - 10 = 90.
	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(90))
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("match: %v", err)
	}

	// Now attempt to delete the component.
	if err := DeleteGatewayPayoutComponent(db, base.companyID, payout.ID, comp.ID); err == nil {
		t.Fatal("expected error when deleting component from matched payout")
	}
}

func TestComponent_DeleteWritesAuditLog(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	comp, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(5),
		Description:     "monthly fee",
	})
	if err != nil {
		t.Fatalf("add component: %v", err)
	}

	if err := DeleteGatewayPayoutComponentWithActor(db, base.companyID, payout.ID, comp.ID, "reviewer@example.com"); err != nil {
		t.Fatalf("delete component: %v", err)
	}

	row := loadAuditLogByAction(t, db, "payout.component_deleted", comp.ID)
	if row.Actor != "reviewer@example.com" {
		t.Fatalf("expected audit actor reviewer@example.com, got %s", row.Actor)
	}
	details := auditDetails(t, row.DetailsJSON)
	before, ok := details["before"].(map[string]any)
	if !ok {
		t.Fatalf("expected before snapshot in audit details, got %#v", details)
	}
	if before["component_type"] != string(models.PayoutComponentFee) {
		t.Fatalf("expected fee before snapshot, got %#v", before["component_type"])
	}
	if before["direction"] != string(models.PayoutComponentDebit) {
		t.Fatalf("expected debit before snapshot, got %#v", before["direction"])
	}
	if before["amount"] != "5.00" {
		t.Fatalf("expected amount 5.00 before snapshot, got %#v", before["amount"])
	}
}

// ── B. Expected net computation ───────────────────────────────────────────────

// TestExpectedNet_NoComponents returns payout.NetAmount unchanged.
func TestExpectedNet_NoComponents(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	net, err := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	if err != nil {
		t.Fatalf("ComputeGatewayPayoutExpectedNet: %v", err)
	}
	if !net.Equal(payout.NetAmount) {
		t.Errorf("expected %s (no components), got %s", payout.NetAmount, net)
	}
}

// TestExpectedNet_FeeReduces verifies a fee component reduces the expected net.
func TestExpectedNet_FeeReduces(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)

	net, err := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	if err != nil {
		t.Fatalf("ComputeGatewayPayoutExpectedNet: %v", err)
	}
	want := decimal.NewFromInt(95)
	if !net.Equal(want) {
		t.Errorf("fee: expected net=%s, got %s", want, net)
	}
}

// TestExpectedNet_ReserveHoldReduces verifies reserve_hold reduces the expected net.
func TestExpectedNet_ReserveHoldReduces(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveHold, models.PayoutComponentDebit, 20)

	net, err := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	if err != nil {
		t.Fatalf("ComputeGatewayPayoutExpectedNet: %v", err)
	}
	want := decimal.NewFromInt(180)
	if !net.Equal(want) {
		t.Errorf("reserve_hold: expected net=%s, got %s", want, net)
	}
}

// TestExpectedNet_ReserveReleaseIncreases verifies reserve_release increases the expected net.
func TestExpectedNet_ReserveReleaseIncreases(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveRelease, models.PayoutComponentCredit, 15)

	net, err := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	if err != nil {
		t.Fatalf("ComputeGatewayPayoutExpectedNet: %v", err)
	}
	want := decimal.NewFromInt(115)
	if !net.Equal(want) {
		t.Errorf("reserve_release: expected net=%s, got %s", want, net)
	}
}

// TestExpectedNet_AdjustmentDebit verifies a debit adjustment reduces expected net.
func TestExpectedNet_AdjustmentDebit(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentAdjustment, models.PayoutComponentDebit, 7)

	net, _ := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	want := decimal.NewFromInt(93)
	if !net.Equal(want) {
		t.Errorf("adj debit: expected net=%s, got %s", want, net)
	}
}

// TestExpectedNet_AdjustmentCredit verifies a credit adjustment increases expected net.
func TestExpectedNet_AdjustmentCredit(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentAdjustment, models.PayoutComponentCredit, 3)

	net, _ := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	want := decimal.NewFromInt(103)
	if !net.Equal(want) {
		t.Errorf("adj credit: expected net=%s, got %s", want, net)
	}
}

// TestExpectedNet_MultipleComponents verifies that multiple mixed components are combined correctly.
// payout net=200, fee=-5, reserve_hold=-20, reserve_release=+15, adjustment_debit=-2
// expected = 200 - 5 - 20 + 15 - 2 = 188
func TestExpectedNet_MultipleComponents(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveHold, models.PayoutComponentDebit, 20)
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveRelease, models.PayoutComponentCredit, 15)
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentAdjustment, models.PayoutComponentDebit, 2)

	net, err := ComputeGatewayPayoutExpectedNet(db, base.companyID, payout)
	if err != nil {
		t.Fatalf("ComputeGatewayPayoutExpectedNet: %v", err)
	}
	want := decimal.NewFromInt(188)
	if !net.Equal(want) {
		t.Errorf("multi-component: expected %s, got %s", want, net)
	}
}

// ── C. Reconciliation extension ───────────────────────────────────────────────

// TestReconExt_WithComponentsHappyPath: payout has components; bank entry matches expected net.
func TestReconExt_WithComponentsHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)

	// payout net=100, reserve_hold=-10 → expected=90
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveHold, models.PayoutComponentDebit, 10)

	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(90))

	baseline := jeCount(db, base.companyID)

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("match with components: %v", err)
	}

	// Reconciliation record must exist.
	rec, err := GetPayoutReconciliation(db, base.companyID, payout.ID)
	if err != nil || rec == nil {
		t.Fatal("expected reconciliation record")
	}

	// No new JE must be created.
	assertNoJEAdded(t, db, base.companyID, baseline)
}

// TestReconExt_WithComponentsAmountMismatch: expected net doesn't match bank entry → reject.
func TestReconExt_WithComponentsAmountMismatch(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)

	// payout net=100, reserve_hold=-10 → expected=90; but bank entry is 100 (wrong)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveHold, models.PayoutComponentDebit, 10)

	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(100))

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected amount mismatch error")
	}
	if err != ErrReconAmountMismatch && !isWrappedErr(err, ErrReconAmountMismatch) {
		t.Errorf("expected ErrReconAmountMismatch, got %v", err)
	}
}

// TestReconExt_ReserveRelease: reserve release increases expected net correctly.
func TestReconExt_ReserveRelease(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)

	// payout net=100, reserve_release=+15 → expected=115
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveRelease, models.PayoutComponentCredit, 15)

	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(115))

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("reserve_release match: %v", err)
	}
}

// TestReconExt_AccountMismatch still rejects despite valid expected net.
func TestReconExt_AccountMismatch(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)

	// Create a second bank account (different from payout's bank account).
	otherBank := models.Account{
		CompanyID:         base.companyID,
		Name:              "Other Bank",
		Code:              "1090",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	db.Create(&otherBank)

	entry := makeBankEntry(t, db, base.companyID, otherBank.ID, decimal.NewFromInt(95))

	err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test")
	if err == nil {
		t.Fatal("expected account mismatch error")
	}
	if err != ErrReconAccountMismatch {
		t.Errorf("expected ErrReconAccountMismatch, got %v", err)
	}
}

// TestReconExt_PayoutAlreadyMatchedReject verifies already-matched payout is rejected.
func TestReconExt_PayoutAlreadyMatchedReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)

	entry1 := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(95))
	entry2 := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(95))

	// First match should succeed.
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry1.ID, "test"); err != nil {
		t.Fatalf("first match: %v", err)
	}
	// Second match should be rejected.
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry2.ID, "test"); err == nil {
		t.Fatal("expected payout already matched error")
	}
}

// TestReconExt_BankEntryAlreadyMatchedReject verifies same bank entry cannot be reused.
func TestReconExt_BankEntryAlreadyMatchedReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)

	p1 := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, p1.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)
	p2 := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, p2.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)

	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(95))

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, p1.ID, entry.ID, "test"); err != nil {
		t.Fatalf("first match: %v", err)
	}
	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, p2.ID, entry.ID, "test"); err == nil {
		t.Fatal("expected bank entry already matched error")
	}
}

// TestReconExt_ConcurrentMatchOnlyOnce: two goroutines race to match the same payout+entry.
// Only one should succeed; the other must get an already-matched error.
func TestReconExt_ConcurrentMatchOnlyOnce(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 10)

	entry := makeBankEntry(t, db, base.companyID, base.bankID, decimal.NewFromInt(90))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test")
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d (errs: %v)", successCount, errs)
	}
}

// ── D. Main-chain non-regression ─────────────────────────────────────────────

// TestReconExt_Batch18SimpleMatchStillWorks verifies Batch 18 simple equal-amount
// matching still works when no components are present.
func TestReconExt_Batch18SimpleMatchStillWorks(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(150))

	// No components → expected net == payout.NetAmount.
	entry := makeBankEntry(t, db, base.companyID, base.bankID, payout.NetAmount)

	if err := MatchGatewayPayoutToBankEntry(db, base.companyID, payout.ID, entry.ID, "test"); err != nil {
		t.Fatalf("simple Batch-18 match broken: %v", err)
	}
}

// TestReconExt_PayoutJEUnchangedAfterComponentAdd verifies payout JE count is unchanged
// after adding components (no new JE should be created).
func TestReconExt_PayoutJEUnchangedAfterComponentAdd(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	baseline := jeCount(db, base.companyID)

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveHold, models.PayoutComponentDebit, 10)

	assertNoJEAdded(t, db, base.companyID, baseline)
}

// TestComponent_DirectionInvalid_FeeCredit rejects fee with credit direction.
func TestComponent_DirectionInvalid_FeeCredit(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentCredit, // wrong direction for fee
		Amount:          decimal.NewFromInt(5),
	})
	if err == nil {
		t.Fatal("expected direction error for fee+credit")
	}
}

// TestComponent_DirectionInvalid_ReserveHoldCredit rejects reserve_hold with credit direction.
func TestComponent_DirectionInvalid_ReserveHoldCredit(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentReserveHold,
		Direction:       models.PayoutComponentCredit, // wrong
		Amount:          decimal.NewFromInt(20),
	})
	if err == nil {
		t.Fatal("expected direction error for reserve_hold+credit")
	}
}

// TestComponent_DirectionInvalid_ReserveReleaseDebit rejects reserve_release with debit direction.
func TestComponent_DirectionInvalid_ReserveReleaseDebit(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentReserveRelease,
		Direction:       models.PayoutComponentDebit, // wrong
		Amount:          decimal.NewFromInt(15),
	})
	if err == nil {
		t.Fatal("expected direction error for reserve_release+debit")
	}
}

// TestComponent_ZeroAmountReject rejects zero amount.
func TestComponent_ZeroAmountReject(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	_, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.Zero,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
	if err != ErrComponentAmountInvalid {
		t.Errorf("expected ErrComponentAmountInvalid, got %v", err)
	}
}

// TestComponent_ListComponents verifies ListGatewayPayoutComponents returns correct rows.
func TestComponent_ListComponents(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(200))

	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentFee, models.PayoutComponentDebit, 5)
	addComponent(t, db, base.companyID, payout.ID, models.PayoutComponentReserveHold, models.PayoutComponentDebit, 20)

	comps, err := ListGatewayPayoutComponents(db, base.companyID, payout.ID)
	if err != nil {
		t.Fatalf("ListGatewayPayoutComponents: %v", err)
	}
	if len(comps) != 2 {
		t.Errorf("expected 2 components, got %d", len(comps))
	}
}

// TestComponent_DeleteHappyPath verifies component deletion before matching.
func TestComponent_DeleteHappyPath(t *testing.T) {
	db := componentTestDB(t)
	base := seedPayoutBase(t, db)
	payout := makeGatewayPayout(t, db, base, decimal.NewFromInt(100))

	comp, _ := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       base.companyID,
		GatewayPayoutID: payout.ID,
		ComponentType:   models.PayoutComponentFee,
		Direction:       models.PayoutComponentDebit,
		Amount:          decimal.NewFromInt(5),
	})

	if err := DeleteGatewayPayoutComponent(db, base.companyID, payout.ID, comp.ID); err != nil {
		t.Fatalf("delete component: %v", err)
	}

	comps, _ := ListGatewayPayoutComponents(db, base.companyID, payout.ID)
	if len(comps) != 0 {
		t.Errorf("expected 0 components after delete, got %d", len(comps))
	}
}

// ── Test helpers ──────────────────────────────────────────────────────────────

// addComponent is a test helper that adds a component and fails the test on error.
func addComponent(t *testing.T, db *gorm.DB, companyID, payoutID uint,
	ct models.PayoutComponentType, dir models.PayoutComponentDirection, amount int64,
) *models.GatewayPayoutComponent {
	t.Helper()
	comp, err := AddGatewayPayoutComponent(db, AddGatewayPayoutComponentInput{
		CompanyID:       companyID,
		GatewayPayoutID: payoutID,
		ComponentType:   ct,
		Direction:       dir,
		Amount:          decimal.NewFromInt(amount),
	})
	if err != nil {
		t.Fatalf("addComponent(%s, %s, %d): %v", ct, dir, amount, err)
	}
	return comp
}

// isWrappedErr checks whether target is wrapped anywhere in err's chain.
func isWrappedErr(err, target error) bool {
	if err == nil {
		return false
	}
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if err == target {
			return true
		}
		uw, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = uw.Unwrap()
	}
	return false
}

func loadAuditLogByAction(t *testing.T, db *gorm.DB, action string, entityID uint) models.AuditLog {
	t.Helper()
	var row models.AuditLog
	if err := db.Where("action = ? AND entity_id = ?", action, entityID).
		Order("id DESC").
		First(&row).Error; err != nil {
		t.Fatalf("load audit log %s/%d: %v", action, entityID, err)
	}
	return row
}

func auditDetails(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal audit details: %v", err)
	}
	return payload
}

func uintFromJSON(t *testing.T, raw any) uint {
	t.Helper()
	v, ok := raw.(float64)
	if !ok {
		t.Fatalf("expected JSON number, got %#v", raw)
	}
	return uint(v)
}
