// 遵循project_guide.md
package web

// ar_return_receipt_handlers.go — Phase I slice I.6a.4 UI handlers for
// the ARReturnReceipt physical-truth document.
//
// Routes bound in routes.go:
//
//   GET  /ar-return-receipts                  — list
//   GET  /ar-return-receipts/new              — create form (supports ?credit_note_id=X pre-fill, Q4 shortcut)
//   POST /ar-return-receipts/save             — create draft from form
//   GET  /ar-return-receipts/:id              — detail (post / void / delete actions)
//   POST /ar-return-receipts/:id/post         — post (rail-aware per I.6a.2 service)
//   POST /ar-return-receipts/:id/void         — void (Q5 document-local)
//   POST /ar-return-receipts/:id/delete       — delete (draft only)
//
// Scope lock (I.6a.4)
// -------------------
// UI wraps the existing I.6a.2 service. No new business rules here:
// Q8 save-time CN link, Q6 exact-coverage (enforced on CN post via
// I.6a.3), Q5 document-local void, rail-aware post are all service-
// layer concerns surfaced to the operator via error banners.

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// ── GET /ar-return-receipts ──────────────────────────────────────────────────

func (s *Server) handleARReturnReceiptsList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	rows, err := services.ListARReturnReceipts(s.DB, companyID, services.ListARReturnReceiptsFilter{Limit: 200})
	if err != nil {
		return pages.ARReturnReceiptsList(pages.ARReturnReceiptsListVM{
			HasCompany: true,
			Rows:       nil,
			FormError:  err.Error(),
		}).Render(c.Context(), c)
	}
	return pages.ARReturnReceiptsList(pages.ARReturnReceiptsListVM{
		HasCompany: true,
		Rows:       rows,
		Saved:      c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// ── GET /ar-return-receipts/new ──────────────────────────────────────────────
//
// Pre-fill (Q4 shortcut): ?credit_note_id=X loads the CN's stock-item
// lines and injects them as pre-filled form rows. Pure-service lines
// are excluded — they have no inventory return leg.

func (s *Server) handleARReturnReceiptNewGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	var customers []models.Customer
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&customers)

	var warehouses []models.Warehouse
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&warehouses)

	vm := pages.ARReturnReceiptFormVM{
		HasCompany: true,
		Customers:  customers,
		Warehouses: warehouses,
	}
	if whID, ok := parseWarehouseIDQuery(c.Query("warehouse_id"), warehouses); ok {
		vm.WarehouseID = whID
	}

	if cnIDStr := c.Query("credit_note_id"); cnIDStr != "" {
		if cnID, err := strconv.ParseUint(cnIDStr, 10, 64); err == nil && cnID > 0 {
			var cn models.CreditNote
			if err := s.DB.Preload("Lines.ProductService").
				Where("id = ? AND company_id = ?", uint(cnID), companyID).
				First(&cn).Error; err == nil {
				vm.CreditNoteID = uint(cnID)
				vm.CreditNoteNumber = cn.CreditNoteNumber
				vm.CustomerID = cn.CustomerID
				if vm.WarehouseID == 0 && len(warehouses) > 0 {
					vm.WarehouseID = warehouses[0].ID
				}
				for _, ln := range cn.Lines {
					if ln.ProductService == nil || !ln.ProductService.IsStockItem {
						continue
					}
					psID := uint(0)
					if ln.ProductServiceID != nil {
						psID = *ln.ProductServiceID
					}
					vm.PrefilledLines = append(vm.PrefilledLines, pages.ARReturnReceiptFormLine{
						CreditNoteLineID: ln.ID,
						ProductServiceID: psID,
						ProductName:      productDisplayName(ln.ProductService),
						Description:      ln.Description,
						Qty:              ln.Qty,
					})
				}
			}
		}
	}

	return pages.ARReturnReceiptForm(vm).Render(c.Context(), c)
}

func parseWarehouseIDQuery(raw string, warehouses []models.Warehouse) (uint, bool) {
	whID, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || whID == 0 {
		return 0, false
	}
	for _, wh := range warehouses {
		if wh.ID == uint(whID) {
			return wh.ID, true
		}
	}
	return 0, false
}

func productDisplayName(p *models.ProductService) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// ── POST /ar-return-receipts/save ────────────────────────────────────────────

