package services

import (
	"testing"
	"time"

	"gobooks/internal/models"
)

func TestIsInvoiceOverdueAsOf(t *testing.T) {
	asOf := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	tomorrow := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		status models.InvoiceStatus
		due    *time.Time
		want   bool
	}{
		{name: "issued past due", status: models.InvoiceStatusIssued, due: &yesterday, want: true},
		{name: "sent past due", status: models.InvoiceStatusSent, due: &yesterday, want: true},
		{name: "partially paid past due", status: models.InvoiceStatusPartiallyPaid, due: &yesterday, want: true},
		{name: "not yet due", status: models.InvoiceStatusIssued, due: &tomorrow, want: false},
		{name: "due date missing", status: models.InvoiceStatusIssued, due: nil, want: false},
		{name: "paid excluded", status: models.InvoiceStatusPaid, due: &yesterday, want: false},
		{name: "voided excluded", status: models.InvoiceStatusVoided, due: &yesterday, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv := models.Invoice{Status: tc.status, DueDate: tc.due}
			if got := IsInvoiceOverdueAsOf(inv, asOf); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestInvoiceAgingBucketForReportAsOf_UsesComputedOverdue(t *testing.T) {
	asOf := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	tomorrow := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)

	overdue := models.Invoice{
		Status:  models.InvoiceStatusIssued,
		DueDate: &yesterday,
	}
	if got := InvoiceAgingBucketForReportAsOf(overdue, asOf); got != InvoiceAgingBucketOverdue {
		t.Fatalf("expected overdue bucket, got %q", got)
	}

	current := models.Invoice{
		Status:  models.InvoiceStatusSent,
		DueDate: &tomorrow,
	}
	if got := InvoiceAgingBucketForReportAsOf(current, asOf); got != InvoiceAgingBucketCurrent {
		t.Fatalf("expected current bucket, got %q", got)
	}
}

func TestEffectiveInvoiceStatusAsOf_ComputesOverdueWithoutDBWriteback(t *testing.T) {
	asOf := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	inv := models.Invoice{
		Status:  models.InvoiceStatusIssued,
		DueDate: &yesterday,
	}
	if got := EffectiveInvoiceStatusAsOf(inv, asOf); got != models.InvoiceStatusOverdue {
		t.Fatalf("expected effective overdue status, got %q", got)
	}
	if inv.Status != models.InvoiceStatusIssued {
		t.Fatalf("helper should not mutate stored status, got %q", inv.Status)
	}
}
