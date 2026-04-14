// 遵循project_guide.md
package pages

import (
	"gobooks/internal/models"
	"gobooks/internal/services"
)

// ── ARWriteOff VMs ────────────────────────────────────────────────────────────

// WriteOffsVM is the view model for the AR Write-Offs list page.
type WriteOffsVM struct {
	HasCompany     bool
	WriteOffs      []models.ARWriteOff
	Customers      []models.Customer
	FilterStatus   string
	FilterCustomer string
	Created        bool
}

// WriteOffDetailVM is the view model for a single ARWriteOff detail / edit page.
type WriteOffDetailVM struct {
	HasCompany bool
	WriteOff   models.ARWriteOff
	Customers  []models.Customer
	Accounts   []models.Account // all active accounts for AR + expense pickers
	Invoices   []models.Invoice // open invoices for same customer
	FormError  string
	Saved      bool
	Posted     bool
	Voided     bool
	Reversed   bool
}

// ── CustomerStatement VM ──────────────────────────────────────────────────────

// CustomerStatementVM is the view model for the Customer Statement page.
type CustomerStatementVM struct {
	HasCompany bool
	Statement  *services.CustomerStatement // nil = not yet queried
	Customers  []models.Customer
	CustomerID string // form value
	FromDate   string // form value
	ToDate     string // form value
	FormError  string
}
