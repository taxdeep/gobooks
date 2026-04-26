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
