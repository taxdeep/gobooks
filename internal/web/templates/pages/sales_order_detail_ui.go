// 遵循project_guide.md
package pages

import (
	"encoding/json"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/ui"
)

// soShellVM maps SalesOrderDetailVM into the shared DocEditorShell wrapper
// used by the migrated Sales Order editor.
func soShellVM(vm SalesOrderDetailVM) ui.DocEditorShellVM {
	title := "New Sales Order"
	subtitle := "Create a new sales order for a customer."
	if vm.Order.ID != 0 {
		title = "Sales Order " + vm.Order.OrderNumber
		subtitle = "View and manage this sales order."
	}
	return ui.DocEditorShellVM{
		Title:     title,
		Subtitle:  subtitle,
		BackURL:   "/sales-orders",
		BackLabel: "Back to Sales Orders",
		FormError: vm.FormError,
	}
}

// soProductsJSON mirrors quoteProductsJSON — same shape, kept separate so
// either editor can extend its product-data shape independently.
func soProductsJSON(products []models.ProductService) string {
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

func soTaxCodesJSON(codes []models.TaxCode) string {
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

func soInitialLinesJSON(lines []models.SalesOrderLine) string {
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
