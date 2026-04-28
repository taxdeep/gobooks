// 遵循project_guide.md
package web

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/searchprojection/producers"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
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

	// Invoice → CN pre-fill. When operators click "Issue credit
	// note" on an invoice's More menu, they arrive here with
	// ?invoice_id=X. Without pre-fill they'd have to re-enter every
	// line AND (critically for stock items) the OriginalInvoiceLineID
	// the IN.5 post path requires — a field the UI never surfaces.
	// Result: stock-line CNs have been effectively unusable via UI.
	// This block loads the invoice + lines and serialises them to
	// InitialLinesJSON so Alpine can hydrate the editor with every
	// field the post path needs.
	if invoiceID > 0 {
		s.prefillCreditNoteFromInvoice(companyID, uint(invoiceID), &vm)
	}

	return pages.CreditNoteForm(vm).Render(c.Context(), c)
}

// prefillCreditNoteFromInvoice loads the invoice (scoped to company)
// and populates vm.InitialLinesJSON with one entry per invoice line.
// Silent no-op on missing / cross-tenant invoice — form renders
// blank in that case rather than surfacing a confusing error.
// Also sets vm.CustomerID from the invoice and captures the number
// for the breadcrumb hint.
func (s *Server) prefillCreditNoteFromInvoice(companyID, invoiceID uint, vm *pages.CreditNoteFormVM) {
	var inv models.Invoice
	if err := s.DB.Preload("Lines.ProductService").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return
	}
	// Override customer + invoice number with truth from the row
	// (query params could lie).
	vm.CustomerID = inv.CustomerID
	vm.InvoiceID = inv.ID
	vm.InvoiceNumber = inv.InvoiceNumber

	type initLine struct {
		Description           string `json:"description"`
		RevenueAccountID      string `json:"revenue_account_id"`
		Qty                   string `json:"qty"`
		UnitPrice             string `json:"unit_price"`
		TaxCodeID             string `json:"tax_code_id"`
		ProductServiceID      string `json:"product_service_id"`
		OriginalInvoiceLineID string `json:"original_invoice_line_id"`
	}
	lines := make([]initLine, 0, len(inv.Lines))
	for _, l := range inv.Lines {
		// Revenue account source preference:
		//   1. ProductService.RevenueAccountID (the product's default)
		//   2. Blank — operator will pick from dropdown
		// (InvoiceLine doesn't carry RevenueAccountID directly.)
		revAcct := ""
		if l.ProductService != nil && l.ProductService.RevenueAccountID != 0 {
			revAcct = strconv.FormatUint(uint64(l.ProductService.RevenueAccountID), 10)
		}
		psID := ""
		if l.ProductServiceID != nil && *l.ProductServiceID != 0 {
			psID = strconv.FormatUint(uint64(*l.ProductServiceID), 10)
		}
		taxID := ""
		if l.TaxCodeID != nil && *l.TaxCodeID != 0 {
			taxID = strconv.FormatUint(uint64(*l.TaxCodeID), 10)
		}
		lines = append(lines, initLine{
			Description:           l.Description,
			RevenueAccountID:      revAcct,
			Qty:                   l.Qty.String(),
			UnitPrice:             l.UnitPrice.StringFixed(4),
			TaxCodeID:             taxID,
			ProductServiceID:      psID,
			OriginalInvoiceLineID: strconv.FormatUint(uint64(l.ID), 10),
		})
	}
	if b, err := json.Marshal(lines); err == nil {
		vm.InitialLinesJSON = string(b)
	}
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
	// product_service_id[] + original_invoice_line_id[] are emitted
	// as hidden inputs by the Invoice→CN pre-fill path. Without them
	// stock-line CNs fail post with ErrCreditNoteStockItemRequiresOriginalLine
	// (IN.5). Blank/absent for standalone CNs — the parse below
	// treats empty strings as nil.
	psIDs := c.Request().PostArgs().PeekMulti("product_service_id[]")
	origLineIDs := c.Request().PostArgs().PeekMulti("original_invoice_line_id[]")

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
		var psIDPtr *uint
		if i < len(psIDs) {
			if pid, err := strconv.ParseUint(strings.TrimSpace(string(psIDs[i])), 10, 64); err == nil && pid > 0 {
				uid := uint(pid)
				psIDPtr = &uid
			}
		}
		var origLineIDPtr *uint
		if i < len(origLineIDs) {
			if oid, err := strconv.ParseUint(strings.TrimSpace(string(origLineIDs[i])), 10, 64); err == nil && oid > 0 {
				uid := uint(oid)
				origLineIDPtr = &uid
			}
		}
		lineInputs = append(lineInputs, services.CreditNoteLineInput{
			Description:           desc,
			Qty:                   qty,
			UnitPrice:             price,
			RevenueAccountID:      uint(revAcctID),
			TaxCodeID:             taxCodeID,
			ProductServiceID:      psIDPtr,
			OriginalInvoiceLineID: origLineIDPtr,
			SortOrder:             uint(i + 1),
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
	_ = producers.ProjectCreditNote(c.Context(), s.DB, s.SearchProjector, companyID, uint(id))
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
	_ = producers.ProjectCreditNote(c.Context(), s.DB, s.SearchProjector, companyID, uint(id))
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
