// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// ChannelAccountsVM is the view-model for the channel accounts settings page.
type ChannelAccountsVM struct {
	HasCompany bool
	Accounts   []models.SalesChannelAccount
	FormError  string
	Created    bool
	Updated    bool
	Deleted    bool
}

// ChannelMappingsVM is the view-model for the item-channel mappings page.
type ChannelMappingsVM struct {
	HasCompany bool
	Mappings   []models.ItemChannelMapping
	Accounts   []models.SalesChannelAccount
	Items      []models.ProductService
	FormError  string
	Created    bool
}

// ChannelOrdersVM is the view-model for the channel orders list page.
type ChannelOrdersVM struct {
	HasCompany bool
	Orders     []services.ChannelOrderSummary
	Accounts   []models.SalesChannelAccount
	FormError  string
	Created    bool
}

// ChannelOrderDetailVM is the view-model for a single channel order detail page.
type ChannelOrderDetailVM struct {
	HasCompany bool
	Order      models.ChannelOrder
	Lines      []models.ChannelOrderLine

	// Conversion state
	IsConvertible      bool
	ConvertibleError   string // non-empty = why it can't be converted
	ConvertedInvoiceID *uint  // non-nil = already converted
	Customers          []models.Customer
	Converted          bool // ?converted=1 success banner

	// Form repopulation
	SelectedCustomerID string
	InvoiceNumber      string
	FormError          string
}
