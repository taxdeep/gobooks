// 遵循project_guide.md
package services

// ap_aging_service.go — APAging: accounts-payable aging report.
//
// The AP aging report shows open bill balances grouped by how many days
// past-due they are as of a given date.
//
// Aging buckets (days past due):
//   Current  — not yet due (due_date >= asOfDate)
//   1–30     — 1 to 30 days past due
//   31–60    — 31 to 60 days past due
//   61–90    — 61 to 90 days past due
//   90+      — more than 90 days past due
//
// Only bills with balance_due > 0 and status IN (posted, partially_paid)
// are included. Draft and paid bills are excluded.

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Output types ──────────────────────────────────────────────────────────────

// APAgedLine holds one vendor's aging buckets.
type APAgedLine struct {
	Vendor   models.Vendor
	Current  decimal.Decimal
	Days1_30  decimal.Decimal
	Days31_60 decimal.Decimal
	Days61_90 decimal.Decimal
	Days90Plus decimal.Decimal
	Total    decimal.Decimal
	OldestBillID *uint
}

// APAging is the full AP aging report for a company as of a date.
type APAging struct {
	AsOfDate time.Time
	Lines    []APAgedLine

	TotalCurrent    decimal.Decimal
	TotalDays1_30   decimal.Decimal
	TotalDays31_60  decimal.Decimal
	TotalDays61_90  decimal.Decimal
	TotalDays90Plus decimal.Decimal
	GrandTotal      decimal.Decimal
}

// ── Query ─────────────────────────────────────────────────────────────────────

// GetAPAging builds an AP aging report for the given company as of asOfDate.
// Only bills with balance_due > 0 and status in (posted, partially_paid) are included.
func GetAPAging(db *gorm.DB, companyID uint, asOfDate time.Time) (*APAging, error) {
	var bills []models.Bill
	err := db.Preload("Vendor").
		Where("company_id = ? AND balance_due > 0 AND status IN ?",
			companyID,
			[]models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid},
		).
		Order("vendor_id asc, due_date asc").
		Find(&bills).Error
	if err != nil {
		return nil, err
	}

	vendorMap := map[uint]*APAgedLine{}
	vendorOrder := []uint{}

	for i := range bills {
		b := &bills[i]
		if _, seen := vendorMap[b.VendorID]; !seen {
			vendorOrder = append(vendorOrder, b.VendorID)
			vendorMap[b.VendorID] = &APAgedLine{Vendor: b.Vendor}
		}
		line := vendorMap[b.VendorID]

		bal := b.BalanceDue
		dueDate := asOfDate // treat as current if no due date
		if b.DueDate != nil {
			dueDate = *b.DueDate
		}
		days := daysPast(dueDate, asOfDate)

		switch {
		case days <= 0:
			line.Current = line.Current.Add(bal)
		case days <= 30:
			line.Days1_30 = line.Days1_30.Add(bal)
		case days <= 60:
			line.Days31_60 = line.Days31_60.Add(bal)
		case days <= 90:
			line.Days61_90 = line.Days61_90.Add(bal)
		default:
			line.Days90Plus = line.Days90Plus.Add(bal)
			if line.OldestBillID == nil {
				line.OldestBillID = &b.ID
			}
		}
	}

	aging := &APAging{AsOfDate: asOfDate}
	for _, vendorID := range vendorOrder {
		line := vendorMap[vendorID]
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
