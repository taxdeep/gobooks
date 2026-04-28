// 遵循project_guide.md
package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleInventoryLedger renders the inventory ledger page for a single item.
// GET /products-services/:id/ledger
func (s *Server) handleInventoryLedger(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := c.Params("id")
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}
	itemID := uint(id64)

	// Load item with account preloads.
	var item models.ProductService
	if err := s.DB.
		Preload("RevenueAccount").
		Preload("COGSAccount").
		Preload("InventoryAccount").
		Where("id = ? AND company_id = ?", itemID, companyID).
		First(&item).Error; err != nil {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}

	// Allow both inventory items and bundle items to have a ledger page.
	isBundle := item.ItemStructureType == models.ItemStructureBundle
	if item.Type != models.ProductServiceTypeInventory && !isBundle {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}

	// Snapshot (only meaningful for inventory items, not bundles).
	snapshot := &services.InventorySnapshot{}
	if !isBundle {
		snapshot, err = services.GetInventorySnapshot(s.DB, companyID, itemID)
		if err != nil {
			snapshot = &services.InventorySnapshot{}
		}
	}

	// Pagination.
	page := 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil && p > 0 {
		page = p
	}
	pageSize := 50
	offset := (page - 1) * pageSize

	// Movements: for bundles there are no direct movements, but we show anyway (will be empty).
	movements, total, err := services.ListMovements(s.DB, companyID, itemID, pageSize, offset)
	if err != nil {
		movements = nil
	}

	// Build account labels.
	revLabel := accountLabel(item.RevenueAccount)
	cogsLabel := ""
	if item.COGSAccount != nil {
		cogsLabel = accountLabel(*item.COGSAccount)
	}
	invLabel := ""
	if item.InventoryAccount != nil {
		invLabel = accountLabel(*item.InventoryAccount)
	}

	// Load bundle components for display.
	var compDisplay []pages.BundleComponentDisplay
	if isBundle {
		comps, _ := services.GetBundleComponents(s.DB, companyID, itemID)
		for _, c := range comps {
			compDisplay = append(compDisplay, pages.BundleComponentDisplay{
				Name:     c.ComponentItem.Name,
				SKU:      c.ComponentItem.SKU,
				Quantity: c.Quantity.String(),
				IsActive: c.ComponentItem.IsActive,
			})
		}
	}

	vm := pages.InventoryLedgerVM{
		HasCompany:            true,
		Item:                  item,
		IsBundle:              isBundle,
		Snapshot:              *snapshot,
		RevenueAccountLabel:   revLabel,
		COGSAccountLabel:      cogsLabel,
		InventoryAccountLabel: invLabel,
		Components:            compDisplay,
		Movements:             movements,
		TotalMovements:        int64(total),
		Page:                  page,
		PageSize:              pageSize,
	}

	return pages.InventoryLedger(vm).Render(c.Context(), c)
}

func accountLabel(a models.Account) string {
	if a.Code != "" {
		return a.Code + " · " + a.Name
	}
	return a.Name
}
