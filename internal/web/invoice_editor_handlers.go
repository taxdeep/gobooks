// 遵循project_guide.md
package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

type parsedInvoiceLine struct {
	ProductServiceID *uint
	Description      string
	Qty              decimal.Decimal
	UnitPrice        decimal.Decimal
	TaxCodeID        *uint
}

// handleInvoiceDetail renders the read-only invoice detail page.
func (s *Server) handleInvoiceDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}

	var inv models.Invoice
	err := s.DB.
		Preload("Customer").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Preload("JournalEntry").
		Where("id = ? AND company_id = ?", uint(id64), companyID).
		First(&inv).Error
	if err != nil {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}

	// Check SMTP readiness for Send Email button
	_, smtpReady, _ := services.EffectiveSMTPForCompany(s.DB, companyID)

	vm := pages.InvoiceDetailVM{
		HasCompany: true,
		Invoice:    inv,
		SMTPReady:  smtpReady,
		JustVoided: c.Query("voided") == "1",
		JustIssued: c.Query("issued") == "1",
		JustSent:   c.Query("sent") == "1",
		JustPaid:   c.Query("paid") == "1",
		VoidError:  c.Query("voiderror"),
	}
	if inv.JournalEntry != nil {
		vm.JournalNo = inv.JournalEntry.JournalNo
	}

	return pages.InvoiceDetail(vm).Render(c.Context(), c)
}

// handleInvoiceNew renders the blank invoice editor.
func (s *Server) handleInvoiceNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	nextNo, err := services.SuggestNextInvoiceNumber(s.DB, companyID)
	if err != nil {
		nextNo = "IN001"
	}

	today := time.Now().Format("2006-01-02")
	vm := pages.InvoiceEditorVM{
		HasCompany:    true,
		IsEdit:        false,
		InvoiceNumber: nextNo,
		InvoiceDate:   today,
		Terms:         string(models.InvoiceTermsNet30),
	}
	// Compute default due date (net 30 from today).
	due := models.ComputeDueDate(time.Now(), models.InvoiceTermsNet30)
	if due != nil {
		vm.DueDate = due.Format("2006-01-02")
	}

	if err := s.loadEditorDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load dropdown data."
	}
	return pages.InvoiceEditor(vm).Render(c.Context(), c)
}

// handleInvoiceEdit renders the editor pre-filled with an existing draft invoice.
func (s *Server) handleInvoiceEdit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}
	invoiceID := uint(id64)

	var inv models.Invoice
	if err := s.DB.Preload("Lines").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}
	if inv.Status != models.InvoiceStatusDraft {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}

	vm := pages.InvoiceEditorVM{
		HasCompany:    true,
		IsEdit:        true,
		EditingID:     invoiceID,
		InvoiceNumber: inv.InvoiceNumber,
		CustomerID:    strconv.FormatUint(uint64(inv.CustomerID), 10),
		InvoiceDate:   inv.InvoiceDate.Format("2006-01-02"),
		Terms:         string(inv.Terms),
		Memo:          inv.Memo,
	}
	if inv.DueDate != nil {
		vm.DueDate = inv.DueDate.Format("2006-01-02")
	}

	// Build line form rows from existing lines.
	for _, l := range inv.Lines {
		vm.Lines = append(vm.Lines, pages.InvoiceLineFormRow{
			ProductServiceID: optUintStr(l.ProductServiceID),
			Description:      l.Description,
			Qty:              l.Qty.String(),
			UnitPrice:        l.UnitPrice.StringFixed(4),
			TaxCodeID:        optUintStr(l.TaxCodeID),
			LineNet:          l.LineNet.StringFixed(2),
			LineTax:          l.LineTax.StringFixed(2),
			LineTotal:        l.LineTotal.StringFixed(2),
		})
	}

	if err := s.loadEditorDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load dropdown data."
	}
	vm.InitialLinesJSON = buildInitialLinesJSON(vm.Lines)
	return pages.InvoiceEditor(vm).Render(c.Context(), c)
}

