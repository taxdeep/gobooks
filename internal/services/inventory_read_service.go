// 遵循project_guide.md
package services

// inventory_read_service.go — Read-only queries for inventory visibility.
// Provides snapshot, valuation, and movement history data for UI display.
// All queries are company-scoped. No writes.

import (
	"balanciz/internal/models"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ── Snapshot ─────────────────────────────────────────────────────────────────

// WarehouseBalance holds per-warehouse stock data for the ledger snapshot.
type WarehouseBalance struct {
	WarehouseID   uint
	WarehouseCode string
	WarehouseName string
	QtyOnHand     decimal.Decimal
	AverageCost   decimal.Decimal
	Value         decimal.Decimal
}

// InventorySnapshot holds the current stock state for a single item.
type InventorySnapshot struct {
	ItemID         uint
	QuantityOnHand decimal.Decimal
	AverageCost    decimal.Decimal
	InventoryValue decimal.Decimal // qty × avg_cost
	CostingMethod  string
	HasOpening     bool
	// Per-warehouse breakdown (populated when multi-warehouse is active)
	WarehouseBreakdown []WarehouseBalance
}

// GetInventorySnapshot returns the current stock snapshot for an inventory item.
// Returns zero values for non-inventory items or items with no balance record.
func GetInventorySnapshot(db *gorm.DB, companyID, itemID uint) (*InventorySnapshot, error) {
	bal, err := GetBalance(db, companyID, itemID)
	if err != nil {
		return nil, err
	}

	// Read company costing method.
	var company models.Company
	method := "moving_average"
	if err := db.Select("id", "inventory_costing_method").First(&company, companyID).Error; err == nil {
		if company.InventoryCostingMethod != "" {
			method = company.InventoryCostingMethod
		}
	}

	// Per-warehouse breakdown: query all balance rows keyed by warehouse_id.
	var whBals []models.InventoryBalance
	db.Preload("Warehouse").
		Where("company_id = ? AND item_id = ? AND warehouse_id IS NOT NULL", companyID, itemID).
		Find(&whBals)

	breakdown := make([]WarehouseBalance, 0, len(whBals))
	for _, wb := range whBals {
		if wb.Warehouse == nil {
			continue
		}
		breakdown = append(breakdown, WarehouseBalance{
			WarehouseID:   *wb.WarehouseID,
			WarehouseCode: wb.Warehouse.Code,
			WarehouseName: wb.Warehouse.Name,
			QtyOnHand:     wb.QuantityOnHand,
			AverageCost:   wb.AverageCost,
			Value:         wb.QuantityOnHand.Mul(wb.AverageCost).RoundBank(2),
		})
	}

	return &InventorySnapshot{
		ItemID:             itemID,
		QuantityOnHand:     bal.QuantityOnHand,
		AverageCost:        bal.AverageCost,
		InventoryValue:     bal.QuantityOnHand.Mul(bal.AverageCost).RoundBank(2),
		CostingMethod:      method,
		HasOpening:         HasOpening(db, companyID, itemID),
		WarehouseBreakdown: breakdown,
	}, nil
}

// ── Valuation rows (list page) ───────────────────────────────────────────────

// ItemValuation is a lightweight struct for the items list table.
type ItemValuation struct {
	QuantityOnHand string
	AverageCost    string
	InventoryValue string
}

// ListItemValuations returns valuation data for all inventory items in a company.
// keyed by item_id. Non-inventory items are not included.
func ListItemValuations(db *gorm.DB, companyID uint) map[uint]ItemValuation {
	var balances []models.InventoryBalance
	db.Where("company_id = ? AND location_type = ? AND location_ref = ?",
		companyID, models.LocationTypeInternal, "").
		Find(&balances)

	result := make(map[uint]ItemValuation, len(balances))
	for _, b := range balances {
		value := b.QuantityOnHand.Mul(b.AverageCost).RoundBank(2)
		result[b.ItemID] = ItemValuation{
			QuantityOnHand: b.QuantityOnHand.String(),
			AverageCost:    b.AverageCost.StringFixed(4),
			InventoryValue: value.StringFixed(2),
		}
	}
	return result
}

// ── Movement history ─────────────────────────────────────────────────────────

// MovementRow is a display-ready inventory movement for the ledger page.
// Phase D.0 slice 8: the JournalEntryID field was removed when the
// underlying column was dropped. Consumers that need the JE for a
// movement resolve it via SourceType + SourceID → business document →
// document.journal_entry_id.
type MovementRow struct {
	ID            uint
	Date          string
	MovementType  string
	MovementLabel string // human-friendly label
	SourceType    string
	SourceLabel   string // human-friendly label
	SourceID      *uint
	QuantityDelta string
	UnitCost      string
	TotalCost     string
	Note          string
	// Warehouse info (empty string = legacy movement, no warehouse routing)
	WarehouseCode string
	WarehouseName string
}

// ListMovements returns paginated movement rows for an item, newest first.
func ListMovements(db *gorm.DB, companyID, itemID uint, limit, offset int) ([]MovementRow, int64, error) {
	if limit <= 0 {
		limit = 50
	}

	baseCurrency := "BASE"
	var company models.Company
	if err := db.Select("base_currency_code").
		Where("id = ?", companyID).
		First(&company).Error; err == nil && company.BaseCurrencyCode != "" {
		baseCurrency = company.BaseCurrencyCode
	}

	var total int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Count(&total)

	var movs []models.InventoryMovement
	err := db.Preload("Warehouse").
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("movement_date DESC, id DESC").
		Limit(limit).Offset(offset).
		Find(&movs).Error
	if err != nil {
		return nil, 0, err
	}

	rows := make([]MovementRow, len(movs))
	for i, m := range movs {
		whCode, whName := "", ""
		if m.Warehouse != nil {
			whCode = m.Warehouse.Code
			whName = m.Warehouse.Name
		}
		rows[i] = MovementRow{
			ID:            m.ID,
			Date:          m.MovementDate.Format("2006-01-02"),
			MovementType:  string(m.MovementType),
			MovementLabel: movementTypeLabel(string(m.MovementType)),
			SourceType:    m.SourceType,
			SourceLabel:   sourceTypeLabel(m.SourceType),
			SourceID:      m.SourceID,
			QuantityDelta: m.QuantityDelta.String(),
			UnitCost:      formatMovementCost(m.UnitCost, m.UnitCostBase, m.CurrencyCode, baseCurrency),
			TotalCost:     formatMovementCost(m.TotalCost, movementTotalCostBase(m), m.CurrencyCode, baseCurrency),
			Note:          m.ReferenceNote,
			WarehouseCode: whCode,
			WarehouseName: whName,
		}
	}
	return rows, total, nil
}

// ── Label helpers ────────────────────────────────────────────────────────────

func movementTypeLabel(t string) string {
	switch t {
	case "opening":
		return "Opening"
	case "adjustment":
		return "Adjustment"
	case "purchase":
		return "Purchase"
	case "sale":
		return "Sale"
	default:
		return t
	}
}

func sourceTypeLabel(s string) string {
	switch s {
	case "opening":
		return "Opening Balance"
	case "adjustment":
		return "Manual Adjustment"
	case "invoice":
		return "Invoice"
	case "bill":
		return "Bill"
	case "invoice_reversal":
		return "Invoice Reversal"
	case "bill_reversal":
		return "Bill Reversal"
	default:
		if s == "" {
			return "—"
		}
		return s
	}
}

func formatOptDecimal(d *decimal.Decimal) string {
	if d == nil {
		return "—"
	}
	return d.StringFixed(2)
}

func movementTotalCostBase(m models.InventoryMovement) *decimal.Decimal {
	if m.UnitCostBase == nil {
		return nil
	}
	total := m.QuantityDelta.Abs().Mul(*m.UnitCostBase).RoundBank(2)
	return &total
}

func formatMovementCost(doc, base *decimal.Decimal, currencyCode, baseCurrencyCode string) string {
	if doc == nil {
		return "â€”"
	}
	out := doc.StringFixed(2)
	currencyCode = normalizeCurrencyCode(currencyCode)
	baseCurrencyCode = normalizeCurrencyCode(baseCurrencyCode)
	if currencyCode == "" {
		return out
	}
	out += " " + currencyCode
	if base != nil && baseCurrencyCode != "" && currencyCode != baseCurrencyCode && !doc.Equal(*base) {
		out += " (" + base.StringFixed(2) + " " + baseCurrencyCode + " base)"
	}
	return out
}
