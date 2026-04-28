// 遵循project_guide.md
package web

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleAccountingBooksGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	// Refresh policies before displaying.
	_ = services.RefreshBookStandardChangePolicy(s.DB, companyID, 0)

	books, err := services.ListAccountingBooks(s.DB, companyID)
	if err != nil {
		books = []models.AccountingBook{}
	}

	profiles, _ := services.ListStandardProfiles(s.DB)

	vm := pages.AccountingBooksVM{
		HasCompany: true,
		Books:      books,
		Profiles:   profiles,
		DrawerOpen: c.Query("drawer") == "create",
		Saved:      c.Query("saved") == "1",
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "Accounting Books"},
		},
	}
	return pages.AccountingBooksHub(vm).Render(c.Context(), c)
}

func (s *Server) handleAccountingBooksCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	bookType := models.AccountingBookType(strings.TrimSpace(c.FormValue("book_type")))
	currency := strings.ToUpper(strings.TrimSpace(c.FormValue("currency")))
	profileCode := models.AccountingStandardProfileCode(strings.TrimSpace(c.FormValue("profile_code")))

	_, err := services.CreateAccountingBook(s.DB, services.CreateAccountingBookInput{
		CompanyID:              companyID,
		BookType:               bookType,
		FunctionalCurrencyCode: currency,
		StandardProfileCode:    profileCode,
	})

	if err == nil {
		return c.Redirect("/settings/accounting-books?saved=1", fiber.StatusSeeOther)
	}

	books, _ := services.ListAccountingBooks(s.DB, companyID)
	profiles, _ := services.ListStandardProfiles(s.DB)

	vm := pages.AccountingBooksVM{
		HasCompany:       true,
		Books:            books,
		Profiles:         profiles,
		DrawerOpen:       true,
		FieldBookType:    string(bookType),
		FieldCurrency:    currency,
		FieldProfileCode: string(profileCode),
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "Accounting Books"},
		},
	}

	if errors.Is(err, services.ErrBookAlreadyExists) {
		vm.FormError = "A book with this type, currency, and standard already exists."
	} else {
		vm.FormError = err.Error()
	}
	return pages.AccountingBooksHub(vm).Render(c.Context(), c)
}

// ── Book detail ───────────────────────────────────────────────────────────────

func (s *Server) handleAccountingBookDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	bookID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/settings/accounting-books", fiber.StatusSeeOther)
	}

	// Refresh policy for this book before display.
	_ = services.RefreshBookStandardChangePolicy(s.DB, companyID, uint(bookID))

	var book models.AccountingBook
	if err := s.DB.Preload("StandardProfile").
		Where("id = ? AND company_id = ?", bookID, companyID).
		First(&book).Error; err != nil {
		return c.Redirect("/settings/accounting-books", fiber.StatusSeeOther)
	}

	periods, _ := services.ListFiscalPeriodsForBook(s.DB, companyID, uint(bookID))
	changes, _ := services.ListBookStandardChanges(s.DB, companyID, uint(bookID))
	profiles, _ := services.ListStandardProfiles(s.DB)

	vm := pages.AccountingBookDetailVM{
		HasCompany: true,
		Book:       book,
		Periods:    periods,
		Changes:    changes,
		Profiles:   profiles,
		Saved:      c.Query("saved") == "1",
		DrawerOpen: c.Query("drawer"),
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings", Href: "/settings"},
			{Label: "Accounting Books", Href: "/settings/accounting-books"},
			{Label: string(book.BookType) + " — " + book.FunctionalCurrencyCode},
		},
	}
	return pages.AccountingBookDetail(vm).Render(c.Context(), c)
}

func (s *Server) handleAccountingBookChangeStandard(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	bookID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/settings/accounting-books", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)

	newProfileCode := models.AccountingStandardProfileCode(strings.TrimSpace(c.FormValue("new_profile_code")))
	cutoverRaw := strings.TrimSpace(c.FormValue("cutover_date"))
	notesVal := strings.TrimSpace(c.FormValue("notes"))

	var cutoverDate *time.Time
	if cutoverRaw != "" {
		if t, err := time.Parse("2006-01-02", cutoverRaw); err == nil {
			cutoverDate = &t
		}
	}

	actor := ""
	if user != nil {
		actor = user.Email
	}

	changeErr := services.ChangeBookStandard(s.DB, services.ChangeBookStandardInput{
		CompanyID:      companyID,
		BookID:         uint(bookID),
		NewProfileCode: newProfileCode,
		CutoverDate:    cutoverDate,
		Notes:          notesVal,
		Actor:          actor,
	})

	if changeErr == nil {
		return c.Redirect("/settings/accounting-books/"+strconv.FormatUint(bookID, 10)+"?saved=1", fiber.StatusSeeOther)
	}

	// Re-render detail page with drawer open + error.
	_ = services.RefreshBookStandardChangePolicy(s.DB, companyID, uint(bookID))
	var book models.AccountingBook
	s.DB.Preload("StandardProfile").Where("id = ? AND company_id = ?", bookID, companyID).First(&book)
	periods, _ := services.ListFiscalPeriodsForBook(s.DB, companyID, uint(bookID))
	changes, _ := services.ListBookStandardChanges(s.DB, companyID, uint(bookID))
	profiles, _ := services.ListStandardProfiles(s.DB)

	vm := pages.AccountingBookDetailVM{
		HasCompany:       true,
		Book:             book,
		Periods:          periods,
		Changes:          changes,
		Profiles:         profiles,
		DrawerOpen:       "change-standard",
		FormError:        changeErr.Error(),
		FieldNewProfile:  string(newProfileCode),
		FieldCutoverDate: cutoverRaw,
		FieldNotes:       notesVal,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings", Href: "/settings"},
			{Label: "Accounting Books", Href: "/settings/accounting-books"},
			{Label: string(book.BookType) + " — " + book.FunctionalCurrencyCode},
		},
	}
	return pages.AccountingBookDetail(vm).Render(c.Context(), c)
}