// handleInvoiceSaveDraft creates or updates a draft invoice with line items.
func (s *Server) handleInvoiceSaveDraft(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// ── Parse header ─────────────────────────────────────────────────────────
	invoiceIDRaw := strings.TrimSpace(c.FormValue("invoice_id"))
	invoiceNo := strings.TrimSpace(c.FormValue("invoice_number"))
	customerRaw := strings.TrimSpace(c.FormValue("customer_id"))
	dateRaw := strings.TrimSpace(c.FormValue("invoice_date"))
	termsRaw := strings.TrimSpace(c.FormValue("terms"))
	dueDateRaw := strings.TrimSpace(c.FormValue("due_date"))
	memo := strings.TrimSpace(c.FormValue("memo"))
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))

	isEdit := invoiceIDRaw != "" && invoiceIDRaw != "0"
	var editingID uint
	if isEdit {
		id64, err := strconv.ParseUint(invoiceIDRaw, 10, 64)
		if err != nil || id64 == 0 {
			return c.Redirect("/invoices", fiber.StatusSeeOther)
		}
		editingID = uint(id64)
	}

	vm := pages.InvoiceEditorVM{
		HasCompany:    true,
		IsEdit:        isEdit,
		EditingID:     editingID,
		InvoiceNumber: invoiceNo,
		CustomerID:    customerRaw,
		InvoiceDate:   dateRaw,
		Terms:         termsRaw,
		DueDate:       dueDateRaw,
		Memo:          memo,
	}
	_ = s.loadEditorDropdowns(companyID, &vm)

	// ── Validate header ───────────────────────────────────────────────────────
	if invoiceNo == "" {
		vm.InvoiceNumberError = "Invoice Number is required."
	} else if err := services.ValidateDocumentNumber(invoiceNo); err != nil {
		vm.InvoiceNumberError = err.Error()
	}
	custID, custErr := services.ParseUint(customerRaw)
	if custErr != nil || custID == 0 {
		vm.CustomerError = "Customer is required."
	}
	invoiceDate, dateErr := time.Parse("2006-01-02", dateRaw)
	if dateErr != nil {
		vm.DateError = "Invoice Date is required."
	}
	terms, _ := func() (models.InvoiceTerms, error) {
		switch models.InvoiceTerms(termsRaw) {
		case models.InvoiceTermsNet15, models.InvoiceTermsNet30, models.InvoiceTermsNet60,
			models.InvoiceTermsDueOnReceipt, models.InvoiceTermsCustom:
			return models.InvoiceTerms(termsRaw), nil
		default:
			return models.InvoiceTermsNet30, fmt.Errorf("unknown")
		}
	}()
	if termsRaw == "" {
		terms = models.InvoiceTermsNet30
	}

	// ── Parse lines ───────────────────────────────────────────────────────────
	lineCount, _ := strconv.Atoi(lineCountRaw)
	if lineCount < 1 {
		lineCount = 0
	}
	var parsedLines []parsedInvoiceLine
	var lineFormRows []pages.InvoiceLineFormRow

	for i := 0; i < lineCount; i++ {
		key := func(field string) string { return fmt.Sprintf("%s[%d]", field, i) }
		desc := strings.TrimSpace(c.FormValue(key("line_description")))
		qtyRaw := strings.TrimSpace(c.FormValue(key("line_qty")))
		priceRaw := strings.TrimSpace(c.FormValue(key("line_unit_price")))
		psIDRaw := strings.TrimSpace(c.FormValue(key("line_product_service_id")))
		tcIDRaw := strings.TrimSpace(c.FormValue(key("line_tax_code_id")))

		row := pages.InvoiceLineFormRow{
			ProductServiceID: psIDRaw,
			Description:      desc,
			Qty:              qtyRaw,
			UnitPrice:        priceRaw,
			TaxCodeID:        tcIDRaw,
		}

		qty, qErr := decimal.NewFromString(qtyRaw)
		if qErr != nil || qty.IsZero() || qty.IsNegative() {
			qty = decimal.NewFromInt(1)
		}
		price, pErr := decimal.NewFromString(priceRaw)
		if pErr != nil || price.IsNegative() {
			price = decimal.Zero
		}
		if desc == "" {
			row.Error = "Description is required."
		}
		lineFormRows = append(lineFormRows, row)

		pl := parsedInvoiceLine{Description: desc, Qty: qty, UnitPrice: price}
		if id64, err := strconv.ParseUint(psIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.ProductServiceID = &id
		}
		if id64, err := strconv.ParseUint(tcIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.TaxCodeID = &id
		}
		parsedLines = append(parsedLines, pl)
	}

	vm.Lines = lineFormRows
	vm.InitialLinesJSON = buildInitialLinesJSON(lineFormRows)

	// ── Line-level validation ─────────────────────────────────────────────────
	hasLineErr := false
	for _, r := range lineFormRows {
		if r.Error != "" {
			hasLineErr = true
			break
		}
	}
	if len(parsedLines) == 0 {
		vm.LinesError = "At least one line item is required."
	}

	if vm.InvoiceNumberError != "" || vm.CustomerError != "" || vm.DateError != "" ||
		vm.LinesError != "" || hasLineErr {
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	if err := s.validateInvoiceDraftReferences(companyID, uint(custID), parsedLines); err != nil {
		vm.FormError = err.Error()
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	// ── Compute line amounts ──────────────────────────────────────────────────
	// Load only the tax codes referenced by lines (with components for tax calc).
	taxCodeCache := map[uint]*models.TaxCode{}
	for _, pl := range parsedLines {
		if pl.TaxCodeID == nil {
			continue
		}
		tcID := *pl.TaxCodeID
		if _, ok := taxCodeCache[tcID]; ok {
			continue
		}
		var tc models.TaxCode
		if err := s.DB.
			Where("id = ? AND company_id = ? AND is_active = true", tcID, companyID).
			First(&tc).Error; err == nil {
			taxCodeCache[tcID] = &tc
		}
	}

	type computedLine struct {
		parsedInvoiceLine
		LineNet    decimal.Decimal
		LineTax    decimal.Decimal
		LineTotal  decimal.Decimal
		TaxResults []services.TaxLineResult
	}
	var computed []computedLine
	subtotal := decimal.Zero
	taxTotal := decimal.Zero

	for _, pl := range parsedLines {
		lineNet := pl.Qty.Mul(pl.UnitPrice).RoundBank(2)
		var lineTax decimal.Decimal
		var taxResults []services.TaxLineResult
		if pl.TaxCodeID != nil {
			if tc, ok := taxCodeCache[*pl.TaxCodeID]; ok {
				taxResults = services.CalculateTax(lineNet, *tc)
				lineTax = services.SumTaxResults(taxResults)
			}
		}
		lineTotal := lineNet.Add(lineTax)
		subtotal = subtotal.Add(lineNet)
		taxTotal = taxTotal.Add(lineTax)
		computed = append(computed, computedLine{
			parsedInvoiceLine: pl,
			LineNet:           lineNet,
			LineTax:           lineTax,
			LineTotal:         lineTotal,
			TaxResults:        taxResults,
		})
	}
	grandTotal := subtotal.Add(taxTotal)

	// ── Compute due date ──────────────────────────────────────────────────────
	var dueDate *time.Time
	if terms != models.InvoiceTermsCustom {
		dueDate = models.ComputeDueDate(invoiceDate, terms)
	} else if dueDateRaw != "" {
		if d, err := time.Parse("2006-01-02", dueDateRaw); err == nil {
			dueDate = &d
		}
	}

	// ── Duplicate number check (new invoices only) ────────────────────────────
	var dupCount int64
	dupQuery := s.DB.Model(&models.Invoice{}).
		Where("company_id = ? AND LOWER(invoice_number) = LOWER(?)", companyID, invoiceNo)
	if isEdit {
		dupQuery = dupQuery.Where("id <> ?", editingID)
	}
	if err := dupQuery.Count(&dupCount).Error; err != nil {
		vm.FormError = "Could not validate invoice number."
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}
	if dupCount > 0 {
		vm.InvoiceNumberError = "Invoice number already exists for this company (case-insensitive)."
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	// ── DB transaction ────────────────────────────────────────────────────────
	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	// ── Load customer for snapshots ──────────────────────────────────────────
	var customer models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", uint(custID), companyID).
		First(&customer).Error; err != nil {
		vm.FormError = "Customer not found."
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var inv models.Invoice

		if isEdit {
			if err := tx.Where("id = ? AND company_id = ?", editingID, companyID).First(&inv).Error; err != nil {
				return fmt.Errorf("invoice not found")
			}
			if inv.Status != models.InvoiceStatusDraft {
				return fmt.Errorf("only draft invoices can be edited")
			}
			inv.InvoiceNumber = invoiceNo
			inv.CustomerID = uint(custID)
			inv.InvoiceDate = invoiceDate
			inv.Terms = terms
			inv.DueDate = dueDate
			inv.Memo = memo
			inv.Subtotal = subtotal
			inv.TaxTotal = taxTotal
			inv.Amount = grandTotal
			inv.BalanceDue = grandTotal
			inv.CustomerNameSnapshot = customer.Name
			inv.CustomerEmailSnapshot = customer.Email
			inv.CustomerAddressSnapshot = customer.Address
			if err := tx.Save(&inv).Error; err != nil {
				return err
			}
			// Delete existing lines and re-insert.
			if err := tx.Where("invoice_id = ?", inv.ID).Delete(&models.InvoiceLine{}).Error; err != nil {
				return err
			}
		} else {
			inv = models.Invoice{
				CompanyID:               companyID,
				InvoiceNumber:           invoiceNo,
				CustomerID:              uint(custID),
				InvoiceDate:             invoiceDate,
				Terms:                   terms,
				DueDate:                 dueDate,
				Status:                  models.InvoiceStatusDraft,
				Memo:                    memo,
				Subtotal:                subtotal,
				TaxTotal:                taxTotal,
				Amount:                  grandTotal,
				BalanceDue:              grandTotal,
				CustomerNameSnapshot:    customer.Name,
				CustomerEmailSnapshot:   customer.Email,
				CustomerAddressSnapshot: customer.Address,
			}
			if err := tx.Create(&inv).Error; err != nil {
				return err
			}
			if err := services.BumpInvoiceNextNumberAfterCreate(tx, companyID); err != nil {
				return err
			}
		}

		// Insert lines.
		for i, cl := range computed {
			line := models.InvoiceLine{
				CompanyID:        companyID,
				InvoiceID:        inv.ID,
				SortOrder:        uint(i + 1),
				Description:      cl.Description,
				Qty:              cl.Qty,
				UnitPrice:        cl.UnitPrice,
				LineNet:          cl.LineNet,
				LineTax:          cl.LineTax,
				LineTotal:        cl.LineTotal,
				ProductServiceID: cl.ProductServiceID,
				TaxCodeID:        cl.TaxCodeID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return err
			}
		}

		action := "invoice.created"
		if isEdit {
			action = "invoice.updated"
		}
		return services.WriteAuditLogWithContextDetails(tx, action, "invoice", inv.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, &uid, nil,
			map[string]any{
				"invoice_number": inv.InvoiceNumber,
				"customer_id":    inv.CustomerID,
				"total":          inv.Amount.StringFixed(2),
				"line_count":     len(computed),
			},
		)
	})
	if err != nil {
		vm.FormError = invoiceSaveErrorMessage(err)
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	return c.Redirect("/invoices?saved=1", fiber.StatusSeeOther)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Server) validateInvoiceDraftReferences(companyID, customerID uint, lines []parsedInvoiceLine) error {
	var customerCount int64
	if err := s.DB.Model(&models.Customer{}).
		Where("id = ? AND company_id = ?", customerID, companyID).
		Count(&customerCount).Error; err != nil {
		return fmt.Errorf("could not validate customer")
	}
	if customerCount == 0 {
		return fmt.Errorf("customer is not valid for this company")
	}

	for i, line := range lines {
		if line.ProductServiceID != nil {
			var productCount int64
			if err := s.DB.Model(&models.ProductService{}).
				Where("id = ? AND company_id = ? AND is_active = true", *line.ProductServiceID, companyID).
				Count(&productCount).Error; err != nil {
				return fmt.Errorf("could not validate line %d product/service", i+1)
			}
			if productCount == 0 {
				return fmt.Errorf("line %d has an invalid product/service for this company", i+1)
			}
		}
		if line.TaxCodeID != nil {
			var taxCodeCount int64
			if err := s.DB.Model(&models.TaxCode{}).
				Where("id = ? AND company_id = ? AND is_active = true", *line.TaxCodeID, companyID).
				Count(&taxCodeCount).Error; err != nil {
				return fmt.Errorf("could not validate line %d tax code", i+1)
			}
			if taxCodeCount == 0 {
				return fmt.Errorf("line %d has an invalid tax code for this company", i+1)
			}
		}
	}

	return nil
}

// optUintStr converts *uint to string; empty string if nil.
func optUintStr(p *uint) string {
	if p == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*p), 10)
}

