// 遵循project_guide.md
package web

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleARAPControlGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	mappings, _ := services.ListARAPControlMappings(s.DB, companyID)
	accounts := s.loadARAPAccounts(companyID)

	vm := pages.ARAPControlVM{
		HasCompany: true,
		Mappings:   mappings,
		Accounts:   accounts,
		DrawerOpen: c.Query("drawer") == "create",
		Saved:      c.Query("saved") == "1",
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "AR/AP Control Accounts"},
		},
	}
	return pages.ARAPControlHub(vm).Render(c.Context(), c)
}

func (s *Server) handleARAPControlCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	docType := models.ARAPControlDocType(strings.TrimSpace(c.FormValue("document_type")))
	currency := strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	accountIDRaw := strings.TrimSpace(c.FormValue("control_account_id"))
	notesVal := strings.TrimSpace(c.FormValue("notes"))

	accountID, _ := strconv.ParseUint(accountIDRaw, 10, 64)

	_, createErr := services.CreateARAPControlMapping(s.DB, services.CreateARAPControlMappingInput{
		CompanyID:        companyID,
		BookID:           0,
		DocumentType:     docType,
		CurrencyCode:     currency,
		ControlAccountID: uint(accountID),
		Notes:            notesVal,
	})

	if createErr == nil {
		return c.Redirect("/settings/ar-ap-control?saved=1", fiber.StatusSeeOther)
	}

	mappings, _ := services.ListARAPControlMappings(s.DB, companyID)
	accounts := s.loadARAPAccounts(companyID)

	vm := pages.ARAPControlVM{
		HasCompany:     true,
		Mappings:       mappings,
		Accounts:       accounts,
		DrawerOpen:     true,
		FieldDocType:   string(docType),
		FieldCurrency:  currency,
		FieldAccountID: accountIDRaw,
		FieldNotes:     notesVal,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "AR/AP Control Accounts"},
		},
	}

	if errors.Is(createErr, services.ErrDuplicateControlMapping) {
		vm.FormError = "A mapping for this document type and currency already exists."
	} else {
		vm.FormError = createErr.Error()
	}
	return pages.ARAPControlHub(vm).Render(c.Context(), c)
}

func (s *Server) handleARAPControlDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	mappingID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/settings/ar-ap-control", fiber.StatusSeeOther)
	}

	_ = services.DeleteARAPControlMapping(s.DB, companyID, uint(mappingID))
	return c.Redirect("/settings/ar-ap-control?saved=1", fiber.StatusSeeOther)
}

func (s *Server) handleARAPControlSeedDefaults(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	_ = services.SeedDefaultARAPMappings(s.DB, companyID)
	return c.Redirect("/settings/ar-ap-control?saved=1", fiber.StatusSeeOther)
}

// loadARAPAccounts returns all active AR and AP accounts for the control-account selector.
func (s *Server) loadARAPAccounts(companyID uint) []models.Account {
	var accounts []models.Account
	s.DB.Where("company_id = ? AND detail_account_type IN ? AND is_active = true",
		companyID, []string{
			string(models.DetailAccountsReceivable),
			string(models.DetailAccountsPayable),
		}).
		Order("code asc").
		Find(&accounts)
	return accounts
}
