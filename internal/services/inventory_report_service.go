// 遵循project_guide.md
package services

import (
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
)

// ── Stock report types ────────────────────────────────────────────────────────

// StockRow represents one item's balance at one location (warehouse or external).
type StockRow struct {
	ItemID   uint
	ItemName string

	// Warehouse (nil for external/legacy locations)
	WarehouseID   *uint
	WarehouseCode string
	WarehouseName string

	// External-channel location (set when WarehouseID is nil)
	LocationType models.LocationType
	LocationRef  string

	QuantityOnHand decimal.Decimal
	AverageCost    decimal.Decimal
	TotalValue     decimal.Decimal
}

// StockReport aggregates per-item, per-location balances for a company.
type StockReport struct {
	Rows       []StockRow
	TotalValue decimal.Decimal
}

// GetStockReport returns all non-zero inventory balances for a company,
// joined with warehouse and item details.
func GetStockReport(db *gorm.DB, companyID uint) (*StockReport, error) {
	// Load all balances with item + optional warehouse.
	type balanceJoin struct {
		// From inventory_balances
		ItemID         uint
		LocationType   models.LocationType
		LocationRef    string
		WarehouseID    *uint
		QuantityOnHand decimal.Decimal
		AverageCost    decimal.Decimal
		// From product_services
		ItemName string
		// From warehouses (left join — may be nil)
		WarehouseCode string
		WarehouseName string
	}

	var rows []balanceJoin
	err := db.Raw(`
		SELECT
			b.item_id,
			b.location_type,
			b.location_ref,
			b.warehouse_id,
			b.quantity_on_hand,
			b.average_cost,
			p.name               AS item_name,
			COALESCE(w.code, '') AS warehouse_code,
			COALESCE(w.name, '') AS warehouse_name
		FROM inventory_balances b
		JOIN product_services p ON p.id = b.item_id
		LEFT JOIN warehouses w ON w.id = b.warehouse_id
		WHERE b.company_id = ?
		  AND b.quantity_on_hand <> 0
		ORDER BY p.name, COALESCE(w.code, b.location_ref)
	`, companyID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	totalValue := decimal.Zero
	stockRows := make([]StockRow, 0, len(rows))
	for _, r := range rows {
		tv := r.QuantityOnHand.Mul(r.AverageCost).RoundBank(2)
		totalValue = totalValue.Add(tv)

		displayLocType := r.LocationType
		displayLocRef := r.LocationRef
		if r.WarehouseID != nil {
			displayLocType = models.LocationTypeInternal
			displayLocRef = ""
		}

		stockRows = append(stockRows, StockRow{
			ItemID:         r.ItemID,
			ItemName:       r.ItemName,
			WarehouseID:    r.WarehouseID,
			WarehouseCode:  r.WarehouseCode,
			WarehouseName:  r.WarehouseName,
			LocationType:   displayLocType,
			LocationRef:    displayLocRef,
			QuantityOnHand: r.QuantityOnHand,
			AverageCost:    r.AverageCost,
			TotalValue:     tv,
		})
	}

	return &StockReport{Rows: stockRows, TotalValue: totalValue}, nil
}
