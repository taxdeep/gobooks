// 遵循project_guide.md
package services

// phase_i6a_smoke_test.go — Phase I slice I.6a.5 smoke suite.
//
// Covers the scenarios charter §7 exit condition #6 names that the
// focused unit tests didn't hit end-to-end:
//
//   1. **Split return.** CN qty 10, two Return Receipts 6 + 4. CN
//      post succeeds (exact coverage in aggregate).
//   2. **Void + re-post.** Post an ARR, void it (coverage drops),
//      create a new ARR, post it (coverage restored), CN posts.
//
// Scenarios already covered by focused tests:
//   - Happy path (exact coverage)       — credit_note_i6a3_test.go
//   - Over / under coverage → rejection — credit_note_i6a3_test.go
//   - Tracked-lot return                — DEFERRED (charter §5 #1
//                                         non-scope for I.6a)
//
// This file uses the existing `testCreditNoteIN5DB` +
// `seedCreditNoteIN5Fixture` helpers from credit_note_in5_test.go,
// which spin an in-memory DB with the full CN + ARR + inventory +
// GL footprint.

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Scenario: split return (6 + 4 summing to CN qty 10) ─────────────────────

func TestSmoke_I6a_SplitReturn_TwoARRsSumToCNQty(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)

	// Original sale: 10 units @ $15.
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	// Flip controlled rail BEFORE CN creation.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// Customer returns all 10 units. CN drafted for qty=10.
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 10, 15.00)
	var cnLine models.CreditNoteLine
	if err := db.Where("credit_note_id = ?", cnID).
		Order("id ASC").First(&cnLine).Error; err != nil {
		t.Fatalf("load CN line: %v", err)
	}

	// Return arrives in two shipments — first 6 units, then 4 units.
	arr1 := createAndPostARR(t, db, fx, cnID, cnLine.ID, "ARR-SPLIT-1", 6, "Widget return wave 1")
	arr2 := createAndPostARR(t, db, fx, cnID, cnLine.ID, "ARR-SPLIT-2", 4, "Widget return wave 2")

	// Sanity: two ARR-sourced movements exist, total qty 10.
	var arrMovs []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, string(models.LedgerSourceARReturnReceipt)).
		Find(&arrMovs).Error; err != nil {
		t.Fatalf("load ARR movements: %v", err)
	}
	if len(arrMovs) != 2 {
		t.Fatalf("ARR movements: got %d want 2 (one per posted ARR)", len(arrMovs))
	}
	total := decimal.Zero
	for _, m := range arrMovs {
		total = total.Add(m.QuantityDelta)
	}
	if !total.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("ARR movement sum: got %s want 10", total)
	}

	// CN post — coverage (6+4) exactly equals CN qty 10 → succeeds.
	if err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("post CN with split coverage: %v", err)
	}

	var cn models.CreditNote
	db.First(&cn, cnID)
	if cn.Status == models.CreditNoteStatusDraft || cn.Status == models.CreditNoteStatusVoided {
		t.Fatalf("CN status: got %q want any posted-family status", cn.Status)
	}
	if cn.JournalEntryID == nil {
		t.Fatalf("CN JE not set post-post")
	}

	// Verify CN JE is revenue-only (controlled-mode invariant —
	// ARR owns the inventory leg).
	var je models.JournalEntry
	if err := db.Preload("Lines").First(&je, *cn.JournalEntryID).Error; err != nil {
		t.Fatalf("load CN JE: %v", err)
	}
	for _, l := range je.Lines {
		if l.AccountID == fx.InventoryAccountID {
			t.Errorf("CN JE under controlled mode touched Inventory — must be revenue-only (ARR owns inventory leg)")
		}
		if l.AccountID == fx.COGSAccountID {
			t.Errorf("CN JE under controlled mode touched COGS — must be revenue-only")
		}
	}

	// Verify zero credit_note-sourced inventory_movements (owner
	// surrender held).
	var cnMovCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, string(models.LedgerSourceCreditNote), cnID).
		Count(&cnMovCount)
	if cnMovCount != 0 {
		t.Errorf("controlled-mode CN produced %d credit_note-sourced movements; must be zero", cnMovCount)
	}

	// Silence unused warnings (IDs are for readability).
	_ = arr1
	_ = arr2
}

