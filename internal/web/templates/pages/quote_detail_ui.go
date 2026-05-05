// 遵循project_guide.md
package pages

import (
	"encoding/json"
	"strings"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/ui"
)

// quoteShellVM maps QuoteDetailVM into the shared DocEditorShell wrapper
// used by the migrated Quote editor (Phase 4 / 0.0.14 UI line).
func quoteShellVM(vm QuoteDetailVM) ui.DocEditorShellVM {
	title := "New Quote"
	subtitle := "Create a new quote for a customer."
	if vm.Quote.ID != 0 {
		title = "Quote " + vm.Quote.QuoteNumber
		subtitle = "View and manage this quote."
	}
	return ui.DocEditorShellVM{
		Title:     title,
		Subtitle:  subtitle,
		BackURL:   "/quotes",
		BackLabel: "Back to Quotes",
		FormError: vm.FormError,
	}
}

// quoteFooterVM is the sticky bottom action bar for the Quote editor.
// Single Save button — Quote has no review-mode toggle (lifecycle moves
// happen via the action-buttons row below the form).
func quoteFooterVM(vm QuoteDetailVM) ui.DocEditorFooterVM {
	return ui.DocEditorFooterVM{
		Cancel: &ui.DocEditorFooterLink{
			Label: "Cancel",
			Href:  "/quotes",
		},
		Buttons: []ui.DocEditorFooterButton{
			{Label: "Save Quote", Variant: ui.FooterBtnPrimary, Type: "submit"},
		},
	}
}

// quoteProductsJSON serialises the product/service catalogue for the
// editor's Alpine factory (auto-fills description / price / tax on item pick).
func quoteProductsJSON(products []models.ProductService) string {
	type row struct {
		ID               uint   `json:"id"`
		Name             string `json:"name"`
		Description      string `json:"description"`
		DefaultPrice     string `json:"default_price"`
		DefaultTaxCodeID *uint  `json:"default_tax_code_id"`
	}
	out := make([]row, 0, len(products))
	for _, p := range products {
		out = append(out, row{
			ID:               p.ID,
			Name:             p.Name,
			Description:      p.Description,
			DefaultPrice:     p.DefaultPrice.StringFixed(2),
			DefaultTaxCodeID: p.DefaultTaxCodeID,
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// quoteTaxCodesJSON serialises tax codes for client-side total computation.
func quoteTaxCodesJSON(codes []models.TaxCode) string {
	type row struct {
		ID   uint   `json:"id"`
		Code string `json:"code"`
		Rate string `json:"rate"`
	}
	out := make([]row, 0, len(codes))
	for _, tc := range codes {
		out = append(out, row{ID: tc.ID, Code: tc.Code, Rate: tc.Rate.String()})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func quoteCustomerCurrenciesJSON(vm QuoteDetailVM) string {
	out := make(map[string]string, len(vm.Customers))
	for _, customer := range vm.Customers {
		out[Uitoa(customer.ID)] = quoteCustomerCurrency(customer, vm)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func quoteInitialCurrency(vm QuoteDetailVM) string {
	if code := strings.ToUpper(strings.TrimSpace(vm.Quote.CurrencyCode)); code != "" {
		return code
	}
	for _, customer := range vm.Customers {
		if customer.ID == vm.Quote.CustomerID {
			return quoteCustomerCurrency(customer, vm)
		}
	}
	return quoteBaseCurrency(vm)
}

func quoteCustomerCurrency(customer models.Customer, vm QuoteDetailVM) string {
	if code := strings.ToUpper(strings.TrimSpace(customer.CurrencyCode)); code != "" {
		return code
	}
	return quoteBaseCurrency(vm)
}

func quoteBaseCurrency(vm QuoteDetailVM) string {
	if code := strings.ToUpper(strings.TrimSpace(vm.BaseCurrencyCode)); code != "" {
		return code
	}
	return "CAD"
}

// quoteInitialLinesJSON converts existing QuoteLines into the shape the
// Alpine line-items factory expects on edit-page hydration.
func quoteInitialLinesJSON(lines []models.QuoteLine) string {
	type row struct {
		ProductServiceID    string `json:"product_service_id"`
		ProductServiceLabel string `json:"product_service_label"`
		Description         string `json:"description"`
		Qty                 string `json:"qty"`
		UnitPrice           string `json:"unit_price"`
		TaxCodeID           string `json:"tax_code_id"`
		LineTotal           string `json:"line_total"`
	}
	out := make([]row, 0, len(lines))
	for _, l := range lines {
		r := row{
			Description: l.Description,
			Qty:         l.Quantity.StringFixed(2),
			UnitPrice:   l.UnitPrice.StringFixed(2),
			LineTotal:   l.LineTotal.StringFixed(2),
		}
		if l.ProductServiceID != nil {
			r.ProductServiceID = Uitoa(*l.ProductServiceID)
			if l.ProductService != nil {
				r.ProductServiceLabel = l.ProductService.Name
			}
		}
		if l.TaxCodeID != nil {
			r.TaxCodeID = Uitoa(*l.TaxCodeID)
		}
		out = append(out, r)
	}
	b, _ := json.Marshal(out)
	return string(b)
}
