// 遵循project_guide.md
package web

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/repository"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleBootstrapForm(c *fiber.Ctx) error {
	// Prevent browser from caching the form — pressing Back after successful
	// bootstrap will re-request this page, which then redirects to "/" instead
	// of showing the cached form with sensitive data still visible.
	c.Set("Cache-Control", "no-store")

	userCount, companyCount, err := countUsersAndCompanies(s.DB)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	if userCount > 0 && companyCount == 0 {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	if userCount > 0 || companyCount > 0 {
		return c.Redirect("/", fiber.StatusSeeOther)
	}

	return pages.Bootstrap(pages.BootstrapViewModel{
		Active: "Setup",
		Values: pages.BootstrapFormValues{
			SetupFormValues: pages.SetupFormValues{
				AccountCodeLength: "4",
			},
		},
		Errors: pages.BootstrapFormErrors{},
	}).Render(c.Context(), c)
}

func (s *Server) handleBootstrapSubmit(c *fiber.Ctx) error {
	userCount, companyCount, err := countUsersAndCompanies(s.DB)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	if userCount != 0 || companyCount != 0 {
		if c.Get("HX-Request") == "true" {
			c.Set("HX-Redirect", "/")
			return c.SendStatus(fiber.StatusNoContent)
		}
		return c.Redirect("/", fiber.StatusSeeOther)
	}

	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	passwordConfirm := c.FormValue("password_confirm")
	displayName := strings.TrimSpace(c.FormValue("display_name"))

	name := strings.TrimSpace(c.FormValue("company_name"))
	entityTypeRaw := strings.TrimSpace(c.FormValue("entity_type"))
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
	accountCodeLengthRaw := strings.TrimSpace(c.FormValue("account_code_length"))

	values := pages.BootstrapFormValues{
		SetupFormValues: pages.SetupFormValues{
			CompanyName:      name,
			EntityType:       entityTypeRaw,
			AddressLine:      addressLine,
			City:             city,
			Province:         province,
			PostalCode:       postalCode,
			Country:          country,
			BusinessNumber:   businessNumber,
			Industry:         industry,
			IncorporatedDate: incorporatedDate,
			FiscalYearEnd:    fiscalYearEnd,
			AccountCodeLength: accountCodeLengthRaw,
		},
		Email:           email,
		Password:        "",
		PasswordConfirm: "",
		DisplayName:     displayName,
	}

	var errs pages.BootstrapFormErrors

	if email == "" {
		errs.Email = "Email is required."
	} else if !strings.Contains(email, "@") || len(email) < 3 {
		errs.Email = "Enter a valid email address."
	}

	if len(password) < 8 {
		errs.Password = "Password must be at least 8 characters."
	}
	if password != passwordConfirm {
		errs.PasswordConfirm = "Passwords do not match."
	}

	if len(displayName) > 200 {
		errs.DisplayName = "Display name is too long."
	}

	companyErrs := validateSetupCompanyForm(values.SetupFormValues)
	errs.SetupFormErrors = companyErrs

	if errs.HasAny() {
		return pages.Bootstrap(pages.BootstrapViewModel{
			Active: "Setup",
			Values: values,
			Errors: errs,
		}).Render(c.Context(), c)
	}

	codeLen, _ := ParseAccountCodeLengthChoice(values.SetupFormValues.AccountCodeLength)

	entityType, businessType, industryValue, err := parseSetupCompanyForm(values.SetupFormValues)
	if err != nil {
		errs.SetupFormErrors = pages.SetupFormErrors{Form: "Could not read company details. Please try again."}
		return pages.Bootstrap(pages.BootstrapViewModel{
			Active: "Setup",
			Values: values,
			Errors: errs,
		}).Render(c.Context(), c)
	}

	emailNorm := strings.ToLower(email)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		errs.SetupFormErrors = pages.SetupFormErrors{Form: "Could not process password. Please try again."}
		return pages.Bootstrap(pages.BootstrapViewModel{
			Active: "Setup",
			Values: values,
			Errors: errs,
		}).Render(c.Context(), c)
	}

	cookieVal, tokenHash, err := NewOpaqueSessionToken()
	if err != nil {
		errs.SetupFormErrors = pages.SetupFormErrors{Form: "Could not create session. Please try again."}
		return pages.Bootstrap(pages.BootstrapViewModel{
			Active: "Setup",
			Values: values,
			Errors: errs,
		}).Render(c.Context(), c)
	}

	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)

	var createdCompanyID uint
	var createdUserID uuid.UUID
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		var uCount, cCount int64
		if err := tx.Model(&models.User{}).Count(&uCount).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Company{}).Count(&cCount).Error; err != nil {
			return err
		}
		if uCount != 0 || cCount != 0 {
			return errBootstrapRace
		}

		userRepo := repository.NewUserRepository(tx)
		u := &models.User{
			Email:        emailNorm,
			PasswordHash: string(hash),
			DisplayName:  displayName,
			IsActive:     true,
		}
		if err := userRepo.CreateUser(u); err != nil {
			return err
		}
		createdUserID = u.ID

		company := models.Company{
			Name:                    name,
			EntityType:            entityType,
			BusinessType:          businessType,
			AddressLine:           addressLine,
			City:                  city,
			Province:              province,
			PostalCode:            postalCode,
			Country:               country,
			BusinessNumber:        businessNumber,
			Industry:              industryValue,
			IncorporatedDate:      incorporatedDate,
			FiscalYearEnd:         fiscalYearEnd,
			AccountCodeLength:     codeLen,
			AccountCodeLengthLocked: false,
		}
		if err := tx.Create(&company).Error; err != nil {
			return err
		}
		createdCompanyID = company.ID

		membership := models.CompanyMembership{
			ID:        uuid.New(),
			UserID:    u.ID,
			CompanyID: company.ID,
			Role:      models.CompanyRoleOwner,
			IsActive:  true,
		}
		if err := tx.Create(&membership).Error; err != nil {
			return err
		}

		if err := services.CreateDefaultAccountsForCompany(tx, company.ID, codeLen); err != nil {
			return err
		}
		if err := tx.Model(&models.Company{}).Where("id = ?", company.ID).Update("account_code_length_locked", true).Error; err != nil {
			return err
		}
		// Batch 1 – Task module: seed TASK_LABOR and TASK_REIM system items.
		if err := services.EnsureSystemTaskItems(tx, company.ID); err != nil {
			return err
		}

		cid := company.ID
		sess := &models.Session{
			TokenHash:       tokenHash,
			UserID:          u.ID,
			ActiveCompanyID: &cid,
			ExpiresAt:       expiresAt,
		}
		sessRepo := repository.NewSessionRepository(tx)
		if err := sessRepo.CreateSession(sess); err != nil {
			return err
		}

		return nil
	})

	if errors.Is(err, errBootstrapRace) {
		if c.Get("HX-Request") == "true" {
			c.Set("HX-Redirect", "/")
			return c.SendStatus(fiber.StatusNoContent)
		}
		return c.Redirect("/", fiber.StatusSeeOther)
	}
	if err != nil {
		log.Printf("bootstrap: transaction failed: %v", err)
		errs.SetupFormErrors = pages.SetupFormErrors{Form: "Could not complete bootstrap. Please try again."}
		return pages.Bootstrap(pages.BootstrapViewModel{
			Active: "Setup",
			Values: values,
			Errors: errs,
		}).Render(c.Context(), c)
	}

	cid := createdCompanyID
	uid := createdUserID
	actor := emailNorm
	services.TryWriteAuditLogWithContext(s.DB, "bootstrap.completed", "company", createdCompanyID, actor, map[string]any{
		"company_name": name,
		"entity_type":  entityTypeRaw,
		"company_id":   createdCompanyID,
	}, &cid, &uid)

	setSessionCookie(c, s.Cfg, cookieVal, SessionCookieMaxAgeSec)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/", fiber.StatusSeeOther)
}

var errBootstrapRace = errors.New("bootstrap race: data changed")

func countUsersAndCompanies(db *gorm.DB) (users int64, companies int64, err error) {
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		return 0, 0, err
	}
	if err := db.Model(&models.Company{}).Count(&companies).Error; err != nil {
		return 0, 0, err
	}
	return users, companies, nil
}
