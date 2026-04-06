package services

import (
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

type InvoicePaymentState string

const (
	InvoicePaymentStateUnpaid        InvoicePaymentState = "unpaid"
	InvoicePaymentStatePartiallyPaid InvoicePaymentState = "partially_paid"
	InvoicePaymentStatePaid          InvoicePaymentState = "paid"
)

// InvoicePaymentVisibility is an operational read model derived from the
// current invoice truth. It intentionally reuses invoice amount/balance_due
// instead of inventing a parallel payment state machine.
type InvoicePaymentVisibility struct {
	State      InvoicePaymentState
	Label      string
	Total      decimal.Decimal
	BalanceDue decimal.Decimal
	PaidAmount decimal.Decimal
	Currency   string
}

func BuildInvoicePaymentVisibility(inv models.Invoice) InvoicePaymentVisibility {
	total := inv.Amount
	balanceDue := inv.BalanceDue
	if balanceDue.IsNegative() {
		balanceDue = decimal.Zero
	}
	if balanceDue.GreaterThan(total) {
		balanceDue = total
	}

	paidAmount := total.Sub(balanceDue)
	if paidAmount.IsNegative() {
		paidAmount = decimal.Zero
	}
	if paidAmount.GreaterThan(total) {
		paidAmount = total
	}

	state := InvoicePaymentStateUnpaid
	label := "Unpaid"
	switch {
	case total.LessThanOrEqual(decimal.Zero), balanceDue.LessThanOrEqual(decimal.Zero):
		state = InvoicePaymentStatePaid
		label = "Paid"
	case paidAmount.GreaterThan(decimal.Zero):
		state = InvoicePaymentStatePartiallyPaid
		label = "Partially Paid"
	}

	return InvoicePaymentVisibility{
		State:      state,
		Label:      label,
		Total:      total,
		BalanceDue: balanceDue,
		PaidAmount: paidAmount,
		Currency:   inv.CurrencyCode,
	}
}
