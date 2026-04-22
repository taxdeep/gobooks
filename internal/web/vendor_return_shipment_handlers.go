// 遵循project_guide.md
package web

// vendor_return_shipment_handlers.go — Phase I slice I.6b.4 UI
// handlers for the VendorReturnShipment physical-truth document.
//
// Routes bound in routes.go:
//
//   GET  /vendor-return-shipments                  — list
//   GET  /vendor-return-shipments/new              — create form (?vendor_credit_note_id=X pre-fill, Q4 shortcut)
//   POST /vendor-return-shipments/save             — create draft from form
//   GET  /vendor-return-shipments/:id              — detail (post / void / delete)
//   POST /vendor-return-shipments/:id/post         — post (rail-aware per I.6b.2)
//   POST /vendor-return-shipments/:id/void         — void (Q5 document-local)
//   POST /vendor-return-shipments/:id/delete       — delete (draft only)
//
// UI label is "Return to Vendor" (Q2) — the user-visible strings on
// all templates use that wording; internal URLs / model names use
// vendor_return_shipment to avoid collision with models.VendorReturn.

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── GET /vendor-return-shipments ─────────────────────────────────────────────

func (s *Server) handleVRSList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	rows, err := services.ListVendorReturnShipments(s.DB, companyID, services.ListVendorReturnShipmentsFilter{Limit: 200})
	if err != nil {
		return pages.VRSList(pages.VRSListVM{
			HasCompany: true, FormError: err.Error(),
		}).Render(c.Context(), c)
	}
	return pages.VRSList(pages.VRSListVM{
		HasCompany: true,
		Rows:       rows,
		Saved:      c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// ── GET /vendor-return-shipments/new ─────────────────────────────────────────
//
// Q4 shortcut: ?vendor_credit_note_id=X pre-fills from the VCN's
// stock-item lines. Pure-service / fee lines are excluded.

func (s *Server) handleVRSNewGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	var vendors []models.Vendor
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&vendors)

	var warehouses []models.Warehouse
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&warehouses)

	vm := pages.VRSFormVM{
		HasCompany: true,
		Vendors:    vendors,
		Warehouses: warehouses,
	}

	if vcnIDStr := c.Query("vendor_credit_note_id"); vcnIDStr != "" {
		if vcnID, err := strconv.ParseUint(vcnIDStr, 10, 64); err == nil && vcnID > 0 {
			var vcn models.VendorCreditNote
			if err := s.DB.Preload("Lines.ProductService").
				Where("id = ? AND company_id = ?", uint(vcnID), companyID).
				First(&vcn).Error; err == nil {
				vm.VendorCreditNoteID = uint(vcnID)
				vm.VendorCreditNoteNumber = vcn.CreditNoteNumber
				if vcn.VendorID != 0 {
					vid := vcn.VendorID
					vm.VendorID = &vid
				}
				if len(warehouses) > 0 {
					vm.WarehouseID = warehouses[0].ID
				}
				for _, ln := range vcn.Lines {
					if ln.ProductService == nil || !ln.ProductService.IsStockItem {
						continue
					}
					psID := uint(0)
					if ln.ProductServiceID != nil {
						psID = *ln.ProductServiceID
					}
					vm.PrefilledLines = append(vm.PrefilledLines, pages.VRSFormLine{
						VendorCreditNoteLineID: ln.ID,
						ProductServiceID:       psID,
						ProductName:            vrsProductDisplayName(ln.ProductService),
						Description:            ln.Description,
						Qty:                    ln.Qty,
					})
				}
			}
		}
	}

	return pages.VRSForm(vm).Render(c.Context(), c)
}

func vrsProductDisplayName(p *models.ProductService) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// ── POST /vendor-return-shipments/save ───────────────────────────────────────

