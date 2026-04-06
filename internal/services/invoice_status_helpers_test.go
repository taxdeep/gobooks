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
	thirtyFiveDaysAgo := time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC)
	sixtyFiveDaysAgo := time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC)
	ninetyFiveDaysAgo := time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC)
	tomorrow := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)

	overdue1To30 := models.Invoice{
		Status:  models.InvoiceStatusIssued,
		DueDate: &yesterday,
	}
	if got := InvoiceAgingBucketForReportAsOf(overdue1To30, asOf); got != InvoiceAgingBucket1To30 {
		t.Fatalf("expected 1-30 bucket, got %q", got)
	}

	overdue31To60 := models.Invoice{
		Status:  models.InvoiceStatusSent,
		DueDate: &thirtyFiveDaysAgo,
	}
	if got := InvoiceAgingBucketForReportAsOf(overdue31To60, asOf); got != InvoiceAgingBucket31To60 {
		t.Fatalf("expected 31-60 bucket, got %q", got)
	}

	overdue61To90 := models.Invoice{
		Status:  models.InvoiceStatusPartiallyPaid,
		DueDate: &sixtyFiveDaysAgo,
	}
	if got := InvoiceAgingBucketForReportAsOf(overdue61To90, asOf); got != InvoiceAgingBucket61To90 {
		t.Fatalf("expected 61-90 bucket, got %q", got)
	}

	overdue91Plus := models.Invoice{
		Status:  models.InvoiceStatusIssued,
		DueDate: &ninetyFiveDaysAgo,
	}
	if got := InvoiceAgingBucketForReportAsOf(overdue91Plus, asOf); got != InvoiceAgingBucket91Plus {
		t.Fatalf("expected 91+ bucket, got %q", got)
	}

	current := models.Invoice{
		Status:  models.InvoiceStatusSent,
		DueDate: &tomorrow,
	}
	if got := InvoiceAgingBucketForReportAsOf(current, asOf); got != InvoiceAgingBucketCurrent {
		t.Fatalf("expected current bucket, got %q", got)
	}

	noDueDate := models.Invoice{Status: models.InvoiceStatusOverdue}
	if got := InvoiceAgingBucketForReportAsOf(noDueDate, asOf); got != InvoiceAgingBucketCurrent {
		t.Fatalf("expected missing due date to stay current bucket, got %q", got)
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
