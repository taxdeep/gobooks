// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// SalesTxVM is the view-model for the unified Sales Transactions page.
// Merges invoices, quotes, SOs, customer receipts, credit notes, and AR
// returns into a single chronological feed with QuickBooks-style KPI
// strip at the top.
type SalesTxVM struct {
	HasCompany bool

	// KPI strip — five segments across the top of the page.
	KPI services.SalesTxKPI

	// Filter echoes — repopulate the filter widgets after submit.
	TypeFilter     string
	DateFilter     string // preset token: "all", "today", "this_month", etc.
	DateFrom       string // "2006-01-02" when Custom
	DateTo         string
	StatusFilter   string
	DeliveryFilter string
	CustomerID     uint
	CustomerLabel  string
	Search         string
	SortBy         string
	SortDir        string

	// Dropdown sources.
	Customers []models.Customer

	// Rows for the current page.
	Rows []services.SalesTxRow

	// Pagination.
	Page       int
	PageSize   int
	Total      int
	TotalPages int

	// SelectedTotal — when the UI passes selected row IDs back via
	// query string, the handler can fill this for the footer. MVP leaves it 0.
	SelectedTotal string
}
