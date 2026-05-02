package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
)

func TestTrialBalancePageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Trial Balance React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-TB-001")

	resp := performRequest(t, app, "/reports/trial-balance?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Trial Balance page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="trial-balance-island"`,
		`data-gb-react="trial-balance"`,
		`data-api-url="/api/reports/trial-balance?`,
		`from=2026-04-01`,
		`to=2026-04-30`,
		`/static/react/trial_balance.js?v=1`,
		`Cash`,
		`Revenue`,
		`Grand Totals`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestTrialBalanceAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Trial Balance API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-TB-API")

	otherCompanyID := seedCompany(t, db, "Other Trial Balance Co")
	seedJournalAccount(t, db, otherCompanyID, "1999", "Other Secret Cash", models.RootAsset, models.DetailBank)

	resp := performRequest(t, app, "/api/reports/trial-balance?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Trial Balance API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		From     string `json:"from"`
		To       string `json:"to"`
		RowCount int    `json:"row_count"`
		Totals   struct {
			Debits     string `json:"debits"`
			Credits    string `json:"credits"`
			Difference string `json:"difference"`
			Balanced   bool   `json:"balanced"`
		} `json:"totals"`
		Sections []struct {
			Title   string `json:"title"`
			Root    string `json:"root"`
			Debits  string `json:"debits"`
			Credits string `json:"credits"`
			Rows    []struct {
				AccountCode    string `json:"account_code"`
				AccountName    string `json:"account_name"`
				Classification string `json:"classification"`
				Debit          string `json:"debit"`
				Credit         string `json:"credit"`
				DrillURL       string `json:"drill_url"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	if payload.From != "2026-04-01" || payload.To != "2026-04-30" {
		t.Fatalf("unexpected report range: %#v", payload)
	}
	if payload.RowCount != 2 {
		t.Fatalf("expected two company accounts, got %d", payload.RowCount)
	}
	if payload.Totals.Debits != "125.00" || payload.Totals.Credits != "125.00" || payload.Totals.Difference != "0.00" || !payload.Totals.Balanced {
		t.Fatalf("unexpected totals: %#v", payload.Totals)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"1000", "Cash", "4000", "Revenue", "/reports/account-transactions?account_id="} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "Other Secret Cash") || strings.Contains(body, "1999") {
		t.Fatalf("API payload leaked another company's account: %s", body)
	}
}
