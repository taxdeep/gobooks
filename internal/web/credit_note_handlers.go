// 遵循project_guide.md
package web

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// GET /credit-notes
func (s *Server) handleCreditNotesList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	cns, _ := services.ListCreditNotes(s.DB, companyID)
	return pages.CreditNotesList(pages.CreditNotesListVM{
		HasCompany:  true,
		CreditNotes: cns,
		Saved:       c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// GET /credit-notes/new
func (s *Server) handleCreditNoteNewGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	// Pre-fill customer from query param (e.g. when navigating from invoice).
	customerID := c.QueryInt("customer_id", 0)
	invoiceID := c.QueryInt("invoice_id", 0)

	var customers []models.Customer
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&customers)

	var accounts []models.Account
	s.DB.Where("company_id = ? AND root_account_type IN ? AND is_active = true",
		companyID, []string{"revenue", "cost_of_sales"}).
		Order("code asc").Find(&accounts)

	var taxCodes []models.TaxCode
	s.DB.Where("company_id = ? AND is_active = true AND scope != ?",
		companyID, "purchase").Find(&taxCodes)

	vm := buildCreditNoteFormVM(companyID, uint(customerID), uint(invoiceID), customers, accounts, taxCodes, "", nil)
	return pages.CreditNoteForm(vm).Render(c.Context(), c)
}

// POST /credit-notes/new
func (s *Server) handleCreditNoteNewPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)

	customerID, _ := strconv.ParseUint(c.FormValue("customer_id"), 10, 64)
	var invoiceID *uint
	if raw := c.FormValue("invoice_id"); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
			uid := uint(id)
			invoiceID = &uid
		}
	}

	dateRaw := strings.TrimSpace(c.FormValue("credit_note_date"))
	cnDate, err := time.Parse("2006-01-02", dateRaw)
	if err != nil {
		cnDate = time.Now()
	}

	reason := models.CreditNoteReason(c.FormValue("reason"))
	memo := c.FormValue("memo")

	// Parse lines.
	descriptions := c.Request().PostArgs().PeekMulti("description[]")
	qtys := c.Request().PostArgs().PeekMulti("qty[]")
	prices := c.Request().PostArgs().PeekMulti("unit_price[]")
	revAccts := c.Request().PostArgs().PeekMulti("revenue_account_id[]")
	taxCodeIDs := c.Request().PostArgs().PeekMulti("tax_code_id[]")

	lineInputs := make([]services.CreditNoteLineInput, 0, len(descriptions))
	for i := range descriptions {
		desc := strings.TrimSpace(string(descriptions[i]))
		if desc == "" {
			continue
		}
		qty, _ := decimal.NewFromString(string(qtys[i]))
		price, _ := decimal.NewFromString(string(prices[i]))
		revAcctID, _ := strconv.ParseUint(string(revAccts[i]), 10, 64)

		var taxCodeID *uint
		if i < len(taxCodeIDs) {
			if tid, err := strconv.ParseUint(string(taxCodeIDs[i]), 10, 64); err == nil && tid > 0 {
				uid := uint(tid)
				taxCodeID = &uid
			}
		}
		lineInputs = append(lineInputs, services.CreditNoteLineInput{
			Description:      desc,
			Qty:              qty,
			UnitPrice:        price,
			RevenueAccountID: uint(revAcctID),
			TaxCodeID:        taxCodeID,
			SortOrder:        uint(i + 1),
		})
	}

	actor := ""
	var userIDPtr interface{ GetID() uint } = nil
	if user != nil {
		actor = user.Email
		_ = userIDPtr
	}

	_, createErr := services.CreateCreditNoteDraft(s.DB, services.CreateCreditNoteDraftInput{
		CompanyID:      companyID,
		CustomerID:     uint(customerID),
		InvoiceID:      invoiceID,
		CreditNoteDate: cnDate,
		Reason:         reason,
		Memo:           memo,
		Lines:          lineInputs,
	})
	_ = actor

	if createErr != nil {
		var customers []models.Customer
		s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").Find(&customers)
		var accounts []models.Account
		s.DB.Where("company_id = ? AND root_account_type IN ? AND is_active = true",
			companyID, []string{"revenue", "cost_of_sales"}).Order("code asc").Find(&accounts)
		var taxCodes []models.TaxCode
		s.DB.Where("company_id = ? AND is_active = true AND scope != ?", companyID, "purchase").Find(&taxCodes)
		vm := buildCreditNoteFormVM(companyID, uint(customerID), 0, customers, accounts, taxCodes, createErr.Error(), nil)
		return pages.CreditNoteForm(vm).Render(c.Context(), c)
	}

	return c.Redirect("/credit-notes?saved=1", fiber.StatusSeeOther)
}