func (s *Server) handleVRSSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	shipDate := parseDateOrNow(c.FormValue("ship_date"))
	vendorID := parseUintPtr(c.FormValue("vendor_id"))
	warehouseID, _ := strconv.ParseUint(c.FormValue("warehouse_id"), 10, 64)
	vcnIDPtr := parseUintPtr(c.FormValue("vendor_credit_note_id"))

	descs := c.Context().PostArgs().PeekMulti("description[]")
	products := c.Context().PostArgs().PeekMulti("product_service_id[]")
	qtys := c.Context().PostArgs().PeekMulti("qty[]")
	vcnLineIDs := c.Context().PostArgs().PeekMulti("vendor_credit_note_line_id[]")

	n := len(products)
	if len(qtys) < n {
		n = len(qtys)
	}
	lines := make([]services.CreateVendorReturnShipmentLineInput, 0, n)
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
		var vcnLineID *uint
		if i < len(vcnLineIDs) {
			if v, err := strconv.ParseUint(string(vcnLineIDs[i]), 10, 64); err == nil && v > 0 {
				u := uint(v)
				vcnLineID = &u
			}
		}
		lines = append(lines, services.CreateVendorReturnShipmentLineInput{
			SortOrder:              i + 1,
			ProductServiceID:       uint(psID),
			Description:            desc,
			Qty:                    qty,
			VendorCreditNoteLineID: vcnLineID,
		})
	}

	in := services.CreateVendorReturnShipmentInput{
		CompanyID:                  companyID,
		VendorReturnShipmentNumber: strings.TrimSpace(c.FormValue("vendor_return_shipment_number")),
		VendorID:                   vendorID,
		WarehouseID:                uint(warehouseID),
		ShipDate:                   shipDate,
		Memo:                       strings.TrimSpace(c.FormValue("memo")),
		Reference:                  strings.TrimSpace(c.FormValue("reference")),
		VendorCreditNoteID:         vcnIDPtr,
		Lines:                      lines,
	}

	created, err := services.CreateVendorReturnShipment(s.DB, in)
	if err != nil {
		var vendors []models.Vendor
		s.DB.Where("company_id = ? AND is_active = true", companyID).
			Order("name asc").Find(&vendors)
		var warehouses []models.Warehouse
		s.DB.Where("company_id = ? AND is_active = true", companyID).
			Order("name asc").Find(&warehouses)
		vm := pages.VRSFormVM{
			HasCompany:                 true,
			Vendors:                    vendors,
			Warehouses:                 warehouses,
			VendorID:                   vendorID,
			WarehouseID:                uint(warehouseID),
			VendorReturnShipmentNumber: in.VendorReturnShipmentNumber,
			Memo:                       in.Memo,
			Reference:                  in.Reference,
			VendorCreditNoteID:         derefUint(vcnIDPtr),
			FormError:                  err.Error(),
		}
		for _, ln := range lines {
			vm.PrefilledLines = append(vm.PrefilledLines, pages.VRSFormLine{
				VendorCreditNoteLineID: derefUint(ln.VendorCreditNoteLineID),
				ProductServiceID:       ln.ProductServiceID,
				Description:            ln.Description,
				Qty:                    ln.Qty,
			})
		}
		return pages.VRSForm(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-return-shipments/"+strconv.FormatUint(uint64(created.ID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── GET /vendor-return-shipments/:id ─────────────────────────────────────────

func (s *Server) handleVRSDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
	}
	if _, err := services.GetVendorReturnShipment(s.DB, companyID, uint(id)); err != nil {
		return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
	}
	var full models.VendorReturnShipment
	if err := s.DB.
		Preload("Vendor").Preload("Warehouse").Preload("VendorCreditNote").
		Preload("Lines.ProductService").
		Where("company_id = ? AND id = ?", companyID, uint(id)).
		First(&full).Error; err != nil {
		return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
	}
	return pages.VRSDetail(pages.VRSDetailVM{
		HasCompany:    true,
		ReturnShipment: full,
		Saved:         c.Query("saved") == "1",
		FormError:     c.Query("err"),
	}).Render(c.Context(), c)
}

// ── POST /vendor-return-shipments/:id/post ───────────────────────────────────

func (s *Server) handleVRSPostAction(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	idRaw := c.Params("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
	}
	actor := ""
	if user != nil {
		actor = user.Email
	}
	if _, err := services.PostVendorReturnShipment(s.DB, companyID, uint(id), actor, nil); err != nil {
		return c.Redirect("/vendor-return-shipments/"+idRaw+"?err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	return c.Redirect("/vendor-return-shipments/"+idRaw+"?saved=1", fiber.StatusSeeOther)
}

// ── POST /vendor-return-shipments/:id/void ───────────────────────────────────

func (s *Server) handleVRSVoidAction(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	idRaw := c.Params("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
	}
	actor := ""
	if user != nil {
		actor = user.Email
	}
	if _, err := services.VoidVendorReturnShipment(s.DB, companyID, uint(id), actor, nil); err != nil {
		return c.Redirect("/vendor-return-shipments/"+idRaw+"?err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	return c.Redirect("/vendor-return-shipments/"+idRaw+"?saved=1", fiber.StatusSeeOther)
}

// ── POST /vendor-return-shipments/:id/delete ─────────────────────────────────

func (s *Server) handleVRSDeleteAction(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	idRaw := c.Params("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
	}
	if err := services.DeleteVendorReturnShipment(s.DB, companyID, uint(id)); err != nil {
		return c.Redirect("/vendor-return-shipments/"+idRaw+"?err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	return c.Redirect("/vendor-return-shipments", fiber.StatusSeeOther)
}

// helpers shared with ARR: parseDateOrNow, parseUintPtr, derefUint
// are defined in ar_return_receipt_handlers.go (same package).
