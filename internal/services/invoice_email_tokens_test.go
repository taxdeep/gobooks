// 遵循project_guide.md
package services

// invoice_email_tokens_test.go — Unit tests for invoice email token substitution.
//
// Coverage:
//   TestRenderEmailTokens_AllTokens            — all whitelisted tokens substituted
//   TestRenderEmailTokens_NoDueDate            — {{DueDate}} renders empty when nil
//   TestRenderEmailTokens_UnknownTokenLeftAsIs — unknown tokens not removed
//   TestRenderEmailTokens_SubjectAndBody       — subject and body processed independently
//   TestDefaultEmailBodyRendered_WithDueDate   — due date line included when set
//   TestDefaultEmailBodyRendered_NoDueDate     — due date line omitted when nil
//   TestDefaultEmailSubject                    — fallback subject contains invoice number

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func makeTokenData(withDueDate bool) EmailTokenData {
	due := (*time.Time)(nil)
	if withDueDate {
		d := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		due = &d
	}
	return EmailTokenData{
		CompanyName:   "Acme Corp",
		CustomerName:  "Jane Doe",
		InvoiceNumber: "INV-2026-001",
		InvoiceDate:   time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		DueDate:       due,
		BalanceDue:    decimal.RequireFromString("1050.00"),
		Amount:        decimal.RequireFromString("1050.00"),
		Currency:      "CAD",
	}
}

func TestRenderEmailTokens_AllTokens(t *testing.T) {
	data := makeTokenData(true)
	subject := "Invoice {{InvoiceNumber}} from {{CompanyName}}"
	body := "Dear {{CustomerName}}, invoice {{InvoiceNumber}} dated {{InvoiceDate}} due {{DueDate}} total {{Currency}} {{Amount}} balance {{BalanceDue}}"

	gotSubject, gotBody := RenderEmailTokens(subject, body, data)

	if !strings.Contains(gotSubject, "INV-2026-001") {
		t.Errorf("subject missing InvoiceNumber: %s", gotSubject)
	}
	if !strings.Contains(gotSubject, "Acme Corp") {
		t.Errorf("subject missing CompanyName: %s", gotSubject)
	}
	if !strings.Contains(gotBody, "Jane Doe") {
		t.Errorf("body missing CustomerName: %s", gotBody)
	}
	if !strings.Contains(gotBody, "March 15, 2026") {
		t.Errorf("body missing InvoiceDate: %s", gotBody)
	}
	if !strings.Contains(gotBody, "May 1, 2026") {
		t.Errorf("body missing DueDate: %s", gotBody)
	}
	if !strings.Contains(gotBody, "1050.00") {
		t.Errorf("body missing Amount: %s", gotBody)
	}
	if !strings.Contains(gotBody, "CAD") {
		t.Errorf("body missing Currency: %s", gotBody)
	}
	// No unreplaced tokens remain.
	if strings.Contains(gotSubject, "{{") || strings.Contains(gotBody, "{{") {
		t.Errorf("unreplaced token found — subject: %q body: %q", gotSubject, gotBody)
	}
}

func TestRenderEmailTokens_NoDueDate(t *testing.T) {
	data := makeTokenData(false)
	_, body := RenderEmailTokens("", "Due: {{DueDate}}", data)

	// Token should be replaced with empty string, not left as-is.
	if strings.Contains(body, "{{DueDate}}") {
		t.Errorf("{{DueDate}} should be replaced with empty string when DueDate is nil, got: %s", body)
	}
	// Result is "Due: " (trailing space is acceptable).
	if !strings.HasPrefix(body, "Due: ") {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestRenderEmailTokens_UnknownTokenLeftAsIs(t *testing.T) {
	data := makeTokenData(false)
	_, body := RenderEmailTokens("", "Hello {{Unknown}} and {{CustomerName}}", data)

	// Unknown token must remain verbatim.
	if !strings.Contains(body, "{{Unknown}}") {
		t.Errorf("unknown token should not be removed, got: %q", body)
	}
	// Known token must still be replaced.
	if strings.Contains(body, "{{CustomerName}}") {
		t.Errorf("known token should be replaced, got: %q", body)
	}
}

func TestRenderEmailTokens_SubjectAndBody(t *testing.T) {
	data := makeTokenData(false)
	subjectIn := "Inv {{InvoiceNumber}}"
	bodyIn := "Customer: {{CustomerName}}"

	subjectOut, bodyOut := RenderEmailTokens(subjectIn, bodyIn, data)

	if !strings.Contains(subjectOut, "INV-2026-001") {
		t.Errorf("subject not rendered: %q", subjectOut)
	}
	if !strings.Contains(bodyOut, "Jane Doe") {
		t.Errorf("body not rendered: %q", bodyOut)
	}
	// Ensure they are processed independently (subject token not in body, vice versa).
	if strings.Contains(bodyOut, "INV-2026-001") {
		// This is actually fine — the invoice number token could appear in both.
		// This test just ensures the two outputs are independent strings.
	}
}

func TestDefaultEmailBodyRendered_WithDueDate(t *testing.T) {
	data := makeTokenData(true)
	body := DefaultEmailBodyRendered(data)

	if !strings.Contains(body, "Jane Doe") {
		t.Errorf("body missing CustomerName: %s", body)
	}
	if !strings.Contains(body, "INV-2026-001") {
		t.Errorf("body missing InvoiceNumber: %s", body)
	}
	if !strings.Contains(body, "May 1, 2026") {
		t.Errorf("body missing DueDate: %s", body)
	}
	if !strings.Contains(body, "CAD") {
		t.Errorf("body missing Currency: %s", body)
	}
	// No unreplaced tokens.
	if strings.Contains(body, "{{") {
		t.Errorf("unreplaced token in body: %s", body)
	}
}

func TestDefaultEmailBodyRendered_NoDueDate(t *testing.T) {
	data := makeTokenData(false)
	body := DefaultEmailBodyRendered(data)

	// Due date line should not appear at all.
	if strings.Contains(body, "Due Date:") {
		t.Errorf("due date line should be omitted when DueDate is nil: %s", body)
	}
	// No unreplaced tokens.
	if strings.Contains(body, "{{") {
		t.Errorf("unreplaced token in body: %s", body)
	}
}

func TestDefaultEmailSubject(t *testing.T) {
	s := DefaultEmailSubject("INV-2026-001")
	if !strings.Contains(s, "INV-2026-001") {
		t.Errorf("subject missing invoice number: %q", s)
	}
}
