// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleTransferList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filter := strings.TrimSpace(c.Query("status"))
	ts, _ := services.ListTransfers(s.DB, companyID, filter)
	ws, _ := services.ListWarehouses(s.DB, companyID)

	return pages.WarehouseTransfers(pages.WarehouseTransfersVM{
		HasCompany: true,
		Transfers:  ts,
		Warehouses: ws,
		Filter:     filter,
		Created:    c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New ───────────────────────────────────────────────────────────────────────

func (s *Server) handleTransferNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.WarehouseTransferDetailVM{HasCompany: true}
	vm.Transfer.TransferDate = time.Now()
	if fromID, err := strconv.ParseUint(strings.TrimSpace(c.Query("from_warehouse_id")), 10, 64); err == nil {
		vm.Transfer.FromWarehouseID = uint(fromID)
	}
	if toID, err := strconv.ParseUint(strings.TrimSpace(c.Query("to_warehouse_id")), 10, 64); err == nil {
		vm.Transfer.ToWarehouseID = uint(toID)
	}
	s.loadTransferFormData(companyID, &vm)
	return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleTransferDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/inventory/transfers", fiber.StatusSeeOther)
	}

	tr, err := services.GetTransfer(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/inventory/transfers", fiber.StatusSeeOther)
	}

	vm := pages.WarehouseTransferDetailVM{
		HasCompany: true,
		Transfer:   *tr,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Cancelled:  c.Query("cancelled") == "1",
	}
	s.loadTransferFormData(companyID, &vm)
	return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
}

// ── Save (create) ─────────────────────────────────────────────────────────────

func (s *Server) handleTransferCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	actor, _ := depositActor(c)
	in, err := parseTransferInput(c)
	if err != nil {
		vm := pages.WarehouseTransferDetailVM{HasCompany: true, FormError: err.Error()}
		s.loadTransferFormData(companyID, &vm)
		return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
	}
	in.CreatedByEmail = actor

	tr, err := services.CreateTransfer(s.DB, companyID, in)
	if err != nil {
		vm := pages.WarehouseTransferDetailVM{HasCompany: true, FormError: err.Error()}
		s.loadTransferFormData(companyID, &vm)
		return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/inventory/transfers/"+strconv.FormatUint(uint64(tr.ID), 10)+"?created=1", fiber.StatusSeeOther)
}

// ── Save (update) ─────────────────────────────────────────────────────────────

func (s *Server) handleTransferUpdate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/inventory/transfers", fiber.StatusSeeOther)
	}

	in, err := parseTransferInput(c)
	if err != nil {
		existing, _ := services.GetTransfer(s.DB, companyID, id)
		vm := pages.WarehouseTransferDetailVM{HasCompany: true, FormError: err.Error()}
		if existing != nil {
			vm.Transfer = *existing
		}
		s.loadTransferFormData(companyID, &vm)
		return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
	}

	tr, err := services.UpdateTransfer(s.DB, companyID, id, in)
	if err != nil {
		existing, _ := services.GetTransfer(s.DB, companyID, id)
		vm := pages.WarehouseTransferDetailVM{HasCompany: true, FormError: err.Error()}
		if existing != nil {
			vm.Transfer = *existing
		}
		s.loadTransferFormData(companyID, &vm)
		return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/inventory/transfers/"+strconv.FormatUint(uint64(tr.ID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleTransferPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/inventory/transfers", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if postErr := services.PostTransfer(s.DB, companyID, id, actor, actorID); postErr != nil {
		tr, _ := services.GetTransfer(s.DB, companyID, id)
		vm := pages.WarehouseTransferDetailVM{HasCompany: true, FormError: postErr.Error()}
		if tr != nil {
			vm.Transfer = *tr
		}
		s.loadTransferFormData(companyID, &vm)
		return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/inventory/transfers/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Cancel ────────────────────────────────────────────────────────────────────

func (s *Server) handleTransferCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/inventory/transfers", fiber.StatusSeeOther)
	}

	if cancelErr := services.CancelTransfer(s.DB, companyID, id); cancelErr != nil {
		tr, _ := services.GetTransfer(s.DB, companyID, id)
		vm := pages.WarehouseTransferDetailVM{HasCompany: true, FormError: cancelErr.Error()}
		if tr != nil {
			vm.Transfer = *tr
		}
		s.loadTransferFormData(companyID, &vm)
		return pages.WarehouseTransferDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/inventory/transfers/"+strconv.FormatUint(uint64(id), 10)+"?cancelled=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadTransferFormData(companyID uint, vm *pages.WarehouseTransferDetailVM) {
	vm.Warehouses, _ = services.ListWarehouses(s.DB, companyID)
	s.DB.Where("company_id = ? AND is_active = true AND type = ?",
		companyID, models.ProductServiceTypeInventory).
		Order("name asc").Find(&vm.Items)
}

func parseTransferInput(c *fiber.Ctx) (services.TransferInput, error) {
	fromIDStr := strings.TrimSpace(c.FormValue("from_warehouse_id"))
	toIDStr := strings.TrimSpace(c.FormValue("to_warehouse_id"))
	itemIDStr := strings.TrimSpace(c.FormValue("item_id"))
	qtyStr := strings.TrimSpace(c.FormValue("quantity"))
	dateStr := strings.TrimSpace(c.FormValue("transfer_date"))

	fromID, _ := strconv.ParseUint(fromIDStr, 10, 64)
	toID, _ := strconv.ParseUint(toIDStr, 10, 64)
	itemID, _ := strconv.ParseUint(itemIDStr, 10, 64)
	qty, _ := decimal.NewFromString(qtyStr)

	var transferDate time.Time
	if dateStr != "" {
		transferDate, _ = time.Parse("2006-01-02", dateStr)
	}

	return services.TransferInput{
		FromWarehouseID: uint(fromID),
		ToWarehouseID:   uint(toID),
		ItemID:          uint(itemID),
		Quantity:        qty,
		TransferDate:    transferDate,
		Notes:           strings.TrimSpace(c.FormValue("notes")),
		Reference:       strings.TrimSpace(c.FormValue("reference")),
	}, nil
}
