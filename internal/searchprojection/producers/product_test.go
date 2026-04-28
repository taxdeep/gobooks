// 遵循project_guide.md
package producers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestProductServiceDocument_StockItemMapping(t *testing.T) {
	now := time.Now()
	item := models.ProductService{
		ID:           101,
		CompanyID:    7,
		Name:         "LED Panel 30W",
		SKU:          "LED-30W",
		Description:  "Commercial grade LED panel, 30W warm white",
		DefaultPrice: decimal.NewFromFloat(89.50),
		IsStockItem:  true,
		IsActive:     true,
		CreatedAt:    now,
	}
	doc := ProductServiceDocument(item)

	if doc.EntityType != EntityTypeProductService {
		t.Errorf("EntityType = %q, want %q", doc.EntityType, EntityTypeProductService)
	}
	if doc.EntityID != 101 || doc.CompanyID != 7 {
		t.Errorf("entity/company IDs mismatch: %+v", doc)
	}
	if doc.Title != "LED Panel 30W" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.DocNumber != "LED-30W" {
		t.Errorf("DocNumber should carry SKU, got %q", doc.DocNumber)
	}
	if doc.Status != "active" {
		t.Errorf("Status = %q", doc.Status)
	}
	// Subtitle must include kind + SKU + price so the dropdown row is
	// informative without extra lookups.
	if !strings.Contains(doc.Subtitle, "stock") {
		t.Errorf("Subtitle missing kind: %q", doc.Subtitle)
	}
	if !strings.Contains(doc.Subtitle, "LED-30W") {
		t.Errorf("Subtitle missing SKU: %q", doc.Subtitle)
	}
	if !strings.Contains(doc.Subtitle, "89.50") {
		t.Errorf("Subtitle missing price: %q", doc.Subtitle)
	}
	if doc.Memo != item.Description {
		t.Errorf("Memo should be Description, got %q", doc.Memo)
	}
	if !strings.HasPrefix(doc.URLPath, "/products-services") {
		t.Errorf("URLPath = %q", doc.URLPath)
	}
}

func TestProductServiceDocument_ServiceNoSKUNoPrice(t *testing.T) {
	item := models.ProductService{
		ID: 1, CompanyID: 1, Name: "Consulting Hour",
		IsStockItem: false, IsActive: true,
	}
	doc := ProductServiceDocument(item)
	// Service with no SKU, no price — subtitle degrades to just "service".
	if doc.Subtitle != "service" {
		t.Errorf("Subtitle = %q, want just kind label", doc.Subtitle)
	}
	if doc.DocNumber != "" {
		t.Errorf("DocNumber should be empty when SKU blank, got %q", doc.DocNumber)
	}
}

func TestProductServiceDocument_InactiveGetsInactiveStatus(t *testing.T) {
	item := models.ProductService{
		ID: 1, CompanyID: 1, Name: "Archived Thing",
		IsStockItem: false, IsActive: false,
	}
	doc := ProductServiceDocument(item)
	if doc.Status != "inactive" {
		t.Errorf("Status = %q, want inactive", doc.Status)
	}
}

func TestProjectProductService_LoadsFromDBAndUpserts(t *testing.T) {
	db := newProductTestDB(t)
	item := &models.ProductService{
		CompanyID: 1, Name: "LED Panel",
		SKU: "LED-30W", IsStockItem: true, IsActive: true,
		DefaultPrice: decimal.NewFromFloat(50.00),
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	rec := &recordingProjector{}
	if err := ProjectProductService(context.Background(), db, rec, item.CompanyID, item.ID); err != nil {
		t.Fatal(err)
	}
	if len(rec.upserts) != 1 {
		t.Fatalf("upserts=%d, want 1", len(rec.upserts))
	}
	got := rec.upserts[0]
	if got.Title != "LED Panel" || got.DocNumber != "LED-30W" {
		t.Errorf("unexpected upsert shape: %+v", got)
	}
}

func TestProjectProductService_NilProjectorIsNoop(t *testing.T) {
	db := newProductTestDB(t)
	if err := ProjectProductService(context.Background(), db, nil, 1, 9999); err != nil {
		t.Errorf("nil projector should be no-op, got %v", err)
	}
}

func TestProjectProductService_MissingItemReturnsError(t *testing.T) {
	db := newProductTestDB(t)
	rec := &recordingProjector{}
	err := ProjectProductService(context.Background(), db, rec, 1, 9999)
	if err == nil {
		t.Error("expected error for missing item")
	}
	if len(rec.upserts) != 0 {
		t.Error("should not upsert when load fails")
	}
}

func TestProjectProductService_RejectsCrossTenantID(t *testing.T) {
	db := newProductTestDB(t)
	a := &models.ProductService{CompanyID: 1, Name: "Co-A Item", IsActive: true}
	b := &models.ProductService{CompanyID: 2, Name: "Co-B Item", IsActive: true}
	if err := db.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	rec := &recordingProjector{}
	err := ProjectProductService(context.Background(), db, rec, 1, b.ID)
	if !errors.Is(err, ErrEntityNotInCompany) {
		t.Errorf("expected ErrEntityNotInCompany, got %v", err)
	}
	if len(rec.upserts) != 0 {
		t.Error("must not upsert cross-tenant item")
	}
}

func TestDeleteProductServiceProjection_PassesTriple(t *testing.T) {
	rec := &recordingProjector{}
	if err := DeleteProductServiceProjection(context.Background(), rec, 7, 101); err != nil {
		t.Fatal(err)
	}
	if len(rec.deletes) != 1 {
		t.Fatalf("deletes=%d, want 1", len(rec.deletes))
	}
	got := rec.deletes[0]
	if got.CompanyID != 7 || got.EntityType != EntityTypeProductService || got.EntityID != 101 {
		t.Errorf("unexpected delete: %+v", got)
	}
}

// newProductTestDB spins up a dedicated sqlite instance for product tests.
// Uses a file URI with the test name so different tests don't bleed rows
// through a shared cache.
func newProductTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:producers_product_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ProductService{}); err != nil {
		t.Fatal(err)
	}
	return db
}
