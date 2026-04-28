// 遵循project_guide.md
package services

// credit_note_i6a3_test.go — Phase I slice I.6a.3 contract tests.
//
// Locks the controlled-mode retrofit of CreditNote post:
//
//  1. **Exact ARR coverage (Q6) → post succeeds, revenue-only JE.**
//     Under shipment_required=true, when posted ARReturnReceiptLines
//     cover the stock-line qty EXACTLY, the CN posts and produces
//     ONLY a revenue-reversal JE (Dr Revenue / Cr AR). No Dr
//     Inventory / Cr COGS fragments; no credit_note-sourced
//     inventory_movements rows. The paired ARR's own post already
//     owns the inventory leg.
//
//  2. **Partial coverage (Σ ARR < CN line qty) → rejected.**
//  3. **Over-coverage (Σ ARR > CN line qty) → rejected.**
//     Both fail with ErrCreditNoteStockItemRequiresReturnReceipt
//     (the sentinel kept stable for RULE4_RUNBOOK §10a triage; the
//     wrapped message cites the actual coverage vs expected).
//
//  4. **Rule4DocARReturnReceipt dispatch.** Verifies the workflow
//     IsMovementOwner table: ARR is owner iff ShipmentRequired=true;
//     CreditNote surrenders ownership under the same rail.
//
// What this file does NOT cover (belongs to later slices)
// -------------------------------------------------------
// - UI "Create matching Return Receipt" shortcut: I.6a.4.
// - Pilot enablement / smoke suite: I.6a.5.

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// postARRForCN helper — create + post an ARR for a given CN line at
// the given qty. Used to build coverage scenarios. Returns the
// posted ARR.
func postARRForCN(t *testing.T, db interface {
	// gorm.DB methods used inside the helper. Using interface gives
	// test-shape flexibility without importing gorm here.
}, _ int) {}

// ── Scenario 1: Exact coverage posts CN with revenue-only JE ─────────────────

func TestPostCreditNote_I6a3_ControlledExactCoverage_PostsRevenueOnly(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	// Flip rail BEFORE creating the CN — so the post path runs the
	// controlled-mode branch.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// Draft CN with stock line: return 4 of 10.
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)

	// Look up the CN line ID for ARR linkage.
	var cnLine models.CreditNoteLine
	if err := db.Where("credit_note_id = ?", cnID).
		Order("id ASC").First(&cnLine).Error; err != nil {
		t.Fatalf("load CN line: %v", err)
	}

	// Create + post an ARR covering exactly 4 units.
	cnIDPtr := cnID
	cnLineIDPtr := cnLine.ID
	arr, err := CreateARReturnReceipt(db, CreateARReturnReceiptInput{
		CompanyID:    fx.CompanyID,
		ReturnReceiptNumber: "ARR-I6A3-EXACT",
		CustomerID:   &fx.CustomerID,
		WarehouseID:  fx.WarehouseID,
		ReturnDate:   time.Now().UTC(),
		CreditNoteID: &cnIDPtr,
		Lines: []CreateARReturnReceiptLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(4), CreditNoteLineID: &cnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create ARR: %v", err)
	}
	if _, err := PostARReturnReceipt(db, fx.CompanyID, arr.ID, "tester", nil); err != nil {
		t.Fatalf("post ARR: %v", err)
	}

	// Baseline before CN post: one ARR-sourced inventory_movement.
	var arrMovCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ?",
			fx.CompanyID, string(models.LedgerSourceARReturnReceipt)).
		Count(&arrMovCount)
	if arrMovCount != 1 {
		t.Fatalf("sanity: expected 1 ARR-sourced movement pre-CN, got %d", arrMovCount)
	}

	// Post the CN. I.6a.3 retrofit accepts the stock line (coverage
	// matches) and produces a revenue-only JE.
	if err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("post CN under controlled with exact coverage: %v", err)
	}

	// CN status has moved out of draft (into issued / partially_applied
	// / fully_applied depending on auto-application to the linked
	// invoice — all represent "posted"). JE present.
	var cn models.CreditNote
	db.First(&cn, cnID)
	if cn.Status == models.CreditNoteStatusDraft || cn.Status == models.CreditNoteStatusVoided {
		t.Fatalf("CN status: got %q want any posted-family status (issued / partially_applied / fully_applied)", cn.Status)
	}
	if cn.JournalEntryID == nil {
		t.Fatalf("CN JE not set")
	}

	// JE has ONLY revenue-reversal fragments (Dr Revenue / Cr AR +
	// tax). No Dr Inventory / Cr COGS. This is the core I.6a.3
	// revenue-only assertion.
	var je models.JournalEntry
	if err := db.Preload("Lines").First(&je, *cn.JournalEntryID).Error; err != nil {
		t.Fatalf("load JE: %v", err)
	}
	var invTouched, cogsTouched bool
	for _, l := range je.Lines {
		if l.AccountID == fx.InventoryAccountID {
			invTouched = true
		}
		if l.AccountID == fx.COGSAccountID {
			cogsTouched = true
		}
	}
	if invTouched {
		t.Errorf("controlled-mode CN JE touched Inventory account — must be revenue-only")
	}
	if cogsTouched {
		t.Errorf("controlled-mode CN JE touched COGS account — must be revenue-only")
	}

	// Zero credit_note-sourced inventory_movements (Rule4DocCreditNote
	// surrendered ownership under controlled mode).
	var cnMovCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, string(models.LedgerSourceCreditNote), cnID).
		Count(&cnMovCount)
	if cnMovCount != 0 {
		t.Errorf("controlled-mode CN produced %d credit_note-sourced movements; must be zero (ARR owns)", cnMovCount)
	}

	// ARR-sourced movement count unchanged — still just the one from
	// pre-CN.
	var arrAfter int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ?",
			fx.CompanyID, string(models.LedgerSourceARReturnReceipt)).
		Count(&arrAfter)
	if arrAfter != arrMovCount {
		t.Errorf("ARR-sourced movements changed from %d → %d during CN post (must be inert)",
			arrMovCount, arrAfter)
	}
}