// ── Scenario: void + re-post ────────────────────────────────────────────────

// Sequence: post ARR-A, post another ARR-B reaching exact coverage,
// void ARR-A (coverage drops below CN qty), CN post now fails; create
// ARR-C to restore coverage, CN post succeeds.

func TestSmoke_I6a_VoidAndRepost_CoverageShortfallThenRestored(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)

	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	// CN for qty=7 (customer keeps 3 of the original 10).
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 7, 15.00)
	var cnLine models.CreditNoteLine
	db.Where("credit_note_id = ?", cnID).Order("id ASC").First(&cnLine)

	// Post two ARRs: 4 + 3 = 7 (exact).
	arrA := createAndPostARR(t, db, fx, cnID, cnLine.ID, "ARR-REPOST-A", 4, "Return wave A")
	createAndPostARR(t, db, fx, cnID, cnLine.ID, "ARR-REPOST-B", 3, "Return wave B")

	// Sanity: CN would post now (we don't actually post yet to keep
	// the void scenario clean).

	// Void ARR-A → coverage drops to 3 (below CN qty 7).
	if _, err := VoidARReturnReceipt(db, fx.CompanyID, arrA, "tester", nil); err != nil {
		t.Fatalf("void ARR-A: %v", err)
	}

	// CN post now fails with coverage shortfall.
	err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected coverage-shortfall rejection after ARR-A void")
	}
	if !isErr(err, ErrCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrCreditNoteStockItemRequiresReturnReceipt", err)
	}
	// CN stays draft (tx roll-back).
	var cnAfterVoid models.CreditNote
	db.First(&cnAfterVoid, cnID)
	if cnAfterVoid.Status != models.CreditNoteStatusDraft {
		t.Fatalf("CN status after failed post: got %q want draft", cnAfterVoid.Status)
	}

	// Post ARR-C for qty 4 to restore coverage (4 + 3 = 7).
	createAndPostARR(t, db, fx, cnID, cnLine.ID, "ARR-REPOST-C", 4, "Return re-post wave C")

	// CN post succeeds now.
	if err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("post CN after coverage restored: %v", err)
	}
	var cnFinal models.CreditNote
	db.First(&cnFinal, cnID)
	if cnFinal.Status == models.CreditNoteStatusDraft || cnFinal.Status == models.CreditNoteStatusVoided {
		t.Fatalf("CN status after final post: got %q want posted-family", cnFinal.Status)
	}

	// The voided ARR-A's own movement has its reversal row, but the
	// voided ARR itself no longer contributes to the CN coverage —
	// the coverage query filters by arr.status='posted'.
	var voidedARR models.ARReturnReceipt
	db.First(&voidedARR, arrA)
	if voidedARR.Status != models.ARReturnReceiptStatusVoided {
		t.Fatalf("ARR-A status: got %q want voided", voidedARR.Status)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// createAndPostARR creates + posts an ARReturnReceipt covering qty on
// the given CN line. Returns the posted ARR ID. Assumes
// shipment_required is already flipped on (caller's responsibility).
func createAndPostARR(t *testing.T, db *gorm.DB, fx creditNoteIN5Fixture,
	cnID, cnLineID uint, rrNumber string, qty int, desc string) uint {
	t.Helper()
	cnIDPtr := cnID
	cnLineIDPtr := cnLineID
	arr, err := CreateARReturnReceipt(db, CreateARReturnReceiptInput{
		CompanyID:           fx.CompanyID,
		ReturnReceiptNumber: rrNumber,
		CustomerID:          &fx.CustomerID,
		WarehouseID:         fx.WarehouseID,
		ReturnDate:          time.Now().UTC(),
		CreditNoteID:        &cnIDPtr,
		Lines: []CreateARReturnReceiptLineInput{{
			SortOrder:        1,
			ProductServiceID: fx.ItemID,
			Description:      desc,
			Qty:              decimal.NewFromInt(int64(qty)),
			CreditNoteLineID: &cnLineIDPtr,
		}},
	})
	if err != nil {
		t.Fatalf("create ARR %s: %v", rrNumber, err)
	}
	if _, err := PostARReturnReceipt(db, fx.CompanyID, arr.ID, "tester", nil); err != nil {
		t.Fatalf("post ARR %s: %v", rrNumber, err)
	}
	return arr.ID
}
