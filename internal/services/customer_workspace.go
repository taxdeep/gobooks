package services

import (
	"errors"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

var ErrCustomerWorkspaceNotFound = errors.New("customer not found")

// CustomerWorkspace is the operational customer-level visibility model used by
// the customer workspace page. It intentionally reuses the existing billable
// summary truth and only adds lightweight recent invoice activity.
type CustomerWorkspace struct {
	Customer                models.Customer
	DefaultPaymentTermLabel string
	BillableSummary         CustomerBillableSummary
	ARSummary               CustomerARSummary
	OutstandingInvoices     []models.Invoice
	RecentInvoices          []models.Invoice
	MostRecentInvoice       *models.Invoice
}

// CustomerARSummary is lightweight operational AR visibility for one customer.
// It is intentionally not a formal aging schedule or accounting report.
type CustomerARSummary struct {
	OutstandingTotals       []CurrencyTotal
	OutstandingInvoiceCount int
	OverdueInvoiceCount     int
}

func GetCustomerWorkspace(db *gorm.DB, companyID, customerID uint) (*CustomerWorkspace, error) {
	if companyID == 0 || customerID == 0 {
		return nil, ErrCustomerWorkspaceNotFound
	}

	var customer models.Customer
	if err := db.Where("company_id = ? AND id = ?", companyID, customerID).First(&customer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCustomerWorkspaceNotFound
		}
		return nil, err
	}

	summaries, err := ListCustomerBillableSummaries(db, companyID)
	if err != nil {
		return nil, err
	}

	arSummary, err := GetCustomerARSummary(db, companyID, customerID)
	if err != nil {
		return nil, err
	}
	outstandingInvoices, err := ListCustomerOutstandingInvoices(db, companyID, customerID, 5)
	if err != nil {
		return nil, err
	}

	var recentInvoices []models.Invoice
	if err := db.
		Where("company_id = ? AND customer_id = ?", companyID, customerID).
		Order("invoice_date desc, id desc").
		Limit(5).
		Find(&recentInvoices).Error; err != nil {
		return nil, err
	}

	workspace := &CustomerWorkspace{
		Customer:            customer,
		BillableSummary:     CustomerSummaryOrZero(summaries, customerID),
		ARSummary:           *arSummary,
		OutstandingInvoices: outstandingInvoices,
		RecentInvoices:      recentInvoices,
	}
	if label, err := customerWorkspacePaymentTermLabel(db, companyID, customer.DefaultPaymentTermCode); err != nil {
		return nil, err
	} else {
		workspace.DefaultPaymentTermLabel = label
	}
	if len(recentInvoices) > 0 {
		mostRecent := recentInvoices[0]
		workspace.MostRecentInvoice = &mostRecent
	}
	return workspace, nil
}

func GetCustomerARSummary(db *gorm.DB, companyID, customerID uint) (*CustomerARSummary, error) {
	outstandingInvoices, err := listCustomerOutstandingInvoices(db, companyID, customerID)
	if err != nil {
		return nil, err
	}
	baseCurrency, err := companyBaseCurrencyCode(db, companyID)
	if err != nil {
		return nil, err
	}

	summary := &CustomerARSummary{
		OutstandingInvoiceCount: len(outstandingInvoices),
	}
	for _, invoice := range outstandingInvoices {
		summary.OutstandingTotals = addCurrencyTotal(
			summary.OutstandingTotals,
			normalizeVisibilityCurrency(invoice.CurrencyCode, baseCurrency),
			invoice.BalanceDue,
		)
		if EffectiveInvoiceStatus(invoice) == models.InvoiceStatusOverdue {
			summary.OverdueInvoiceCount++
		}
	}
	return summary, nil
}

func ListCustomerOutstandingInvoices(db *gorm.DB, companyID, customerID uint, limit int) ([]models.Invoice, error) {
	outstandingInvoices, err := listCustomerOutstandingInvoices(db, companyID, customerID)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(outstandingInvoices) > limit {
		return outstandingInvoices[:limit], nil
	}
	return outstandingInvoices, nil
}

func listCustomerOutstandingInvoices(db *gorm.DB, companyID, customerID uint) ([]models.Invoice, error) {
	var invoices []models.Invoice
	if err := db.
		Where("company_id = ? AND customer_id = ?", companyID, customerID).
		Order("invoice_date desc, id desc").
		Find(&invoices).Error; err != nil {
		return nil, err
	}

	outstanding := make([]models.Invoice, 0, len(invoices))
	for _, invoice := range invoices {
		if invoice.BalanceDue.LessThanOrEqual(decimal.Zero) {
			continue
		}
		switch invoice.Status {
		case models.InvoiceStatusDraft, models.InvoiceStatusVoided:
			continue
		}
		outstanding = append(outstanding, invoice)
	}
	return outstanding, nil
}

func customerWorkspacePaymentTermLabel(db *gorm.DB, companyID uint, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", nil
	}

	var paymentTerm models.PaymentTerm
	if err := db.Where("company_id = ? AND code = ?", companyID, code).First(&paymentTerm).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return code, nil
		}
		return "", err
	}
	return paymentTerm.DropdownLabel(), nil
}
