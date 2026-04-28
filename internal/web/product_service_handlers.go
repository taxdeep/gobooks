// 遵循project_guide.md
package web

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/searchprojection/producers"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleProductServices(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm := pages.ProductServicesVM{
		HasCompany:       true,
		Created:          c.Query("created") == "1",
		Updated:          c.Query("updated") == "1",
		InactiveOK:       c.Query("inactive") == "1",
		OpeningOK:        c.Query("opening") == "1",
		AdjustmentOK:     c.Query("adjustment") == "1",
		FormError:        strings.TrimSpace(c.Query("error")),
		FilterQ:          strings.TrimSpace(c.Query("q")),
		FilterType:       normaliseProductType(c.Query("type")),
		FilterStatus:     normaliseListStatus(c.Query("status")),
		FilterStockLevel: normaliseStockLevel(c.Query("stock")),
	}

	// Inactive count for the Status select option label — unfiltered.
	var inactiveCount int64
	s.DB.Model(&models.ProductService{}).
		Where("company_id = ? AND is_active = false", companyID).
		Count(&inactiveCount)
	vm.InactiveItemCount = int(inactiveCount)

	if c.Query("new") == "1" {
		vm.DrawerOpen = true
		vm.DrawerMode = "create"
	}

	if editRaw := strings.TrimSpace(c.Query("edit")); editRaw != "" {
		id64, err := strconv.ParseUint(editRaw, 10, 64)
		if err == nil && id64 > 0 {
			var item models.ProductService
			if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&item).Error; err == nil {
				vm.DrawerOpen = true
				vm.DrawerMode = "edit"
				vm.EditingID = uint(id64)
				vm.Name = item.Name
				vm.SKU = item.SKU
				vm.Type = string(item.Type)
				vm.StructureType = string(item.ItemStructureType)
				vm.Description = item.Description
				vm.DefaultPrice = item.DefaultPrice.StringFixed(2)
				vm.PurchasePrice = item.PurchasePrice.StringFixed(2)
				vm.RevenueAccountID = strconv.FormatUint(uint64(item.RevenueAccountID), 10)
				if item.COGSAccountID != nil {
					vm.COGSAccountID = strconv.FormatUint(uint64(*item.COGSAccountID), 10)
				}
				if item.InventoryAccountID != nil {
					vm.InventoryAccountID = strconv.FormatUint(uint64(*item.InventoryAccountID), 10)
				}
				if item.DefaultTaxCodeID != nil {
					vm.DefaultTaxCodeID = strconv.FormatUint(uint64(*item.DefaultTaxCodeID), 10)
				}
				// UOM (Phase U1) — populate display strings + the
				// has-stock flag so psUOMSection can disable the
				// stock-UOM change link when on-hand > 0.
				vm.StockUOM = item.StockUOM
				vm.SellUOM = item.SellUOM
				vm.SellUOMFactor = item.SellUOMFactor.StringFixed(2)
				vm.PurchaseUOM = item.PurchaseUOM
				vm.PurchaseUOMFactor = item.PurchaseUOMFactor.StringFixed(2)
				if item.IsStockItem {
					var sum struct{ Total float64 }
					_ = s.DB.Model(&models.InventoryBalance{}).
						Select("COALESCE(SUM(quantity_on_hand), 0) AS total").
						Where("company_id = ? AND item_id = ?", companyID, item.ID).
						Scan(&sum).Error
					vm.UOMHasStock = sum.Total != 0
				}
				vm.UOMOK = c.Query("uom_ok") == "1"
				vm.UOMError = strings.TrimSpace(c.Query("uom_error"))
				// Load bundle components for edit.
				if item.ItemStructureType == models.ItemStructureBundle {
					comps, _ := services.GetBundleComponents(s.DB, companyID, item.ID)
					for _, c := range comps {
						vm.Components = append(vm.Components, pages.BundleComponentRow{
							ComponentItemID: strconv.FormatUint(uint64(c.ComponentItemID), 10),
							Quantity:        c.Quantity.String(),
							ComponentName:   c.ComponentItem.Name,
						})
					}
				}
			}
		}
	}

	if err := s.loadProductServicesDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load dropdown data."
	}
	s.loadItemsForVM(companyID, &vm)

	return pages.ProductServices(vm).Render(c.Context(), c)
}

