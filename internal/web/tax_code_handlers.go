// 遵循产品需求 v1.0
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

// handleCompanySalesTaxGet renders Settings > Company > Sales Tax.
func (s *Server) handleCompanySalesTaxGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm := pages.SalesTaxVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanySalesTax(),
		Created:    c.Query("created") == "1",
		Updated:    c.Query("updated") == "1",
		InactiveOK: c.Query("inactive") == "1",
	}

	if c.Query("new") == "1" {
		vm.DrawerOpen = true
		vm.DrawerMode = "create"
	}

	if editRaw := strings.TrimSpace(c.Query("edit")); editRaw != "" {
		if id64, err := strconv.ParseUint(editRaw, 10, 64); err == nil && id64 > 0 {
			var tc models.TaxCode
			if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&tc).Error; err == nil {
				vm.DrawerOpen = true
				vm.DrawerMode = "edit"
				vm.EditingID = uint(id64)
				vm.Name = tc.Name
				// Store rate as percentage string for display.
				vm.Rate = tc.Rate.Mul(decimal.NewFromInt(100)).StringFixed(4)
				// Trim trailing zeros.
				vm.Rate = trimTrailingZeros(vm.Rate)
				vm.RecoveryMode = string(tc.RecoveryMode)
				vm.RecoveryRate = tc.RecoveryRate.StringFixed(0)
				if tc.RecoveryRate.IsZero() {
					vm.RecoveryRate = ""
				}
				vm.SalesTaxAccountID = strconv.FormatUint(uint64(tc.SalesTaxAccountID), 10)
				if tc.PurchaseRecoverableAccountID != nil {
					vm.PurchaseRecoverableAccountID = strconv.FormatUint(uint64(*tc.PurchaseRecoverableAccountID), 10)
				}
			}
		}
	}

	if err := s.loadSalesTaxDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load account data."
	}
	s.loadTaxCodeItems(companyID, &vm)

	return pages.CompanySalesTax(vm).Render(c.Context(), c)
}