// GET /credit-notes/:id
func (s *Server) handleCreditNoteDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}
	cn, err := services.GetCreditNote(s.DB, companyID, uint(id))
	if err != nil {
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}

	var openInvoices []models.Invoice
	if cn.Status == models.CreditNoteStatusIssued || cn.Status == models.CreditNoteStatusPartiallyApplied {
		s.DB.Where("company_id = ? AND customer_id = ? AND status IN ? AND balance_due > 0",
			companyID, cn.CustomerID,
			[]string{
				string(models.InvoiceStatusIssued),
				string(models.InvoiceStatusSent),
				string(models.InvoiceStatusPartiallyPaid),
				string(models.InvoiceStatusOverdue),
			}).Order("invoice_date asc").Find(&openInvoices)
	}

	removeErr := ""
	if c.Query("removeerr") == "1" {
		removeErr = "Failed to remove credit application."
	}
	return pages.CreditNoteDetail(pages.CreditNoteDetailVM{
		HasCompany:      true,
		CreditNote:      *cn,
		OpenInvoices:    openInvoices,
		ApplyDrawerOpen: c.Query("apply_drawer") == "1",
		Removed:         c.Query("removed") == "1",
		RemoveError:     removeErr,
	}).Render(c.Context(), c)
}

// POST /credit-notes/:id/issue
func (s *Server) handleCreditNoteIssue(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}

	actor := ""
	if user != nil {
		actor = user.Email
	}

	issueErr := services.PostCreditNote(s.DB, companyID, uint(id), actor, nil)
	if issueErr != nil {
		cn, _ := services.GetCreditNote(s.DB, companyID, uint(id))
		if cn == nil {
			return c.Redirect("/credit-notes", fiber.StatusSeeOther)
		}
		return pages.CreditNoteDetail(pages.CreditNoteDetailVM{
			HasCompany: true,
			CreditNote: *cn,
			FormError:  issueErr.Error(),
		}).Render(c.Context(), c)
	}
	return c.Redirect(c.Path()[:len(c.Path())-len("/issue")], fiber.StatusSeeOther)
}

// POST /credit-notes/:id/void
func (s *Server) handleCreditNoteVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}
	actor := ""
	if user != nil {
		actor = user.Email
	}
	services.VoidCreditNote(s.DB, companyID, uint(id), actor, nil)
	return c.Redirect(c.Path()[:len(c.Path())-len("/void")], fiber.StatusSeeOther)
}

// POST /credit-notes/:id/apply
func (s *Server) handleCreditNoteApply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}
	invoiceID, _ := strconv.ParseUint(c.FormValue("invoice_id"), 10, 64)
	amount, _ := decimal.NewFromString(c.FormValue("amount"))

	actor := ""
	if user != nil {
		actor = user.Email
	}

	applyErr := services.ApplyCreditNoteToInvoice(s.DB, companyID, uint(id), uint(invoiceID), amount, actor, nil)
	if applyErr != nil {
		cn, _ := services.GetCreditNote(s.DB, companyID, uint(id))
		if cn == nil {
			return c.Redirect("/credit-notes", fiber.StatusSeeOther)
		}
		var openInvoices []models.Invoice
		s.DB.Where("company_id = ? AND customer_id = ? AND status IN ? AND balance_due > 0",
			companyID, cn.CustomerID,
			[]string{string(models.InvoiceStatusIssued), string(models.InvoiceStatusSent),
				string(models.InvoiceStatusPartiallyPaid), string(models.InvoiceStatusOverdue)}).
			Find(&openInvoices)
		return pages.CreditNoteDetail(pages.CreditNoteDetailVM{
			HasCompany:      true,
			CreditNote:      *cn,
			OpenInvoices:    openInvoices,
			FormError:       applyErr.Error(),
			ApplyDrawerOpen: true,
		}).Render(c.Context(), c)
	}
	return c.Redirect(c.Path()[:len(c.Path())-len("/apply")], fiber.StatusSeeOther)
}

// POST /credit-notes/applications/:id/remove
func (s *Server) handleCreditNoteRemoveApplication(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}
	appID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || appID == 0 {
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}
	cnID := strings.TrimSpace(c.FormValue("cn_id"))

	if removeErr := services.ReverseARCreditNoteApplication(s.DB, companyID, uint(appID)); removeErr != nil {
		if cnID != "" {
			return c.Redirect("/credit-notes/"+cnID+"?removeerr=1", fiber.StatusSeeOther)
		}
		return c.Redirect("/credit-notes", fiber.StatusSeeOther)
	}
	if cnID != "" {
		return c.Redirect("/credit-notes/"+cnID+"?removed=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/credit-notes", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func buildCreditNoteFormVM(companyID, customerID, invoiceID uint,
	customers []models.Customer, accounts []models.Account, taxCodes []models.TaxCode,
	formError string, _ interface{}) pages.CreditNoteFormVM {
	return pages.CreditNoteFormVM{
		HasCompany: true,
		CompanyID:  companyID,
		CustomerID: customerID,
		InvoiceID:  invoiceID,
		Customers:  customers,
		Accounts:   accounts,
		TaxCodes:   taxCodes,
		FormError:  formError,
		Reasons:    models.AllCreditNoteReasons(),
	}
}

var _ = errors.New // keep import
