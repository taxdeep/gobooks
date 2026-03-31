// 遵循project_guide.md
package services

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
)

func testRenderData() InvoiceRenderData {
	return InvoiceRenderData{
		InvoiceNumber:   "INV-001",
		InvoiceDate:     "January 15, 2026",
		DueDate:         "February 14, 2026",
		CustomerName:    "ACME Corp",
		CustomerEmail:   "billing@acme.com",
		CustomerAddress: "123 Main St, Toronto, ON",
		CompanyName:     "GoBooks Inc",
		CompanyAddress:  "456 Bay St\nToronto, ON M5J 2T3",
		Lines: []InvoiceLineRender{
			{
				Description: "Consulting Services",
				Quantity:    decimal.NewFromInt(10),
				UnitPrice:   decimal.NewFromInt(200),
				TaxRate:     "5.00%",
				LineNet:     decimal.NewFromInt(2000),
				LineTax:     decimal.NewFromInt(100),
				LineTotal:   decimal.NewFromInt(2100),
			},
		},
		Subtotal: decimal.NewFromInt(2000),
		TaxTotal: decimal.NewFromInt(100),
		Amount:   decimal.NewFromInt(2100),
		Terms:    "Net 30",
		Memo:     "Thank you for your business.",
		Status:   "issued",
	}
}

func TestRenderInvoiceToHTML_ClassicTemplate(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("classic")

	html := RenderInvoiceToHTML(data)

	// Verify essential content
	checks := []string{
		"INV-001",
		"ACME Corp",
		"GoBooks Inc",
		"Consulting Services",
		"$2,100.00", // Nope, our format is $2100.00
		"INVOICE",
		"Bill To",
		"Net 30",
	}
	// We use $2100.00 format not $2,100.00
	checks = []string{
		"INV-001",
		"ACME Corp",
		"GoBooks Inc",
		"Consulting Services",
		"$2100.00",
		"INVOICE",
		"Bill To",
		"Net 30",
		"January 15, 2026",
		"February 14, 2026",
		"issued",
	}

	for _, check := range checks {
		if !strings.Contains(html, check) {
			t.Errorf("Classic template missing expected content: %q", check)
		}
	}

	// Verify it's valid HTML
	if !strings.HasPrefix(html, "<!DOCTYPE html>") {
		t.Error("Classic template should start with DOCTYPE")
	}
	if !strings.Contains(html, "</html>") {
		t.Error("Classic template should end with </html>")
	}
}

func TestRenderInvoiceToHTML_ModernTemplate(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("modern")

	html := RenderInvoiceToHTML(data)

	// Modern template uses top-bar style
	if !strings.Contains(html, "top-bar") {
		t.Error("Modern template should use top-bar class")
	}
	if !strings.Contains(html, "#1a1a2e") {
		t.Error("Modern template should use dark accent color")
	}
	if !strings.Contains(html, "INV-001") {
		t.Error("Modern template missing invoice number")
	}
	if !strings.Contains(html, "ACME Corp") {
		t.Error("Modern template missing customer name")
	}
}

func TestRenderInvoiceToHTML_CustomAccentColor(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.TemplateConfig{
		TemplateStyle:  "classic",
		AccentColor:    "#ff6600",
		ShowLogo:       true,
		ShowTaxSummary: true,
		ShowNotes:      true,
		ShowFooter:     true,
	}

	html := RenderInvoiceToHTML(data)

	if !strings.Contains(html, "#ff6600") {
		t.Error("Custom accent color not applied")
	}
}

func TestRenderInvoiceToHTML_HideTaxSummary(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("classic")
	data.TemplateConfig.ShowTaxSummary = false

	html := RenderInvoiceToHTML(data)

	// Tax column should not be in table header
	if strings.Contains(html, "<th class=\"numeric\">Tax</th>") {
		t.Error("Tax column should be hidden when ShowTaxSummary is false")
	}
}

func TestRenderInvoiceToHTML_ShowsLogo(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("classic")
	data.LogoImageBase64 = "iVBORw0KGgoAAAANSUhEUg==" // fake base64

	html := RenderInvoiceToHTML(data)

	if !strings.Contains(html, "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==") {
		t.Error("Logo not embedded in HTML")
	}
}

func TestRenderInvoiceToHTML_HidesLogoWhenDisabled(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("classic")
	data.TemplateConfig.ShowLogo = false
	data.LogoImageBase64 = "iVBORw0KGgoAAAANSUhEUg=="

	html := RenderInvoiceToHTML(data)

	if strings.Contains(html, "data:image/png;base64") {
		t.Error("Logo should be hidden when ShowLogo is false")
	}
}

func TestRenderInvoiceToHTML_PaymentInstructions(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("classic")
	data.TemplateConfig.PaymentInstructions = "Wire to: Bank of Canada #12345"

	html := RenderInvoiceToHTML(data)

	if !strings.Contains(html, "Wire to: Bank of Canada #12345") {
		t.Error("Payment instructions not rendered")
	}
}

func TestRenderInvoiceToHTML_XSSEscaping(t *testing.T) {
	data := testRenderData()
	data.CustomerName = "<script>alert('xss')</script>"
	data.TemplateConfig = models.DefaultTemplateConfig("classic")

	html := RenderInvoiceToHTML(data)

	if strings.Contains(html, "<script>") {
		t.Error("XSS: script tag not escaped in HTML output")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("XSS: script tag should be HTML-escaped")
	}
}

func TestRenderInvoiceToHTML_FooterText(t *testing.T) {
	data := testRenderData()
	data.TemplateConfig = models.DefaultTemplateConfig("classic")
	data.TemplateConfig.FooterText = "All sales are final."

	html := RenderInvoiceToHTML(data)

	if !strings.Contains(html, "All sales are final.") {
		t.Error("Footer text not rendered")
	}
}
