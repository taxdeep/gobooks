package services

import (
	"time"

	"gobooks/internal/models"
)

type InvoiceAgingBucket string

const (
	InvoiceAgingBucketCurrent InvoiceAgingBucket = "current"
	InvoiceAgingBucket1To30   InvoiceAgingBucket = "days_1_30"
	InvoiceAgingBucket31To60  InvoiceAgingBucket = "days_31_60"
	InvoiceAgingBucket61To90  InvoiceAgingBucket = "days_61_90"
	InvoiceAgingBucket91Plus  InvoiceAgingBucket = "days_91_plus"
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

func AllInvoiceAgingBuckets() []InvoiceAgingBucket {
	return []InvoiceAgingBucket{
		InvoiceAgingBucketCurrent,
		InvoiceAgingBucket1To30,
		InvoiceAgingBucket31To60,
		InvoiceAgingBucket61To90,
		InvoiceAgingBucket91Plus,
	}
}

func InvoiceAgingBucketLabel(bucket InvoiceAgingBucket) string {
	switch bucket {
	case InvoiceAgingBucket1To30:
		return "1-30"
	case InvoiceAgingBucket31To60:
		return "31-60"
	case InvoiceAgingBucket61To90:
		return "61-90"
	case InvoiceAgingBucket91Plus:
		return "91+"
	default:
		return "Current"
	}
}

// InvoiceAgingBucketForReport keeps report bucketing aligned with the same
// due-date truth used by the workspace overdue helpers. Formal A/R Aging
// report code should call this helper instead of duplicating bucket rules.
func InvoiceAgingBucketForReport(inv models.Invoice) InvoiceAgingBucket {
	return InvoiceAgingBucketForReportAsOf(inv, time.Now())
}

// InvoiceAgingBucketForReportAsOf maps one invoice into the formal A/R Aging
// buckets using the current overdue truth plus days-past-due as of the report
// date. Invoices that are not past due, or that cannot be bucketed because
// they have no due date, stay in Current.
func InvoiceAgingBucketForReportAsOf(inv models.Invoice, asOf time.Time) InvoiceAgingBucket {
	if EffectiveInvoiceStatusAsOf(inv, asOf) != models.InvoiceStatusOverdue || inv.DueDate == nil {
		return InvoiceAgingBucketCurrent
	}
	daysPastDue := int(dateOnly(asOf).Sub(dateOnly(*inv.DueDate)).Hours() / 24)
	switch {
	case daysPastDue <= 30:
		return InvoiceAgingBucket1To30
	case daysPastDue <= 60:
		return InvoiceAgingBucket31To60
	case daysPastDue <= 90:
		return InvoiceAgingBucket61To90
	default:
		return InvoiceAgingBucket91Plus
	}
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
