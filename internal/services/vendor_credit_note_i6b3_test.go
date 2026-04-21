// 遵循project_guide.md
package services

// vendor_credit_note_i6b3_test.go — Phase I slice I.6b.3 contract
// tests.
//
// Locks the controlled-mode retrofit of VendorCreditNote post:
//
//  1. **Exact VRS coverage (Q6) → VCN posts without double-booking AP.**
//     Under receipt_required=true, when posted
//     VendorReturnShipmentLines cover each VCN stock-line qty
//     EXACTLY, the VCN posts. VRS already did Dr AP / Cr Inventory
//     at traced cost; VCN's JE excludes the stock portion so there
//     is no double-debit on AP. If vcn.Amount equals the stock
//     portion at cost (stock-only VCN), VCN posts with NO JE at all
//     (`journal_entry_id` stays nil, consistent with Shipment/ARR
//     "no-stock-no-JE" branch).
//
//  2. **Partial coverage (Σ VRS < VCN line qty) → rejected.**
//  3. **Over-coverage (Σ VRS > VCN line qty) → rejected.**
//     Both fail with ErrVendorCreditNoteStockItemRequiresReturnReceipt
//     carrying `vcn_qty=X posted_vrs_coverage=Y`.
//
//  4. **No double-count invariant.** Posting VCN under controlled
//     mode must produce ZERO `vendor_credit_note`-sourced
//     inventory_movements (VRS owns the stock leg).

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Scenario 1: exact coverage posts (stock-only VCN at cost → no JE) ───────

func TestPostVCN_I6b3_ControlledExactCoverage_StockOnlyAtCost_NoJE(t *testing.T) {
	db := testVCNIN6DBWithVRS(t)
	fx := seedVCNIN6Fixture(t, db)

	// Original Bill: 10 units @ $20 (pre-controlled-mode bill;
	// legacy Bill post books Dr Inventory / Cr AP directly).
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	// Flip receipt_required BEFORE VCN is drafted (operator
	// transitions to controlled mode after initial bill).
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// Draft VCN for 4 units @ $20 (matches traced cost).
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	if err := db.Where("vendor_credit_note_id = ?", vcnID).
		Order("id ASC").First(&vcnLine).Error; err != nil {
		t.Fatalf("load VCN line: %v", err)
	}

	// Create + post VRS covering exactly 4 units.
	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-I6B3-EXACT",
		VendorID:                   &fx.VendorID,
		WarehouseID:                fx.WarehouseID,
		ShipDate:                   time.Now().UTC(),
		VendorCreditNoteID:         &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(4), VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create VRS: %v", err)
	}
	if _, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil); err != nil {
		t.Fatalf("post VRS: %v", err)
	}

	// Baseline: count VCN-sourced movements (should be 0; will stay 0).
	var vcnMovsBefore int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ?",
			fx.CompanyID, string(models.LedgerSourceVendorCreditNote)).
		Count(&vcnMovsBefore)
	if vcnMovsBefore != 0 {
		t.Fatalf("sanity: expected 0 VCN-sourced movements pre-VCN-post, got %d", vcnMovsBefore)
	}

	// Post the VCN. I.6b.3 retrofit: coverage matches → accepts.
	if err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil); err != nil {
		t.Fatalf("post VCN under controlled with exact coverage: %v", err)
	}

	// VCN status moved out of draft.
	var vcn models.VendorCreditNote
	db.First(&vcn, vcnID)
	if vcn.Status == models.VendorCreditNoteStatusDraft || vcn.Status == models.VendorCreditNoteStatusVoided {
		t.Fatalf("VCN status: got %q want posted-family", vcn.Status)
	}

	// Stock-only VCN at cost: NO JE produced — Offset net amount is zero.
	if vcn.JournalEntryID != nil {
		t.Fatalf("stock-only VCN at cost under controlled mode must produce NO JE (VRS already booked full reversal); got je_id=%d",
			*vcn.JournalEntryID)
	}

	// Zero VCN-sourced inventory movements — Rule #4 non-owner
	// path. VRS owns the inventory leg.
	var vcnMovCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, string(models.LedgerSourceVendorCreditNote), vcnID).
		Count(&vcnMovCount)
	if vcnMovCount != 0 {
		t.Errorf("controlled-mode VCN produced %d VCN-sourced movements; must be zero (VRS owns)",
			vcnMovCount)
	}
}

// ── Scenario 2: partial coverage rejects ────────────────────────────────────

func TestPostVCN_I6b3_ControlledPartialCoverage_Rejected(t *testing.T) {
	db := testVCNIN6DBWithVRS(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	// VRS covers only 3 — short of VCN qty 4.
	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-I6B3-PARTIAL",
		VendorID:                   &fx.VendorID,
		WarehouseID:                fx.WarehouseID,
		ShipDate:                   time.Now().UTC(),
		VendorCreditNoteID:         &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(3), VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create VRS: %v", err)
	}
	if _, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil); err != nil {
		t.Fatalf("post VRS: %v", err)
	}

	err = PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection for partial coverage")
	}
	if !isErr(err, ErrVendorCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrVendorCreditNoteStockItemRequiresReturnReceipt", err)
	}

	// VCN stays draft.
	var vcn models.VendorCreditNote
	db.First(&vcn, vcnID)
	if vcn.Status != models.VendorCreditNoteStatusDraft {
		t.Fatalf("status: got %q want draft (tx rolled back)", vcn.Status)
	}
}

