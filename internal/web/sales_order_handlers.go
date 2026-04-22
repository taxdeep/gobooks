// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── List ─────────────────────────────────────────────────────────────────────

func (s *Server) handleSalesOrders(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, _ := s.customersForCompany(companyID)
	filterStatus := strings.TrimSpace(c.Query("status"))
	filterCustomer := strings.TrimSpace(c.Query("customer_id"))

	var customerID uint
	if filterCustomer != "" {
		if id, err := strconv.ParseUint(filterCustomer, 10, 64); err == nil {
			customerID = uint(id)
		}
	}

	orders, err := services.ListSalesOrders(s.DB, companyID, filterStatus, customerID)
	if err != nil {
		orders = nil
	}

	return pages.SalesOrders(pages.SalesOrdersVM{
		HasCompany:     true,
		Orders:         orders,
		Customers:      customers,
		FilterStatus:   filterStatus,
		FilterCustomer: filterCustomer,
		Created:        c.Query("created") == "1",
		Saved:          c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleSalesOrderNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.SalesOrderDetailVM{HasCompany: true}
	vm.Order.OrderDate = time.Now()
	s.loadSOFormData(companyID, &vm)
	return pages.SalesOrderDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleSalesOrderDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/sales-orders", fiber.StatusSeeOther)
	}

	so, err := services.GetSalesOrder(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/sales-orders", fiber.StatusSeeOther)
	}

	vm := pages.SalesOrderDetailVM{
		HasCompany: true,
		Order:      *so,
		Saved:      c.Query("saved") == "1",
		Confirmed:  c.Query("confirmed") == "1",
		Cancelled:  c.Query("cancelled") == "1",
	}
	s.loadSOFormData(companyID, &vm)

	// Load invoices raised against this SO (migration 085 link).
	// Best-effort — an error here logs but doesn't block rendering.
	// Shown only on the read-only view of non-draft SOs; for Draft
	// SOs the list is always empty so querying is a no-op anyway.
	if so.Status != models.SalesOrderStatusDraft {
		var linked []models.Invoice
		if err := s.DB.
			Where("company_id = ? AND sales_order_id = ?", companyID, so.ID).
			Order("invoice_date desc, id desc").
			Find(&linked).Error; err == nil {
			vm.LinkedInvoices = linked
		}
	}

	return pages.SalesOrderDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleSalesOrderSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	orderIDStr := strings.TrimSpace(c.FormValue("order_id"))
	var orderID uint
	if orderIDStr != "" {
		if id, err := strconv.ParseUint(orderIDStr, 10, 64); err == nil {
			orderID = uint(id)
		}
	}

	in, err := parseSalesOrderInput(c)
	if err != nil {
		vm := pages.SalesOrderDetailVM{HasCompany: true, FormError: err.Error()}
		if orderID > 0 {
			if so, e := services.GetSalesOrder(s.DB, companyID, orderID); e == nil {
				vm.Order = *so
			}
		}
		s.loadSOFormData(companyID, &vm)
		return pages.SalesOrderDetail(vm).Render(c.Context(), c)
	}

	if orderID == 0 {
		so, err := services.CreateSalesOrder(s.DB, companyID, in)
		if err != nil {
			vm := pages.SalesOrderDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadSOFormData(companyID, &vm)
			return pages.SalesOrderDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/sales-orders/"+strconv.FormatUint(uint64(so.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateSalesOrder(s.DB, companyID, orderID, in)
	if err != nil {
		vm := pages.SalesOrderDetailVM{HasCompany: true, FormError: err.Error()}
		if so, e := services.GetSalesOrder(s.DB, companyID, orderID); e == nil {
			vm.Order = *so
		}
		s.loadSOFormData(companyID, &vm)
		return pages.SalesOrderDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/sales-orders/"+strconv.FormatUint(uint64(orderID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Status transitions ────────────────────────────────────────────────────────

func (s *Server) handleSalesOrderConfirm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/sales-orders", fiber.StatusSeeOther)
	}
	_ = services.ConfirmSalesOrder(s.DB, companyID, id)
	return c.Redirect("/sales-orders/"+strconv.FormatUint(uint64(id), 10)+"?confirmed=1", fiber.StatusSeeOther)
}

func (s *Server) handleSalesOrderCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/sales-orders", fiber.StatusSeeOther)
	}
	_ = services.CancelSalesOrder(s.DB, companyID, id)
	return c.Redirect("/sales-orders/"+strconv.FormatUint(uint64(id), 10)+"?cancelled=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadSOFormData(companyID uint, vm *pages.SalesOrderDetailVM) {
	vm.Customers, _ = s.customersForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true AND scope != ?",
		companyID, models.TaxScopePurchase).Order("name asc").Find(&vm.TaxCodes)
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&vm.ProductServices)
}

func parseSalesOrderInput(c *fiber.Ctx) (services.SalesOrderInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	if customerIDStr == "" {
		return services.SalesOrderInput{}, fiber.NewError(fiber.StatusBadRequest, "customer is required")
	}
	cid, err := strconv.ParseUint(customerIDStr, 10, 64)
	if err != nil || cid == 0 {
		return services.SalesOrderInput{}, fiber.NewError(fiber.StatusBadRequest, "invalid customer")
	}

	orderDateStr := strings.TrimSpace(c.FormValue("order_date"))
	orderDate := time.Now()
	if orderDateStr != "" {
		if d, e := time.Parse("2006-01-02", orderDateStr); e == nil {
			orderDate = d
		}
	}

	var requiredBy *time.Time
	if rb := strings.TrimSpace(c.FormValue("required_by")); rb != "" {
		if d, e := time.Parse("2006-01-02", rb); e == nil {
			requiredBy = &d
		}
	}

	lines := parseDocumentLines(c)
	if len(lines) == 0 {
		return services.SalesOrderInput{}, fiber.NewError(fiber.StatusBadRequest, "at least one line is required")
	}

	in := services.SalesOrderInput{
		CustomerID:       uint(cid),
		CurrencyCode:     strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code"))),
		OrderDate:        orderDate,
		RequiredBy:       requiredBy,
		Notes:            strings.TrimSpace(c.FormValue("notes")),
		Memo:             services.SanitizeMemoHTML(c.FormValue("memo")),
		CustomerPONumber: strings.TrimSpace(c.FormValue("customer_po_number")),
	}

	for _, l := range lines {
		in.Lines = append(in.Lines, services.SalesOrderLineInput{
			ProductServiceID: l.ProductServiceID,
			TaxCodeID:        l.TaxCodeID,
			Description:      l.Description,
			Quantity:         l.Quantity,
			UnitPrice:        l.UnitPrice,
		})
	}
	return in, nil
}