// loadEditorDropdowns fills customers, products, taxCodes + JSON blobs on vm.
func (s *Server) loadEditorDropdowns(companyID uint, vm *pages.InvoiceEditorVM) error {
	if err := s.DB.Where("company_id = ?", companyID).Order("name asc").
		Find(&vm.Customers).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
		Find(&vm.Products).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
		Find(&vm.TaxCodes).Error; err != nil {
		return err
	}
	vm.ProductsJSON = buildProductsJSON(vm.Products)
	vm.TaxCodesJSON = buildTaxCodesJSON(vm.TaxCodes)
	return nil
}

type productJSONItem struct {
	ID               uint   `json:"id"`
	Name             string `json:"name"`
	DefaultPrice     string `json:"default_price"`
	DefaultTaxCodeID *uint  `json:"default_tax_code_id"`
}

type taxCodeJSONItem struct {
	ID   uint   `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func buildProductsJSON(products []models.ProductService) string {
	items := make([]productJSONItem, 0, len(products))
	for _, p := range products {
		items = append(items, productJSONItem{
			ID:               p.ID,
			Name:             p.Name,
			DefaultPrice:     p.DefaultPrice.StringFixed(2),
			DefaultTaxCodeID: p.DefaultTaxCodeID,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func buildTaxCodesJSON(codes []models.TaxCode) string {
	items := make([]taxCodeJSONItem, 0, len(codes))
	for _, tc := range codes {
		items = append(items, taxCodeJSONItem{ID: tc.ID, Code: tc.Name, Name: tc.Name})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// buildInitialLinesJSON serialises InvoiceLineFormRow slice for Alpine's data-initial-lines.
func buildInitialLinesJSON(rows []pages.InvoiceLineFormRow) string {
	type alpineLine struct {
		ProductServiceID string `json:"product_service_id"`
		Description      string `json:"description"`
		Qty              string `json:"qty"`
		UnitPrice        string `json:"unit_price"`
		TaxCodeID        string `json:"tax_code_id"`
		LineNet          string `json:"line_net"`
	}
	items := make([]alpineLine, 0, len(rows))
	for _, r := range rows {
		net := r.LineNet
		if net == "" {
			net = "0.00"
		}
		items = append(items, alpineLine{
			ProductServiceID: r.ProductServiceID,
			Description:      r.Description,
			Qty:              r.Qty,
			UnitPrice:        r.UnitPrice,
			TaxCodeID:        r.TaxCodeID,
			LineNet:          net,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}
