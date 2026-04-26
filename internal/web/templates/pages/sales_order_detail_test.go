// 遵循project_guide.md
package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

func TestSalesOrderDraftEditorDoesNotNestStatusForms(t *testing.T) {
	var sb strings.Builder
	vm := SalesOrderDetailVM{
		HasCompany: true,
		Order: models.SalesOrder{
			ID:           3,
			OrderNumber:  "SO-0003",
			Status:       models.SalesOrderStatusDraft,
			OrderDate:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			CurrencyCode: "CAD",
			Lines: []models.SalesOrderLine{
				{
					Description: "Computer 1",
					Quantity:    decimal.NewFromInt(10),
					UnitPrice:   decimal.NewFromInt(500),
					LineTotal:   decimal.NewFromInt(5000),
				},
			},
		},
	}

	if err := SalesOrderDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render sales order detail: %v", err)
	}
	html := sb.String()

	if strings.Contains(html, `<form method="post" action="/sales-orders/3/confirm"`) {
		t.Fatal("draft editor must not render confirm as a nested form inside the save form")
	}
	if strings.Contains(html, `<form method="post" action="/sales-orders/3/cancel"`) {
		t.Fatal("draft editor must not render cancel as a nested form inside the save form")
	}
	if !strings.Contains(html, `formaction="/sales-orders/3/confirm"`) {
		t.Fatal("draft editor confirm action should be a submit button with formaction")
	}
	if !strings.Contains(html, `formaction="/sales-orders/3/cancel"`) {
		t.Fatal("draft editor cancel action should be a submit button with formaction")
	}
	if !strings.Contains(html, `href="/sales-orders"`) || !strings.Contains(html, `>Back</a>`) {
		t.Fatal("draft editor footer should render a Back link")
	}
	if !strings.Contains(html, `>Save Order</button>`) {
		t.Fatal("draft editor footer should render Save Order in the same action bar")
	}
}

func TestSalesOrderReadOnlyLineItemsShowProductService(t *testing.T) {
	item := &models.ProductService{ID: 11, Name: "Computer 1"}
	var sb strings.Builder
	vm := SalesOrderDetailVM{
		HasCompany: true,
		Order: models.SalesOrder{
			ID:           3,
			OrderNumber:  "SO-0003",
			Status:       models.SalesOrderStatusConfirmed,
			OrderDate:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			CurrencyCode: "CAD",
			Customer:     models.Customer{Name: "AR TESTING"},
			Lines: []models.SalesOrderLine{
				{
					ProductServiceID: &item.ID,
					ProductService:   item,
					Description:      "Computer workstation",
					Quantity:         decimal.NewFromInt(10),
					UnitPrice:        decimal.NewFromInt(500),
					LineNet:          decimal.NewFromInt(5000),
					LineTotal:        decimal.NewFromInt(5000),
				},
			},
		},
	}

	if err := SalesOrderDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render sales order detail: %v", err)
	}
	html := sb.String()
	for _, want := range []string{"Item / Description", "Computer 1", "Computer workstation"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected sales order detail HTML to contain %q", want)
		}
	}
}
