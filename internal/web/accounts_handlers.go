// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// setAccountFieldRecommendationSourcesFromForm stores client-reported apply vs manual for analytics only.
// It does not validate account data and must not be used as proof that values came from rules or AI.
// Create/edit handlers always validate submitted fields independently (see handleAccountCreate/Update).
func setAccountFieldRecommendationSourcesFromForm(acc *models.Account, c *fiber.Ctx) {
	j, err := models.BuildAccountFieldRecommendationSourcesJSON(
		c.FormValue("reco_account_name_source"),
		c.FormValue("reco_account_code_source"),
		c.FormValue("reco_gifi_code_source"),
	)
	if err != nil {
		return
	}
	acc.FieldRecommendationSourcesJSON = &j
}

func (s *Server) handleAccounts(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	c.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Set("Pragma", "no-cache")

	codeLen, _ := companyAccountCodeLength(s.DB, companyID)

	vm := pages.AccountsVM{
		HasCompany:        true,
		Active:            "Accounts",
		Created:           c.Query("created") == "1",
		Updated:           c.Query("updated") == "1",
		InactiveOK:        c.Query("inactive") == "1",
		AccountCodeLength: codeLen,
		ActiveCompanyID:   companyID,
	}

	if c.Query("new") == "1" {
		vm.AccountDrawerOpen = true
		vm.DrawerMode = "create"
	}

	if editRaw := strings.TrimSpace(c.Query("edit")); editRaw != "" {
		id64, err := strconv.ParseUint(editRaw, 10, 64)
		if err == nil && id64 > 0 {
			var acc models.Account
			if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&acc).Error; err == nil {
				vm.AccountDrawerOpen = true
				vm.DrawerMode = "edit"
				vm.EditingAccountID = uint(id64)
				vm.Code = acc.Code
				vm.Name = acc.Name
				vm.Root = string(acc.RootAccountType)
				vm.Detail = string(acc.DetailAccountType)
				vm.GifiCode = acc.GifiCode
			}
		}
	}

	var accounts []models.Account
	if err := s.DB.Where("company_id = ?", companyID).Order("code asc").Find(&accounts).Error; err != nil {
		return pages.Accounts(pages.AccountsVM{
			HasCompany:        true,
			Active:            "Accounts",
			FormError:         "Could not load accounts.",
			Accounts:          []models.Account{},
			AccountCodeLength: codeLen,
			ActiveCompanyID:   companyID,
		}).Render(c.Context(), c)
	}
	vm.Accounts = accounts

	return pages.Accounts(vm).Render(c.Context(), c)
}

func (s *Server) handleAccountCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	codeLen, err := companyAccountCodeLength(s.DB, companyID)
	if err != nil {
		return pages.Accounts(pages.AccountsVM{
			HasCompany:      true,
			Active:          "Accounts",
			FormError:       "Could not load company settings.",
			ActiveCompanyID: companyID,
		}).Render(c.Context(), c)
	}

	code := strings.TrimSpace(c.FormValue("code"))
	name := models.NormalizeAccountNameForSave(c.FormValue("name"))
	rootRaw := strings.TrimSpace(c.FormValue("root_account_type"))
	detailRaw := strings.TrimSpace(c.FormValue("detail_account_type"))
	gifiTrim := models.TrimGifiForStorage(c.FormValue("gifi_code"))

	vm := pages.AccountsVM{
		HasCompany:        true,
		Active:            "Accounts",
		DrawerMode:        "create",
		Code:              code,
		Name:              name,
		Root:              rootRaw,
		Detail:            detailRaw,
		GifiCode:          gifiTrim,
		AccountCodeLength: codeLen,
		ActiveCompanyID:   companyID,
	}

	root, rerr := models.ParseRootAccountType(rootRaw)
	if rerr != nil {
		vm.RootError = "Root type is required."
	}
	detail, derr := models.ParseDetailAccountType(detailRaw)
	if derr != nil {
		vm.DetailError = "Detail type is required."
	}
	if rerr == nil && derr == nil {
		if err := models.ValidateRootDetail(root, detail); err != nil {
			vm.DetailError = err.Error()
		}
	}

	if code == "" {
		vm.CodeError = "Code is required."
	} else if rerr != nil {
		if err := models.ValidateAccountCodeStrict(code, codeLen); err != nil {
			vm.CodeError = err.Error()
		}
	} else if err := models.ValidateAccountCodeAndClassification(code, codeLen, root); err != nil {
		vm.CodeError = err.Error()
	}
	if name == "" {
		vm.NameError = "Name is required."
	}
	if err := models.ValidateGifiCode(gifiTrim); err != nil {
		vm.GifiError = err.Error()
	}

	accounts, listErr := s.accountsForCompany(companyID)
	if listErr != nil {
		vm.FormError = "Could not load accounts."
		vm.AccountDrawerOpen = true
	} else {
		vm.Accounts = accounts
	}

	if vm.CodeError != "" || vm.NameError != "" || vm.RootError != "" || vm.DetailError != "" || vm.GifiError != "" {
		vm.AccountDrawerOpen = true
		return pages.Accounts(vm).Render(c.Context(), c)
	}

	var count int64
	if err := s.DB.Model(&models.Account{}).Where("company_id = ? AND code = ?", companyID, code).Count(&count).Error; err != nil {
		vm.FormError = "Could not validate account code."
		vm.AccountDrawerOpen = true
		return pages.Accounts(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.CodeError = "This code is already in use for this company."
		vm.AccountDrawerOpen = true
		return pages.Accounts(vm).Render(c.Context(), c)
	}

	acc := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              name,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
		GifiCode:          gifiTrim,
	}
	setAccountFieldRecommendationSourcesFromForm(&acc, c)

	if err := s.DB.Create(&acc).Error; err != nil {
		vm.FormError = "Could not create account. Please try again."
		vm.AccountDrawerOpen = true
		return pages.Accounts(vm).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	createdMeta := map[string]any{
		"code":       acc.Code,
		"name":       acc.Name,
		"root":       acc.RootAccountType,
		"detail":     acc.DetailAccountType,
		"company_id": companyID,
	}
	if acc.FieldRecommendationSourcesJSON != nil {
		createdMeta["field_recommendation_sources"] = *acc.FieldRecommendationSourcesJSON
	}
	services.TryWriteAuditLogWithContext(s.DB, "account.created", "account", acc.ID, actor, createdMeta, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/accounts?created=1")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/accounts?created=1", fiber.StatusSeeOther)
}

