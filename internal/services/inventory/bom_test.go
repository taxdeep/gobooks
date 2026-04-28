// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// seedBomFixture creates a small BOM for testing:
//
//	parent (bundle) ←───┐
//	  ├─ widget (2 per)   │   widget has stock
//	  └─ gadget (1 per)   │   gadget has stock
//
// Returns the parent, widget and gadget IDs plus a shared warehouse.
func seedBomFixture(t *testing.T, db *gorm.DB) (companyID, parentID, widgetID, gadgetID, warehouseID uint) {
	t.Helper()
	c := models.Company{Name: "Co", IsActive: true}
	db.Create(&c)
	wh := models.Warehouse{CompanyID: c.ID, Name: "Main", IsActive: true}
	db.Create(&wh)

	widget := models.ProductService{
		CompanyID: c.ID, Name: "Widget", Type: models.ProductServiceTypeInventory,
		IsStockItem: true, IsActive: true,
	}
	db.Create(&widget)
	gadget := models.ProductService{
		CompanyID: c.ID, Name: "Gadget", Type: models.ProductServiceTypeInventory,
		IsStockItem: true, IsActive: true,
	}
	db.Create(&gadget)
	parent := models.ProductService{
		CompanyID: c.ID, Name: "Parent Bundle",
		Type:              models.ProductServiceTypeNonInventory,
		IsActive:          true,
		ItemStructureType: models.ItemStructureBundle,
	}
	db.Create(&parent)

	// 2 widgets + 1 gadget per parent.
	db.Create(&models.ItemComponent{
		CompanyID: c.ID, ParentItemID: parent.ID,
		ComponentItemID: widget.ID, Quantity: decimal.NewFromInt(2), SortOrder: 1,
	})
	db.Create(&models.ItemComponent{
		CompanyID: c.ID, ParentItemID: parent.ID,
		ComponentItemID: gadget.ID, Quantity: decimal.NewFromInt(1), SortOrder: 2,
	})

	return c.ID, parent.ID, widget.ID, gadget.ID, wh.ID
}

// Single-level explode returns one row per direct component with the right
// per-unit and effective quantities.
func TestExplodeBOM_SingleLevel(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{}) // not in the default fixture
	companyID, parentID, widgetID, gadgetID, _ := seedBomFixture(t, db)

	rows, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID:    companyID,
		ParentItemID: parentID,
		Quantity:     decimal.NewFromInt(5),
	})
	if err != nil {
		t.Fatalf("ExplodeBOM: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d want 2", len(rows))
	}

	byID := map[uint]BOMExplodeRow{}
	for _, r := range rows {
		byID[r.ComponentItemID] = r
	}
	// Widget: 2 per × 5 parents = 10
	if w, ok := byID[widgetID]; !ok || !w.TotalQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("widget TotalQuantity: got %v want 10", w.TotalQuantity)
	}
	// Gadget: 1 per × 5 parents = 5
	if g, ok := byID[gadgetID]; !ok || !g.TotalQuantity.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("gadget TotalQuantity: got %v want 5", g.TotalQuantity)
	}
	// Depth 0 for direct children.
	for _, r := range rows {
		if r.Depth != 0 {
			t.Fatalf("Depth: got %d want 0", r.Depth)
		}
	}
}

// IncludeCostEstimate populates unit + total cost using the component's
// current weighted avg at the target warehouse.
func TestExplodeBOM_CostEstimate(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})
	companyID, parentID, widgetID, gadgetID, warehouseID := seedBomFixture(t, db)

	seedReceive(t, db, companyID, widgetID, warehouseID, 100, 3) // widget avg = 3
	seedReceive(t, db, companyID, gadgetID, warehouseID, 50, 7)  // gadget avg = 7

	whID := warehouseID
	rows, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID:           companyID,
		ParentItemID:        parentID,
		Quantity:            decimal.NewFromInt(4),
		IncludeCostEstimate: true,
		WarehouseID:         &whID,
	})
	if err != nil {
		t.Fatalf("ExplodeBOM: %v", err)
	}

	for _, r := range rows {
		if r.EstimatedUnitCostBase == nil {
			t.Fatalf("component %d: missing cost estimate", r.ComponentItemID)
		}
		switch r.ComponentItemID {
		case widgetID:
			// 2 × 4 = 8 widgets × $3 = $24
			if !r.EstimatedUnitCostBase.Equal(decimal.NewFromInt(3)) {
				t.Fatalf("widget unit: got %s want 3", r.EstimatedUnitCostBase)
			}
			if !r.EstimatedTotalCostBase.Equal(decimal.NewFromInt(24)) {
				t.Fatalf("widget total: got %s want 24", r.EstimatedTotalCostBase)
			}
		case gadgetID:
			// 1 × 4 = 4 gadgets × $7 = $28
			if !r.EstimatedUnitCostBase.Equal(decimal.NewFromInt(7)) {
				t.Fatalf("gadget unit: got %s want 7", r.EstimatedUnitCostBase)
			}
			if !r.EstimatedTotalCostBase.Equal(decimal.NewFromInt(28)) {
				t.Fatalf("gadget total: got %s want 28", r.EstimatedTotalCostBase)
			}
		}
	}
}

