// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// InventoryLedgerVM is the view-model for the inventory ledger page.
type InventoryLedgerVM struct {
	HasCompany bool

	Item     models.ProductService
	IsBundle bool
	Snapshot services.InventorySnapshot

	// Account display strings (code · name)
	RevenueAccountLabel   string
	COGSAccountLabel      string
	InventoryAccountLabel string

	// Bundle components (read-only display for bundle items)
	Components []BundleComponentDisplay

	// Movement rows
	Movements      []services.MovementRow
	TotalMovements int64
	Page           int
	PageSize       int
}

// BundleComponentDisplay holds display-ready data for a bundle component in the ledger.
type BundleComponentDisplay struct {
	Name     string
	SKU      string
	Quantity string
	IsActive bool
}
