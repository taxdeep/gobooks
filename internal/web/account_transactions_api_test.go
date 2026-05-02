package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func accountIDByCode(t *testing.T, db *gorm.DB, companyID uint, code string) uint {
	t.Helper()
	var account models.Account
	if err := db.Where("company_id = ? AND code = ?", companyID, code).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	return account.ID
}

func TestAccountTransactionsPageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Account Tx React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-AT-001")
	cashID := accountIDByCode(t, db, companyID, "1000")

	path := fmt.Sprintf("/reports/account-transactions?account_id=%d&from=2026-04-01&to=2026-04-30", cashID)
	resp := performRequest(t, app, path, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Account Transactions page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="account-transactions-island"`,
		`data-gb-react="account-transactions"`,
		`data-api-url="/api/reports/account-transactions?`,
		`account_id=`,
		`from=2026-04-01`,
		`to=2026-04-30`,
		`/static/react/account_transactions.js?v=1`,
		`JE-AT-001`,
		`Cash`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestAccountTransactionsAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Account Tx API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-AT-API")
	cashID := accountIDByCode(t, db, companyID, "1000")

	otherCompanyID := seedCompany(t, db, "Other Account Tx Co")
	seedGeneralLedgerPostedEntry(t, db, otherCompanyID, "JE-AT-OTHER")

	path := fmt.Sprintf("/api/reports/account-transactions?account_id=%d&from=2026-04-01&to=2026-04-30", cashID)
	resp := performRequest(t, app, path, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Account Transactions API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		From          string `json:"from"`
		To            string `json:"to"`
		AccountCode   string `json:"account_code"`
		AccountName   string `json:"account_name"`
		Starting      string `json:"starting_balance"`
		TotalDebits   string `json:"total_debits"`
		TotalCredits  string `json:"total_credits"`
		Ending        string `json:"ending_balance"`
		BalanceChange string `json:"balance_change"`
		RowCount      int    `json:"row_count"`
		Rows          []struct {
			DocumentNumber   string `json:"document_number"`
			DocumentURL      string `json:"document_url"`
			CounterpartyName string `json:"counterparty_name"`
			Description      string `json:"description"`
			Debit            string `json:"debit"`
			Credit           string `json:"credit"`
			Balance          string `json:"balance"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	if payload.From != "2026-04-01" || payload.To != "2026-04-30" {
		t.Fatalf("unexpected report range: %#v", payload)
	}
	if payload.AccountCode != "1000" || payload.AccountName != "Cash" {
		t.Fatalf("unexpected account: %#v", payload)
	}
	if payload.RowCount != 1 || len(payload.Rows) != 1 {
		t.Fatalf("expected one account transaction row, got row_count=%d rows=%d", payload.RowCount, len(payload.Rows))
	}
	if payload.Starting != "0.00" || payload.TotalDebits != "125.00" || payload.TotalCredits != "0.00" || payload.Ending != "125.00" || payload.BalanceChange != "125.00" {
		t.Fatalf("unexpected balances: %#v", payload)
	}
	row := payload.Rows[0]
	if row.DocumentNumber != "JE-AT-API" || row.Debit != "125.00" || row.Credit != "0.00" || row.Balance != "125.00" {
		t.Fatalf("unexpected transaction row: %#v", row)
	}
	if !strings.HasPrefix(row.DocumentURL, "/journal-entry/") {
		t.Fatalf("expected drill-through URL, got %q", row.DocumentURL)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"JE-AT-API", "GL Customer", "GL fixture sale"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "JE-AT-OTHER") {
		t.Fatalf("API payload leaked another company's journal entry: %s", body)
	}
}
