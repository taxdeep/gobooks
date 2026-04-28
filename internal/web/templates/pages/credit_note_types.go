// 遵循project_guide.md
package pages

import "balanciz/internal/models"

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
	// InitialLinesJSON — JSON array consumed by Alpine's x-data
	// initializer to pre-fill lines. Populated when navigating
	// from /credit-notes/new?invoice_id=X so stock-item CNs
	// automatically inherit the invoice's lines (including
	// OriginalInvoiceLineID — the IN.5 cost-trace key, without
	// which stock-line CN post fails with
	// ErrCreditNoteStockItemRequiresOriginalLine). Blank for
	// standalone CNs; the Alpine init falls back to an empty
	// row in that case.
	InitialLinesJSON string
	// InvoiceNumber is shown in a "from Invoice X" breadcrumb
	// when InvoiceID is set; decorative only.
	InvoiceNumber string
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
