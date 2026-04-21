// 遵循project_guide.md
package services

// rule4_invariant_test.go — IN.3 unit tests for the post-time
// invariant assertion. Drives AssertRule4PostTimeInvariant against
// synthetic inventory_movements fixtures without routing through
// the full document post paths — this file locks the assertion's
// own behavior, not any specific doc's wiring.
//
// Integration-level locks (that the Bill / Invoice / Expense post
// paths call this assertion on the correct branches) are covered by
// the existing IN.1 / IN.2 contract tests continuing to pass.

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
)

func testRule4DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rule4_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.InventoryMovement{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func seedRule4Movement(t *testing.T, db *gorm.DB, companyID uint, sourceType string, sourceID uint) {
	t.Helper()
	sid := sourceID
	wh := uint(1)
	m := models.InventoryMovement{
		CompanyID:     companyID,
		SourceType:    sourceType,
		SourceID:      &sid,
		ItemID:        1,
		WarehouseID:   &wh,
		QuantityDelta: decimal.NewFromInt(1),
		MovementDate:  time.Now().UTC(),
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed movement: %v", err)
	}
}

// ── Owner-dispatch table ────────────────────────────────────────────────────

func TestRule4_IsMovementOwner_DispatchTable(t *testing.T) {
	cases := []struct {
		name string
		doc  Rule4DocumentType
		w    Rule4WorkflowState
		want bool
	}{
		{"bill legacy", Rule4DocBill, Rule4WorkflowState{}, true},
		{"bill controlled", Rule4DocBill, Rule4WorkflowState{ReceiptRequired: true}, false},
		{"invoice legacy", Rule4DocInvoice, Rule4WorkflowState{}, true},
		{"invoice controlled", Rule4DocInvoice, Rule4WorkflowState{ShipmentRequired: true}, false},
		{"expense legacy", Rule4DocExpense, Rule4WorkflowState{}, true},
		{"expense controlled", Rule4DocExpense, Rule4WorkflowState{ReceiptRequired: true}, false},
		{"unknown doc", Rule4DocumentType("quote"), Rule4WorkflowState{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.w.IsMovementOwner(c.doc)
			if got != c.want {
				t.Fatalf("IsMovementOwner(%q, %+v) = %v want %v", c.doc, c.w, got, c.want)
			}
		})
	}
}

// ── Owner path ──────────────────────────────────────────────────────────────

// Happy path: Bill under legacy mode owns movement. A stock line and
// at least one inventory_movements row → assertion passes.
func TestAssertRule4_OwnerWithMovement_Passes(t *testing.T) {
	db := testRule4DB(t)
	seedRule4Movement(t, db, 1, "bill", 42)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocBill, 42, 1,
		Rule4WorkflowState{})
	if err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

// Silent-swallow: Bill owns movement, has 1 stock line, but ZERO
// inventory_movements rows exist → Rule #4 violation. This is the
// exact regression class IN.3 exists to catch.
func TestAssertRule4_OwnerWithNoMovement_FailsLoud(t *testing.T) {
	db := testRule4DB(t)
	// No movement seeded on purpose.
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocBill, 42, 1,
		Rule4WorkflowState{})
	if err == nil {
		t.Fatalf("expected rule4 violation, got nil")
	}
	// Error message must call out the silent-swallow class explicitly;
	// this text shows up in logs / ops alerts.
	if !contains(err.Error(), "silent swallow") {
		t.Fatalf("error should mention 'silent swallow'; got %q", err.Error())
	}
}

// Owner with zero stock lines → assertion no-ops (nothing to check).
func TestAssertRule4_OwnerWithZeroStockLines_NoOp(t *testing.T) {
	db := testRule4DB(t)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocBill, 42, 0,
		Rule4WorkflowState{})
	if err != nil {
		t.Fatalf("expected pass on zero stock lines, got %v", err)
	}
}

// ── Non-owner path ──────────────────────────────────────────────────────────

// Happy path: Bill under controlled mode is NOT the owner. Stock
// line exists on the bill but Receipt already formed the movement
// under its own source_type. No 'bill' movement rows should exist.
func TestAssertRule4_NonOwnerWithNoMovement_Passes(t *testing.T) {
	db := testRule4DB(t)
	// Receipt formed its own movement under a different source_type;
	// not relevant to this assertion which scopes by source_type='bill'.
	seedRule4Movement(t, db, 1, "receipt", 99)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocBill, 42, 1,
		Rule4WorkflowState{ReceiptRequired: true})
	if err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

// Duplicate owner violation: Bill under controlled mode is NOT the
// owner, but somehow a movement exists with source_type='bill'. Must
// fail — this means a legacy code path slipped through and would
// double-count inventory (both Bill + Receipt forming movement).
func TestAssertRule4_NonOwnerWithMovement_FailsLoud(t *testing.T) {
	db := testRule4DB(t)
	seedRule4Movement(t, db, 1, "bill", 42)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocBill, 42, 1,
		Rule4WorkflowState{ReceiptRequired: true})
	if err == nil {
		t.Fatalf("expected duplicate-owner violation, got nil")
	}
	if !contains(err.Error(), "duplicate owner") {
		t.Fatalf("error should mention 'duplicate owner'; got %q", err.Error())
	}
}

// ── Scope: Invoice + Expense + cross-company ────────────────────────────────

// Invoice / shipment_required=true path: stock line on invoice but
// Shipment owns movement. No 'invoice' source_type row should exist.
func TestAssertRule4_InvoiceShipmentFirst_Passes(t *testing.T) {
	db := testRule4DB(t)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocInvoice, 5, 1,
		Rule4WorkflowState{ShipmentRequired: true})
	if err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

// Expense legacy mode with stock line but no movement → silent swallow.
func TestAssertRule4_ExpenseLegacyWithNoMovement_FailsLoud(t *testing.T) {
	db := testRule4DB(t)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocExpense, 7, 2,
		Rule4WorkflowState{})
	if err == nil {
		t.Fatalf("expected silent-swallow violation, got nil")
	}
}

// Cross-company isolation: movement seeded for company 2 must not
// count toward company 1's assertion.
func TestAssertRule4_CrossCompanyIsolation(t *testing.T) {
	db := testRule4DB(t)
	// Company 2 has the movement; we're asserting on company 1.
	seedRule4Movement(t, db, 2, "bill", 42)
	err := AssertRule4PostTimeInvariant(db, 1, Rule4DocBill, 42, 1,
		Rule4WorkflowState{})
	if err == nil {
		t.Fatalf("expected company-1 bill to fail (its stock line has no company-1 movement)")
	}
}

// Small helper — we test substring presence rather than import a
// heavier string-matching lib.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
