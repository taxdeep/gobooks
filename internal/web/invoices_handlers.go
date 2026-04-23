// 遵循project_guide.md
package web

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleInvoices(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, err := s.customersForCompany(companyID)
	if err != nil {
		return pages.Invoices(pages.InvoicesVM{
			HasCompany: true,
			FormError:  "Could not load customers.",
		}).Render(c.Context(), c)
	}

	filterQ := strings.TrimSpace(c.Query("q"))
	filterCustomerID := strings.TrimSpace(c.Query("customer_id"))
	filterStatus := strings.TrimSpace(c.Query("status"))
	filterFrom := strings.TrimSpace(c.Query("from"))
	filterTo := strings.TrimSpace(c.Query("to"))
	asOf := time.Now()

	qry := s.DB.Preload("Customer").Model(&models.Invoice{}).Where("company_id = ?", companyID)
	if filterQ != "" {
		qry = qry.Where("LOWER(invoice_number) LIKE LOWER(?)", "%"+filterQ+"%")
	}
	if filterCustomerID != "" {
		if id, err := services.ParseUint(filterCustomerID); err == nil && id > 0 {
			qry = qry.Where("customer_id = ?", uint(id))
		}
	}
	if filterFrom != "" {
		if d, err := time.Parse("2006-01-02", filterFrom); err == nil {
			qry = qry.Where("invoice_date >= ?", d)
		}
	}
	if filterTo != "" {
		if d, err := time.Parse("2006-01-02", filterTo); err == nil {
			qry = qry.Where("invoice_date < ?", d.AddDate(0, 0, 1))
		}
	}

	var invoices []models.Invoice
	if err := qry.Order("invoice_date desc, id desc").Find(&invoices).Error; err != nil {
		return pages.Invoices(pages.InvoicesVM{
			HasCompany: true,
			FormError:  "Could not load invoices.",
		}).Render(c.Context(), c)
	}
	if filterStatus != "" {
		filtered := make([]models.Invoice, 0, len(invoices))
		for _, inv := range invoices {
			if string(services.EffectiveInvoiceStatusAsOf(inv, asOf)) == filterStatus {
				filtered = append(filtered, inv)
			}
		}
		invoices = filtered
	}

	nextNo, err := services.SuggestNextInvoiceNumber(s.DB, companyID)
	if err != nil {
		nextNo = "IN001"
	}

	return pages.Invoices(pages.InvoicesVM{
		HasCompany:       true,
		Customers:        customers,
		Invoices:         invoices,
		InvoiceDate:      time.Now().Format("2006-01-02"),
		InvoiceNumber:    nextNo,
		Created:          c.Query("created") == "1",
		Saved:            c.Query("saved") == "1",
		Posted:           c.Query("posted") == "1",
		Deleted:          c.Query("deleted") == "1",
		FilterQ:          filterQ,
		FilterCustomerID: filterCustomerID,
		FilterStatus:     filterStatus,
		FilterFrom:       filterFrom,
		FilterTo:         filterTo,
	}).Render(c.Context(), c)
}

