package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

func TestQuoteReadOnlyLineItemsShowProductService(t *testing.T) {
	item := &models.ProductService{ID: 7, Name: "Implementation Service"}
	var sb strings.Builder
	vm := QuoteDetailVM{
		HasCompany: true,
		Quote: models.Quote{
			ID:           1,
			QuoteNumber:  "QUO-0001",
			Status:       models.QuoteStatusSent,
			QuoteDate:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			CurrencyCode: "CAD",
			Customer:     models.Customer{Name: "AR TESTING"},
			Lines: []models.QuoteLine{
				{
					ProductServiceID: &item.ID,
					ProductService:   item,
					Description:      "Implementation work",
					Quantity:         decimal.NewFromInt(2),
					UnitPrice:        decimal.NewFromInt(150),
					LineNet:          decimal.NewFromInt(300),
					LineTotal:        decimal.NewFromInt(300),
				},
			},
		},
	}

	if err := QuoteDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render quote detail: %v", err)
	}
	html := sb.String()
	for _, want := range []string{"Item / Description", "Implementation Service", "Implementation work"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected quote detail HTML to contain %q", want)
		}
	}
}

func TestQuoteEditorCustomerUsesSmartPicker(t *testing.T) {
	customer := models.Customer{ID: 11, Name: "Smart Quote Customer", Email: "smart@example.com"}
	var sb strings.Builder
	vm := QuoteDetailVM{
		HasCompany: true,
		Quote: models.Quote{
			Status:       models.QuoteStatusDraft,
			QuoteDate:    time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
			CurrencyCode: "CAD",
			CustomerID:   customer.ID,
			Customer:     customer,
		},
		Customers: []models.Customer{customer},
	}

	if err := QuoteDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render quote editor: %v", err)
	}
	html := sb.String()
	for _, want := range []string{
		`data-entity="customer"`,
		`data-context="quote.customer_picker"`,
		`data-field-name="customer_id"`,
		`data-value="11"`,
		`data-selected-label="Smart Quote Customer"`,
		`name="customer_id"`,
		`— Select Customer —`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected quote editor HTML to contain %q", want)
		}
	}
}