// ── Scenario 2: Partial coverage rejects ────────────────────────────────────

func TestPostCreditNote_I6a3_ControlledPartialCoverage_Rejected(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// CN wants to credit 4 units.
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)
	var cnLine models.CreditNoteLine
	db.Where("credit_note_id = ?", cnID).Order("id ASC").First(&cnLine)

	// ARR only covers 3 — short of CN qty 4.
	cnIDPtr := cnID
	cnLineIDPtr := cnLine.ID
	arr, err := CreateARReturnReceipt(db, CreateARReturnReceiptInput{
		CompanyID:    fx.CompanyID,
		ReturnReceiptNumber: "ARR-I6A3-PARTIAL",
		CustomerID:   &fx.CustomerID,
		WarehouseID:  fx.WarehouseID,
		ReturnDate:   time.Now().UTC(),
		CreditNoteID: &cnIDPtr,
		Lines: []CreateARReturnReceiptLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(3), CreditNoteLineID: &cnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create ARR: %v", err)
	}
	if _, err := PostARReturnReceipt(db, fx.CompanyID, arr.ID, "tester", nil); err != nil {
		t.Fatalf("post ARR: %v", err)
	}

	err = PostCreditNote(db, fx.CompanyID, cnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection for partial coverage")
	}
	if !isErr(err, ErrCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrCreditNoteStockItemRequiresReturnReceipt", err)
	}

	// CN stays draft.
	var cn models.CreditNote
	db.First(&cn, cnID)
	if cn.Status != models.CreditNoteStatusDraft {
		t.Fatalf("status: got %q want draft (tx should roll back)", cn.Status)
	}
}

// ── Scenario 3: Over-coverage rejects ───────────────────────────────────────

