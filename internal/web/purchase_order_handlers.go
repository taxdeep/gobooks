// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vendors, _ := s.vendorsForCompany(companyID)
	filterStatus := strings.TrimSpace(c.Query("status"))
	filterVendor := strings.TrimSpace(c.Query("vendor_id"))

	var vendorID uint
	if filterVendor != "" {
		if id, err := strconv.ParseUint(filterVendor, 10, 64); err == nil {
			vendorID = uint(id)
		}
	}

	pos, err := services.ListPurchaseOrders(s.DB, companyID, filterStatus, vendorID)
	if err != nil {
		pos = nil
	}

	return pages.PurchaseOrders(pages.PurchaseOrdersVM{
		HasCompany:     true,
		PurchaseOrders: pos,
		Vendors:        vendors,
		FilterStatus:   filterStatus,
		FilterVendor:   filterVendor,
		Created:        c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.PurchaseOrderDetailVM{HasCompany: true}
	vm.PurchaseOrder.PODate = time.Now()
	vm.PurchaseOrder.ExchangeRate = decimal.NewFromInt(1)
	s.loadPOFormData(companyID, &vm)
	return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/purchase-orders", fiber.StatusSeeOther)
	}

	po, err := services.GetPurchaseOrder(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/purchase-orders", fiber.StatusSeeOther)
	}

	vm := pages.PurchaseOrderDetailVM{
		HasCompany:    true,
		PurchaseOrder: *po,
		Saved:         c.Query("saved") == "1",
		Confirmed:     c.Query("confirmed") == "1",
		Cancelled:     c.Query("cancelled") == "1",
	}
	s.loadPOFormData(companyID, &vm)
	return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	poIDStr := strings.TrimSpace(c.FormValue("po_id"))
	var poID uint
	if poIDStr != "" {
		if id, err := strconv.ParseUint(poIDStr, 10, 64); err == nil {
			poID = uint(id)
		}
	}

	in, err := parsePOInput(c)
	if err != nil {
		vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: err.Error()}
		if poID > 0 {
			if po, e := services.GetPurchaseOrder(s.DB, companyID, poID); e == nil {
				vm.PurchaseOrder = *po
			}
		}
		s.loadPOFormData(companyID, &vm)
		return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
	}

	if poID == 0 {
		po, err := services.CreatePurchaseOrder(s.DB, companyID, in)
		if err != nil {
			vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadPOFormData(companyID, &vm)
			return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/purchase-orders/"+strconv.FormatUint(uint64(po.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdatePurchaseOrder(s.DB, companyID, poID, in)
	if err != nil {
		vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: err.Error()}
		if po, e := services.GetPurchaseOrder(s.DB, companyID, poID); e == nil {
			vm.PurchaseOrder = *po
		}
		s.loadPOFormData(companyID, &vm)
		return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/purchase-orders/"+strconv.FormatUint(uint64(poID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Confirm ───────────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderConfirm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/purchase-orders", fiber.StatusSeeOther)
	}

	if confErr := services.ConfirmPurchaseOrder(s.DB, companyID, id); confErr != nil {
		po, _ := services.GetPurchaseOrder(s.DB, companyID, id)
		vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: confErr.Error()}
		if po != nil {
			vm.PurchaseOrder = *po
		}
		s.loadPOFormData(companyID, &vm)
		return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/purchase-orders/"+strconv.FormatUint(uint64(id), 10)+"?confirmed=1", fiber.StatusSeeOther)
}

// ── Cancel ────────────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/purchase-orders", fiber.StatusSeeOther)
	}

	if cancelErr := services.CancelPurchaseOrder(s.DB, companyID, id); cancelErr != nil {
		po, _ := services.GetPurchaseOrder(s.DB, companyID, id)
		vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: cancelErr.Error()}
		if po != nil {
			vm.PurchaseOrder = *po
		}
		s.loadPOFormData(companyID, &vm)
		return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/purchase-orders/"+strconv.FormatUint(uint64(id), 10)+"?cancelled=1", fiber.StatusSeeOther)
}

// ── Mark Received ─────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderMarkReceived(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/purchase-orders", fiber.StatusSeeOther)
	}

	if recErr := services.MarkPOReceived(s.DB, companyID, id); recErr != nil {
		po, _ := services.GetPurchaseOrder(s.DB, companyID, id)
		vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: recErr.Error()}
		if po != nil {
			vm.PurchaseOrder = *po
		}
		s.loadPOFormData(companyID, &vm)
		return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/purchase-orders/"+strconv.FormatUint(uint64(id), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Close ─────────────────────────────────────────────────────────────────────

func (s *Server) handlePurchaseOrderClose(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/purchase-orders", fiber.StatusSeeOther)
	}

	if closeErr := services.ClosePurchaseOrder(s.DB, companyID, id); closeErr != nil {
		po, _ := services.GetPurchaseOrder(s.DB, companyID, id)
		vm := pages.PurchaseOrderDetailVM{HasCompany: true, FormError: closeErr.Error()}
		if po != nil {
			vm.PurchaseOrder = *po
		}
		s.loadPOFormData(companyID, &vm)
		return pages.PurchaseOrderDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/purchase-orders/"+strconv.FormatUint(uint64(id), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadPOFormData(companyID uint, vm *pages.PurchaseOrderDetailVM) {
	vm.Vendors, _ = s.vendorsForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code asc").Find(&vm.Accounts)
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").Find(&vm.Products)
	s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vm.TaxCodes)
}

func parsePOInput(c *fiber.Ctx) (services.POInput, error) {
	vendorIDStr := strings.TrimSpace(c.FormValue("vendor_id"))
	vendorID, _ := strconv.ParseUint(vendorIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("po_date"))
	var poDate time.Time
	if dateStr != "" {
		poDate, _ = time.Parse("2006-01-02", dateStr)
	}

	var expectedDate *time.Time
	if expStr := strings.TrimSpace(c.FormValue("expected_date")); expStr != "" {
		if t, err := time.Parse("2006-01-02", expStr); err == nil {
			expectedDate = &t
		}
	}

	rateStr := strings.TrimSpace(c.FormValue("exchange_rate"))
	rate, _ := decimal.NewFromString(rateStr)
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	// Parse lines from form: lines[0][description], lines[0][qty], lines[0][unit_price]
	var lines []services.POLineInput
	for i := 0; i < 100; i++ {
		prefix := "lines[" + strconv.Itoa(i) + "]"
		desc := strings.TrimSpace(c.FormValue(prefix + "[description]"))
		qtyStr := strings.TrimSpace(c.FormValue(prefix + "[qty]"))
		priceStr := strings.TrimSpace(c.FormValue(prefix + "[unit_price]"))
		if desc == "" && qtyStr == "" && priceStr == "" {
			break
		}
		qty, _ := decimal.NewFromString(qtyStr)
		price, _ := decimal.NewFromString(priceStr)
		lines = append(lines, services.POLineInput{
			SortOrder:   uint(i + 1),
			Description: desc,
			Qty:         qty,
			UnitPrice:   price,
		})
	}

	return services.POInput{
		VendorID:     uint(vendorID),
		PODate:       poDate,
		ExpectedDate: expectedDate,
		CurrencyCode: strings.TrimSpace(c.FormValue("currency_code")),
		ExchangeRate: rate,
		Notes:        strings.TrimSpace(c.FormValue("notes")),
		Memo:         strings.TrimSpace(c.FormValue("memo")),
		Lines:        lines,
	}, nil
}

