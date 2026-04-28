// 遵循project_guide.md
package web

// bill_edit_reload_test.go — regression lock for the Edit Bill
// (review mode) reload path. A prior revision of handleBillEdit
// rebuilt BillLineFormRow from the persisted BillLine but silently
// dropped ProductServiceID, Qty, and UnitPrice — which collapsed
// every item-aware line into a "— Expense only —" row with Qty=1
// and UnitPrice=0 when the operator landed on the review screen
// after Save Draft. Submit then either round-tripped garbage or
// failed the post with an opaque "Could not submit bill." message.
//
// This test saves a draft with a stock item + non-default Qty/
// UnitPrice, then issues GET /bills/:id/edit and asserts the
// rendered HTML carries all three fields through to the Alpine
// InitialLinesJSON that hydrates the editor.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
)

func TestBillEdit_ReloadPreservesItemQtyUnitPrice(t *testing.T) {
	db := testBillIN1DB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	fx := seedIN1Fixture(t, db)
	// Seed N30 payment term — handleBillEdit loads payment_terms to
	// populate the Terms dropdown and needs at least one active row.
	if err := db.Create(&models.PaymentTerm{
		CompanyID: fx.CompanyID, Code: "N30", Description: "Net 30",
		NetDays: 30, IsDefault: true, IsActive: true, SortOrder: 1,
	}).Error; err != nil {
		t.Fatalf("seed payment term: %v", err)
	}
	// editorFlowApp doesn't register the /bills/:id/edit route,
	// so extend it with that handler for this test.
	app := editorFlowApp(server, user, fx.CompanyID)
	app.Get("/bills/:id/edit", func(c *fiber.Ctx) error {
		return server.handleBillEdit(c)
	})

	form := url.Values{
		"bill_number":                {"BILL-RELOAD-001"},
		"vendor_id":                  {fmt.Sprintf("%d", fx.VendorID)},
		"bill_date":                  {"2026-04-21"},
		"terms":                      {"N30"},
		"due_date":                   {"2026-05-21"},
		"warehouse_id":               {fmt.Sprintf("%d", fx.WarehouseID)},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", fx.ItemID)},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", fx.InventoryAccountID)},
		"line_description[0]":        {"Reload widget"},
		"line_qty[0]":                {"7"},
		"line_unit_price[0]":         {"42.50"},
		"line_amount[0]":             {"297.50"},
		"line_tax_code_id[0]":        {""},
	}
	saveResp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save draft: status %d", saveResp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", fx.CompanyID, "BILL-RELOAD-001").
		First(&bill).Error; err != nil {
		t.Fatalf("load bill: %v", err)
	}

	// GET the edit page (review mode).
	editResp := performFormRequest(t, app, http.MethodGet,
		fmt.Sprintf("/bills/%d/edit?locked=1", bill.ID), nil, "")
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("GET edit: status %d", editResp.StatusCode)
	}
	body, _ := io.ReadAll(editResp.Body)
	html := string(body)

	// The InitialLinesJSON (data-initial-lines attribute) feeds the
	// Alpine editor. templ HTML-escapes attribute values so JSON
	// quotes appear as &#34; in the rendered output. All three of
	// the previously-dropped fields MUST be present with the
	// committed values — not defaults.
	itemIDStr := fmt.Sprintf(`&#34;product_service_id&#34;:&#34;%d&#34;`, fx.ItemID)
	if !strings.Contains(html, itemIDStr) {
		t.Errorf("expected product_service_id=%d in rendered InitialLinesJSON (Rule #4 trace key must round-trip), got body snippet:\n%s",
			fx.ItemID, extractBillEditorInitialLines(html))
	}
	if !strings.Contains(html, `&#34;qty&#34;:&#34;7&#34;`) {
		t.Errorf("expected qty=7 in rendered InitialLinesJSON (not the default 1), got:\n%s",
			extractBillEditorInitialLines(html))
	}
	if !strings.Contains(html, `&#34;unit_price&#34;:&#34;42.5000&#34;`) {
		t.Errorf("expected unit_price=42.5000 in rendered InitialLinesJSON (not the default 0.00), got:\n%s",
			extractBillEditorInitialLines(html))
	}
}

// extractBillEditorInitialLines pulls the data-initial-lines attribute
// value out of the rendered HTML for error-message context.
func extractBillEditorInitialLines(html string) string {
	const marker = `data-initial-lines=`
	i := strings.Index(html, marker)
	if i < 0 {
		return "(data-initial-lines not found)"
	}
	start := i + len(marker)
	// Value is quoted by templ's attribute renderer — double quote or
	// HTML-escaped single quote. Grab up to the next quote.
	if start < len(html) && (html[start] == '"' || html[start] == '\'') {
		quote := html[start]
		end := strings.IndexByte(html[start+1:], quote)
		if end > 0 {
			return html[start+1 : start+1+end]
		}
	}
	// Fallback: next 500 chars.
	if start+500 < len(html) {
		return html[start : start+500]
	}
	return html[start:]
}
