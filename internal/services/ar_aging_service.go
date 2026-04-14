// 遵循project_guide.md
package services

// ar_aging_service.go — ARAging: accounts-receivable aging report.
//
// The aging report shows open invoice balances grouped by how many days
// past-due they are as of a given date.
//
// Aging buckets (days past due):
//   Current  — not yet due (due_date >= asOfDate)
//   1–30     — 1 to 30 days past due
//   31–60    — 31 to 60 days past due
//   61–90    — 61 to 90 days past due
//   90+      — more than 90 days past due
//
// Only invoices with balance_due > 0 and status IN (sent, partially_paid)
// are included. Draft and paid invoices are excluded.

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Output types ──────────────────────────────────────────────────────────────

// ARAgedLine holds one customer's aging buckets.
type ARAgedLine struct {
	Customer        models.Customer
	Current         decimal.Decimal // not yet due
	Days1_30        decimal.Decimal // 1–30 days past due
	Days31_60       decimal.Decimal // 31–60 days past due
	Days61_90       decimal.Decimal // 61–90 days past due
	Days90Plus      decimal.Decimal // > 90 days past due
	Total           decimal.Decimal // sum of all buckets
	OldestInvoiceID *uint           // ID of the oldest overdue invoice (for drilldown)
}

// ARAging is the full aging report for a company as of a date.
type ARAging struct {
	AsOfDate time.Time
	Lines    []ARAgedLine

	// Totals across all customers
	TotalCurrent    decimal.Decimal
	TotalDays1_30   decimal.Decimal
	TotalDays31_60  decimal.Decimal
	TotalDays61_90  decimal.Decimal
	TotalDays90Plus decimal.Decimal
	GrandTotal      decimal.Decimal
}

// ── Query ─────────────────────────────────────────────────────────────────────

// GetARAging builds an AR aging report for the given company as of asOfDate.
// Only invoices with balance_due > 0 and status in (sent, partially_paid) are included.
func GetARAging(db *gorm.DB, companyID uint, asOfDate time.Time) (*ARAging, error) {
	// Load all open invoices with their customer for this company.
	var invoices []models.Invoice
	err := db.Preload("Customer").
		Where("company_id = ? AND balance_due > 0 AND status IN ?",
			companyID,
			[]models.InvoiceStatus{models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid},
		).
		Order("customer_id asc, due_date asc").
		Find(&invoices).Error
	if err != nil {
		return nil, err
	}

	// Group by customer, bucketing by days-past-due.
	customerMap := map[uint]*ARAgedLine{}
	customerOrder := []uint{}

	for i := range invoices {
		inv := &invoices[i]
		if _, seen := customerMap[inv.CustomerID]; !seen {
			customerOrder = append(customerOrder, inv.CustomerID)
			customerMap[inv.CustomerID] = &ARAgedLine{Customer: inv.Customer}
		}
		line := customerMap[inv.CustomerID]

		bal := inv.BalanceDue
		dueDate := asOfDate // treat as current if no due date
		if inv.DueDate != nil {
			dueDate = *inv.DueDate
		}
		daysPastDue := daysPast(dueDate, asOfDate)

		switch {
		case daysPastDue <= 0:
			line.Current = line.Current.Add(bal)
		case daysPastDue <= 30:
			line.Days1_30 = line.Days1_30.Add(bal)
		case daysPastDue <= 60:
			line.Days31_60 = line.Days31_60.Add(bal)
		case daysPastDue <= 90:
			line.Days61_90 = line.Days61_90.Add(bal)
		default:
			line.Days90Plus = line.Days90Plus.Add(bal)
			if line.OldestInvoiceID == nil {
				line.OldestInvoiceID = &inv.ID
			}
		}
	}

	// Assemble result in insertion order (oldest overdue first per customer).
	aging := &ARAging{AsOfDate: asOfDate}
	for _, custID := range customerOrder {
		line := customerMap[custID]
		line.Total = line.Current.
			Add(line.Days1_30).
			Add(line.Days31_60).
			Add(line.Days61_90).
			Add(line.Days90Plus)

		aging.Lines = append(aging.Lines, *line)
		aging.TotalCurrent = aging.TotalCurrent.Add(line.Current)
		aging.TotalDays1_30 = aging.TotalDays1_30.Add(line.Days1_30)
		aging.TotalDays31_60 = aging.TotalDays31_60.Add(line.Days31_60)
		aging.TotalDays61_90 = aging.TotalDays61_90.Add(line.Days61_90)
		aging.TotalDays90Plus = aging.TotalDays90Plus.Add(line.Days90Plus)
		aging.GrandTotal = aging.GrandTotal.Add(line.Total)
	}

	return aging, nil
}

// daysPast returns how many days asOfDate is past dueDate.
// Returns 0 or negative when dueDate is in the future.
func daysPast(dueDate, asOfDate time.Time) int {
	due := dueDate.UTC().Truncate(24 * time.Hour)
	asOf := asOfDate.UTC().Truncate(24 * time.Hour)
	diff := asOf.Sub(due)
	days := int(diff.Hours() / 24)
	return days
}
