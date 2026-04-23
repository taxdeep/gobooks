// 遵循project_guide.md
package services

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GenerateInvoicePDF converts invoice HTML to PDF bytes.
// Uses wkhtmltopdf external tool for rendering.
//
// Preconditions:
// - wkhtmltopdf must be installed on the system: `apt-get install wkhtmltopdf`
// - HTML must be valid
//
// Returns PDF as []byte ready for email attachment or download.
// On error (wkhtmltopdf not found, timeout, etc.), returns error.
func GenerateInvoicePDF(htmlContent string) ([]byte, error) {
	// 1. Check if wkhtmltopdf is available
	_, err := exec.LookPath("wkhtmltopdf")
	if err != nil {
		return nil, fmt.Errorf(
			"wkhtmltopdf not found (install via: apt-get install wkhtmltopdf): %w", err,
		)
	}

	// 2. Create temp file for HTML input (wkhtmltopdf needs file path)
	tempHTML, err := os.CreateTemp("", "invoice-*.html")
	if err != nil {
		return nil, fmt.Errorf("temp HTML file creation failed: %w", err)
	}
	defer os.Remove(tempHTML.Name())

	if _, err := tempHTML.WriteString(htmlContent); err != nil {
		tempHTML.Close()
		return nil, fmt.Errorf("temp HTML write failed: %w", err)
	}
	tempHTML.Close()

	// 3. Create temp file for PDF output
	tempPDF, err := os.CreateTemp("", "invoice-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("temp PDF file creation failed: %w", err)
	}
	tempPDF.Close()
	defer os.Remove(tempPDF.Name())

	// 4. Run wkhtmltopdf
	// Options:
	// --quiet: suppress progress messages
	// --enable-local-file-access: allow reading from temp file
	// --page-size A4: standard page size
	// --margin-*: set margins
	// --print-media-type: use print media CSS
	cmd := exec.Command(
		"wkhtmltopdf",
		"--quiet",
		"--enable-local-file-access",
		"--page-size", "A4",
		"--margin-top", "10mm",
		"--margin-right", "10mm",
		"--margin-bottom", "10mm",
		"--margin-left", "10mm",
		"--print-media-type",
		tempHTML.Name(),
		tempPDF.Name(),
	)

	// Capture stderr for error reporting
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Run with 30-second timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf(
				"wkhtmltopdf failed: %v (stderr: %s)", err, stderr.String(),
			)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		return nil, fmt.Errorf("wkhtmltopdf timeout (30 seconds)")
	}

	// 5. Read generated PDF
	pdfBytes, err := os.ReadFile(tempPDF.Name())
	if err != nil {
		return nil, fmt.Errorf("PDF read failed: %w", err)
	}

	return pdfBytes, nil
}