func (s *Server) handleInvoiceCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, err := s.customersForCompany(companyID)
	if err != nil {
		return pages.Invoices(pages.InvoicesVM{
			HasCompany: true,
			FormError:  "Could not load customers.",
		}).Render(c.Context(), c)
	}

	invoices, err := s.invoicesForCompany(companyID)
	if err != nil {
		return pages.Invoices(pages.InvoicesVM{
			HasCompany: true,
			FormError:  "Could not load invoices.",
		}).Render(c.Context(), c)
	}

	invoiceNo := strings.TrimSpace(c.FormValue("invoice_number"))
	customerRaw := strings.TrimSpace(c.FormValue("customer_id"))
	dateRaw := strings.TrimSpace(c.FormValue("invoice_date"))
	amountRaw := strings.TrimSpace(c.FormValue("amount"))
	memo := strings.TrimSpace(c.FormValue("memo"))
	forceDuplicate := strings.TrimSpace(c.FormValue("force_duplicate")) == "1"

	vm := pages.InvoicesVM{
		HasCompany:    true,
		Customers:     customers,
		Invoices:      invoices,
		InvoiceNumber: invoiceNo,
		CustomerID:    customerRaw,
		InvoiceDate:   dateRaw,
		Amount:        amountRaw,
		Memo:          memo,
	}

	if invoiceNo == "" {
		vm.InvoiceNumberError = "Invoice Number is required."
	} else if err := services.ValidateDocumentNumber(invoiceNo); err != nil {
		vm.InvoiceNumberError = err.Error()
	}
	custID, err := services.ParseUint(customerRaw)
	if err != nil || custID == 0 {
		vm.CustomerError = "Customer is required."
	}
	invoiceDate, err := time.Parse("2006-01-02", dateRaw)
	if err != nil {
		vm.DateError = "Invoice Date is required."
	}
	amount, err := services.ParseDecimalMoney(amountRaw)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		vm.AmountError = "Amount must be greater than 0."
	}

	if vm.InvoiceNumberError != "" || vm.CustomerError != "" || vm.DateError != "" || vm.AmountError != "" {
		return pages.Invoices(vm).Render(c.Context(), c)
	}

	var custCount int64
	if err := s.DB.Model(&models.Customer{}).
		Where("id = ? AND company_id = ?", uint(custID), companyID).
		Count(&custCount).Error; err != nil {
		vm.FormError = "Could not validate customer."
		return pages.Invoices(vm).Render(c.Context(), c)
	}
	if custCount == 0 {
		vm.CustomerError = "Customer is not valid for this company."
		return pages.Invoices(vm).Render(c.Context(), c)
	}

	var dupCount int64
	if err := s.DB.Model(&models.Invoice{}).
		Where("company_id = ? AND LOWER(invoice_number) = LOWER(?) AND status <> ?", companyID, invoiceNo, models.InvoiceStatusVoided).
		Count(&dupCount).Error; err != nil {
		vm.FormError = "Could not validate Invoice Number."
		return pages.Invoices(vm).Render(c.Context(), c)
	}
	if dupCount > 0 && !forceDuplicate {
		vm.DuplicateWarning = true
		vm.DuplicateMessage = "Invoice Number conflict detected (case-insensitive)."
		return pages.Invoices(vm).Render(c.Context(), c)
	}

	// Load customer for snapshots
	var customer models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", uint(custID), companyID).
		First(&customer).Error; err != nil {
		vm.FormError = "Customer not found."
		return pages.Invoices(vm).Render(c.Context(), c)
	}

	inv := models.Invoice{
		CompanyID:               companyID,
		InvoiceNumber:           invoiceNo,
		CustomerID:              uint(custID),
		InvoiceDate:             invoiceDate,
		Amount:                  amount,
		BalanceDue:              amount,
		Memo:                    memo,
		CustomerNameSnapshot:    customer.Name,
		CustomerEmailSnapshot:   customer.Email,
		CustomerAddressSnapshot: customer.FormattedAddress(),
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}
		if err := services.BumpInvoiceNextNumberAfterCreate(tx, companyID); err != nil {
			return err
		}
		return services.WriteAuditLogWithContextDetails(tx, "invoice.created", "invoice", inv.ID, actor, map[string]any{
			"company_id": companyID,
		}, &cid, &uid, nil, map[string]any{
			"invoice_number": inv.InvoiceNumber,
			"customer_id":    inv.CustomerID,
			"amount":         inv.Amount.StringFixed(2),
		})
	})
	if err != nil {
		vm.FormError = invoiceSaveErrorMessage(err)
		return pages.Invoices(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, inv.ID)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/invoices?created=1")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/invoices?created=1", fiber.StatusSeeOther)
}

// handleInvoicePost and handleInvoiceVoid are defined in invoice_lifecycle_handlers.go
// (JSON API pattern with proper validation — used by routes.go)

func (s *Server) invoicesForCompany(companyID uint) ([]models.Invoice, error) {
	var invoices []models.Invoice
	err := s.DB.Preload("Customer").Where("company_id = ?", companyID).Order("invoice_date desc, id desc").Find(&invoices).Error
	return invoices, err
}
