package services

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

type ARAgingBucketTotals struct {
	Current    decimal.Decimal
	Days1To30  decimal.Decimal
	Days31To60 decimal.Decimal
	Days61To90 decimal.Decimal
	Days91Plus decimal.Decimal
	Total      decimal.Decimal
}

// ARAgingDetailRow holds invoice-level aging detail for one outstanding invoice.
// Bucket fields are mutually exclusive: exactly one will be non-zero per row,
// matching the same bucket assignment used for the customer summary totals.
type ARAgingDetailRow struct {
	InvoiceID     uint
	InvoiceNumber string
	InvoiceDate   time.Time
	DueDate       *time.Time
	Terms         string // TermCode from the invoice's PaymentTermSnapshot
	BalanceDue    decimal.Decimal
	Current       decimal.Decimal
	Days1To30     decimal.Decimal
	Days31To60    decimal.Decimal
	Days61To90    decimal.Decimal
	Days91Plus    decimal.Decimal
}

type ARAgingCustomerRow struct {
	CustomerID   uint
	CustomerName string
	Current      decimal.Decimal
	Days1To30    decimal.Decimal
	Days31To60   decimal.Decimal
	Days61To90   decimal.Decimal
	Days91Plus   decimal.Decimal
	Total        decimal.Decimal
	DetailRows   []ARAgingDetailRow
}

type ARAgingReport struct {
	AsOf         time.Time
	CurrencyCode string
	Rows         []ARAgingCustomerRow
	Totals       ARAgingBucketTotals
}

func BuildARAgingReport(db *gorm.DB, companyID uint, asOf time.Time) (ARAgingReport, error) {
	currencyCode, err := companyBaseCurrencyCode(db, companyID)
	if err != nil {
		return ARAgingReport{}, err
	}

	var invoices []models.Invoice
	if err := db.
		Preload("Customer").
		Where("company_id = ?", companyID).
		Where("invoice_date < ?", dateOnly(asOf).AddDate(0, 0, 1)).
		Where("status NOT IN ?", []models.InvoiceStatus{models.InvoiceStatusDraft, models.InvoiceStatusVoided}).
		Order("invoice_date asc, id asc").
		Find(&invoices).Error; err != nil {
		return ARAgingReport{}, err
	}

	report := ARAgingReport{
		AsOf:         dateOnly(asOf),
		CurrencyCode: strings.ToUpper(strings.TrimSpace(currencyCode)),
		Rows:         make([]ARAgingCustomerRow, 0),
	}

	rowIndex := make(map[uint]int)
	for _, inv := range invoices {
		outstanding := invoiceOutstandingForARAging(inv)
		if !outstanding.GreaterThan(decimal.Zero) {
			continue
		}

		idx, ok := rowIndex[inv.CustomerID]
		if !ok {
			name := strings.TrimSpace(inv.Customer.Name)
			if name == "" {
				name = fmt.Sprintf("Customer #%d", inv.CustomerID)
			}
			report.Rows = append(report.Rows, ARAgingCustomerRow{
				CustomerID:   inv.CustomerID,
				CustomerName: name,
			})
			idx = len(report.Rows) - 1
			rowIndex[inv.CustomerID] = idx
		}

		bucket := InvoiceAgingBucketForReportAsOf(inv, asOf)
		addARAgingBucketToRow(&report.Rows[idx], bucket, outstanding)
		addARAgingBucketToTotals(&report.Totals, bucket, outstanding)

		detail := ARAgingDetailRow{
			InvoiceID:     inv.ID,
			InvoiceNumber: inv.InvoiceNumber,
			InvoiceDate:   inv.InvoiceDate,
			DueDate:       inv.DueDate,
			Terms:         strings.TrimSpace(inv.TermCode),
			BalanceDue:    outstanding,
		}
		setARAgingDetailBucket(&detail, bucket, outstanding)
		report.Rows[idx].DetailRows = append(report.Rows[idx].DetailRows, detail)
	}

	sort.SliceStable(report.Rows, func(i, j int) bool {
		return strings.ToLower(report.Rows[i].CustomerName) < strings.ToLower(report.Rows[j].CustomerName)
	})
	for i := range report.Rows {
		sortARAgingDetailRows(report.Rows[i].DetailRows)
	}

	return report, nil
}