func TestPostCreditNote_I6a3_ControlledOverCoverage_Rejected(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// CN wants to credit 4 units.
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)
	var cnLine models.CreditNoteLine
	db.Where("credit_note_id = ?", cnID).Order("id ASC").First(&cnLine)

	// ARR covers 5 — MORE than CN qty 4. Q6 disallows over-coverage.
	cnIDPtr := cnID
	cnLineIDPtr := cnLine.ID
	arr, err := CreateARReturnReceipt(db, CreateARReturnReceiptInput{
		CompanyID:    fx.CompanyID,
		ReturnReceiptNumber: "ARR-I6A3-OVER",
		CustomerID:   &fx.CustomerID,
		WarehouseID:  fx.WarehouseID,
		ReturnDate:   time.Now().UTC(),
		CreditNoteID: &cnIDPtr,
		Lines: []CreateARReturnReceiptLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(5), CreditNoteLineID: &cnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create ARR: %v", err)
	}
	if _, err := PostARReturnReceipt(db, fx.CompanyID, arr.ID, "tester", nil); err != nil {
		t.Fatalf("post ARR: %v", err)
	}

	err = PostCreditNote(db, fx.CompanyID, cnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection for over-coverage")
	}
	if !isErr(err, ErrCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrCreditNoteStockItemRequiresReturnReceipt", err)
	}
}

// ── Scenario 4: Rule4DocARReturnReceipt dispatch table ──────────────────────

func TestRule4Dispatch_ARReturnReceipt_OwnerUnderControlledMode(t *testing.T) {
	// ARR is the owner ONLY when shipment_required=true.
	legacy := Rule4WorkflowState{ShipmentRequired: false, ReceiptRequired: false}
	controlled := Rule4WorkflowState{ShipmentRequired: true, ReceiptRequired: false}

	if legacy.IsMovementOwner(Rule4DocARReturnReceipt) {
		t.Errorf("ARR should NOT be owner under legacy mode (CreditNote IN.5 keeps ownership)")
	}
	if !controlled.IsMovementOwner(Rule4DocARReturnReceipt) {
		t.Errorf("ARR MUST be owner under controlled mode (charter Q7)")
	}

	// Symmetric surrender: CreditNote is owner under legacy, non-
	// owner under controlled (I.6a.3 surrender).
	if !legacy.IsMovementOwner(Rule4DocCreditNote) {
		t.Errorf("CreditNote MUST be owner under legacy mode (IN.5)")
	}
	if controlled.IsMovementOwner(Rule4DocCreditNote) {
		t.Errorf("CreditNote MUST NOT be owner under controlled mode (I.6a.3 surrender)")
	}
}

// ── Scenario 5: Rail flip between ARR post and CN post — edge case ──────────
//
// A subtle invariant — if the rail is flipped AFTER the ARR is
// posted, the CN's pre-flight must still work on whatever the
// CURRENT rail state is at CN post time. Re-reading the company
// inside PostCreditNote picks up the flip. This test just documents
// that timing doesn't corrupt the coverage check.

func TestPostCreditNote_I6a3_ARRPostedUnderLegacy_CNUnderControlled_CoverageHolds(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)
	var cnLine models.CreditNoteLine
	db.Where("credit_note_id = ?", cnID).Order("id ASC").First(&cnLine)

	// ARR created + posted under legacy (status-flip only, zero
	// inventory effect per I.6a.2 rail-aware rule). But the ARR
	// row still exists with status=posted — the coverage query
	// finds it.
	cnIDPtr := cnID
	cnLineIDPtr := cnLine.ID
	arr, err := CreateARReturnReceipt(db, CreateARReturnReceiptInput{
		CompanyID:    fx.CompanyID,
		ReturnReceiptNumber: "ARR-RAILFLIP",
		CustomerID:   &fx.CustomerID,
		WarehouseID:  fx.WarehouseID,
		ReturnDate:   time.Now().UTC(),
		CreditNoteID: &cnIDPtr,
		Lines: []CreateARReturnReceiptLineInput{{
			SortOrder: 1, ProductServiceID: fx.ItemID,
			Qty: decimal.NewFromInt(4), CreditNoteLineID: &cnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create ARR: %v", err)
	}
	if _, err := PostARReturnReceipt(db, fx.CompanyID, arr.ID, "tester", nil); err != nil {
		t.Fatalf("post ARR under legacy: %v", err)
	}

	// Now flip the rail on. CN post MUST accept the stock line
	// because posted-ARR coverage already exists.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	if err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("post CN post-flip: %v", err)
	}
	var cn models.CreditNote
	db.First(&cn, cnID)
	if cn.Status == models.CreditNoteStatusDraft || cn.Status == models.CreditNoteStatusVoided {
		t.Fatalf("CN status: got %q want any posted-family status", cn.Status)
	}
}
