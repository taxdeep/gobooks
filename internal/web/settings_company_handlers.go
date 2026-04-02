// 遵循project_guide.md
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleCompanyHub(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	_ = companyID
	return pages.CompanyHub(pages.CompanyHubVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanyHub(),
	}).Render(c.Context(), c)
}

func (s *Server) handleCompanyProfileForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var company models.Company
	if err := s.DB.Where("id = ?", companyID).First(&company).Error; err != nil {
		return pages.CompanyProfile(pages.CompanySettingsVM{
			HasCompany: false,
			Breadcrumb: breadcrumbSettingsCompanyProfile(),
			Values:     pages.SetupFormValues{},
			Errors: pages.SetupFormErrors{
				Form: "Company not found. Please run setup first.",
			},
		}).Render(c.Context(), c)
	}

	return pages.CompanyProfile(pages.CompanySettingsVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanyProfile(),
		Values: pages.SetupFormValues{
			CompanyName:      company.Name,
			EntityType:       string(company.EntityType),
			BusinessType:     string(company.BusinessType),
			AddressLine:      company.AddressLine,
			City:             company.City,
			Province:         company.Province,
			PostalCode:       company.PostalCode,
			Country:          company.Country,
			BusinessNumber:   company.BusinessNumber,
			Industry:         string(company.Industry),
			IncorporatedDate: company.IncorporatedDate,
			FiscalYearEnd:    company.FiscalYearEnd,
		},
		Errors:    pages.SetupFormErrors{},
		Saved:     c.Query("saved") == "1",
		LogoPath:  company.LogoPath,
		LogoError: logoErrorMessage(c.Query("logo_error")),
	}).Render(c.Context(), c)
}

func (s *Server) handleCompanyProfileSubmit(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	name := strings.TrimSpace(c.FormValue("company_name"))
	entityTypeRaw := strings.TrimSpace(c.FormValue("entity_type"))
	businessTypeRaw := strings.TrimSpace(c.FormValue("business_type"))
	addressLine := strings.TrimSpace(c.FormValue("address_line"))
	city := strings.TrimSpace(c.FormValue("city"))
	province := strings.TrimSpace(c.FormValue("province"))
	postalCode := NormalizePostalCode(c.FormValue("postal_code"))
	country := strings.TrimSpace(c.FormValue("country"))
	businessNumber := strings.TrimSpace(c.FormValue("business_number"))
	industry := strings.TrimSpace(c.FormValue("industry"))
	incorporatedDate := strings.TrimSpace(c.FormValue("incorporated_date"))
	fiscalMonth := strings.TrimSpace(c.FormValue("fiscal_year_end_month"))
	fiscalDay := strings.TrimSpace(c.FormValue("fiscal_year_end_day"))
	fiscalYearEnd := ""
	if fiscalMonth != "" && fiscalDay != "" {
		fiscalYearEnd = fiscalMonth + "-" + fiscalDay
	}

	values := pages.SetupFormValues{
		CompanyName:      name,
		EntityType:       entityTypeRaw,
		BusinessType:     businessTypeRaw,
		AddressLine:      addressLine,
		City:             city,
		Province:         province,
		PostalCode:       postalCode,
		Country:          country,
		BusinessNumber:   businessNumber,
		Industry:         industry,
		IncorporatedDate: incorporatedDate,
		FiscalYearEnd:    fiscalYearEnd,
	}

	errs := validateSetupCompanyForm(values)
	businessType, err2 := models.ParseBusinessType(businessTypeRaw)
	if err2 != nil {
		errs.BusinessType = "Business Type is required."
	}

	if errs.HasAny() {
		// Load logo path so the preview is still shown on validation error.
		var cur models.Company
		_ = s.DB.Select("logo_path").Where("id = ?", companyID).First(&cur).Error
		return pages.CompanyProfile(pages.CompanySettingsVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsCompanyProfile(),
			Values:     values,
			Errors:     errs,
			Saved:      false,
			LogoPath:   cur.LogoPath,
		}).Render(c.Context(), c)
	}

	entityType, _ := models.ParseEntityType(entityTypeRaw)
	industryValue, _ := models.ParseIndustry(industry)

	var company models.Company
	if err := s.DB.Where("id = ?", companyID).First(&company).Error; err != nil {
		return pages.CompanyProfile(pages.CompanySettingsVM{
			HasCompany: false,
			Breadcrumb: breadcrumbSettingsCompanyProfile(),
			Values:     values,
			Errors: pages.SetupFormErrors{
				Form: "Company not found. Please run setup first.",
			},
			Saved: false,
		}).Render(c.Context(), c)
	}

	before := services.CompanyAuditSnapshot(company)

	company.Name = name
	company.EntityType = entityType
	company.BusinessType = businessType
	company.AddressLine = addressLine
	company.City = city
	company.Province = province
	company.PostalCode = postalCode
	company.Country = country
	company.BusinessNumber = businessNumber
	company.Industry = industryValue
	company.IncorporatedDate = incorporatedDate
	company.FiscalYearEnd = fiscalYearEnd

	if err := s.DB.Save(&company).Error; err != nil {
		return pages.CompanyProfile(pages.CompanySettingsVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsCompanyProfile(),
			Values:     values,
			Errors: pages.SetupFormErrors{
				Form: "Could not save. Please try again.",
			},
			Saved:    false,
			LogoPath: company.LogoPath,
		}).Render(c.Context(), c)
	}

	after := services.CompanyAuditSnapshot(company)
	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContextDetails(s.DB, "settings.company.saved", "company", companyID, actor, map[string]any{
		"company_id": companyID,
	}, &cid, &uid, before, after)

	return c.Redirect("/settings/company/profile?saved=1", fiber.StatusSeeOther)
}

func (s *Server) handleCompanyTemplatesGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	_ = companyID
	return pages.CompanyTemplates(pages.CompanySubpageVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanyTemplates(),
	}).Render(c.Context(), c)
}

