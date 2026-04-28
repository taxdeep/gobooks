// 遵循project_guide.md
package services

// vendor_return_shipment_service_test.go — Phase I slice I.6b.2
// contract tests. Locks:
//
//  1. **Charter Q8 — no standalone Return Shipment.** Create /
//     Update require a draft-or-posted VCN link; voided VCN or nil
//     ID is rejected.
//
//  2. **Charter Q7 — identity-chain consistency.** A line's
//     VendorCreditNoteLineID must belong to the header's VCN.
//
//  3. **Cross-company scope boundary.** Any reference ID (vendor /
//     warehouse / product / vendor_credit_note / vendor_credit_note_line)
//     from a different tenant is rejected pre-write.
//
//  4. **Legacy mode = status flip only.** Post under
//     receipt_required=false produces NO inventory_movements row,
//     NO JE, NO journal_entry_id link. IN.6a's VCN retains
//     movement ownership under legacy (charter §3.4).
//
//  5. **Controlled mode = outflow + AP/Inventory JE at traced cost.**
//     Post under receipt_required=true produces:
//       - One inventory_movement row with source_type='vendor_return_shipment'
//         at the ORIGINAL bill's unit_cost_base.
//       - A JE with Dr AP / Cr Inventory at traced_cost × qty.
//       - vendor_return_shipments.journal_entry_id populated.
//
//  6. **Charter Q5 — document-local void.** Voiding a posted VRS
//     reverses ONLY this document's movement + JE. The paired VCN
//     state is untouched.
//
//  7. **Draft-only mutations.** Update / Delete refuse posted or
//     voided shipments.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// testVRSDB spins an in-memory DB with the full VRS + VCN + Bill +
// inventory + GL footprint needed for controlled-mode post tests.
func testVRSDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:vrs_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Vendor{},
		&models.Account{},
		&models.ARAPControlMapping{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Warehouse{},
		&models.Bill{},
		&models.BillLine{},
		&models.VendorCreditNote{},
		&models.VendorCreditNoteLine{},
		&models.APCreditApplication{},
		&models.VendorReturnShipment{},
		&models.VendorReturnShipmentLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// reuse seedVCNIN6Fixture + postBillWithStockLine from
// vendor_credit_note_in6_test.go (same package).

func flipReceiptRequired(t *testing.T, db *gorm.DB, companyID uint, on bool) {
	t.Helper()
	if err := db.Model(&models.Company{}).
		Where("id = ?", companyID).
		Update("receipt_required", on).Error; err != nil {
		t.Fatalf("flip receipt_required: %v", err)
	}
}

// ── Create / Q8 enforcement ─────────────────────────────────────────────────

func TestCreateVRS_RequiresVCNID(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)

	_, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:   fx.CompanyID,
		WarehouseID: fx.WarehouseID,
		ShipDate:    time.Now().UTC(),
		// VendorCreditNoteID deliberately omitted
	})
	if !errors.Is(err, ErrVendorReturnShipmentVCNRequired) {
		t.Fatalf("expected ErrVendorReturnShipmentVCNRequired, got: %v", err)
	}
}

func TestCreateVRS_RejectsVoidedVCN(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)

	// Create a VCN draft with a bogus-but-valid line, then flip to voided.
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 5, 20.00)
	if err := db.Model(&models.VendorCreditNote{}).
		Where("id = ?", vcnID).
		Update("status", models.VendorCreditNoteStatusVoided).Error; err != nil {
		t.Fatalf("void VCN: %v", err)
	}

	vcnIDPtr := vcnID
	_, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:          fx.CompanyID,
		WarehouseID:        fx.WarehouseID,
		ShipDate:           time.Now().UTC(),
		VendorCreditNoteID: &vcnIDPtr,
	})
	if !errors.Is(err, ErrVendorReturnShipmentVCNVoided) {
		t.Fatalf("expected ErrVendorReturnShipmentVCNVoided, got: %v", err)
	}
}

