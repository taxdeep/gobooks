package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestJournalEntryReportPageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "JE Report React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-REPORT-001")

	resp := performRequest(t, app, "/reports/journal-entries?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Journal Entries report page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="journal-entry-report-island"`,
		`data-gb-react="journal-entry-report"`,
		`data-api-url="/api/reports/journal-entries?`,
		`from=2026-04-01`,
		`to=2026-04-30`,
		`/static/react/journal_entry_report.js?v=1`,
		`JE-REPORT-001`,
		`Cash`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestJournalEntryReportAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "JE Report API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-REPORT-API")

	otherCompanyID := seedCompany(t, db, "Other JE Report Co")
	seedGeneralLedgerPostedEntry(t, db, otherCompanyID, "JE-REPORT-OTHER")

	resp := performRequest(t, app, "/api/reports/journal-entries?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Journal Entries API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		From       string `json:"from"`
		To         string `json:"to"`
		EntryCount int    `json:"entry_count"`
		LineCount  int    `json:"line_count"`
		Totals     struct {
			Debits  string `json:"debits"`
			Credits string `json:"credits"`
		} `json:"totals"`
		Entries []struct {
			JournalNo   string `json:"journal_no"`
			DocumentURL string `json:"document_url"`
			Debits      string `json:"debits"`
			Credits     string `json:"credits"`
			Lines       []struct {
				AccountCode string `json:"account_code"`
				AccountName string `json:"account_name"`
				Memo        string `json:"memo"`
				Debit       string `json:"debit"`
				Credit      string `json:"credit"`
			} `json:"lines"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	if payload.From != "2026-04-01" || payload.To != "2026-04-30" {
		t.Fatalf("unexpected report range: %#v", payload)
	}
	if payload.EntryCount != 1 || payload.LineCount != 2 {
		t.Fatalf("expected one journal entry and two lines, got entries=%d lines=%d", payload.EntryCount, payload.LineCount)
	}
	if payload.Totals.Debits != "125.00" || payload.Totals.Credits != "125.00" {
		t.Fatalf("unexpected totals: %#v", payload.Totals)
	}
	if len(payload.Entries) != 1 || payload.Entries[0].JournalNo != "JE-REPORT-API" {
		t.Fatalf("unexpected entries: %#v", payload.Entries)
	}
	if !strings.HasPrefix(payload.Entries[0].DocumentURL, "/journal-entry/") {
		t.Fatalf("expected drill-through URL, got %q", payload.Entries[0].DocumentURL)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"1000", "Cash", "4000", "Revenue", "GL fixture sale"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "JE-REPORT-OTHER") {
		t.Fatalf("API payload leaked another company's journal entry: %s", body)
	}
}