func (s *Server) handleARReturnReceiptSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	rDate := parseDateOrNow(c.FormValue("return_date"))

	customerID := parseUintPtr(c.FormValue("customer_id"))
	warehouseID, _ := strconv.ParseUint(c.FormValue("warehouse_id"), 10, 64)
	cnIDPtr := parseUintPtr(c.FormValue("credit_note_id"))

	descs := c.Context().PostArgs().PeekMulti("description[]")
	products := c.Context().PostArgs().PeekMulti("product_service_id[]")
	qtys := c.Context().PostArgs().PeekMulti("qty[]")
	cnLineIDs := c.Context().PostArgs().PeekMulti("credit_note_line_id[]")

	n := len(products)
	if len(qtys) < n {
		n = len(qtys)
	}
	lines := make([]services.CreateARReturnReceiptLineInput, 0, n)
	for i := 0; i < n; i++ {
		psID, _ := strconv.ParseUint(string(products[i]), 10, 64)
		if psID == 0 {
			continue
		}
		qty, _ := decimal.NewFromString(string(qtys[i]))
		desc := ""
		if i < len(descs) {
			desc = string(descs[i])
		}
		var cnLineID *uint
		if i < len(cnLineIDs) {
			if v, err := strconv.ParseUint(string(cnLineIDs[i]), 10, 64); err == nil && v > 0 {
				u := uint(v)
				cnLineID = &u
			}
		}
		lines = append(lines, services.CreateARReturnReceiptLineInput{
			SortOrder:        i + 1,
			ProductServiceID: uint(psID),
			Description:      desc,
			Qty:              qty,
			CreditNoteLineID: cnLineID,
		})
	}

	in := services.CreateARReturnReceiptInput{
		CompanyID:           companyID,
		ReturnReceiptNumber: strings.TrimSpace(c.FormValue("return_receipt_number")),
		CustomerID:          customerID,
		WarehouseID:         uint(warehouseID),
		ReturnDate:          rDate,
		Memo:                strings.TrimSpace(c.FormValue("memo")),
		Reference:           strings.TrimSpace(c.FormValue("reference")),
		CreditNoteID:        cnIDPtr,
		Lines:               lines,
	}

	created, err := services.CreateARReturnReceipt(s.DB, in)
	if err != nil {
		var customers []models.Customer
		s.DB.Where("company_id = ? AND is_active = true", companyID).
			Order("name asc").Find(&customers)
		var warehouses []models.Warehouse
		s.DB.Where("company_id = ? AND is_active = true", companyID).
			Order("name asc").Find(&warehouses)
		vm := pages.ARReturnReceiptFormVM{
			HasCompany:          true,
			Customers:           customers,
			Warehouses:          warehouses,
			CustomerID:          derefUint(customerID),
			WarehouseID:         uint(warehouseID),
			ReturnReceiptNumber: in.ReturnReceiptNumber,
			Memo:                in.Memo,
			Reference:           in.Reference,
			CreditNoteID:        derefUint(cnIDPtr),
			FormError:           err.Error(),
		}
		for _, ln := range lines {
			vm.PrefilledLines = append(vm.PrefilledLines, pages.ARReturnReceiptFormLine{
				CreditNoteLineID: derefUint(ln.CreditNoteLineID),
				ProductServiceID: ln.ProductServiceID,
				Description:      ln.Description,
				Qty:              ln.Qty,
			})
		}
		return pages.ARReturnReceiptForm(vm).Render(c.Context(), c)
	}
	return c.Redirect("/ar-return-receipts/"+strconv.FormatUint(uint64(created.ID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── GET /ar-return-receipts/:id ──────────────────────────────────────────────

func (s *Server) handleARReturnReceiptDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
	}
	if _, err := services.GetARReturnReceipt(s.DB, companyID, uint(id)); err != nil {
		return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
	}
	var full models.ARReturnReceipt
	if err := s.DB.
		Preload("Customer").Preload("Warehouse").Preload("CreditNote").
		Preload("Lines.ProductService").
		Where("company_id = ? AND id = ?", companyID, uint(id)).
		First(&full).Error; err != nil {
		return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
	}
	return pages.ARReturnReceiptDetail(pages.ARReturnReceiptDetailVM{
		HasCompany:    true,
		ReturnReceipt: full,
		Saved:         c.Query("saved") == "1",
		FormError:     c.Query("err"),
	}).Render(c.Context(), c)
}

// ── POST /ar-return-receipts/:id/post ────────────────────────────────────────

func (s *Server) handleARReturnReceiptPostAction(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	idRaw := c.Params("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
	}
	actor := ""
	if user != nil {
		actor = user.Email
	}
	if _, err := services.PostARReturnReceipt(s.DB, companyID, uint(id), actor, nil); err != nil {
		return c.Redirect("/ar-return-receipts/"+idRaw+"?err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	return c.Redirect("/ar-return-receipts/"+idRaw+"?saved=1", fiber.StatusSeeOther)
}

// ── POST /ar-return-receipts/:id/void ────────────────────────────────────────

func (s *Server) handleARReturnReceiptVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	idRaw := c.Params("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
	}
	actor := ""
	if user != nil {
		actor = user.Email
	}
	if _, err := services.VoidARReturnReceipt(s.DB, companyID, uint(id), actor, nil); err != nil {
		return c.Redirect("/ar-return-receipts/"+idRaw+"?err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	return c.Redirect("/ar-return-receipts/"+idRaw+"?saved=1", fiber.StatusSeeOther)
}

// ── POST /ar-return-receipts/:id/delete ──────────────────────────────────────

func (s *Server) handleARReturnReceiptDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	idRaw := c.Params("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
	}
	if err := services.DeleteARReturnReceipt(s.DB, companyID, uint(id)); err != nil {
		return c.Redirect("/ar-return-receipts/"+idRaw+"?err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	return c.Redirect("/ar-return-receipts", fiber.StatusSeeOther)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func parseDateOrNow(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Now()
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Now()
	}
	return t
}

func parseUintPtr(raw string) *uint {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || v == 0 {
		return nil
	}
	u := uint(v)
	return &u
}

func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}