// PDFGeneratorAvailable returns true when wkhtmltopdf is installed and usable.
//
// This is the single source of truth for PDF capability across all paths:
// internal detail page, hosted invoice page, and email attachment.
// All three paths call this function rather than each running their own LookPath.
// An inexpensive OS call (~0.1 ms); safe to call per-request.
//
// Phase 3 G4-cleanup: PDF generation switched from wkhtmltopdf to chromedp
// (headless Chrome). The check accepts any of the chromium-family binaries
// chromedp's auto-detection looks for. wkhtmltopdf is also accepted for the
// transitional period in case any caller still uses the legacy renderer
// (none after G4-cleanup, but kept for forward compat).
func PDFGeneratorAvailable() bool {
	for _, bin := range []string{"chromium-browser", "chromium", "google-chrome", "chrome", "wkhtmltopdf"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return false
}

// InvoicePDFSafeFilename returns the Content-Disposition filename for an invoice PDF.
//
// Safety contract:
//   - Output contains only ASCII letters, digits, '.', '_', and '-'.
//   - All other characters (quotes, semicolons, control chars, CR/LF, <, >, |, :, etc.)
//     are replaced with '-'. This prevents malformed Content-Disposition headers.
//   - Consecutive '-' are collapsed to a single '-'.
//   - Leading and trailing '-' are trimmed from the sanitized segment.
//   - If the sanitized segment is empty, "unknown" is used as fallback.
//   - Final format: "Invoice-<safe>.pdf"
//
// Used by both the internal /invoices/:id/pdf route and the hosted /i/:token/download route.
// This is the single source of truth for PDF filename safety — do not inline alternatives.
func InvoicePDFSafeFilename(invoiceNumber string) string {
	// Build safe segment using ASCII whitelist: [A-Za-z0-9._-]
	// All other bytes → '-'
	buf := make([]byte, 0, len(invoiceNumber))
	for i := 0; i < len(invoiceNumber); i++ {
		b := invoiceNumber[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') || b == '.' || b == '_' || b == '-' {
			buf = append(buf, b)
		} else {
			buf = append(buf, '-')
		}
	}
	safe := string(buf)

	// Collapse consecutive '-' to a single '-'.
	for strings.Contains(safe, "--") {
		safe = strings.ReplaceAll(safe, "--", "-")
	}
	// Trim leading/trailing '-'.
	safe = strings.Trim(safe, "-")

	// Empty-after-clean fallback.
	if safe == "" {
		safe = "unknown"
	}

	return "Invoice-" + safe + ".pdf"
}

// GeneratePDFFilename creates a timestamped filename for invoice PDF.
// Format: invoice-<invoice_number>-<timestamp>.pdf
func GeneratePDFFilename(invoiceNumber string) string {
	timestamp := time.Now().Format("20060102-150405")
	// Sanitize invoice number (remove special chars)
	safeNumber := invoiceNumber
	for _, char := range []string{"<", ">", ":", "\"", "|", "?", "*", "/", "\\"} {
		safeNumber = strings.ReplaceAll(safeNumber, char, "_")
	}
	return fmt.Sprintf("invoice-%s-%s.pdf", safeNumber, timestamp)
}

// SavePDFToFile saves PDF bytes to a file in the given directory.
// Returns the full file path.
// Creates directory if it doesn't exist.
func SavePDFToFile(pdfBytes []byte, outputDir string, filename string) (string, error) {
	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("output directory creation failed: %w", err)
	}

	fullPath := filepath.Join(outputDir, filename)

	// Write PDF file (0644 = rw-r--r--)
	if err := os.WriteFile(fullPath, pdfBytes, 0644); err != nil {
		return "", fmt.Errorf("PDF file write failed: %w", err)
	}

	return fullPath, nil
}

// SavePDFToTempDirectory saves PDF to a temporary directory.
// Returns full path and a cleanup function.
// Cleanup function should be deferred to remove temp files when done.
func SavePDFToTempDirectory(pdfBytes []byte, invoiceNumber string) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "gobooks-invoices-*")
	if err != nil {
		return "", nil, fmt.Errorf("temp directory creation failed: %w", err)
	}

	filename := GeneratePDFFilename(invoiceNumber)
	fullPath, err := SavePDFToFile(pdfBytes, tempDir, filename)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", nil, err
	}

	// Cleanup function removes both file and temp directory
	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return fullPath, cleanup, nil
}

// GenerateInvoicePDFWithUniqueID creates a PDF and saves it with a unique ID in filename.
// Useful for storing PDFs by ID rather than invoice number.
// Returns: filePath, fileID (for database storage)
func GenerateInvoicePDFWithUniqueID(pdfBytes []byte, invoiceNumber string, outputDir string) (string, string, error) {
	fileID := uuid.New().String()
	filename := fmt.Sprintf("invoice-%s-%s.pdf", fileID, time.Now().Format("20060102"))

	fullPath, err := SavePDFToFile(pdfBytes, outputDir, filename)
	if err != nil {
		return "", "", err
	}

	return fullPath, fileID, nil
}