func (s *Server) handleProductServiceCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	name, sku, typeRaw, structureType, description, priceRaw, purchasePriceRaw,
		revenueIDRaw, cogsIDRaw, invAcctIDRaw, taxCodeIDRaw, compRows := parseItemForm(c)

	vm := pages.ProductServicesVM{
		HasCompany:         true,
		DrawerMode:         "create",
		DrawerOpen:         true,
		Name:               name,
		SKU:                sku,
		Type:               typeRaw,
		StructureType:      structureType,
		Description:        description,
		DefaultPrice:       priceRaw,
		PurchasePrice:      purchasePriceRaw,
		RevenueAccountID:   revenueIDRaw,
		COGSAccountID:      cogsIDRaw,
		InventoryAccountID: invAcctIDRaw,
		DefaultTaxCodeID:   taxCodeIDRaw,
		Components:         compRows,
	}
	_ = s.loadProductServicesDropdowns(companyID, &vm)

	psType, err := validateItemCommon(&vm, name, typeRaw, priceRaw, revenueIDRaw)
	if err != nil {
		// errors set on vm fields
	}
	purchasePrice := parseOptionalDecimal(purchasePriceRaw)
	cogsID := parseOptionalUint(cogsIDRaw)
	invAcctID := parseOptionalUint(invAcctIDRaw)
	taxCodeID := parseOptionalUint(taxCodeIDRaw)
	revenueID64, _ := strconv.ParseUint(revenueIDRaw, 10, 64)

	// Inventory-type validation.
	validateInventoryAccounts(&vm, psType, cogsID, invAcctID)

	// Company-scope validation: reject any account/tax IDs not owned by this company.
	if revenueID64 > 0 {
		s.validateItemAccountsCompanyScope(companyID, psType, uint(revenueID64), cogsID, invAcctID, taxCodeID, &vm)
	}

	if hasItemFormErrors(vm) {
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}

	// Duplicate name check.
	var count int64
	if err := s.DB.Model(&models.ProductService{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		vm.FormError = "Could not validate item name."
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.NameError = "An item with this name already exists for this company."
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}

	item := models.ProductService{
		CompanyID:          companyID,
		Name:               name,
		SKU:                sku,
		Type:               psType,
		Description:        description,
		DefaultPrice:       parseDecimalOrZero(priceRaw),
		PurchasePrice:      purchasePrice,
		RevenueAccountID:   uint(revenueID64),
		COGSAccountID:      cogsID,
		InventoryAccountID: invAcctID,
		DefaultTaxCodeID:   taxCodeID,
		IsActive:           true,
	}
	// Set structure type. Bundle forces non_inventory type.
	if structureType == string(models.ItemStructureBundle) {
		item.ItemStructureType = models.ItemStructureBundle
		item.Type = models.ProductServiceTypeNonInventory
		item.IsStockItem = false
		item.CanBeSold = true
	}
	item.ApplyTypeDefaults()

	// Validate bundle type + components if bundle.
	var bundleComps []models.ItemComponent
	if item.ItemStructureType == models.ItemStructureBundle {
		if err := services.ValidateBundleItemType(item.Type); err != nil {
			vm.FormError = err.Error()
			s.loadItemsForVM(companyID, &vm)
			return pages.ProductServices(vm).Render(c.Context(), c)
		}
		bundleComps = parseBundleComponents(compRows)
		if err := services.ValidateBundleComponents(s.DB, companyID, 0, bundleComps); err != nil {
			vm.ComponentError = err.Error()
			s.loadItemsForVM(companyID, &vm)
			return pages.ProductServices(vm).Render(c.Context(), c)
		}
	}

	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		if item.ItemStructureType == models.ItemStructureBundle {
			if err := services.SaveBundleComponents(tx, companyID, item.ID, bundleComps); err != nil {
				return err
			}
		}
		// PS.1: saving a stock item without a warehouse is a latent
		// Rule #4 trap — inventory movements need a warehouse. Auto-
		// provision a MAIN default on first stock item; idempotent
		// thereafter.
		if item.IsStockItem {
			if _, err := services.EnsureDefaultWarehouse(tx, companyID); err != nil {
				return fmt.Errorf("ensure default warehouse: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		vm.FormError = "Could not create item. Please try again."
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectProductService(c.Context(), s.DB, s.SearchProjector, companyID, item.ID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "product_service.created", "product_service", item.ID, actor, map[string]any{
		"name": name, "type": typeRaw, "structure": structureType, "company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/products-services?created=1", fiber.StatusSeeOther)
}

func (s *Server) handleProductServiceUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("item_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}
	itemID := uint(id64)

	var existing models.ProductService
	if err := s.DB.Where("id = ? AND company_id = ?", itemID, companyID).First(&existing).Error; err != nil {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}

	name, sku, typeRaw, structureType, description, priceRaw, purchasePriceRaw,
		revenueIDRaw, cogsIDRaw, invAcctIDRaw, taxCodeIDRaw, compRows := parseItemForm(c)

	vm := pages.ProductServicesVM{
		HasCompany:         true,
		DrawerMode:         "edit",
		DrawerOpen:         true,
		EditingID:          itemID,
		Name:               name,
		SKU:                sku,
		Type:               typeRaw,
		StructureType:      structureType,
		Description:        description,
		DefaultPrice:       priceRaw,
		PurchasePrice:      purchasePriceRaw,
		RevenueAccountID:   revenueIDRaw,
		COGSAccountID:      cogsIDRaw,
		InventoryAccountID: invAcctIDRaw,
		DefaultTaxCodeID:   taxCodeIDRaw,
		Components:         compRows,
	}
	_ = s.loadProductServicesDropdowns(companyID, &vm)

	psType, _ := validateItemCommon(&vm, name, typeRaw, priceRaw, revenueIDRaw)
	cogsID := parseOptionalUint(cogsIDRaw)
	invAcctID := parseOptionalUint(invAcctIDRaw)
	taxCodeID := parseOptionalUint(taxCodeIDRaw)
	revenueID64, _ := strconv.ParseUint(revenueIDRaw, 10, 64)

	validateInventoryAccounts(&vm, psType, cogsID, invAcctID)

	// Company-scope validation: reject any account/tax IDs not owned by this company.
	if revenueID64 > 0 {
		s.validateItemAccountsCompanyScope(companyID, psType, uint(revenueID64), cogsID, invAcctID, taxCodeID, &vm)
	}
	// Early exit: if scope validation set any error (including vm.FormError for tax code),
	// return before bundle validation so its vm.FormError = err.Error() cannot overwrite ours.
	if hasItemFormErrors(vm) {
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}

	// Validate bundle components.
	isBundle := structureType == string(models.ItemStructureBundle)
	var bundleComps []models.ItemComponent
	if isBundle {
		if err := services.ValidateBundleItemType(psType); err != nil {
			vm.FormError = err.Error()
			s.loadItemsForVM(companyID, &vm)
			return pages.ProductServices(vm).Render(c.Context(), c)
		}
		bundleComps = parseBundleComponents(compRows)
		if err := services.ValidateBundleComponents(s.DB, companyID, itemID, bundleComps); err != nil {
			vm.ComponentError = err.Error()
		}
	}

	if hasItemFormErrors(vm) {
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}

	desiredType := psType
	if isBundle {
		desiredType = models.ProductServiceTypeNonInventory
	}
	if err := services.ValidateSystemItemTypeChange(existing, desiredType); err != nil {
		vm.FormError = err.Error()
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}

	// Duplicate name check (exclude self).
	var count int64
	if err := s.DB.Model(&models.ProductService{}).
		Where("company_id = ? AND lower(name) = lower(?) AND id <> ?", companyID, name, itemID).
		Count(&count).Error; err != nil {
		vm.FormError = "Could not validate item name."
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.NameError = "An item with this name already exists for this company."
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}

	existing.Name = name
	existing.SKU = sku
	existing.Type = psType
	existing.Description = description
	existing.DefaultPrice = parseDecimalOrZero(priceRaw)
	existing.PurchasePrice = parseOptionalDecimal(purchasePriceRaw)
	existing.RevenueAccountID = uint(revenueID64)
	existing.COGSAccountID = cogsID
	existing.InventoryAccountID = invAcctID
	existing.DefaultTaxCodeID = taxCodeID
	if isBundle {
		existing.ItemStructureType = models.ItemStructureBundle
		existing.Type = models.ProductServiceTypeNonInventory
		existing.IsStockItem = false
		existing.CanBeSold = true
	} else {
		existing.ItemStructureType = models.ItemStructureSingle
	}

	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		if isBundle {
			if err := services.SaveBundleComponents(tx, companyID, itemID, bundleComps); err != nil {
				return err
			}
		} else if existing.ItemStructureType == models.ItemStructureSingle {
			// If switching away from bundle, clear components.
			tx.Where("company_id = ? AND parent_item_id = ?", companyID, itemID).
				Delete(&models.ItemComponent{})
		}
		// PS.1: parallel to the Create path — if the saved item is a
		// stock item, ensure the company has a default warehouse.
		// Idempotent.
		if existing.IsStockItem {
			if _, err := services.EnsureDefaultWarehouse(tx, companyID); err != nil {
				return fmt.Errorf("ensure default warehouse: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		vm.FormError = "Could not update item. Please try again."
		s.loadItemsForVM(companyID, &vm)
		return pages.ProductServices(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectProductService(c.Context(), s.DB, s.SearchProjector, companyID, existing.ID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "product_service.updated", "product_service", existing.ID, actor, map[string]any{
		"name": name, "type": typeRaw, "company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/products-services?updated=1", fiber.StatusSeeOther)
}

func (s *Server) handleProductServiceInactive(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("item_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}

	var item models.ProductService
	if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&item).Error; err != nil {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}
	if !item.IsActive {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}
	if err := services.ValidateSystemItemInactivation(item); err != nil {
		return redirectErr(c, "/products-services", err.Error())
	}
	s.DB.Model(&item).Update("is_active", false)
	// Re-project with status=inactive so search results fade the row out
	// (kept in the index so reactivation flows still find it).
	_ = producers.ProjectProductService(c.Context(), s.DB, s.SearchProjector, companyID, item.ID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "product_service.deactivated", "product_service", item.ID, actor, map[string]any{
		"name": item.Name, "company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/products-services?inactive=1", fiber.StatusSeeOther)
}

// ── Inventory opening / adjustment handlers ──────────────────────────────────

func (s *Server) handleInventoryOpening(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	itemIDRaw := strings.TrimSpace(c.FormValue("item_id"))
	itemID64, err := strconv.ParseUint(itemIDRaw, 10, 64)
	if err != nil || itemID64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}

	qtyRaw := strings.TrimSpace(c.FormValue("opening_qty"))
	costRaw := strings.TrimSpace(c.FormValue("opening_cost"))
	dateRaw := strings.TrimSpace(c.FormValue("opening_date"))
	whIDRaw := strings.TrimSpace(c.FormValue("warehouse_id"))

	qty, _ := decimal.NewFromString(qtyRaw)
	unitCost, _ := decimal.NewFromString(costRaw)
	asOfDate := parseTimeOrToday(dateRaw)

	var openingWHID *uint
	if wid64, err := strconv.ParseUint(whIDRaw, 10, 64); err == nil && wid64 > 0 {
		wid := uint(wid64)
		openingWHID = &wid
	}

	_, err = services.CreateOpeningBalance(s.DB, services.OpeningBalanceInput{
		CompanyID:   companyID,
		ItemID:      uint(itemID64),
		Quantity:    qty,
		UnitCost:    unitCost,
		AsOfDate:    asOfDate,
		WarehouseID: openingWHID,
	})
	if err != nil {
		return c.Redirect("/products-services?edit="+itemIDRaw+"&openerr=1", fiber.StatusSeeOther)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "inventory.opening", "product_service", uint(itemID64), actor, map[string]any{
		"quantity": qty.String(), "unit_cost": unitCost.String(), "company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/products-services?opening=1", fiber.StatusSeeOther)
}

func (s *Server) handleInventoryAdjustment(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	itemIDRaw := strings.TrimSpace(c.FormValue("item_id"))
	itemID64, err := strconv.ParseUint(itemIDRaw, 10, 64)
	if err != nil || itemID64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}

	qtyRaw := strings.TrimSpace(c.FormValue("adj_qty"))
	costRaw := strings.TrimSpace(c.FormValue("adj_cost"))
	dateRaw := strings.TrimSpace(c.FormValue("adj_date"))
	note := strings.TrimSpace(c.FormValue("adj_note"))
	adjWHIDRaw := strings.TrimSpace(c.FormValue("warehouse_id"))

	qtyDelta, _ := decimal.NewFromString(qtyRaw)
	var unitCost *decimal.Decimal
	if costRaw != "" {
		uc, err := decimal.NewFromString(costRaw)
		if err == nil {
			unitCost = &uc
		}
	}

	movDate := parseTimeOrToday(dateRaw)

	var adjWHID *uint
	if wid64, err := strconv.ParseUint(adjWHIDRaw, 10, 64); err == nil && wid64 > 0 {
		wid := uint(wid64)
		adjWHID = &wid
	}

	_, err = services.CreateAdjustment(s.DB, services.AdjustmentInput{
		CompanyID:     companyID,
		ItemID:        uint(itemID64),
		QuantityDelta: qtyDelta,
		UnitCost:      unitCost,
		MovementDate:  movDate,
		Note:          note,
		WarehouseID:   adjWHID,
	})
	if err != nil {
		return c.Redirect("/products-services?edit="+itemIDRaw+"&adjerr=1", fiber.StatusSeeOther)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "inventory.adjustment", "product_service", uint(itemID64), actor, map[string]any{
		"quantity_delta": qtyDelta.String(), "note": note, "company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/products-services?adjustment=1", fiber.StatusSeeOther)
}

// ── Shared helpers ───────────────────────────────────────────────────────────

func parseItemForm(c *fiber.Ctx) (name, sku, typeRaw, structureType, description, priceRaw, purchasePriceRaw, revenueIDRaw, cogsIDRaw, invAcctIDRaw, taxCodeIDRaw string, components []pages.BundleComponentRow) {
	name = strings.TrimSpace(c.FormValue("name"))
	sku = strings.TrimSpace(c.FormValue("sku"))
	typeRaw = strings.TrimSpace(c.FormValue("type"))
	structureType = strings.TrimSpace(c.FormValue("structure_type"))
	description = strings.TrimSpace(c.FormValue("description"))
	priceRaw = strings.TrimSpace(c.FormValue("default_price"))
	purchasePriceRaw = strings.TrimSpace(c.FormValue("purchase_price"))
	revenueIDRaw = strings.TrimSpace(c.FormValue("revenue_account_id"))
	cogsIDRaw = strings.TrimSpace(c.FormValue("cogs_account_id"))
	invAcctIDRaw = strings.TrimSpace(c.FormValue("inventory_account_id"))
	taxCodeIDRaw = strings.TrimSpace(c.FormValue("default_tax_code_id"))

	// Parse bundle components.
	if structureType == string(models.ItemStructureBundle) {
		countRaw := strings.TrimSpace(c.FormValue("component_count"))
		count, _ := strconv.Atoi(countRaw)
		for i := 0; i < count; i++ {
			cid := strings.TrimSpace(c.FormValue(fmt.Sprintf("comp_item[%d]", i)))
			qty := strings.TrimSpace(c.FormValue(fmt.Sprintf("comp_qty[%d]", i)))
			if cid == "" && qty == "" {
				continue
			}
			components = append(components, pages.BundleComponentRow{
				ComponentItemID: cid,
				Quantity:        qty,
			})
		}
	}
	return
}

func validateItemCommon(vm *pages.ProductServicesVM, name, typeRaw, priceRaw, revenueIDRaw string) (models.ProductServiceType, error) {
	if name == "" {
		vm.NameError = "Name is required."
	}
	psType, typeErr := models.ParseProductServiceType(typeRaw)
	if typeErr != nil {
		vm.TypeError = "Type is required."
	}
	if priceRaw != "" {
		p, err := decimal.NewFromString(priceRaw)
		if err != nil || p.IsNegative() {
			vm.DefaultPriceError = "Enter a valid non-negative amount (e.g. 150.00)."
		}
	}
	if _, err := strconv.ParseUint(revenueIDRaw, 10, 64); err != nil {
		vm.RevenueAccountIDError = "Account code is required."
	}
	return psType, typeErr
}

func validateInventoryAccounts(vm *pages.ProductServicesVM, psType models.ProductServiceType, cogsID, invAcctID *uint) {
	if psType == models.ProductServiceTypeInventory {
		if invAcctID == nil {
			vm.InventoryAccountIDError = "Inventory asset account is required for inventory items."
		}
		if cogsID == nil {
			vm.COGSAccountIDError = "Cost of goods sold account is required for inventory items."
		}
	} else {
		// Non-inventory types: clear inventory-specific accounts.
		// We don't error — we just ignore them on save.
	}
}

// validateItemAccountsCompanyScope checks that the submitted account and tax
// IDs all belong to companyID. This is the server-side guard against forged
// cross-company POST requests — the UI only shows company-owned options, but
// we must not trust raw form values.
func (s *Server) validateItemAccountsCompanyScope(
	companyID uint,
	psType models.ProductServiceType,
	revenueID uint,
	cogsID, invAcctID, taxCodeID *uint,
	vm *pages.ProductServicesVM,
) {
	// Account Code: for Other Charge items the account must be an Expense or COGS account;
	// for all other types it must be a Revenue account.
	var accountCount int64
	if psType == models.ProductServiceTypeOtherCharge {
		if err := s.DB.Model(&models.Account{}).
			Where("id = ? AND company_id = ? AND is_active = true AND root_account_type IN ?",
				revenueID, companyID, []string{string(models.RootCostOfSales), string(models.RootExpense)}).
			Count(&accountCount).Error; err != nil || accountCount == 0 {
			vm.RevenueAccountIDError = "Account code is not valid for this company (must be an Expense or Cost of Sales account)."
		}
	} else {
		if err := s.DB.Model(&models.Account{}).
			Where("id = ? AND company_id = ? AND is_active = true AND root_account_type = ?",
				revenueID, companyID, models.RootRevenue).
			Count(&accountCount).Error; err != nil || accountCount == 0 {
			vm.RevenueAccountIDError = "Account code is not valid for this company."
		}
	}

	// COGS account: if provided, must be a cost-of-sales account owned by this company.
	// Mirrors loadProductServicesDropdowns: root_account_type = RootCostOfSales.
	if cogsID != nil {
		var cogsCount int64
		if err := s.DB.Model(&models.Account{}).
			Where("id = ? AND company_id = ? AND is_active = true AND root_account_type = ?",
				*cogsID, companyID, models.RootCostOfSales).
			Count(&cogsCount).Error; err != nil || cogsCount == 0 {
			vm.COGSAccountIDError = "Cost of goods sold account is not valid for this company."
		}
	}

	// Inventory asset account: if provided, must be a Detail=Inventory account owned by this company.
	// Mirrors loadProductServicesDropdowns: detail_account_type = DetailInventory.
	if invAcctID != nil {
		var invCount int64
		if err := s.DB.Model(&models.Account{}).
			Where("id = ? AND company_id = ? AND is_active = true AND detail_account_type = ?",
				*invAcctID, companyID, models.DetailInventory).
			Count(&invCount).Error; err != nil || invCount == 0 {
			vm.InventoryAccountIDError = "Inventory asset account is not valid for this company."
		}
	}

	// Default tax code: if provided, must belong to this company and be applicable to sales
	// (scope = 'sales' or 'both'). Purchase-only codes cannot be used as a product default
	// because products are used on sales invoices where purchase-only codes are invalid.
	if taxCodeID != nil {
		var taxCount int64
		if err := s.DB.Model(&models.TaxCode{}).
			Where("id = ? AND company_id = ? AND is_active = true AND scope != ?",
				*taxCodeID, companyID, models.TaxScopePurchase).
			Count(&taxCount).Error; err != nil || taxCount == 0 {
			vm.FormError = "Default tax code is not valid for this company or does not apply to sales."
		}
	}
}

func hasItemFormErrors(vm pages.ProductServicesVM) bool {
	return vm.NameError != "" || vm.TypeError != "" || vm.DefaultPriceError != "" ||
		vm.RevenueAccountIDError != "" || vm.COGSAccountIDError != "" || vm.InventoryAccountIDError != "" ||
		vm.ComponentError != "" || vm.FormError != ""
}

// parseBundleComponents converts form rows into model components.
func parseBundleComponents(rows []pages.BundleComponentRow) []models.ItemComponent {
	var comps []models.ItemComponent
	for _, r := range rows {
		cid := parseOptionalUint(r.ComponentItemID)
		if cid == nil {
			continue
		}
		qty := parseOptionalDecimal(r.Quantity)
		comps = append(comps, models.ItemComponent{
			ComponentItemID: *cid,
			Quantity:        qty,
		})
	}
	return comps
}

func parseOptionalUint(s string) *uint {
	if s == "" {
		return nil
	}
	id64, err := strconv.ParseUint(s, 10, 64)
	if err != nil || id64 == 0 {
		return nil
	}
	id := uint(id64)
	return &id
}

func parseOptionalDecimal(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func parseDecimalOrZero(s string) decimal.Decimal {
	return parseOptionalDecimal(s)
}

func parseTimeOrToday(s string) time.Time {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Now()
}

func (s *Server) loadProductServicesDropdowns(companyID uint, vm *pages.ProductServicesVM) error {
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND root_account_type = ?", companyID, models.RootRevenue).
		Order("code asc").
		Find(&vm.RevenueAccounts).Error; err != nil {
		return err
	}
	// Other Charge items link to an Expense or Cost-of-Sales account.
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND root_account_type IN ?", companyID,
			[]string{string(models.RootCostOfSales), string(models.RootExpense)}).
		Order("code asc").
		Find(&vm.OtherChargeAccounts).Error; err != nil {
		return err
	}
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND root_account_type = ?", companyID, models.RootCostOfSales).
		Order("code asc").
		Find(&vm.COGSAccounts).Error; err != nil {
		return err
	}
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND detail_account_type = ?", companyID, models.DetailInventory).
		Order("code asc").
		Find(&vm.InventoryAccounts).Error; err != nil {
		return err
	}
	// Only show sales/both tax codes — products are used on sales invoices, so
	// purchase-only codes are never valid choices. This matches the backend validation.
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND scope != ?", companyID, models.TaxScopePurchase).
		Order("name asc").
		Find(&vm.TaxCodes).Error; err != nil {
		return err
	}
	// Inventory items for bundle component picker.
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND type = ? AND item_structure_type = ?",
			companyID, models.ProductServiceTypeInventory, models.ItemStructureSingle).
		Order("name asc").
		Find(&vm.InventoryItems).Error; err != nil {
		return err
	}
	// Warehouses for opening balance / adjustment routing (ignore error — optional feature).
	vm.Warehouses, _ = services.ListWarehouses(s.DB, companyID)
	return nil
}

func (s *Server) loadItemsForVM(companyID uint, vm *pages.ProductServicesVM) {
	listQuery := s.DB.Preload("RevenueAccount").Where("company_id = ?", companyID)

	// Status filter — empty (not set by caller) preserves the original
	// "show every item including inactive" behaviour so POST-handler
	// rerenders (validation errors, etc.) don't suddenly hide rows. The
	// main GET handler sets FilterStatus = "active" explicitly.
	switch vm.FilterStatus {
	case "active":
		listQuery = listQuery.Where("is_active = ?", true)
	case "inactive":
		listQuery = listQuery.Where("is_active = ?", false)
	}

	// Stock filter forces type = "inventory" — services / non-inventory
	// don't track quantities, so a stock-level filter on them is
	// semantically meaningless. Honoured even when the operator sets
	// Type = "All" because the stock filter is the more specific intent.
	stockFilter := vm.FilterStockLevel
	if stockFilter != "" {
		listQuery = listQuery.Where("type = ?", string(models.ProductServiceTypeInventory))
	} else if vm.FilterType != "" {
		listQuery = listQuery.Where("type = ?", vm.FilterType)
	}
	if vm.FilterQ != "" {
		like := "%" + strings.ToLower(vm.FilterQ) + "%"
		listQuery = listQuery.Where("LOWER(name) LIKE ? OR LOWER(sku) LIKE ?", like, like)
	}

	var items []models.ProductService
	if err := listQuery.Order("name asc").Find(&items).Error; err == nil {
		vm.Items = items
	}

	// Load valuations for inventory items (single query).
	rawVals := services.ListItemValuations(s.DB, companyID)
	vm.Balances = make(map[uint]string)
	vm.Valuations = make(map[uint]pages.ItemValuationVM)
	for itemID, v := range rawVals {
		vm.Balances[itemID] = v.QuantityOnHand
		vm.Valuations[itemID] = pages.ItemValuationVM{
			QuantityOnHand: v.QuantityOnHand,
			AverageCost:    v.AverageCost,
			InventoryValue: v.InventoryValue,
		}
	}

	// Apply stock filter post-query: ListItemValuations returns rows for
	// items that have a balance row. "in_stock" = positive qty; "out_of_stock"
	// = no balance OR non-positive qty. Done in Go because qty_on_hand is a
	// computed aggregate (inventory_balances.location_ref="" rollup) rather
	// than a column on product_services — adding a join + HAVING here would
	// be heavier than just walking the items slice.
	if stockFilter != "" {
		filtered := items[:0]
		for _, item := range items {
			val, hasBalance := rawVals[item.ID]
			inStock := false
			if hasBalance {
				if qty, err := decimal.NewFromString(val.QuantityOnHand); err == nil && qty.IsPositive() {
					inStock = true
				}
			}
			switch stockFilter {
			case "in_stock":
				if inStock {
					filtered = append(filtered, item)
				}
			case "out_of_stock":
				if !inStock {
					filtered = append(filtered, item)
				}
			}
		}
		vm.Items = filtered
	}
}

// ── UOM (Phase U1 — 2026-04-25) ─────────────────────────────────────────────

// handleProductServiceUOMSave persists the Sell + Purchase UOMs + factors
// for a single inventory item.  Stock UOM is NOT touched here (separate
// endpoint /products-services/stock-uom which has its own on-hand guard).
//
// On success: redirect back to the edit drawer with ?uom_ok=1.
// On failure: redirect with ?uom_error=<message> URL-escaped so the
//             error renders inline in the UOM section banner.
func (s *Server) handleProductServiceUOMSave(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	itemID64, err := strconv.ParseUint(strings.TrimSpace(c.FormValue("item_id")), 10, 64)
	if err != nil || itemID64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}
	itemID := uint(itemID64)

	sellUOM := c.FormValue("sell_uom")
	purchaseUOM := c.FormValue("purchase_uom")
	sellFactor, _ := decimal.NewFromString(strings.TrimSpace(c.FormValue("sell_uom_factor")))
	purchaseFactor, _ := decimal.NewFromString(strings.TrimSpace(c.FormValue("purchase_uom_factor")))

	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	if err := services.SaveProductUOMs(s.DB, services.SaveProductUOMsInput{
		CompanyID:         companyID,
		ItemID:            itemID,
		SellUOM:           sellUOM,
		SellUOMFactor:     sellFactor,
		PurchaseUOM:       purchaseUOM,
		PurchaseUOMFactor: purchaseFactor,
		Actor:             actor,
		ActorUserID:       &uid,
	}); err != nil {
		return c.Redirect(
			fmt.Sprintf("/products-services?edit=%d&uom_error=%s", itemID, encodeQuery(err.Error())),
			fiber.StatusSeeOther,
		)
	}

	return c.Redirect(
		fmt.Sprintf("/products-services?edit=%d&uom_ok=1", itemID),
		fiber.StatusSeeOther,
	)
}

// handleProductServiceStockUOMChange transitions the item's Stock UOM
// after the on-hand guard. Resets Sell + Purchase UOMs to match (factors
// to 1) so the operator can't carry stale conversion factors against
// the new stock unit.
func (s *Server) handleProductServiceStockUOMChange(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	itemID64, err := strconv.ParseUint(strings.TrimSpace(c.FormValue("item_id")), 10, 64)
	if err != nil || itemID64 == 0 {
		return c.Redirect("/products-services", fiber.StatusSeeOther)
	}
	itemID := uint(itemID64)

	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	if err := services.ChangeStockUOM(s.DB, services.ChangeStockUOMInput{
		CompanyID:   companyID,
		ItemID:      itemID,
		NewStockUOM: c.FormValue("stock_uom"),
		Actor:       actor,
		ActorUserID: &uid,
	}); err != nil {
		return c.Redirect(
			fmt.Sprintf("/products-services?edit=%d&uom_error=%s", itemID, encodeQuery(err.Error())),
			fiber.StatusSeeOther,
		)
	}
	return c.Redirect(
		fmt.Sprintf("/products-services?edit=%d&uom_ok=1", itemID),
		fiber.StatusSeeOther,
	)
}

// encodeQuery is a tiny URL-encoder shim — the redirect path needs the
// error message escaped, but we don't want to drag in net/url at the top
// of this file just for one call.
func encodeQuery(s string) string {
	r := strings.NewReplacer(
		" ", "+", "&", "%26", "?", "%3F", "#", "%23",
		"+", "%2B", "%", "%25",
	)
	return r.Replace(s)
}
