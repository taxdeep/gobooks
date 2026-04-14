// 遵循project_guide.md
package pages

import (
	"gobooks/internal/models"
	"gobooks/internal/services"
)

// ── Stock report ──────────────────────────────────────────────────────────────

type StockReportVM struct {
	HasCompany bool
	Report     *services.StockReport
}

// ── Warehouse transfers ───────────────────────────────────────────────────────

type WarehouseTransfersVM struct {
	HasCompany bool
	Transfers  []models.WarehouseTransfer
	Warehouses []models.Warehouse
	Filter     string
	Created    bool
}

type WarehouseTransferDetailVM struct {
	HasCompany bool
	Transfer   models.WarehouseTransfer
	Warehouses []models.Warehouse
	Items      []models.ProductService
	FormError  string
	Saved      bool
	Posted     bool
	Cancelled  bool
}
