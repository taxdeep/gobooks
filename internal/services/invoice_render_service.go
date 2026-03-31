// 遵循project_guide.md
package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
)

// InvoiceRenderData holds the complete data needed to render an invoice to HTML.
type InvoiceRenderData struct {
	InvoiceNumber   string
	InvoiceDate     string
	DueDate         string
	CustomerName    string
	CustomerEmail   string
	CustomerAddress string
	CompanyName     string
	CompanyAddress  string
	Lines           []InvoiceLineRender
	Subtotal        decimal.Decimal
	TaxTotal        decimal.Decimal
	Amount          decimal.Decimal
	Terms           string
	Memo            string
	Status          string
	LogoImageBase64 string // empty string if no logo

	// Template configuration (from InvoiceTemplate.ConfigJSON)
	TemplateConfig models.TemplateConfig
}

// InvoiceLineRender represents a single line item for rendering.
type InvoiceLineRender struct {
	Description string
	Quantity    decimal.Decimal
	UnitPrice   decimal.Decimal
	TaxRate     string
	LineNet     decimal.Decimal
	LineTax     decimal.Decimal
	LineTotal   decimal.Decimal
}

// RenderInvoiceToHTML converts invoice data to HTML string for PDF generation.
// Dispatches to Classic or Modern template based on TemplateConfig.TemplateStyle.
func RenderInvoiceToHTML(data InvoiceRenderData) string {
	style := data.TemplateConfig.TemplateStyle
	if style == "modern" {
		return renderModernTemplate(data)
	}
	return renderClassicTemplate(data)
}

// ── Classic Template ─────────────────────────────────────────────────────────