func (s *Server) handleAccountUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	codeLen, err := companyAccountCodeLength(s.DB, companyID)
	if err != nil {
		return pages.Accounts(pages.AccountsVM{
			HasCompany:      true,
			Active:          "Accounts",
			FormError:       "Could not load company settings.",
			ActiveCompanyID: companyID,
		}).Render(c.Context(), c)
	}

	idRaw := strings.TrimSpace(c.FormValue("account_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/accounts", fiber.StatusSeeOther)
	}
	accountID := uint(id64)

	name := models.NormalizeAccountNameForSave(c.FormValue("name"))
	rootRaw := strings.TrimSpace(c.FormValue("root_account_type"))
	detailRaw := strings.TrimSpace(c.FormValue("detail_account_type"))
	gifiTrim := models.TrimGifiForStorage(c.FormValue("gifi_code"))

	var existing models.Account
	if err := s.DB.Where("id = ? AND company_id = ?", accountID, companyID).First(&existing).Error; err != nil {
		return c.Redirect("/accounts", fiber.StatusSeeOther)
	}

	vm := pages.AccountsVM{
		HasCompany:        true,
		Active:            "Accounts",
		DrawerMode:        "edit",
		EditingAccountID:  accountID,
		Code:              existing.Code,
		Name:              name,
		Root:              rootRaw,
		Detail:            detailRaw,
		GifiCode:          gifiTrim,
		AccountCodeLength: codeLen,
		ActiveCompanyID:   companyID,
	}

	if name == "" {
		vm.NameError = "Name is required."
	}
	root, rerr := models.ParseRootAccountType(rootRaw)
	if rerr != nil {
		vm.RootError = "Root type is required."
	}
	detail, derr := models.ParseDetailAccountType(detailRaw)
	if derr != nil {
		vm.DetailError = "Detail type is required."
	}
	if rerr == nil && derr == nil {
		if err := models.ValidateRootDetail(root, detail); err != nil {
			vm.DetailError = err.Error()
		} else if err := models.ValidateAccountCodeAndClassification(existing.Code, codeLen, root); err != nil {
			vm.RootError = err.Error()
		}
	}
	if err := models.ValidateGifiCode(gifiTrim); err != nil {
		vm.GifiError = err.Error()
	}

	accounts, listErr := s.accountsForCompany(companyID)
	if listErr != nil {
		vm.FormError = "Could not load accounts."
		vm.AccountDrawerOpen = true
	} else {
		vm.Accounts = accounts
	}

	if vm.NameError != "" || vm.RootError != "" || vm.DetailError != "" || vm.GifiError != "" {
		vm.AccountDrawerOpen = true
		return pages.Accounts(vm).Render(c.Context(), c)
	}

	existing.Name = name
	existing.RootAccountType = root
	existing.DetailAccountType = detail
	existing.GifiCode = gifiTrim
	setAccountFieldRecommendationSourcesFromForm(&existing, c)
	if err := s.DB.Save(&existing).Error; err != nil {
		vm.FormError = "Could not update account. Please try again."
		vm.AccountDrawerOpen = true
		return pages.Accounts(vm).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	updatedMeta := map[string]any{
		"code":       existing.Code,
		"name":       existing.Name,
		"root":       existing.RootAccountType,
		"detail":     existing.DetailAccountType,
		"company_id": companyID,
	}
	if existing.FieldRecommendationSourcesJSON != nil {
		updatedMeta["field_recommendation_sources"] = *existing.FieldRecommendationSourcesJSON
	}
	services.TryWriteAuditLogWithContext(s.DB, "account.updated", "account", existing.ID, actor, updatedMeta, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/accounts?updated=1", fiber.StatusSeeOther)
}

func (s *Server) handleAccountInactive(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("account_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/accounts", fiber.StatusSeeOther)
	}
	accountID := uint(id64)

	var acc models.Account
	if err := s.DB.Where("id = ? AND company_id = ?", accountID, companyID).First(&acc).Error; err != nil {
		return c.Redirect("/accounts", fiber.StatusSeeOther)
	}
	if !acc.IsActive {
		return c.Redirect("/accounts", fiber.StatusSeeOther)
	}

	if err := s.DB.Model(&acc).Update("is_active", false).Error; err != nil {
		return c.Redirect("/accounts", fiber.StatusSeeOther)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "account.deactivated", "account", acc.ID, actor, map[string]any{
		"code":       acc.Code,
		"name":       acc.Name,
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/accounts?inactive=1", fiber.StatusSeeOther)
}

func (s *Server) accountsForCompany(companyID uint) ([]models.Account, error) {
	var accounts []models.Account
	err := s.DB.Where("company_id = ?", companyID).Order("code asc").Find(&accounts).Error
	return accounts, err
}
