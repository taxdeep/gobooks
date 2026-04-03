package services

import (
	"time"

	"gobooks/internal/models"
)

type InvoiceAgingBucket string

const (
	InvoiceAgingBucketCurrent InvoiceAgingBucket = "current"
	InvoiceAgingBucketOverdue InvoiceAgingBucket = "overdue"
)

func IsInvoiceOverdue(inv models.Invoice) bool {
	return IsInvoiceOverdueAsOf(inv, time.Now())
}

func IsInvoiceOverdueAsOf(inv models.Invoice, asOf time.Time) bool {
	if inv.DueDate == nil {
		return false
	}
	switch inv.Status {
	case models.InvoiceStatusIssued, models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid:
	default:
		return false
	}
	return dateOnly(*inv.DueDate).Before(dateOnly(asOf))
}

func EffectiveInvoiceStatus(inv models.Invoice) models.InvoiceStatus {
	return EffectiveInvoiceStatusAsOf(inv, time.Now())
}

func EffectiveInvoiceStatusAsOf(inv models.Invoice, asOf time.Time) models.InvoiceStatus {
	if inv.Status == models.InvoiceStatusOverdue || IsInvoiceOverdueAsOf(inv, asOf) {
		return models.InvoiceStatusOverdue
	}
	return inv.Status
}

// InvoiceAgingBucketForReport is intentionally kept even though the current
// repo has no dedicated AR aging report route/page/service yet. List/detail
// overdue display already flows through the overdue/effective-status helpers
// above; when an AR aging report is added later, it should reuse this bucket
// helper instead of re-implementing past-due rules in a report-specific layer.
func InvoiceAgingBucketForReport(inv models.Invoice) InvoiceAgingBucket {
	return InvoiceAgingBucketForReportAsOf(inv, time.Now())
}

// InvoiceAgingBucketForReportAsOf is the future report-truth hook for AR aging
// buckets. There is no direct consumer today outside tests, but future aging
// report code should call this helper so report bucketing stays aligned with
// the same overdue truth used by EffectiveInvoiceStatusAsOf.
func InvoiceAgingBucketForReportAsOf(inv models.Invoice, asOf time.Time) InvoiceAgingBucket {
	if EffectiveInvoiceStatusAsOf(inv, asOf) == models.InvoiceStatusOverdue {
		return InvoiceAgingBucketOverdue
	}
	return InvoiceAgingBucketCurrent
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