func TestCreateVRS_Success_LinksToDraftVCN(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)

	var vcnLine models.VendorCreditNoteLine
	if err := db.Where("vendor_credit_note_id = ?", vcnID).
		Order("id ASC").First(&vcnLine).Error; err != nil {
		t.Fatalf("load VCN line: %v", err)
	}

	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	out, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-001",
		VendorID:                   &fx.VendorID,
		WarehouseID:                fx.WarehouseID,
		ShipDate:                   time.Now().UTC(),
		VendorCreditNoteID:         &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder:              1,
			ProductServiceID:       fx.ItemID,
			Description:            "Widget return",
			Qty:                    decimal.NewFromInt(4),
			Unit:                   "ea",
			VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create VRS: %v", err)
	}
	if out.Status != models.VendorReturnShipmentStatusDraft {
		t.Fatalf("status: got %s want draft", out.Status)
	}
	if out.VendorCreditNoteID == nil || *out.VendorCreditNoteID != vcnID {
		t.Fatalf("vcn_id: got %v want %d", out.VendorCreditNoteID, vcnID)
	}
	if len(out.Lines) != 1 {
		t.Fatalf("lines: got %d want 1", len(out.Lines))
	}
}

func TestCreateVRS_RejectsCrossVCNLine(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	vcnA := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 3, 20.00)
	vcnB := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 2, 20.00)

	var vcnBLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnB).Order("id ASC").First(&vcnBLine)

	vcnAIDPtr := vcnA
	vcnBLineIDPtr := vcnBLine.ID
	_, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:          fx.CompanyID,
		WarehouseID:        fx.WarehouseID,
		ShipDate:           time.Now().UTC(),
		VendorCreditNoteID: &vcnAIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder:              1,
			ProductServiceID:       fx.ItemID,
			Qty:                    decimal.NewFromInt(2),
			VendorCreditNoteLineID: &vcnBLineIDPtr, // line from vcnB, header is vcnA
		}},
	})
	if !errors.Is(err, ErrVendorReturnShipmentLineVCNLineMismatch) {
		t.Fatalf("expected ErrVendorReturnShipmentLineVCNLineMismatch, got: %v", err)
	}
}

// ── Post under legacy (receipt_required=false) — status flip only ───────────

func TestPostVRS_Legacy_StatusFlipOnly(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-LEGACY",
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

	// Baseline: one inventory_movement from Bill post (legacy).
	var movBaseline int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ?", fx.CompanyID).Count(&movBaseline)
	var jeBaseline int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ?", fx.CompanyID).Count(&jeBaseline)

	posted, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil)
	if err != nil {
		t.Fatalf("post VRS under legacy: %v", err)
	}
	if posted.Status != models.VendorReturnShipmentStatusPosted {
		t.Fatalf("status: got %s want posted", posted.Status)
	}
	if posted.JournalEntryID != nil {
		t.Fatalf("legacy VRS post produced JE %d — must be status-flip-only",
			*posted.JournalEntryID)
	}

	var movAfter int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ?", fx.CompanyID).Count(&movAfter)
	if movAfter != movBaseline {
		t.Fatalf("legacy VRS post produced %d new inventory movement(s); must be zero",
			movAfter-movBaseline)
	}
	var jeAfter int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ?", fx.CompanyID).Count(&jeAfter)
	if jeAfter != jeBaseline {
		t.Fatalf("legacy VRS post produced %d new JE(s); must be zero",
			jeAfter-jeBaseline)
	}
}

// ── Post under controlled (receipt_required=true) — outflow + JE at traced cost ─

func TestPostVRS_Controlled_FormsOutflowAndJEAtTracedCost(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)
	// Bill posts under legacy (forms source_type='bill' movement at
	// $20/unit). Then flip receipt_required so VRS post runs the
	// rail=true branch; the narrow verb traces from the bill movement.
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-CTRL",
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

	flipReceiptRequired(t, db, fx.CompanyID, true)

	posted, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil)
	if err != nil {
		t.Fatalf("post VRS under controlled: %v", err)
	}
	if posted.JournalEntryID == nil {
		t.Fatalf("controlled VRS post must produce a JE, got nil")
	}

	// Outflow movement at traced $20 cost.
	var vrsMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, string(models.LedgerSourceVendorReturnShipment), vrs.ID).Find(&vrsMovs)
	if len(vrsMovs) != 1 {
		t.Fatalf("vrs movements: got %d want 1", len(vrsMovs))
	}
	if !vrsMovs[0].QuantityDelta.Equal(decimal.NewFromInt(-4)) {
		t.Fatalf("qty_delta: got %s want -4 (outflow)", vrsMovs[0].QuantityDelta)
	}
	if vrsMovs[0].UnitCostBase == nil || !vrsMovs[0].UnitCostBase.Equal(decimal.NewFromInt(20)) {
		var got string
		if vrsMovs[0].UnitCostBase != nil {
			got = vrsMovs[0].UnitCostBase.String()
		}
		t.Fatalf("unit_cost_base: got %q want 20 (traced original bill cost)", got)
	}
	if vrsMovs[0].MovementType != models.MovementTypeVendorReturn {
		t.Fatalf("movement_type: got %s want vendor_return", vrsMovs[0].MovementType)
	}

	// JE: Dr AP 80 / Cr Inventory 80 (4 × $20).
	var je models.JournalEntry
	if err := db.Preload("Lines").First(&je, *posted.JournalEntryID).Error; err != nil {
		t.Fatalf("load JE: %v", err)
	}
	var apDebit, invCredit decimal.Decimal
	for _, l := range je.Lines {
		if l.AccountID == fx.APAccountID {
			apDebit = apDebit.Add(l.Debit)
		}
		if l.AccountID == fx.InventoryAccountID {
			invCredit = invCredit.Add(l.Credit)
		}
	}
	want := decimal.NewFromInt(80)
	if !apDebit.Equal(want) {
		t.Fatalf("Dr AP: got %s want %s", apDebit, want)
	}
	if !invCredit.Equal(want) {
		t.Fatalf("Cr Inventory: got %s want %s", invCredit, want)
	}
}

