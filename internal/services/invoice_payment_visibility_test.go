package services

import (
	"testing"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

func TestBuildInvoicePaymentVisibility_DerivesStateAndAmounts(t *testing.T) {
	tests := []struct {
		name      string
		invoice   models.Invoice
		wantState InvoicePaymentState
		wantLabel string
		wantPaid  string
		wantDue   string
	}{
		{
			name: "unpaid",
			invoice: models.Invoice{
				Amount:       decimal.RequireFromString("125.00"),
				BalanceDue:   decimal.RequireFromString("125.00"),
				CurrencyCode: "CAD",
			},
			wantState: InvoicePaymentStateUnpaid,
			wantLabel: "Unpaid",
			wantPaid:  "0.00",
			wantDue:   "125.00",
		},
		{
			name: "partially paid",
			invoice: models.Invoice{
				Amount:       decimal.RequireFromString("125.00"),
				BalanceDue:   decimal.RequireFromString("40.00"),
				CurrencyCode: "CAD",
			},
			wantState: InvoicePaymentStatePartiallyPaid,
			wantLabel: "Partially Paid",
			wantPaid:  "85.00",
			wantDue:   "40.00",
		},
		{
			name: "paid",
			invoice: models.Invoice{
				Amount:       decimal.RequireFromString("125.00"),
				BalanceDue:   decimal.Zero,
				CurrencyCode: "CAD",
			},
			wantState: InvoicePaymentStatePaid,
			wantLabel: "Paid",
			wantPaid:  "125.00",
			wantDue:   "0.00",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildInvoicePaymentVisibility(tc.invoice)
			if got.State != tc.wantState {
				t.Fatalf("expected state %q, got %q", tc.wantState, got.State)
			}
			if got.Label != tc.wantLabel {
				t.Fatalf("expected label %q, got %q", tc.wantLabel, got.Label)
			}
			if got.PaidAmount.StringFixed(2) != tc.wantPaid {
				t.Fatalf("expected paid amount %s, got %s", tc.wantPaid, got.PaidAmount.StringFixed(2))
			}
			if got.BalanceDue.StringFixed(2) != tc.wantDue {
				t.Fatalf("expected balance due %s, got %s", tc.wantDue, got.BalanceDue.StringFixed(2))
			}
			if got.Currency != "CAD" {
				t.Fatalf("expected currency CAD, got %q", got.Currency)
			}
		})
	}
}
