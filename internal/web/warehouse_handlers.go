// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleWarehouseList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	ws, _ := services.ListWarehouses(s.DB, companyID)
	return pages.Warehouses(pages.WarehousesVM{
		HasCompany: true,
		Warehouses: ws,
		Created:    c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New ───────────────────────────────────────────────────────────────────────

func (s *Server) handleWarehouseNew(c *fiber.Ctx) error {
	_, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	return pages.WarehouseDetail(pages.WarehouseDetailVM{HasCompany: true}).Render(c.Context(), c)
}

// ── Detail (edit form) ────────────────────────────────────────────────────────

func (s *Server) handleWarehouseDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/warehouses", fiber.StatusSeeOther)
	}

	w, err := services.GetWarehouse(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/warehouses", fiber.StatusSeeOther)
	}

	return pages.WarehouseDetail(pages.WarehouseDetailVM{
		HasCompany: true,
		Warehouse:  *w,
		Saved:      c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// ── Save (create) ─────────────────────────────────────────────────────────────

func (s *Server) handleWarehouseCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	in := parseWarehouseInput(c)
	w, err := services.CreateWarehouse(s.DB, companyID, in)
	if err != nil {
		return pages.WarehouseDetail(pages.WarehouseDetailVM{
			HasCompany: true,
			FormError:  err.Error(),
		}).Render(c.Context(), c)
	}
	return c.Redirect("/warehouses/"+strconv.FormatUint(uint64(w.ID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Save (update) ─────────────────────────────────────────────────────────────

func (s *Server) handleWarehouseUpdate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/warehouses", fiber.StatusSeeOther)
	}

	in := parseWarehouseInput(c)
	w, err := services.UpdateWarehouse(s.DB, companyID, id, in)
	if err != nil {
		existing, _ := services.GetWarehouse(s.DB, companyID, id)
		vm := pages.WarehouseDetailVM{HasCompany: true, FormError: err.Error()}
		if existing != nil {
			vm.Warehouse = *existing
		}
		return pages.WarehouseDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/warehouses/"+strconv.FormatUint(uint64(w.ID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func parseWarehouseInput(c *fiber.Ctx) services.WarehouseInput {
	osValue, _ := decimal.NewFromString(strings.TrimSpace(c.FormValue("over_shipment_value")))
	if osValue.IsNegative() {
		osValue = decimal.Zero
	}
	return services.WarehouseInput{
		Code:                strings.TrimSpace(c.FormValue("code")),
		Name:                strings.TrimSpace(c.FormValue("name")),
		Description:         strings.TrimSpace(c.FormValue("description")),
		IsDefault:           c.FormValue("is_default") == "on",
		IsActive:            c.FormValue("is_active") == "on",
		AddressLine1:        strings.TrimSpace(c.FormValue("address_line1")),
		City:                strings.TrimSpace(c.FormValue("city")),
		Country:             strings.TrimSpace(c.FormValue("country")),
		OverShipmentEnabled: c.FormValue("over_shipment_enabled") == "on",
		OverShipmentMode:    models.OverShipmentMode(strings.TrimSpace(c.FormValue("over_shipment_mode"))),
		OverShipmentValue:   osValue,
	}
}
