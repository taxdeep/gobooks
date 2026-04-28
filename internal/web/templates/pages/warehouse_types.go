// 遵循project_guide.md
package pages

import "balanciz/internal/models"

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
