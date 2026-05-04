package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

func TestPurchaseOrderEditorVendorCurrencyIsRenderedForSync(t *testing.T) {
	vendor := models.Vendor{ID: 7, Name: "USD Vendor", CurrencyCode: "USD", IsActive: true}
	vm := PurchaseOrderDetailVM{
		HasCompany:       true,
		BaseCurrencyCode: "CAD",
		PurchaseOrder: models.PurchaseOrder{
			Status:       models.POStatusDraft,
			PODate:       time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
			VendorID:     vendor.ID,
			ExchangeRate: decimal.NewFromInt(1),
		},
		Vendors: []models.Vendor{vendor},
	}

	var sb strings.Builder
	if err := PurchaseOrderDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render purchase order detail: %v", err)
	}
	html := sb.String()
	for _, want := range []string{
		`data-base-currency="CAD"`,
		`data-initial-currency="USD"`,
		`data-counterparty-currencies=`,
		`value="7" data-currency="USD" selected`,
		`name="currency_code" value="USD" :value="currencyCode || baseCurrency"`,
		`x-text="currencyRateLeftLabel()"`,
		`&times;`,
		`pr-10`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected purchase order editor HTML to contain %q", want)
		}
	}
}
