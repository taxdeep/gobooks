// 遵循project_guide.md
package web

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
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

	rate, salesAcctID, purchaseAcctID, valid := validateTaxCodeForm(s.DB, companyID, &vm, name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw)
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

	recoveryRate := parseRecoveryRateDecimal(recoveryModeRaw, recoveryRateRaw)
	tc := models.TaxCode{
		CompanyID:                    companyID,
		Name:                         name,
		Code:                         name,
		TaxType:                      "taxable",
		Rate:                         rate,
		Scope:                        models.TaxScopeBoth,
		RecoveryMode:                 models.TaxRecoveryMode(recoveryModeRaw),
		RecoveryRate:                 recoveryRate,
		SalesTaxAccountID:            salesAcctID,
		PurchaseRecoverableAccountID: purchaseAcctID,
		IsActive:                     true,
	}
	if err := s.DB.Create(&tc).Error; err != nil {
		logging.L().Warn("tax_code create failed", "err", err.Error(), "company_id", companyID, "name", name)
		vm.FormError = taxCodeSaveErrorMessage(err, false)
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

	rate, salesAcctID, purchaseAcctID, valid := validateTaxCodeForm(s.DB, companyID, &vm, name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw)
	if !valid {
		s.loadTaxCodeItems(companyID, &vm)
		return pages.CompanySalesTax(vm).Render(c.Context(), c)
	}

	recoveryRate := parseRecoveryRateDecimal(recoveryModeRaw, recoveryRateRaw)

	existing.Name = name
	existing.Code = name
	existing.TaxType = "taxable"
	existing.Rate = rate
	existing.RecoveryMode = models.TaxRecoveryMode(recoveryModeRaw)
	existing.RecoveryRate = recoveryRate
	existing.SalesTaxAccountID = salesAcctID
	existing.PurchaseRecoverableAccountID = purchaseAcctID

	if err := s.DB.Save(&existing).Error; err != nil {
		logging.L().Warn("tax_code update failed", "err", err.Error(), "company_id", companyID, "tax_code_id", taxCodeID)
		vm.FormError = taxCodeSaveErrorMessage(err, true)
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
		Where("company_id = ? AND is_active = true AND root_account_type = 'liability'", companyID).
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
	db *gorm.DB,
	companyID uint,
	vm *pages.SalesTaxVM,
	name, rateRaw, recoveryModeRaw, recoveryRateRaw, salesAcctRaw, purchaseAcctRaw string,
) (rate decimal.Decimal, salesAcctID uint, purchaseAcctID *uint, valid bool) {
	valid = true

	var taxNameRe = regexp.MustCompile(`^[A-Za-z0-9%\s]+$`)
	if name == "" {
		vm.NameError = "Name is required."
		valid = false
	} else if !taxNameRe.MatchString(name) {
		vm.NameError = "Name may only contain letters, numbers, spaces, and '%'."
		valid = false
	}

	// Rate: accept percentage string (e.g. "5" or "5.00"), round to 2dp, convert to fraction.
	if rateRaw == "" {
		vm.RateError = "Rate is required."
		valid = false
	} else {
		rPct, err := decimal.NewFromString(rateRaw)
		if err != nil || rPct.IsNegative() || rPct.GreaterThan(decimal.NewFromInt(100)) {
			vm.RateError = "Enter a valid number between 0 and 100."
			valid = false
		} else {
			rPct = rPct.Round(2)
			vm.Rate = rPct.StringFixed(2) // echo back formatted value
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
		} else {
			vm.PurchaseRecoverableAccountIDError = "Purchase recoverable account must be a valid account."
			valid = false
		}
	}

	if valid {
		validateTaxCodeAccounts(db, companyID, vm, salesAcctID, purchaseAcctID, &valid)
	}

	return rate, salesAcctID, purchaseAcctID, valid
}

func validateTaxCodeAccounts(db *gorm.DB, companyID uint, vm *pages.SalesTaxVM, salesAcctID uint, purchaseAcctID *uint, valid *bool) {
	var salesAcct models.Account
	if err := db.Where("id = ? AND company_id = ? AND is_active = true", salesAcctID, companyID).First(&salesAcct).Error; err != nil {
		vm.SalesTaxAccountIDError = "Sales tax account must be an active liability account in this company."
		*valid = false
	} else if salesAcct.RootAccountType != models.RootLiability {
		vm.SalesTaxAccountIDError = "Sales tax account must be a liability account."
		*valid = false
	}

	if purchaseAcctID == nil {
		return
	}

	var purchaseAcct models.Account
	if err := db.Where("id = ? AND company_id = ? AND is_active = true", *purchaseAcctID, companyID).First(&purchaseAcct).Error; err != nil {
		vm.PurchaseRecoverableAccountIDError = "Purchase recoverable account must be an active liability account in this company."
		*valid = false
		return
	}
	if purchaseAcct.RootAccountType != models.RootLiability {
		vm.PurchaseRecoverableAccountIDError = "Purchase recoverable account must be a liability account."
		*valid = false
	}
}

func parseRecoveryRateDecimal(recoveryModeRaw, recoveryRateRaw string) decimal.Decimal {
	if recoveryModeRaw != "partial" {
		return decimal.Zero
	}
	rr, err := decimal.NewFromString(strings.TrimSpace(recoveryRateRaw))
	if err != nil {
		return decimal.Zero
	}
	return rr
}

// taxCodeSaveErrorMessage maps common DB errors to a short UI hint; full error is always logged.
func taxCodeSaveErrorMessage(err error, update bool) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	base := "Could not create tax code. "
	if update {
		base = "Could not update tax code. "
	}
	switch {
	case strings.Contains(msg, "tax_codes") && strings.Contains(msg, "foreign key"):
		return base + "The selected GL account was rejected by the database. Pick another sales tax (or ITC) account from the dropdowns."
	case strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint"):
		return base + "A conflicting tax code already exists (name or code may need to be unique)."
	case strings.Contains(msg, "violates not-null constraint"):
		return base + "The database schema may be out of date. Apply migration 008_tax_code_redesign.sql or redeploy so tax_codes matches the current app model."
	default:
		return base + "Please try again. (Details were written to the server log.)"
	}
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