// ── Scenario 3: over-coverage rejects ───────────────────────────────────────

func TestPostVCN_I6b3_ControlledOverCoverage_Rejected(t *testing.T) {
	db := testVCNIN6DBWithVRS(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	// VRS covers 5 — MORE than VCN qty 4.
	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-I6B3-OVER",
		VendorID:                   &fx.VendorID,
		WarehouseID:                fx.WarehouseID,
		ShipDate:                   time.Now().UTC(),
		VendorCreditNoteID:         &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(5), VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create VRS: %v", err)
	}
	if _, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil); err != nil {
		t.Fatalf("post VRS: %v", err)
	}

	err = PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection for over-coverage")
	}
	if !isErr(err, ErrVendorCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrVendorCreditNoteStockItemRequiresReturnReceipt", err)
	}
}

// ── Rule4DocVendorReturnShipment dispatch ───────────────────────────────────

func TestRule4Dispatch_VendorReturnShipment_OwnerUnderControlledMode(t *testing.T) {
	legacy := Rule4WorkflowState{ReceiptRequired: false, ShipmentRequired: false}
	controlled := Rule4WorkflowState{ReceiptRequired: true, ShipmentRequired: false}

	if legacy.IsMovementOwner(Rule4DocVendorReturnShipment) {
		t.Errorf("VRS should NOT be owner under legacy (IN.6a's VCN keeps ownership)")
	}
	if !controlled.IsMovementOwner(Rule4DocVendorReturnShipment) {
		t.Errorf("VRS MUST be owner under controlled mode (charter Q7)")
	}

	// Symmetric surrender: VCN owns under legacy, non-owner under
	// controlled (I.6b.3 surrender).
	if !legacy.IsMovementOwner(Rule4DocVendorCreditNote) {
		t.Errorf("VCN MUST be owner under legacy (IN.6a)")
	}
	if controlled.IsMovementOwner(Rule4DocVendorCreditNote) {
		t.Errorf("VCN MUST NOT be owner under controlled mode (I.6b.3 surrender)")
	}
}

// ── Scenario 5: posted-void (Q5 symmetry extension) ─────────────────────────

// Pre-I.6b.3 the VCN void path only accepted status=draft. Q5
// symmetry with ARR/VRS posted-void extends this: posted VCN may be
// voided, reversing its JE (and any VCN-sourced inventory movements
// under legacy mode). Document-local — paired VRS untouched.

// Legacy mode posted-void is NOT supported in I.6b.3. The IN.6a
// inventory-reversal rows can't be re-reversed (inventory.
// ReverseMovement rejects reversal-row input). Posted-void landing
// for legacy mode is a follow-on slice. The test locks the current
// rejection.

func TestVoidVCN_I6b3_PostedLegacyMode_Rejected(t *testing.T) {
	db := testVCNIN6DBWithVRS(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 10, 20.00)
	if err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil); err != nil {
		t.Fatalf("post VCN legacy: %v", err)
	}

	err := VoidVendorCreditNote(db, fx.CompanyID, vcnID)
	if err == nil {
		t.Fatalf("expected rejection for legacy-mode posted VCN void")
	}
	// Wrapped error includes ErrVendorCreditNoteInvalidStatus.
	if !isErr(err, ErrVendorCreditNoteInvalidStatus) {
		t.Fatalf("got %v want ErrVendorCreditNoteInvalidStatus", err)
	}
}

func TestVoidVCN_I6b3_PostedControlled_NoCascadeToVRS(t *testing.T) {
	db := testVCNIN6DBWithVRS(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	// Post a VRS first (VCN needs coverage).
	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-I6B3-VOIDTEST",
		VendorID:                   &fx.VendorID,
		WarehouseID:                fx.WarehouseID,
		ShipDate:                   time.Now().UTC(),
		VendorCreditNoteID:         &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(4), VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create VRS: %v", err)
	}
	posted, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil)
	if err != nil {
		t.Fatalf("post VRS: %v", err)
	}
	vrsJEID := posted.JournalEntryID

	// Now post + void the VCN.
	if err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil); err != nil {
		t.Fatalf("post VCN: %v", err)
	}
	if err := VoidVendorCreditNote(db, fx.CompanyID, vcnID); err != nil {
		t.Fatalf("void posted VCN: %v", err)
	}

	// Q5 doc-local: VRS unchanged (still posted, its JE still live).
	var vrsAfter models.VendorReturnShipment
	db.First(&vrsAfter, vrs.ID)
	if vrsAfter.Status != models.VendorReturnShipmentStatusPosted {
		t.Fatalf("VRS status cascaded on VCN void: got %q want still-posted",
			vrsAfter.Status)
	}
	if vrsJEID != nil {
		var vrsJE models.JournalEntry
		db.First(&vrsJE, *vrsJEID)
		if vrsJE.Status == models.JournalEntryStatusReversed {
			t.Fatalf("VRS JE cascaded to reversed on VCN void — Q5 doc-local violated")
		}
	}
}

// ── Test DB helper: VCN fixture + VRS tables ─────────────────────────────────

// testVCNIN6DBWithVRS reuses testVendorCreditNoteIN6DB which already
// includes VRS tables per the IN6 test-file update done in I.6b.3.
func testVCNIN6DBWithVRS(t *testing.T) *gorm.DB {
	return testVendorCreditNoteIN6DB(t)
}