func (s *Server) handleAccountingBookAddPeriod(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	bookID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/settings/accounting-books", fiber.StatusSeeOther)
	}

	label := strings.TrimSpace(c.FormValue("label"))
	startRaw := strings.TrimSpace(c.FormValue("period_start"))
	endRaw := strings.TrimSpace(c.FormValue("period_end"))

	start, startErr := time.Parse("2006-01-02", startRaw)
	end, endErr := time.Parse("2006-01-02", endRaw)

	var addErr error
	if startErr != nil || endErr != nil {
		addErr = errors.New("invalid date format — use YYYY-MM-DD")
	} else {
		_, addErr = services.CreateFiscalPeriod(s.DB, services.CreateFiscalPeriodInput{
			CompanyID:   companyID,
			BookID:      uint(bookID),
			Label:       label,
			PeriodStart: start,
			PeriodEnd:   end,
		})
	}

	if addErr == nil {
		return c.Redirect("/settings/accounting-books/"+strconv.FormatUint(bookID, 10)+"?saved=1", fiber.StatusSeeOther)
	}

	// Re-render with drawer open + error.
	_ = services.RefreshBookStandardChangePolicy(s.DB, companyID, uint(bookID))
	var book models.AccountingBook
	s.DB.Preload("StandardProfile").Where("id = ? AND company_id = ?", bookID, companyID).First(&book)
	periods, _ := services.ListFiscalPeriodsForBook(s.DB, companyID, uint(bookID))
	changes, _ := services.ListBookStandardChanges(s.DB, companyID, uint(bookID))
	profiles, _ := services.ListStandardProfiles(s.DB)

	vm := pages.AccountingBookDetailVM{
		HasCompany:       true,
		Book:             book,
		Periods:          periods,
		Changes:          changes,
		Profiles:         profiles,
		DrawerOpen:       "add-period",
		FormError:        addErr.Error(),
		FieldPeriodLabel: label,
		FieldPeriodStart: startRaw,
		FieldPeriodEnd:   endRaw,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings", Href: "/settings"},
			{Label: "Accounting Books", Href: "/settings/accounting-books"},
			{Label: string(book.BookType) + " — " + book.FunctionalCurrencyCode},
		},
	}
	return pages.AccountingBookDetail(vm).Render(c.Context(), c)
}

func (s *Server) handleAccountingBookClosePeriod(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	bookID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/settings/accounting-books", fiber.StatusSeeOther)
	}
	periodID, err := strconv.ParseUint(c.Params("period_id"), 10, 64)
	if err != nil {
		return c.Redirect("/settings/accounting-books/"+strconv.FormatUint(bookID, 10), fiber.StatusSeeOther)
	}

	user := UserFromCtx(c)
	actor := ""
	if user != nil {
		actor = user.Email
	}

	closeErr := services.CloseFiscalPeriod(s.DB, companyID, uint(periodID), actor)
	if closeErr != nil {
		_ = services.RefreshBookStandardChangePolicy(s.DB, companyID, uint(bookID))
		var book models.AccountingBook
		s.DB.Preload("StandardProfile").Where("id = ? AND company_id = ?", bookID, companyID).First(&book)
		periods, _ := services.ListFiscalPeriodsForBook(s.DB, companyID, uint(bookID))
		changes, _ := services.ListBookStandardChanges(s.DB, companyID, uint(bookID))
		profiles, _ := services.ListStandardProfiles(s.DB)

		vm := pages.AccountingBookDetailVM{
			HasCompany: true,
			Book:       book,
			Periods:    periods,
			Changes:    changes,
			Profiles:   profiles,
			FormError:  closeErr.Error(),
			Breadcrumb: []pages.SettingsBreadcrumbPart{
				{Label: "Settings", Href: "/settings"},
				{Label: "Accounting Books", Href: "/settings/accounting-books"},
				{Label: string(book.BookType) + " — " + book.FunctionalCurrencyCode},
			},
		}
		return pages.AccountingBookDetail(vm).Render(c.Context(), c)
	}

	return c.Redirect("/settings/accounting-books/"+strconv.FormatUint(bookID, 10)+"?saved=1", fiber.StatusSeeOther)
}
