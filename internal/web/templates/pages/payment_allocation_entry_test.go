package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"balanciz/internal/services"
)

func TestPaymentTransactions_RenderShowsAllocateEntry(t *testing.T) {
	postedJEID := uint(99)
	vm := PaymentTransactionsVM{
		HasCompany: true,
		Transactions: []models.PaymentTransaction{
			{
				ID:                   42,
				GatewayAccount:       models.PaymentGatewayAccount{DisplayName: "Manual"},
				TransactionType:      models.TxnTypeCharge,
				Amount:               decimal.RequireFromString("125.00"),
				CurrencyCode:         "CAD",
				PostedJournalEntryID: &postedJEID,
			},
		},
		TxnStates: map[uint]services.PaymentActionState{
			42: {
				IsPosted:         true,
				PostedJEID:       postedJEID,
				CanMultiAllocate: true,
			},
		},
	}

	var buf bytes.Buffer
	if err := PaymentTransactions(vm).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render payment transactions: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "/settings/payment-gateways/transactions/42/allocate") {
		t.Fatalf("expected allocate link in payment transactions page, body=%s", body)
	}
}

func TestPaymentTransactions_RenderIncludesTxnAnchors(t *testing.T) {
	vm := PaymentTransactionsVM{
		HasCompany: true,
		Transactions: []models.PaymentTransaction{
			{
				ID:              42,
				GatewayAccount:  models.PaymentGatewayAccount{DisplayName: "Manual"},
				TransactionType: models.TxnTypeCharge,
				Amount:          decimal.RequireFromString("125.00"),
				CurrencyCode:    "CAD",
			},
		},
	}

	var buf bytes.Buffer
	if err := PaymentTransactions(vm).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render payment transactions: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, `id="txn-42"`) {
		t.Fatalf("expected stable transaction anchor in payment transactions page, body=%s", body)
	}
}

func TestCustomerCredits_RenderShowsAllocateEntry(t *testing.T) {
	vm := CustomerCreditsVM{
		HasCompany: true,
		Customer: models.Customer{
			ID:   7,
			Name: "Acme",
		},
		Credits: []models.CustomerCredit{
			{
				ID:              13,
				Status:          models.CustomerCreditActive,
				OriginalAmount:  decimal.RequireFromString("200.00"),
				RemainingAmount: decimal.RequireFromString("150.00"),
				SourceType:      models.CreditSourceOverpayment,
			},
		},
	}

	var buf bytes.Buffer
	if err := CustomerCredits(vm).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render customer credits: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "/customers/7/credits/13/allocate") {
		t.Fatalf("expected allocate link in customer credits page, body=%s", body)
	}
}
