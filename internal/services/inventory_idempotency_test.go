// 遵循project_guide.md
package services

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

// testIdempotencyDB migrates only the inventory_movements table — the helper
// under test reads no other relations.
func testIdempotencyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:invidem_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.InventoryMovement{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedMovement(t *testing.T, db *gorm.DB, companyID uint, sourceType string, sourceID uint, idemKey string) {
	t.Helper()
	id := sourceID
	mov := models.InventoryMovement{
		CompanyID:      companyID,
		ItemID:         1,
		MovementType:   models.MovementTypePurchase,
		QuantityDelta:  decimal.NewFromInt(1),
		SourceType:     sourceType,
		SourceID:       &id,
		IdempotencyKey: idemKey,
		MovementDate:   time.Now(),
	}
	if err := db.Create(&mov).Error; err != nil {
		t.Fatalf("seed movement: %v", err)
	}
}

// First call against a scope with no prior movements returns version 1.
func TestNextIdempotencyVersion_EmptyScopeReturnsOne(t *testing.T) {
	db := testIdempotencyDB(t)
	v, err := nextIdempotencyVersion(db, 1, "bill", 42)
	if err != nil {
		t.Fatalf("nextIdempotencyVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("empty scope: got %d want 1", v)
	}
}

// After a v1 post lands, the next call returns v2. Mirrors the post → void
// → re-post flow end to end.
func TestNextIdempotencyVersion_BumpsOverPostedMovements(t *testing.T) {
	db := testIdempotencyDB(t)
	seedMovement(t, db, 1, "bill", 42, "bill:42:line:1:v1")
	seedMovement(t, db, 1, "bill", 42, "bill:42:line:2:v1")

	v, err := nextIdempotencyVersion(db, 1, "bill", 42)
	if err != nil {
		t.Fatalf("nextIdempotencyVersion: %v", err)
	}
	if v != 2 {
		t.Fatalf("after v1 post: got %d want 2", v)
	}
}

// Mixed versions: picks max + 1, not count + 1. Guards against the naive
// off-by-one where someone counts rows instead of scanning suffixes.
func TestNextIdempotencyVersion_PicksMaxPlusOne(t *testing.T) {
	db := testIdempotencyDB(t)
	seedMovement(t, db, 1, "bill", 42, "bill:42:line:1:v1")
	seedMovement(t, db, 1, "bill", 42, "bill:42:line:1:v3")
	seedMovement(t, db, 1, "bill", 42, "bill:42:line:2:v2")

	v, err := nextIdempotencyVersion(db, 1, "bill", 42)
	if err != nil {
		t.Fatalf("nextIdempotencyVersion: %v", err)
	}
	if v != 4 {
		t.Fatalf("max v3 + 1 should be 4, got %d", v)
	}
}

// Scope isolation: versions for bill:42 must not be affected by movements
// on bill:43 or invoice:42.
func TestNextIdempotencyVersion_ScopeIsolation(t *testing.T) {
	db := testIdempotencyDB(t)
	seedMovement(t, db, 1, "bill", 42, "bill:42:line:1:v5")
	seedMovement(t, db, 1, "bill", 43, "bill:43:line:1:v9")
	seedMovement(t, db, 1, "invoice", 42, "invoice:42:line:1:v7")

	v, err := nextIdempotencyVersion(db, 1, "bill", 42)
	if err != nil {
		t.Fatalf("nextIdempotencyVersion: %v", err)
	}
	if v != 6 {
		t.Fatalf("bill:42 should bump to 6 (ignoring bill:43 and invoice:42): got %d", v)
	}
}

// Legacy keys without a :v<n> suffix are treated as version 0, so the next
// post lands at v1 — matches the fresh-scope semantics.
func TestNextIdempotencyVersion_IgnoresUnversionedKeys(t *testing.T) {
	db := testIdempotencyDB(t)
	seedMovement(t, db, 1, "bill", 42, "bill:42:legacy-without-version")

	v, err := nextIdempotencyVersion(db, 1, "bill", 42)
	if err != nil {
		t.Fatalf("nextIdempotencyVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("legacy keys should map to v0, next call wants v1: got %d", v)
	}
}

// Company isolation: movements for the same (source_type, source_id) in a
// different company must not bump this company's version.
func TestNextIdempotencyVersion_CompanyIsolation(t *testing.T) {
	db := testIdempotencyDB(t)
	seedMovement(t, db, 2, "bill", 42, "bill:42:line:1:v9")

	v, err := nextIdempotencyVersion(db, 1, "bill", 42)
	if err != nil {
		t.Fatalf("nextIdempotencyVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("company 1 scope is empty; got %d want 1", v)
	}
}