// ── Void under controlled — doc-local, no VCN cascade ──────────────────────

func TestVoidVRS_Controlled_ReversesOwnMovementAndJE_NoCascade(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:                  fx.CompanyID,
		VendorReturnShipmentNumber: "VRS-VOID",
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
	flipReceiptRequired(t, db, fx.CompanyID, true)
	posted, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil)
	if err != nil {
		t.Fatalf("post VRS: %v", err)
	}

	// VCN baseline — should stay draft after VRS void (Q5 no cascade).
	var vcnBefore models.VendorCreditNote
	db.First(&vcnBefore, vcnID)
	if vcnBefore.Status != models.VendorCreditNoteStatusDraft {
		t.Fatalf("sanity VCN status before void: got %s want draft", vcnBefore.Status)
	}

	voided, err := VoidVendorReturnShipment(db, fx.CompanyID, posted.ID, "tester", nil)
	if err != nil {
		t.Fatalf("void VRS: %v", err)
	}
	if voided.Status != models.VendorReturnShipmentStatusVoided {
		t.Fatalf("status: got %s want voided", voided.Status)
	}

	// Reversal movement row exists.
	var revMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, "vendor_return_shipment_reversal").Find(&revMovs)
	if len(revMovs) < 1 {
		t.Fatalf("expected reversal movement, got %d", len(revMovs))
	}

	// Original JE flipped to reversed.
	var origJE models.JournalEntry
	db.First(&origJE, *posted.JournalEntryID)
	if origJE.Status != models.JournalEntryStatusReversed {
		t.Fatalf("orig JE status: got %s want reversed", origJE.Status)
	}

	// Q5 doc-local: VCN state unchanged.
	var vcnAfter models.VendorCreditNote
	db.First(&vcnAfter, vcnID)
	if vcnAfter.Status != vcnBefore.Status {
		t.Fatalf("VCN status cascaded on VRS void: before=%s after=%s (Q5 doc-local violated)",
			vcnBefore.Status, vcnAfter.Status)
	}
}

// ── Delete draft only ──────────────────────────────────────────────────────

func TestDeleteVRS_RejectsPosted(t *testing.T) {
	db := testVRSDB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)
	var vcnLine models.VendorCreditNoteLine
	db.Where("vendor_credit_note_id = ?", vcnID).Order("id ASC").First(&vcnLine)

	vcnIDPtr := vcnID
	vcnLineIDPtr := vcnLine.ID
	vrs, err := CreateVendorReturnShipment(db, CreateVendorReturnShipmentInput{
		CompanyID:          fx.CompanyID,
		WarehouseID:        fx.WarehouseID,
		ShipDate:           time.Now().UTC(),
		VendorCreditNoteID: &vcnIDPtr,
		Lines: []CreateVendorReturnShipmentLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(4), VendorCreditNoteLineID: &vcnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Post under legacy so we don't need the full inventory chain for
	// this test.
	if _, err := PostVendorReturnShipment(db, fx.CompanyID, vrs.ID, "tester", nil); err != nil {
		t.Fatalf("post: %v", err)
	}

	err = DeleteVendorReturnShipment(db, fx.CompanyID, vrs.ID)
	if !errors.Is(err, ErrVendorReturnShipmentNotDraft) {
		t.Fatalf("expected ErrVendorReturnShipmentNotDraft, got: %v", err)
	}
}