// IncludeAvailability reports shortfalls against the target warehouse.
// Widget requires 10; we stock only 6 → ShortBy = 4.
func TestExplodeBOM_AvailabilityShortfall(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})
	companyID, parentID, widgetID, _, warehouseID := seedBomFixture(t, db)

	seedReceive(t, db, companyID, widgetID, warehouseID, 6, 3) // 6 on hand, need 10

	whID := warehouseID
	rows, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID:           companyID,
		ParentItemID:        parentID,
		Quantity:            decimal.NewFromInt(5), // 2×5 = 10 widgets required
		IncludeAvailability: true,
		WarehouseID:         &whID,
	})
	if err != nil {
		t.Fatalf("ExplodeBOM: %v", err)
	}
	for _, r := range rows {
		if r.ComponentItemID != widgetID {
			continue
		}
		if r.AvailableQuantity == nil || !r.AvailableQuantity.Equal(decimal.NewFromInt(6)) {
			t.Fatalf("AvailableQuantity: got %v want 6", r.AvailableQuantity)
		}
		if r.ShortBy == nil || !r.ShortBy.Equal(decimal.NewFromInt(4)) {
			t.Fatalf("ShortBy: got %v want 4", r.ShortBy)
		}
	}
}

// Cycle detection: a → b → a fails with ErrBOMCycle.
func TestExplodeBOM_CycleDetected(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})
	c := models.Company{Name: "Co", IsActive: true}
	db.Create(&c)

	a := models.ProductService{
		CompanyID: c.ID, Name: "A",
		Type: models.ProductServiceTypeNonInventory, IsActive: true,
		ItemStructureType: models.ItemStructureBundle,
	}
	b := models.ProductService{
		CompanyID: c.ID, Name: "B",
		Type: models.ProductServiceTypeNonInventory, IsActive: true,
		ItemStructureType: models.ItemStructureBundle,
	}
	db.Create(&a)
	db.Create(&b)

	// A -> B
	db.Create(&models.ItemComponent{
		CompanyID: c.ID, ParentItemID: a.ID, ComponentItemID: b.ID,
		Quantity: decimal.NewFromInt(1),
	})
	// B -> A (cycle)
	db.Create(&models.ItemComponent{
		CompanyID: c.ID, ParentItemID: b.ID, ComponentItemID: a.ID,
		Quantity: decimal.NewFromInt(1),
	})

	_, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID: c.ID, ParentItemID: a.ID,
		Quantity: decimal.NewFromInt(1), MultiLevel: true,
	})
	if !errors.Is(err, ErrBOMCycle) {
		t.Fatalf("got %v, want ErrBOMCycle", err)
	}
}

