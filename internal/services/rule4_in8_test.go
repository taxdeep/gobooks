// 遵循project_guide.md
package services

// rule4_in8_test.go — IN.8 contract tests for the Rule #4
// invariant extension to Receipt (H.3) and Shipment (I.3) post
// paths.
//
// IN.8 closes the coverage gap called out in the pre-IN.8 comment
// on rule4_invariant.go (since rewritten). Before IN.8 the
// invariant ran only on Bill / Invoice / Expense / CreditNote /
// VCN / ARR / VRS posts — the document families where a future
// refactor dropping the `CreateXxxMovements` call would silently
// break Rule #4. Receipt and Shipment are structurally symmetric
// and deserve the same CI-level guard.
//
// Locks:
//
//  1. **Dispatch table.** `Rule4DocReceipt` owner iff
//     `ReceiptRequired=true`; `Rule4DocShipment` owner iff
//     `ShipmentRequired=true`. Mirror of the Bill / Invoice
//     entries (which surrender ownership under the respective
//     rail).
//
//  2. **Silent-swallow catch on Receipt.** If the assertion is
//     called with `Rule4DocReceipt` + `ReceiptRequired=true` +
//     stockLineCount>0 but zero inventory_movements rows exist
//     for the (company, source_type='receipt', source_id), the
//     assertion returns an error naming the doc + count.
//
//  3. **Silent-swallow catch on Shipment.** Same shape for
//     Rule4DocShipment under ShipmentRequired=true.
//
//  4. **Non-owner path passes with zero movements.** Receipt
//     under legacy (receipt_required=false) is non-owner — the
//     invariant expects zero movements and passes.

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

func testRule4InvariantDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rule4_in8_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.InventoryMovement{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// ── Dispatch table (Receipt / Shipment) ─────────────────────────────────────

func TestRule4Dispatch_Receipt_OwnerUnderControlledMode(t *testing.T) {
	legacy := Rule4WorkflowState{ReceiptRequired: false, ShipmentRequired: false}
	controlled := Rule4WorkflowState{ReceiptRequired: true, ShipmentRequired: false}

	if legacy.IsMovementOwner(Rule4DocReceipt) {
		t.Errorf("Receipt should NOT be owner under legacy (Bill IN.1 keeps ownership)")
	}
	if !controlled.IsMovementOwner(Rule4DocReceipt) {
		t.Errorf("Receipt MUST be owner under controlled mode (Phase H.4 surrender)")
	}
	// Symmetric surrender: Bill owns under legacy, non-owner under controlled.
	if !legacy.IsMovementOwner(Rule4DocBill) {
		t.Errorf("Bill MUST be owner under legacy")
	}
	if controlled.IsMovementOwner(Rule4DocBill) {
		t.Errorf("Bill MUST NOT be owner under controlled mode (Phase H.4 surrender)")
	}
}

func TestRule4Dispatch_Shipment_OwnerUnderControlledMode(t *testing.T) {
	legacy := Rule4WorkflowState{ReceiptRequired: false, ShipmentRequired: false}
	controlled := Rule4WorkflowState{ReceiptRequired: false, ShipmentRequired: true}

	if legacy.IsMovementOwner(Rule4DocShipment) {
		t.Errorf("Shipment should NOT be owner under legacy (Invoice keeps ownership)")
	}
	if !controlled.IsMovementOwner(Rule4DocShipment) {
		t.Errorf("Shipment MUST be owner under controlled mode (Phase I.4 surrender)")
	}
	if !legacy.IsMovementOwner(Rule4DocInvoice) {
		t.Errorf("Invoice MUST be owner under legacy")
	}
	if controlled.IsMovementOwner(Rule4DocInvoice) {
		t.Errorf("Invoice MUST NOT be owner under controlled mode (Phase I.4 surrender)")
	}
}

// ── Silent-swallow catch ────────────────────────────────────────────────────

func TestAssertRule4_Receipt_SilentSwallowCaught(t *testing.T) {
	// Simulate a "future refactor dropped CreateReceiptMovements"
	// regression: Receipt posted under controlled mode with
	// stockLineCount>0 but ZERO inventory_movements rows.
	db := testRule4InvariantDB(t)

	const companyID = uint(1)
	const receiptID = uint(42)

	err := AssertRule4PostTimeInvariant(db, companyID,
		Rule4DocReceipt, receiptID, 1,
		Rule4WorkflowState{ReceiptRequired: true},
	)
	if err == nil {
		t.Fatalf("expected rule4 violation error (silent swallow)")
	}
	// Error message should name the document type + the violation class.
	msg := err.Error()
	if !containsAll(msg, "rule4 violation", "silent swallow", "receipt") {
		t.Fatalf("error message missing expected tokens: %q", msg)
	}
}

func TestAssertRule4_Shipment_SilentSwallowCaught(t *testing.T) {
	db := testRule4InvariantDB(t)

	const companyID = uint(1)
	const shipmentID = uint(77)

	err := AssertRule4PostTimeInvariant(db, companyID,
		Rule4DocShipment, shipmentID, 1,
		Rule4WorkflowState{ShipmentRequired: true},
	)
	if err == nil {
		t.Fatalf("expected rule4 violation error (silent swallow)")
	}
	msg := err.Error()
	if !containsAll(msg, "rule4 violation", "silent swallow", "shipment") {
		t.Fatalf("error message missing expected tokens: %q", msg)
	}
}

// ── Non-owner passes with zero movements ────────────────────────────────────

func TestAssertRule4_Receipt_NonOwnerPasses(t *testing.T) {
	db := testRule4InvariantDB(t)

	// Legacy mode: Receipt is non-owner. Zero movements expected.
	err := AssertRule4PostTimeInvariant(db, 1,
		Rule4DocReceipt, 1, 1,
		Rule4WorkflowState{ReceiptRequired: false},
	)
	if err != nil {
		t.Fatalf("non-owner path with zero movements should pass; got %v", err)
	}
}

func TestAssertRule4_Shipment_NonOwnerPasses(t *testing.T) {
	db := testRule4InvariantDB(t)
	err := AssertRule4PostTimeInvariant(db, 1,
		Rule4DocShipment, 1, 1,
		Rule4WorkflowState{ShipmentRequired: false},
	)
	if err != nil {
		t.Fatalf("non-owner path with zero movements should pass; got %v", err)
	}
}

// containsAll reports whether every substring appears in s
// (case-insensitive via lowercasing both sides — all tokens we
// check are already lowercase).
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !stringContains(s, sub) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