func renderClassicTemplate(data InvoiceRenderData) string {
	accent := data.TemplateConfig.AccentColor
	if accent == "" {
		accent = "#0066cc"
	}
	cfg := data.TemplateConfig

	var html strings.Builder

	html.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Invoice ` + escapeHTML(data.InvoiceNumber) + `</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Arial, sans-serif; color: #333; line-height: 1.6; padding: 40px; background: white; }
        .container { max-width: 900px; margin: 0 auto; }
        .header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 40px; padding-bottom: 20px; border-bottom: 2px solid #f0f0f0; }
        .company-info h1 { font-size: 28px; color: #000; margin-bottom: 10px; }
        .company-logo { max-height: 80px; max-width: 180px; margin-bottom: 10px; }
        .invoice-meta { text-align: right; }
        .invoice-meta h2 { font-size: 32px; font-weight: bold; color: ` + accent + `; margin-bottom: 20px; }
        .invoice-meta p { font-size: 14px; margin-bottom: 5px; }
        .details-section { display: flex; justify-content: space-between; margin-bottom: 40px; gap: 40px; }
        .details-section div { flex: 1; }
        .details-section h3 { font-size: 12px; font-weight: bold; color: #666; text-transform: uppercase; margin-bottom: 10px; }
        .details-section p { font-size: 14px; line-height: 1.8; margin-bottom: 3px; }
        table { width: 100%; border-collapse: collapse; margin-bottom: 40px; font-size: 14px; }
        table thead { background: #f8f9fa; border-top: 2px solid #ddd; border-bottom: 2px solid #ddd; }
        table th { padding: 12px; text-align: left; font-weight: 600; }
        table td { padding: 12px; border-bottom: 1px solid #eee; }
        table td.numeric, table th.numeric { text-align: right; font-family: "Courier New", monospace; }
        .summary-section { display: flex; justify-content: flex-end; margin-bottom: 40px; }
        .summary { width: 360px; }
        .summary-row { display: flex; justify-content: space-between; padding: 8px 0; font-size: 14px; border-bottom: 1px solid #eee; }
        .summary-row.total { font-weight: bold; font-size: 16px; border-top: 2px solid #333; border-bottom: 2px solid #333; padding: 12px 0; }
        .footer { font-size: 12px; color: #666; margin-top: 40px; padding-top: 20px; border-top: 1px solid #ddd; }
        .footer p { margin-bottom: 5px; }
        .status-badge { display: inline-block; padding: 4px 10px; border-radius: 4px; font-size: 11px; font-weight: bold; text-transform: uppercase; }
        .status-draft { background: #e7f3ff; color: #0050b3; }
        .status-issued { background: #e7f3ff; color: #0050b3; }
        .status-sent { background: #e6f7ff; color: #006d75; }
        .status-paid { background: #f6ffed; color: #135200; }
        .status-overdue { background: #fff1f0; color: #a4221c; }
        .status-voided { background: #f0f0f0; color: #666; text-decoration: line-through; }
    </style>
</head>
<body>
    <div class="container">
`)

	// Header
	html.WriteString(`        <div class="header">
            <div class="company-info">
`)
	if cfg.ShowLogo && data.LogoImageBase64 != "" {
		html.WriteString(`                <img src="data:image/png;base64,` + data.LogoImageBase64 + `" alt="Logo" class="company-logo"><br>
`)
	}
	html.WriteString(`                <h1>` + escapeHTML(data.CompanyName) + `</h1>
`)
	if cfg.ShowCompanyAddress && data.CompanyAddress != "" {
		for _, line := range strings.Split(data.CompanyAddress, "\n") {
			html.WriteString(`                <p style="font-size: 13px; color: #666;">` + escapeHTML(line) + `</p>
`)
		}
	}
	html.WriteString(`            </div>
            <div class="invoice-meta">
                <h2>INVOICE</h2>
                <p><strong>Invoice #:</strong> ` + escapeHTML(data.InvoiceNumber) + `</p>
                <p><strong>Date:</strong> ` + escapeHTML(data.InvoiceDate) + `</p>
`)
	if data.DueDate != "" {
		html.WriteString(`                <p><strong>Due Date:</strong> ` + escapeHTML(data.DueDate) + `</p>
`)
	}
	statusClass := "status-" + strings.ToLower(data.Status)
	html.WriteString(`                <span class="status-badge ` + statusClass + `">` + escapeHTML(data.Status) + `</span>
            </div>
        </div>
`)

	// Bill To
	html.WriteString(`        <div class="details-section">
            <div>
                <h3>Bill To</h3>
                <p><strong>` + escapeHTML(data.CustomerName) + `</strong></p>
`)
	if data.CustomerAddress != "" {
		html.WriteString(`                <p>` + escapeHTML(data.CustomerAddress) + `</p>
`)
	}
	if data.CustomerEmail != "" {
		html.WriteString(`                <p>` + escapeHTML(data.CustomerEmail) + `</p>
`)
	}
	html.WriteString(`            </div>
            <div>
                <h3>Payment Terms</h3>
                <p>` + escapeHTML(data.Terms) + `</p>
`)
	if cfg.PaymentInstructions != "" {
		html.WriteString(`                <h3 style="margin-top: 15px;">Payment Instructions</h3>
                <p>` + escapeHTML(cfg.PaymentInstructions) + `</p>
`)
	}
	html.WriteString(`            </div>
        </div>
`)

	// Line items table
	writeLineItemsTable(&html, data, cfg)

	// Summary
	writeSummarySection(&html, data, cfg)

	// Footer
	if cfg.ShowFooter || cfg.ShowNotes {
		html.WriteString(`        <div class="footer">
`)
		if cfg.ShowNotes && data.Memo != "" {
			html.WriteString(`            <p><strong>Notes:</strong> ` + escapeHTML(data.Memo) + `</p>
`)
		}
		if cfg.FooterText != "" {
			html.WriteString(`            <p style="margin-top: 10px;">` + escapeHTML(cfg.FooterText) + `</p>
`)
		}
		html.WriteString(`        </div>
`)
	}

	html.WriteString(`    </div>
</body>
</html>
`)
	return html.String()
}

// ── Modern Template ──────────────────────────────────────────────────────────

func renderModernTemplate(data InvoiceRenderData) string {
	accent := data.TemplateConfig.AccentColor
	if accent == "" {
		accent = "#1a1a2e"
	}
	cfg := data.TemplateConfig

	var html strings.Builder

	html.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Invoice ` + escapeHTML(data.InvoiceNumber) + `</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: "Helvetica Neue", Helvetica, Arial, sans-serif; color: #2d2d2d; line-height: 1.5; padding: 0; background: white; }
        .top-bar { background: ` + accent + `; color: white; padding: 30px 50px; display: flex; justify-content: space-between; align-items: center; }
        .top-bar h1 { font-size: 22px; font-weight: 300; letter-spacing: 1px; }
        .top-bar .inv-label { font-size: 36px; font-weight: 700; letter-spacing: 3px; }
        .company-logo { max-height: 60px; max-width: 160px; margin-right: 20px; }
        .content { padding: 40px 50px; }
        .meta-grid { display: flex; justify-content: space-between; margin-bottom: 40px; }
        .meta-box h4 { font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: 1.5px; color: #999; margin-bottom: 8px; }
        .meta-box p { font-size: 14px; margin-bottom: 3px; }
        .meta-box .value { font-weight: 600; color: ` + accent + `; }
        table { width: 100%; border-collapse: collapse; margin-bottom: 40px; font-size: 13px; }
        table thead th { background: ` + accent + `; color: white; padding: 12px 16px; text-align: left; font-size: 11px; text-transform: uppercase; letter-spacing: 1px; }
        table tbody td { padding: 14px 16px; border-bottom: 1px solid #f0f0f0; }
        table tbody tr:nth-child(even) { background: #fafafa; }
        table td.numeric, table th.numeric { text-align: right; font-family: "SF Mono", "Courier New", monospace; }
        .summary-section { display: flex; justify-content: flex-end; margin-bottom: 40px; }
        .summary { width: 340px; }
        .summary-row { display: flex; justify-content: space-between; padding: 10px 0; font-size: 14px; }
        .summary-row.total { font-weight: 700; font-size: 18px; color: ` + accent + `; border-top: 2px solid ` + accent + `; margin-top: 8px; padding-top: 16px; }
        .footer { font-size: 12px; color: #888; margin-top: 40px; padding-top: 20px; border-top: 1px solid #eee; }
        .footer p { margin-bottom: 4px; }
        .status-badge { display: inline-block; padding: 4px 12px; border-radius: 20px; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; }
        .status-draft { background: #e3f2fd; color: #1565c0; }
        .status-issued { background: #e3f2fd; color: #1565c0; }
        .status-sent { background: #e0f7fa; color: #00695c; }
        .status-paid { background: #e8f5e9; color: #2e7d32; }
        .status-overdue { background: #ffebee; color: #c62828; }
        .status-voided { background: #f5f5f5; color: #757575; }
    </style>
</head>
<body>
`)

	// Top bar with company name and INVOICE label
	html.WriteString(`    <div class="top-bar">
        <div style="display:flex; align-items:center;">
`)
	if cfg.ShowLogo && data.LogoImageBase64 != "" {
		html.WriteString(`            <img src="data:image/png;base64,` + data.LogoImageBase64 + `" alt="Logo" class="company-logo">
`)
	}
	html.WriteString(`            <h1>` + escapeHTML(data.CompanyName) + `</h1>
        </div>
        <div class="inv-label">INVOICE</div>
    </div>
    <div class="content">
`)

	// Meta grid
	statusClass := "status-" + strings.ToLower(data.Status)
	html.WriteString(`        <div class="meta-grid">
            <div class="meta-box">
                <h4>Bill To</h4>
                <p><strong>` + escapeHTML(data.CustomerName) + `</strong></p>
`)
	if data.CustomerAddress != "" {
		html.WriteString(`                <p>` + escapeHTML(data.CustomerAddress) + `</p>
`)
	}
	if data.CustomerEmail != "" {
		html.WriteString(`                <p>` + escapeHTML(data.CustomerEmail) + `</p>
`)
	}
	html.WriteString(`            </div>
            <div class="meta-box" style="text-align:right;">
                <h4>Invoice Details</h4>
                <p>Invoice # <span class="value">` + escapeHTML(data.InvoiceNumber) + `</span></p>
                <p>Date: <span class="value">` + escapeHTML(data.InvoiceDate) + `</span></p>
`)
	if data.DueDate != "" {
		html.WriteString(`                <p>Due: <span class="value">` + escapeHTML(data.DueDate) + `</span></p>
`)
	}
	html.WriteString(`                <p>Terms: ` + escapeHTML(data.Terms) + `</p>
                <p style="margin-top:8px;"><span class="status-badge ` + statusClass + `">` + escapeHTML(data.Status) + `</span></p>
            </div>
        </div>
`)

	// Line items table
	writeLineItemsTable(&html, data, cfg)

	// Summary
	writeSummarySection(&html, data, cfg)

	// Footer
	if cfg.ShowFooter || cfg.ShowNotes {
		html.WriteString(`        <div class="footer">
`)
		if cfg.ShowNotes && data.Memo != "" {
			html.WriteString(`            <p><strong>Notes:</strong> ` + escapeHTML(data.Memo) + `</p>
`)
		}
		if cfg.PaymentInstructions != "" {
			html.WriteString(`            <p><strong>Payment Instructions:</strong> ` + escapeHTML(cfg.PaymentInstructions) + `</p>
`)
		}
		if cfg.FooterText != "" {
			html.WriteString(`            <p style="margin-top:10px;">` + escapeHTML(cfg.FooterText) + `</p>
`)
		}
		html.WriteString(`        </div>
`)
	}

	html.WriteString(`    </div>
</body>
</html>
`)
	return html.String()
}

// ── Shared render helpers ────────────────────────────────────────────────────

func writeLineItemsTable(html *strings.Builder, data InvoiceRenderData, cfg models.TemplateConfig) {
	html.WriteString(`        <table>
            <thead>
                <tr>
                    <th>Description</th>
                    <th class="numeric">Qty</th>
                    <th class="numeric">Unit Price</th>
`)
	if cfg.ShowTaxSummary {
		html.WriteString(`                    <th class="numeric">Tax</th>
`)
	}
	html.WriteString(`                    <th class="numeric">Amount</th>
                </tr>
            </thead>
            <tbody>
`)
	for _, line := range data.Lines {
		html.WriteString(`                <tr>
                    <td>` + escapeHTML(line.Description) + `</td>
                    <td class="numeric">` + line.Quantity.String() + `</td>
                    <td class="numeric">` + formatCurrency(line.UnitPrice) + `</td>
`)
		if cfg.ShowTaxSummary {
			html.WriteString(`                    <td class="numeric">` + escapeHTML(line.TaxRate) + `</td>
`)
		}
		html.WriteString(`                    <td class="numeric">` + formatCurrency(line.LineTotal) + `</td>
                </tr>
`)
	}
	html.WriteString(`            </tbody>
        </table>
`)
}

func writeSummarySection(html *strings.Builder, data InvoiceRenderData, cfg models.TemplateConfig) {
	html.WriteString(`        <div class="summary-section">
            <div class="summary">
                <div class="summary-row">
                    <span>Subtotal</span>
                    <span>` + formatCurrency(data.Subtotal) + `</span>
                </div>
`)
	if cfg.ShowTaxSummary && !data.TaxTotal.IsZero() {
		html.WriteString(`                <div class="summary-row">
                    <span>Tax</span>
                    <span>` + formatCurrency(data.TaxTotal) + `</span>
                </div>
`)
	}
	html.WriteString(`                <div class="summary-row total">
                    <span>Total</span>
                    <span>` + formatCurrency(data.Amount) + `</span>
                </div>
            </div>
        </div>
`)
}

// ── Data builders ────────────────────────────────────────────────────────────

// BuildInvoiceRenderData constructs render data from invoice + company + template.
// Loads all necessary relationships, snapshots, and logo.
func BuildInvoiceRenderData(db *gorm.DB, companyID uint, invoice *models.Invoice) (*InvoiceRenderData, error) {
	// Load company
	var company models.Company
	if err := db.Where("id = ?", companyID).First(&company).Error; err != nil {
		return nil, fmt.Errorf("company lookup failed: %w", err)
	}

	// Load template config (from invoice's template, or company default, or fallback)
	tmplCfg := models.DefaultTemplateConfig("classic")
	if invoice.TemplateID != nil {
		var tmpl models.InvoiceTemplate
		if err := db.Where("id = ? AND company_id = ?", *invoice.TemplateID, companyID).
			First(&tmpl).Error; err == nil {
			if parsed, err := tmpl.UnmarshalConfig(); err == nil {
				tmplCfg = *parsed
			}
		}
	} else {
		// Try company default template
		var tmpl models.InvoiceTemplate
		if err := db.Where("company_id = ? AND is_default = ?", companyID, true).
			First(&tmpl).Error; err == nil {
			if parsed, err := tmpl.UnmarshalConfig(); err == nil {
				tmplCfg = *parsed
			}
		}
	}

	// Build line renders
	lines := make([]InvoiceLineRender, len(invoice.Lines))
	for i, iline := range invoice.Lines {
		taxRate := "0%"
		if iline.TaxCode != nil {
			taxRate = iline.TaxCode.Rate.Mul(decimal.NewFromInt(100)).StringFixed(2) + "%"
		}
		lines[i] = InvoiceLineRender{
			Description: iline.Description,
			Quantity:    iline.Qty,
			UnitPrice:   iline.UnitPrice,
			TaxRate:     taxRate,
			LineNet:     iline.LineNet,
			LineTax:     iline.LineTax,
			LineTotal:   iline.LineTotal,
		}
	}

	// Format dates
	invoiceDate := invoice.InvoiceDate.Format("January 2, 2006")
	dueDate := ""
	if invoice.DueDate != nil {
		dueDate = invoice.DueDate.Format("January 2, 2006")
	}

	// Load logo as base64
	logoBase64 := ""
	if tmplCfg.ShowLogo && company.LogoPath != "" {
		if data, err := os.ReadFile(company.LogoPath); err == nil {
			logoBase64 = base64.StdEncoding.EncodeToString(data)
		}
	}

	return &InvoiceRenderData{
		InvoiceNumber:   invoice.InvoiceNumber,
		InvoiceDate:     invoiceDate,
		DueDate:         dueDate,
		CustomerName:    invoice.CustomerNameSnapshot,
		CustomerEmail:   invoice.CustomerEmailSnapshot,
		CustomerAddress: invoice.CustomerAddressSnapshot,
		CompanyName:     company.Name,
		CompanyAddress:  buildCompanyAddress(company),
		Lines:           lines,
		Subtotal:        invoice.Subtotal,
		TaxTotal:        invoice.TaxTotal,
		Amount:          invoice.Amount,
		Terms:           models.InvoiceTermsLabel(invoice.Terms),
		Memo:            invoice.Memo,
		Status:          string(invoice.Status),
		LogoImageBase64: logoBase64,
		TemplateConfig:  tmplCfg,
	}, nil
}

// ── String helpers ───────────────────────────────────────────────────────────

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

func formatCurrency(value decimal.Decimal) string {
	return "$" + value.StringFixed(2)
}

func buildCompanyAddress(c models.Company) string {
	parts := make([]string, 0, 4)
	if c.AddressLine != "" {
		parts = append(parts, c.AddressLine)
	}
	cityProv := ""
	if c.City != "" {
		cityProv = c.City
	}
	if c.Province != "" {
		if cityProv != "" {
			cityProv += ", "
		}
		cityProv += c.Province
	}
	if c.PostalCode != "" {
		if cityProv != "" {
			cityProv += " "
		}
		cityProv += c.PostalCode
	}
	if cityProv != "" {
		parts = append(parts, cityProv)
	}
	if c.Country != "" {
		parts = append(parts, c.Country)
	}
	return strings.Join(parts, "\n")
}
