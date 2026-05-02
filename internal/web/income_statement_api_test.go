package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
)

func TestIncomeStatementPageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Income React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-IS-001")

	resp := performRequest(t, app, "/reports/income-statement?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Income Statement page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="income-statement-island"`,
		`data-gb-react="income-statement"`,
		`data-api-url="/api/reports/income-statement?`,
		`from=2026-04-01`,
		`to=2026-04-30`,
		`/static/react/income_statement.js?v=1`,
		`Revenue`,
		`Gross Profit`,
		`Net Income`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestIncomeStatementAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Income API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-IS-API")

	otherCompanyID := seedCompany(t, db, "Other Income Co")
	seedJournalAccount(t, db, otherCompanyID, "4999", "Other Secret Revenue", models.RootRevenue, models.DetailSalesRevenue)

	resp := performRequest(t, app, "/api/reports/income-statement?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Income Statement API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Source string `json:"source"`
		Totals struct {
			Revenue     string `json:"revenue"`
			CostOfSales string `json:"cost_of_sales"`
			GrossProfit string `json:"gross_profit"`
			Expenses    string `json:"expenses"`
			NetIncome   string `json:"net_income"`
		} `json:"totals"`
		Sections []struct {
			Title string `json:"title"`
			Root  string `json:"root"`
			Total string `json:"total"`
			Rows  []struct {
				AccountCode string `json:"account_code"`
				AccountName string `json:"account_name"`
				Amount      string `json:"amount"`
				DrillURL    string `json:"drill_url"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	if payload.From != "2026-04-01" || payload.To != "2026-04-30" {
		t.Fatalf("unexpected report range: %#v", payload)
	}
	if payload.Totals.Revenue != "125.00" || payload.Totals.CostOfSales != "0.00" || payload.Totals.GrossProfit != "125.00" || payload.Totals.Expenses != "0.00" || payload.Totals.NetIncome != "125.00" {
		t.Fatalf("unexpected totals: %#v", payload.Totals)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"4000", "Revenue", "/reports/account-transactions?account_id="} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "Other Secret Revenue") || strings.Contains(body, "4999") {
		t.Fatalf("API payload leaked another company's account: %s", body)
	}
}