func addARAgingBucketToRow(row *ARAgingCustomerRow, bucket InvoiceAgingBucket, amount decimal.Decimal) {
	switch bucket {
	case InvoiceAgingBucket1To30:
		row.Days1To30 = row.Days1To30.Add(amount)
	case InvoiceAgingBucket31To60:
		row.Days31To60 = row.Days31To60.Add(amount)
	case InvoiceAgingBucket61To90:
		row.Days61To90 = row.Days61To90.Add(amount)
	case InvoiceAgingBucket91Plus:
		row.Days91Plus = row.Days91Plus.Add(amount)
	default:
		row.Current = row.Current.Add(amount)
	}
	row.Total = row.Total.Add(amount)
}

func addARAgingBucketToTotals(totals *ARAgingBucketTotals, bucket InvoiceAgingBucket, amount decimal.Decimal) {
	switch bucket {
	case InvoiceAgingBucket1To30:
		totals.Days1To30 = totals.Days1To30.Add(amount)
	case InvoiceAgingBucket31To60:
		totals.Days31To60 = totals.Days31To60.Add(amount)
	case InvoiceAgingBucket61To90:
		totals.Days61To90 = totals.Days61To90.Add(amount)
	case InvoiceAgingBucket91Plus:
		totals.Days91Plus = totals.Days91Plus.Add(amount)
	default:
		totals.Current = totals.Current.Add(amount)
	}
	totals.Total = totals.Total.Add(amount)
}

func setARAgingDetailBucket(detail *ARAgingDetailRow, bucket InvoiceAgingBucket, amount decimal.Decimal) {
	switch bucket {
	case InvoiceAgingBucket1To30:
		detail.Days1To30 = amount
	case InvoiceAgingBucket31To60:
		detail.Days31To60 = amount
	case InvoiceAgingBucket61To90:
		detail.Days61To90 = amount
	case InvoiceAgingBucket91Plus:
		detail.Days91Plus = amount
	default:
		detail.Current = amount
	}
}

// sortARAgingDetailRows sorts in place: DueDate asc (nil last), then InvoiceDate asc,
// then InvoiceNumber asc, then InvoiceID asc as final tiebreaker.
func sortARAgingDetailRows(rows []ARAgingDetailRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		di, dj := rows[i].DueDate, rows[j].DueDate
		switch {
		case di == nil && dj == nil:
			// fall through to secondary sort
		case di == nil:
			return false // nil due dates sort last
		case dj == nil:
			return true
		default:
			onlyDate := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
			if !onlyDate(*di).Equal(onlyDate(*dj)) {
				return onlyDate(*di).Before(onlyDate(*dj))
			}
		}
		onlyDate := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
		if !onlyDate(rows[i].InvoiceDate).Equal(onlyDate(rows[j].InvoiceDate)) {
			return onlyDate(rows[i].InvoiceDate).Before(onlyDate(rows[j].InvoiceDate))
		}
		if rows[i].InvoiceNumber != rows[j].InvoiceNumber {
			return rows[i].InvoiceNumber < rows[j].InvoiceNumber
		}
		return rows[i].InvoiceID < rows[j].InvoiceID
	})
}

func invoiceOutstandingForARAging(inv models.Invoice) decimal.Decimal {
	if inv.BalanceDueBase.GreaterThan(decimal.Zero) {
		return inv.BalanceDueBase
	}
	if inv.BalanceDue.GreaterThan(decimal.Zero) {
		return inv.BalanceDue
	}
	return decimal.Zero
}
