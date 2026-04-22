// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// CreditNoteFormVM is the view-model for the /credit-notes/new form.
type CreditNoteFormVM struct {
	HasCompany bool
	CompanyID  uint
	CustomerID uint
	InvoiceID  uint
	Customers  []models.Customer
	Accounts   []models.Account // revenue + cost_of_sales accounts
	TaxCodes   []models.TaxCode
	FormError  string
	Reasons    []models.CreditNoteReason
}

// creditNoteHasStockLine reports whether the CN carries at least one
// stock-item line — i.e. whether a matching Return Receipt could be
// produced from it (Q4 shortcut visibility). Lines must be preloaded
// with ProductService (GetCreditNote does this).
func creditNoteHasStockLine(cn models.CreditNote) bool {
	for _, ln := range cn.Lines {
		if ln.ProductService != nil && ln.ProductService.IsStockItem {
			return true
		}
	}
	return false
}
