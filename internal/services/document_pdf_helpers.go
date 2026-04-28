// 遵循project_guide.md
package services

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// document_pdf_helpers.go — shared utilities for the per-doc-type PDF
// adapters introduced in Phase 3 (G4 + G5). Lives in services/ because
// it touches the GORM models; pdf/ stays model-agnostic.

// LoadPDFTemplate picks the company's chosen default for the given doc
// type, falling back to the system Classic preset, then to any active
// system row of the doc type. Never returns "no template" — seed.go
// guarantees Classic exists.
func LoadPDFTemplate(db *gorm.DB, companyID uint, docType models.PDFDocumentType) (*models.PDFTemplate, error) {
	var t models.PDFTemplate
	err := db.Where("company_id = ? AND document_type = ? AND is_default = ? AND is_active = ?",
		companyID, string(docType), true, true).First(&t).Error
	if err == nil {
		return &t, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("lookup company template: %w", err)
	}
	err = db.Where("company_id IS NULL AND document_type = ? AND is_default = ? AND is_active = ?",
		string(docType), true, true).First(&t).Error
	if err == nil {
		return &t, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("lookup system default template: %w", err)
	}
	if err := db.Where("company_id IS NULL AND document_type = ? AND is_active = ?",
		string(docType), true).First(&t).Error; err != nil {
		return nil, fmt.Errorf("no PDF template available for %s: %w", docType, err)
	}
	return &t, nil
}

// FormatPDFMoney converts a Decimal to "1,234.56 CCY" with thousands
// separators. Currency code is appended only when non-empty; the
// renderer's formatValue() handles HTML escaping downstream.
func FormatPDFMoney(d decimal.Decimal, currency string) string {
	formatted := pdfThousandsSeparators(d.StringFixed(2))
	if currency == "" {
		return formatted
	}
	return formatted + " " + currency
}

// pdfThousandsSeparators inserts commas into the integer part of a
// "[-]NNNN.NN" string. Hand-rolled (no fmt.Sprintf) to stay locale-neutral.
func pdfThousandsSeparators(s string) string {
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	intPart := s
	frac := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart = s[:dot]
		frac = s[dot:]
	}
	out := make([]byte, 0, len(intPart)+len(intPart)/3+1)
	for i, c := range []byte(intPart) {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	result := string(out) + frac
	if neg {
		result = "-" + result
	}
	return result
}

// LoadCompanyLogoDataURL reads the company logo file (if any) and returns
// a `data:image/...;base64,...` URL ready to drop into an <img src>.
// Empty string when the company has no LogoPath set, the file is missing,
// or detection fails — system templates use HideWhenEmpty on company.logo
// so a missing logo gracefully renders nothing.
func LoadCompanyLogoDataURL(company models.Company) string {
	if strings.TrimSpace(company.LogoPath) == "" {
		return ""
	}
	data, err := os.ReadFile(company.LogoPath)
	if err != nil {
		return ""
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// PDFEffectiveCurrency returns the document's currency code, falling back
// to the company base when the document doesn't specify one.
func PDFEffectiveCurrency(docCurrency, baseCurrency string) string {
	if strings.TrimSpace(docCurrency) != "" {
		return docCurrency
	}
	return baseCurrency
}

// sanitizePDFFilenameSegment returns a filesystem-safe version of a
// document number for use in PDF filenames. Mirrors the rules in
// InvoicePDFSafeFilename but exposed at package scope for the per-doc-type
// adapters (Quote / SO / Bill / PO / Shipment).
func sanitizePDFFilenameSegment(s string) string {
	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') || b == '.' || b == '_' || b == '-' {
			buf = append(buf, b)
		} else {
			buf = append(buf, '-')
		}
	}
	out := string(buf)
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		out = "unknown"
	}
	return out
}
