// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// ProductServicesVM is the view-model for the Products & Services page.
type ProductServicesVM struct {
	HasCompany bool

	// Dropdown data
	RevenueAccounts     []models.Account // root_account_type = 'revenue'
	OtherChargeAccounts []models.Account // root_account_type IN ('cost_of_sales', 'expense') — for Other Charge items
	COGSAccounts        []models.Account // root_account_type = 'cost_of_sales'
	InventoryAccounts   []models.Account // detail_account_type = 'inventory' (asset)
	TaxCodes         []models.TaxCode

	// Component picker dropdown (inventory items only, for bundle component selection)
	InventoryItems []models.ProductService

	// Warehouses available for opening balance / adjustment routing.
	Warehouses []models.Warehouse

	// Form fields
	Name               string
	SKU                string
	Type               string
	StructureType      string // single | bundle
	Description        string
	DefaultPrice       string
	PurchasePrice      string
	RevenueAccountID   string
	COGSAccountID      string
	InventoryAccountID string
	DefaultTaxCodeID   string

	// UOM display fields (Phase U1) — populated from the item being
	// edited. Used by psUOMSection to render current values + the
	// inline mini-form. Default values for create-mode are EA / 1.
	StockUOM          string
	SellUOM           string
	SellUOMFactor     string
	PurchaseUOM       string
	PurchaseUOMFactor string
	// UOMHasStock is true when the item has on-hand > 0; the templ uses
	// this to disable the StockUOM change link (parallels the
	// TrackingMode rule).
	UOMHasStock bool
	// UOMOK / UOMError are flash flags from the save round-trip.
	UOMOK    bool
	UOMError string

	// Bundle components (for edit mode)
	Components     []BundleComponentRow
	ComponentError string

	// Field-level errors
	NameError              string
	TypeError              string
	DefaultPriceError      string
	RevenueAccountIDError  string
	COGSAccountIDError     string
	InventoryAccountIDError string

	// Form-level error
	FormError string

	// Success banners
	Created    bool
	Updated    bool
	InactiveOK bool

	// Inventory actions
	OpeningOK    bool // ?opening=1
	AdjustmentOK bool // ?adjustment=1

	// DrawerMode is "create", "edit", or empty (drawer closed on first paint).
	DrawerMode string
	// EditingID is set when DrawerMode == "edit".
	EditingID uint

	// DrawerOpen opens the slide-over when true.
	DrawerOpen bool

	// Data to render the table (with balance info for inventory items)
	Items      []models.ProductService
	Balances   map[uint]string // item_id → qty_on_hand display string (legacy compat)
	Valuations map[uint]ItemValuationVM // item_id → full valuation data

	// Filter state — echoed back into the filter bar so the URL fully
	// describes the result set.
	FilterQ      string // substring match against name + sku
	FilterType   string // "" (all), "service", "product", "other_charge", "bundle"
	FilterStatus string // "active" (default), "inactive", "all"
	// FilterStockLevel narrows the inventory list:
	//   "" / "any" → no stock filter (default)
	//   "in_stock" → qty_on_hand > 0 (implies type = inventory)
	//   "out_of_stock" → qty_on_hand <= 0 OR no balance row (implies type = inventory)
	// "Low stock" is intentionally absent — the schema has no reorder_point
	// column yet; surface honest options rather than guess a hardcoded
	// threshold.
	FilterStockLevel string
	// InactiveItemCount is the unfiltered count of deactivated items, used
	// in the Status select option label so the operator sees how many are
	// hidden by the default Active-only filter.
	InactiveItemCount int
}

// ItemValuationVM holds display-ready valuation data for the items list table.
type ItemValuationVM struct {
	QuantityOnHand string
	AverageCost    string
	InventoryValue string
}

// BundleComponentRow holds one component line for the bundle editor form.
type BundleComponentRow struct {
	ComponentItemID string
	Quantity        string
	ComponentName   string // display-only (for repopulating on validation error)
}