// handleTaxCodeCreate processes POST /settings/company/sales-tax.
func (s *Server) handleTaxCodeCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw := parseTaxCodeForm(c)

	vm := pages.SalesTaxVM{
		HasCompany:                   true,
		Breadcrumb:                   breadcrumbSettingsCompanySalesTax(),
		DrawerOpen:                   true,
		DrawerMode:                   "create",
		Name:                         name,
		Rate:                         rateRaw,
		RecoveryMode:                 recoveryModeRaw,
		RecoveryRate:                 recoveryRateRaw,
		SalesTaxAccountID:            salesAcctRaw,
		PurchaseRecoverableAccountID: purchaseAcctRaw,
	}
	_ = s.loadSalesTaxDropdowns(companyID, &vm)

	rate, salesAcctID, purchaseAcctID, valid := validateTaxCodeForm(&vm, name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw)
	if !valid {
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}

	// Duplicate name check.
	var count int64
	if err := s.DB.Model(&models.TaxCode{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		vm.FormError = "Could not validate name."
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.NameError = "A tax code with this name already exists for this company."
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}

	recoveryRate, _ := decimal.NewFromString(recoveryRateRaw)
	tc := models.TaxCode{
		CompanyID:                    companyID,
		Name:                         name,
		Rate:                         rate,
		Scope:                        models.TaxScopeBoth,
		RecoveryMode:                 models.TaxRecoveryMode(recoveryModeRaw),
		RecoveryRate:                 recoveryRate,
		SalesTaxAccountID:            salesAcctID,
		PurchaseRecoverableAccountID: purchaseAcctID,
		IsActive:                     true,
	}
	if err := s.DB.Create(&tc).Error; err != nil {
		vm.FormError = "Could not create tax code. Please try again."
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "tax_code.created", "tax_code", tc.ID, actor, map[string]any{
		"name":       name,
		"rate":       rate.StringFixed(6),
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/settings/company/sales-tax?created=1", fiber.StatusSeeOther)
}

// handleTaxCodeUpdate processes POST /settings/company/sales-tax/update.
func (s *Server) handleTaxCodeUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("tax_code_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/settings/company/sales-tax", fiber.StatusSeeOther)
	}
	taxCodeID := uint(id64)

	var existing models.TaxCode
	if err := s.DB.Where("id = ? AND company_id = ?", taxCodeID, companyID).First(&existing).Error; err != nil {
		return c.Redirect("/settings/company/sales-tax", fiber.StatusSeeOther)
	}

	name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw := parseTaxCodeForm(c)

	vm := pages.SalesTaxVM{
		HasCompany:                   true,
		Breadcrumb:                   breadcrumbSettingsCompanySalesTax(),
		DrawerOpen:                   true,
		DrawerMode:                   "edit",
		EditingID:                    taxCodeID,
		Name:                         name,
		Rate:                         rateRaw,
		RecoveryMode:                 recoveryModeRaw,
		RecoveryRate:                 recoveryRateRaw,
		SalesTaxAccountID:            salesAcctRaw,
		PurchaseRecoverableAccountID: purchaseAcctRaw,
	}
	_ = s.loadSalesTaxDropdowns(companyID, &vm)

	rate, salesAcctID, purchaseAcctID, valid := validateTaxCodeForm(&vm, name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw)
	if !valid {
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}

	recoveryRate, _ := decimal.NewFromString(recoveryRateRaw)

	existing.Name = name
	existing.Rate = rate
	existing.RecoveryMode = models.TaxRecoveryMode(recoveryModeRaw)
	existing.RecoveryRate = recoveryRate
	existing.SalesTaxAccountID = salesAcctID
	existing.PurchaseRecoverableAccountID = purchaseAcctID

	if err := s.DB.Save(&existing).Error; err != nil {
		vm.FormError = "Could not update tax code. Please try again."
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "tax_code.updated", "tax_code", existing.ID, actor, map[string]any{
		"name":       name,
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/settings/company/sales-tax?updated=1", fiber.StatusSeeOther)
}

// handleTaxCodeDeactivate processes POST /settings/company/sales-tax/deactivate.
func (s *Server) handleTaxCodeDeactivate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("tax_code_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/settings/company/sales-tax", fiber.StatusSeeOther)
	}

	var tc models.TaxCode
	if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&tc).Error; err != nil {
		return c.Redirect("/settings/company/sales-tax", fiber.StatusSeeOther)
	}
	if !tc.IsActive {
		return c.Redirect("/settings/company/sales-tax", fiber.StatusSeeOther)
	}

	if err := s.DB.Model(&tc).Update("is_active", false).Error; err != nil {
		return c.Redirect("/settings/company/sales-tax", fiber.StatusSeeOther)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "tax_code.deactivated", "tax_code", tc.ID, actor, map[string]any{
		"name":       tc.Name,
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/settings/company/sales-tax?inactive=1", fiber.StatusSeeOther)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Server) loadSalesTaxDropdowns(companyID uint, vm *pages.SalesTaxVM) error {
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND root_account_type = 'liability'", companyID).
		Order("code asc").
		Find(&vm.LiabilityAccounts).Error; err != nil {
		return err
	}
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND root_account_type = 'asset'", companyID).
		Order("code asc").
		Find(&vm.AssetAccounts).Error; err != nil {
		return err
	}
	return nil
}

func (s *Server) loadTaxCodeItems(companyID uint, vm *pages.SalesTaxVM) {
	var items []models.TaxCode
	if err := s.DB.
		Where("company_id = ?", companyID).
		Order("is_active desc, name asc").
		Find(&items).Error; err == nil {
		vm.Items = items
	}
}

// parseTaxCodeForm extracts all tax code form fields from a POST request.
func parseTaxCodeForm(c *fiber.Ctx) (name, rate, recoveryMode, recoveryRate, salesAcct, purchaseAcct string) {
	name = strings.TrimSpace(c.FormValue("name"))
	rate = strings.TrimSpace(c.FormValue("rate"))
	recoveryMode = strings.TrimSpace(c.FormValue("recovery_mode"))
	recoveryRate = strings.TrimSpace(c.FormValue("recovery_rate"))
	salesAcct = strings.TrimSpace(c.FormValue("sales_tax_account_id"))
	purchaseAcct = strings.TrimSpace(c.FormValue("purchase_recoverable_account_id"))
	return
}

// validateTaxCodeForm validates fields and returns parsed values.
// Sets field errors on vm and returns valid=false if anything fails.
func validateTaxCodeForm(
	vm *pages.SalesTaxVM,
	name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw string,
) (rate decimal.Decimal, salesAcctID uint, purchaseAcctID *uint, valid bool) {
	valid = true

	if name == "" {
		vm.NameError = "Name is required."
		valid = false
	}

	// Rate: accept percentage string (e.g. "5" or "5.00"), convert to fraction.
	if rateRaw == "" {
		vm.RateError = "Rate is required."
		valid = false
	} else {
		rPct, err := decimal.NewFromString(rateRaw)
		if err != nil || rPct.IsNegative() || rPct.GreaterThan(decimal.NewFromInt(100)) {
			vm.RateError = "Enter a valid rate between 0 and 100."
			valid = false
		} else {
			rate = rPct.Div(decimal.NewFromInt(100))
		}
	}

	switch recoveryModeRaw {
	case "none", "full", "partial":
		// valid
	default:
		// default to none if blank (recovery_mode select always has a value)
		if recoveryModeRaw == "" {
			recoveryModeRaw = "none"
			vm.RecoveryMode = "none"
		} else {
			vm.RecoveryModeError = "Invalid recovery mode."
			valid = false
		}
	}

	if recoveryModeRaw == "partial" {
		if recoveryRateRaw == "" {
			vm.RecoveryRateError = "Recovery rate is required for partial recovery."
			valid = false
		} else {
			rr, err := decimal.NewFromString(recoveryRateRaw)
			if err != nil || rr.IsNegative() || rr.GreaterThan(decimal.NewFromInt(100)) {
				vm.RecoveryRateError = "Enter a percentage between 0 and 100."
				valid = false
			}
		}
	}

	if salesAcctRaw == "" {
		vm.SalesTaxAccountIDError = "Sales tax account is required."
		valid = false
	} else {
		id64, err := strconv.ParseUint(salesAcctRaw, 10, 64)
		if err != nil || id64 == 0 {
			vm.SalesTaxAccountIDError = "Sales tax account is required."
			valid = false
		} else {
			salesAcctID = uint(id64)
		}
	}

	if purchaseAcctRaw != "" {
		id64, err := strconv.ParseUint(purchaseAcctRaw, 10, 64)
		if err == nil && id64 > 0 {
			id := uint(id64)
			purchaseAcctID = &id
		}
	}

	return rate, salesAcctID, purchaseAcctID, valid
}

// trimTrailingZeros removes trailing zeros after a decimal point (e.g. "5.0000" → "5").
func trimTrailingZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
