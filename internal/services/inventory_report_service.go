// 遵循project_guide.md
package services

import (
	"balanciz/internal/models"
	"github.com/shopspring/decimal"
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

// WarehouseStockRow is one stock item as held by a specific warehouse.
type WarehouseStockRow struct {
	ItemID            uint
	ItemName          string
	SKU               string
	IsActive          bool
	QuantityOnHand    decimal.Decimal
	QuantityReserved  decimal.Decimal
	QuantityAvailable decimal.Decimal
	AverageCost       decimal.Decimal
	TotalValue        decimal.Decimal
}

// WarehouseStockReport aggregates all stock items for a single warehouse.
type WarehouseStockReport struct {
	Warehouse          models.Warehouse
	Rows               []WarehouseStockRow
	TotalOnHand        decimal.Decimal
	TotalReserved      decimal.Decimal
	TotalAvailable     decimal.Decimal
	TotalValue         decimal.Decimal
	ItemsWithStock     int
	ActiveStockItems   int
	InactiveStockItems int
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

// GetWarehouseStockReport returns every stock item for a warehouse, including
// items with no balance row yet so operators can see zeros explicitly.
func GetWarehouseStockReport(db *gorm.DB, companyID, warehouseID uint) (*WarehouseStockReport, error) {
	warehouse, err := GetWarehouse(db, companyID, warehouseID)
	if err != nil {
		return nil, err
	}

	type stockJoin struct {
		ItemID           uint
		ItemName         string
		SKU              string
		IsActive         bool
		QuantityOnHand   decimal.Decimal
		QuantityReserved decimal.Decimal
		AverageCost      decimal.Decimal
	}

	var rows []stockJoin
	err = db.Raw(`
		SELECT
			p.id                                  AS item_id,
			p.name                                AS item_name,
			p.sku                                 AS sku,
			p.is_active                           AS is_active,
			COALESCE(b.quantity_on_hand, 0)       AS quantity_on_hand,
			COALESCE(b.quantity_reserved, 0)      AS quantity_reserved,
			COALESCE(b.average_cost, 0)           AS average_cost
		FROM product_services p
		LEFT JOIN inventory_balances b
		  ON b.company_id = p.company_id
		 AND b.item_id = p.id
		 AND b.warehouse_id = ?
		WHERE p.company_id = ?
		  AND p.is_stock_item = ?
		ORDER BY p.name, p.sku
	`, warehouseID, companyID, true).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	report := &WarehouseStockReport{
		Warehouse: *warehouse,
		Rows:      make([]WarehouseStockRow, 0, len(rows)),
	}
	for _, r := range rows {
		available := r.QuantityOnHand.Sub(r.QuantityReserved)
		value := r.QuantityOnHand.Mul(r.AverageCost).RoundBank(2)
		if r.QuantityOnHand.IsPositive() {
			report.ItemsWithStock++
		}
		if r.IsActive {
			report.ActiveStockItems++
		} else {
			report.InactiveStockItems++
		}
		report.TotalOnHand = report.TotalOnHand.Add(r.QuantityOnHand)
		report.TotalReserved = report.TotalReserved.Add(r.QuantityReserved)
		report.TotalAvailable = report.TotalAvailable.Add(available)
		report.TotalValue = report.TotalValue.Add(value)
		report.Rows = append(report.Rows, WarehouseStockRow{
			ItemID:            r.ItemID,
			ItemName:          r.ItemName,
			SKU:               r.SKU,
			IsActive:          r.IsActive,
			QuantityOnHand:    r.QuantityOnHand,
			QuantityReserved:  r.QuantityReserved,
			QuantityAvailable: available,
			AverageCost:       r.AverageCost,
			TotalValue:        value,
		})
	}

	return report, nil
}
