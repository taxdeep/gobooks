// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// WarehousesVM is the view-model for the warehouse list page.
type WarehousesVM struct {
	HasCompany bool
	Warehouses []models.Warehouse
	Created    bool
}

// WarehouseDetailVM is the view-model for the warehouse create/edit page.
type WarehouseDetailVM struct {
	HasCompany bool
	Warehouse  models.Warehouse
	FormError  string
	Saved      bool
}

// WarehouseStockVM is the view-model for the warehouse stock detail page.
type WarehouseStockVM struct {
	HasCompany bool
	Report     *services.WarehouseStockReport
}
