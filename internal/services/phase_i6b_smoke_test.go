// 遵循project_guide.md
package services

// phase_i6b_smoke_test.go — Phase I slice I.6b.5 smoke suite.
//
// Covers the AP-side charter §7 exit condition #6 scenarios that
// the focused-unit tests didn't hit end-to-end. Scenarios already
// covered by focused tests:
//   - Happy exact coverage (stock-only at cost, no VCN JE)  — vendor_credit_note_i6b3_test.go
//   - Under / over coverage rejection                        — vendor_credit_note_i6b3_test.go
//   - Posted-void controlled-mode                            — vendor_credit_note_i6b3_test.go
//   - Narrow verb traced-cost guarantees                     — inventory/issue_vendor_return_test.go
//
// What this file adds (gaps filled for Phase I.6 exit criteria):
//
//   1. **Split return.** VCN qty 10, two VRSs 6 + 4 summing — the
//      canonical partial-qty pattern that closes IN.6a's deferred
//      gap (charter §7 exit condition #3).
//
//   2. **Void + re-post.** Post VRS, void it (coverage drops → VCN
//      can't post), post a fresh VRS to restore coverage, VCN posts.
//
// Tracked-lot returns remain deliberately out of scope (non-scope
// item #1 in charter, documented in PHASE_I6_RUNBOOK §13).

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Scenario: split return (6 + 4 summing to VCN qty 10) ────────────────────

func TestSmoke_I6b_SplitReturn_TwoVRSsSumToVCNQty(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)

	// Original Bill: 10 units @ $20 (legacy mode posting).
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	// Flip controlled rail BEFORE VCN creation.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// VCN for all 10 units.
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 10, 20.00)
	var vcnLine models.VendorCreditNoteLine
	if err := db.Where("vendor_credit_note_id = ?", vcnID).
		Order("id ASC").First(&vcnLine).Error; err != nil {
		t.Fatalf("load VCN line: %v", err)
	}

	// Two VRSs — 6 + 4 = 10.
	vrs1 := createAndPostVRS(t, db, fx, vcnID, vcnLine.ID, "VRS-SPLIT-1", 6)
	vrs2 := createAndPostVRS(t, db, fx, vcnID, vcnLine.ID, "VRS-SPLIT-2", 4)

	// Sanity: two VRS-sourced movements total qty 10 (as outflow).
	var vrsMovs []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, string(models.LedgerSourceVendorReturnShipment)).
		Find(&vrsMovs).Error; err != nil {
		t.Fatalf("load VRS movements: %v", err)
	}
	if len(vrsMovs) != 2 {
		t.Fatalf("VRS movements: got %d want 2", len(vrsMovs))
	}
	totalOut := decimal.Zero
	for _, m := range vrsMovs {
		totalOut = totalOut.Add(m.QuantityDelta)
	}
	// Total should be -10 (outflow magnitude 10).
	if !totalOut.Equal(decimal.NewFromInt(-10)) {
		t.Fatalf("VRS movement sum: got %s want -10", totalOut)
	}

	// VCN post with stock-only-at-cost → succeeds with NO JE (VRS
	// covered full reversal).
	if err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil); err != nil {
		t.Fatalf("post VCN with split coverage: %v", err)
	}

	var vcn models.VendorCreditNote
	db.First(&vcn, vcnID)
	if vcn.Status == models.VendorCreditNoteStatusDraft || vcn.Status == models.VendorCreditNoteStatusVoided {
		t.Fatalf("VCN status: got %q want posted-family", vcn.Status)
	}
	if vcn.JournalEntryID != nil {
		t.Errorf("stock-only VCN at cost should produce NO JE; got je_id=%d", *vcn.JournalEntryID)
	}

	// Zero VCN-sourced inventory movements (owner surrender held).
	var vcnMovCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, string(models.LedgerSourceVendorCreditNote), vcnID).
		Count(&vcnMovCount)
	if vcnMovCount != 0 {
		t.Errorf("controlled-mode VCN produced %d VCN-sourced movements; must be zero",
			vcnMovCount)
	}

	_ = vrs1
	_ = vrs2
}

// ── Scenario: void + re-post (coverage shortfall then restored) ─────────────

func TestSmoke_I6b_VoidAndRepost_CoverageShortfallThenRestored(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)

	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// VCN for 7 units (keep 3 of the original 10).
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 7, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	// Post two VRSs reaching exact coverage: 4 + 3.
	vrsA := createAndPostVRS(t, db, fx, vcnID, vcnLine.ID, "VRS-REPOST-A", 4)
	createAndPostVRS(t, db, fx, vcnID, vcnLine.ID, "VRS-REPOST-B", 3)

	// Void VRS-A → coverage drops to 3 (below VCN qty 7).
	if _, err := VoidVendorReturnShipment(db, fx.CompanyID, vrsA, "tester", nil); err != nil {
		t.Fatalf("void VRS-A: %v", err)
	}

	// VCN post now fails.
	err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected coverage shortfall after VRS-A void")
	}
	if !isErr(err, ErrVendorCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrVendorCreditNoteStockItemRequiresReturnReceipt", err)
	}
	// VCN still draft (tx rollback).
	var vcnAfterVoid models.VendorCreditNote
	db.First(&vcnAfterVoid, vcnID)
	if vcnAfterVoid.Status != models.VendorCreditNoteStatusDraft {
		t.Fatalf("VCN status: got %q want draft", vcnAfterVoid.Status)
	}

	// Post VRS-C for 4 to restore coverage (4 + 3 = 7).
	createAndPostVRS(t, db, fx, vcnID, vcnLine.ID, "VRS-REPOST-C", 4)

	// VCN post succeeds.
	if err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil); err != nil {
		t.Fatalf("post VCN after coverage restored: %v", err)
	}
	var vcnFinal models.VendorCreditNote
	db.First(&vcnFinal, vcnID)
	if vcnFinal.Status == models.VendorCreditNoteStatusDraft || vcnFinal.Status == models.VendorCreditNoteStatusVoided {
		t.Fatalf("VCN status after final post: got %q want posted-family", vcnFinal.Status)
	}

	// Voided VRS-A's status is 'voided' and no longer contributes
	// to coverage (filter is status='posted').
	var voidedVRS models.VendorReturnShipment
	db.First(&voidedVRS, vrsA)
	if voidedVRS.Status != models.VendorReturnShipmentStatusVoided {
		t.Fatalf("VRS-A status: got %q want voided", voidedVRS.Status)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func createAndPostVRS(t *testing.T, db *gorm.DB, fx vcnIN6Fixture,
	vcnID, vcnLineID uint, vrsNumber string, qty int) uint {
	t.Helper()
	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLineID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: vrsNumber,
		VendorID:                   &fx.VendorID,
		WarehouseID:                fx.WarehouseID,
		ShipDate:                   time.Now().UTC(),
		VendorCreditNoteID:         &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder:              1,
			ProductServiceID:       fx.ItemID,
			Qty:                    decimal.NewFromInt(int64(qty)),
			VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create VRS %s: %v", vrsNumber, err)
	}
	if _, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil); err != nil {
		t.Fatalf("post VRS %s: %v", vrsNumber, err)
	}
	return vrs.ID
}
