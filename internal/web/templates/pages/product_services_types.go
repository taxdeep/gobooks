// 遵循project_guide.md
package pages

import "gobooks/internal/models"

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