// Multi-level explode: a → b → leaf. With MultiLevel=true we get the leaf,
// not the intermediate.
func TestExplodeBOM_MultiLevelReachesLeaves(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})
	c := models.Company{Name: "Co", IsActive: true}
	db.Create(&c)

	mkItem := func(name string, structure models.ItemStructureType) models.ProductService {
		p := models.ProductService{
			CompanyID: c.ID, Name: name,
			Type: models.ProductServiceTypeNonInventory, IsActive: true,
			ItemStructureType: structure,
		}
		db.Create(&p)
		return p
	}
	top := mkItem("Top", models.ItemStructureBundle)
	mid := mkItem("Mid", models.ItemStructureBundle)
	leaf := mkItem("Leaf", models.ItemStructureSingle)

	db.Create(&models.ItemComponent{
		CompanyID: c.ID, ParentItemID: top.ID, ComponentItemID: mid.ID,
		Quantity: decimal.NewFromInt(3),
	})
	db.Create(&models.ItemComponent{
		CompanyID: c.ID, ParentItemID: mid.ID, ComponentItemID: leaf.ID,
		Quantity: decimal.NewFromInt(4),
	})

	rows, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID:    c.ID,
		ParentItemID: top.ID,
		Quantity:     decimal.NewFromInt(2),
		MultiLevel:   true,
	})
	if err != nil {
		t.Fatalf("ExplodeBOM multi-level: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1 (only leaf should appear in multi-level)", len(rows))
	}
	r := rows[0]
	if r.ComponentItemID != leaf.ID {
		t.Fatalf("leaf id: got %d want %d", r.ComponentItemID, leaf.ID)
	}
	// Effective qty: 2 top × 3 mid × 4 leaf = 24
	if !r.TotalQuantity.Equal(decimal.NewFromInt(24)) {
		t.Fatalf("leaf total: got %s want 24", r.TotalQuantity)
	}
	if r.Depth != 1 {
		t.Fatalf("leaf depth: got %d want 1", r.Depth)
	}
	if len(r.Path) != 3 || r.Path[0] != top.ID || r.Path[1] != mid.ID || r.Path[2] != leaf.ID {
		t.Fatalf("leaf path: %v", r.Path)
	}
}

// GetAvailableForBuild reports the correct bottleneck + max buildable units.
// Parent needs 2 widgets + 1 gadget per unit. Widget 6 on hand → supports 3.
// Gadget 4 on hand → supports 4. Answer: 3 units, bottleneck=widget.
func TestGetAvailableForBuild_BottleneckComponent(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})
	companyID, parentID, widgetID, gadgetID, warehouseID := seedBomFixture(t, db)
	seedReceive(t, db, companyID, widgetID, warehouseID, 6, 3)
	seedReceive(t, db, companyID, gadgetID, warehouseID, 4, 7)

	max, bottleneck, err := GetAvailableForBuild(db, companyID, parentID, warehouseID)
	if err != nil {
		t.Fatalf("GetAvailableForBuild: %v", err)
	}
	if !max.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("max buildable: got %s want 3", max)
	}
	if bottleneck != widgetID {
		t.Fatalf("bottleneck: got %d want %d (widget)", bottleneck, widgetID)
	}
}

// Validation: missing required inputs.
func TestExplodeBOM_ValidationErrors(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})

	if _, err := ExplodeBOM(db, BOMExplodeQuery{
		ParentItemID: 1, Quantity: decimal.NewFromInt(1),
	}); err == nil {
		t.Fatalf("missing CompanyID: expected error")
	}
	if _, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID: 1, Quantity: decimal.NewFromInt(1),
	}); err == nil {
		t.Fatalf("missing ParentItemID: expected error")
	}
	if _, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID: 1, ParentItemID: 1,
		Quantity: decimal.NewFromInt(0),
	}); !errors.Is(err, ErrNegativeQuantity) {
		t.Fatalf("zero qty: got %v want ErrNegativeQuantity", err)
	}
	if _, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID: 1, ParentItemID: 1,
		Quantity:            decimal.NewFromInt(1),
		IncludeAvailability: true,
		WarehouseID:         nil,
	}); err == nil {
		t.Fatalf("availability without warehouse: expected error")
	}
}

// Empty BOM returns nil rows + nil err (not an error — zero-component
// parents are legal during BOM construction UI).
func TestExplodeBOM_EmptyBOMOK(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&models.ItemComponent{})

	c := models.Company{Name: "Co", IsActive: true}
	db.Create(&c)
	p := models.ProductService{
		CompanyID: c.ID, Name: "Empty", Type: models.ProductServiceTypeNonInventory, IsActive: true,
		ItemStructureType: models.ItemStructureBundle,
	}
	db.Create(&p)

	rows, err := ExplodeBOM(db, BOMExplodeQuery{
		CompanyID: c.ID, ParentItemID: p.ID,
		Quantity: decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("empty BOM: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows: got %d want 0", len(rows))
	}
}

