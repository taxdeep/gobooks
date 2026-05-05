package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
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
	customer := models.Customer{ID: 11, Name: "Smart Quote Customer", Email: "smart@example.com", CurrencyCode: "USD"}
	var sb strings.Builder
	vm := QuoteDetailVM{
		HasCompany:       true,
		BaseCurrencyCode: "CAD",
		Quote: models.Quote{
			Status:       models.QuoteStatusDraft,
			QuoteDate:    time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
			CurrencyCode: "USD",
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
		`data-base-currency="CAD"`,
		`data-initial-currency="USD"`,
		`data-lock-counterparty-currency="true"`,
		`data-counterparty-label="customer"`,
		`data-exchange-rate-date-offset-days="-1"`,
		`data-counterparty-currencies=`,
		`name="quote_date" value="2026-04-28" required @input.debounce.300ms="onDocumentDateChange()" @change="onDocumentDateChange()"`,
		`name="currency_code" value="USD" :value="currencyCode || baseCurrency"`,
		`name="exchange_rate"`,
		`x-text="currencyRateLeftLabel()"`,
		`x-text="baseCurrency"`,
		`Select a customer to load its currency.`,
		`<noscript><select name="customer_id"`,
		`name="customer_id"`,
		`— Select Customer —`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected quote editor HTML to contain %q", want)
		}
	}
	if strings.Contains(html, `x-init="var s=$el.querySelector('select')`) {
		t.Fatal("expected quote customer fallback select to be hidden in noscript, not Alpine-controlled")
	}
	if strings.Contains(html, `maxlength="3"`) {
		t.Fatal("expected quote currency to be locked to customer currency, not freeform text")
	}
}

func TestQuoteDraftFooterHasLifecycleActions(t *testing.T) {
	customer := models.Customer{ID: 11, Name: "Smart Quote Customer", CurrencyCode: "USD"}
	var sb strings.Builder
	vm := QuoteDetailVM{
		HasCompany:       true,
		BaseCurrencyCode: "CAD",
		Quote: models.Quote{
			ID:           5,
			QuoteNumber:  "QUO-0005",
			Status:       models.QuoteStatusDraft,
			QuoteDate:    time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
			CurrencyCode: "USD",
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
		`formaction="/quotes/5/cancel"`,
		`formaction="/quotes/5/send"`,
		`formaction="/quotes/save"`,
		"Cancel",
		"Mark as Sent",
		"Save",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected quote editor HTML to contain %q", want)
		}
	}
	for _, notWant := range []string{
		"Cancel Quote",
		"Save Quote",
	} {
		if strings.Contains(html, notWant) {
			t.Fatalf("expected quote editor HTML not to contain %q", notWant)
		}
	}
}
