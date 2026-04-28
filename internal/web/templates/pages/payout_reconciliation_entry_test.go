package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
)

func TestGatewayPayoutsList_RenderShowsReconciliationEntries(t *testing.T) {
	vm := GatewayPayoutsListVM{
		HasCompany: true,
		Payouts: []models.GatewayPayout{
			{
				ID:               42,
				ProviderPayoutID: "po_42",
				PayoutDate:       time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
				GrossAmount:      decimal.RequireFromString("120.00"),
				FeeAmount:        decimal.RequireFromString("5.00"),
				NetAmount:        decimal.RequireFromString("115.00"),
				CurrencyCode:     "CAD",
			},
		},
	}

	var buf bytes.Buffer
	if err := GatewayPayoutsList(vm).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render gateway payouts list: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "/settings/payment-gateways/payout-reconciliation") {
		t.Fatalf("expected reconciliation overview entry, body=%s", body)
	}
	if !strings.Contains(body, "/settings/payment-gateways/payouts/42/reconcile") {
		t.Fatalf("expected row-level reconcile entry, body=%s", body)
	}
}

func TestGatewayPayoutDetail_RenderShowsReconcileEntry(t *testing.T) {
	vm := GatewayPayoutDetailVM{
		HasCompany: true,
		Payout: &models.GatewayPayout{
			ID:               42,
			ProviderPayoutID: "po_42",
			PayoutDate:       time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
			GrossAmount:      decimal.RequireFromString("120.00"),
			FeeAmount:        decimal.RequireFromString("5.00"),
			NetAmount:        decimal.RequireFromString("115.00"),
			CurrencyCode:     "CAD",
		},
	}

	var buf bytes.Buffer
	if err := GatewayPayoutDetail(vm).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render gateway payout detail: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "/settings/payment-gateways/payouts/42/reconcile") {
		t.Fatalf("expected detail-level reconcile entry, body=%s", body)
	}
}
