package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func seedCashFlowFixture(t *testing.T, db *gorm.DB, companyID uint) {
	t.Helper()

	bankID := seedJournalAccount(t, db, companyID, "1000", "Operating Bank", models.RootAsset, models.DetailBank)
	equityID := seedJournalAccount(t, db, companyID, "3000", "Owner Equity", models.RootEquity, models.DetailOwnerContribution)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Sales Revenue", models.RootRevenue, models.DetailSalesRevenue)
	expenseID := seedJournalAccount(t, db, companyID, "6100", "Office Expense", models.RootExpense, models.DetailOfficeExpense)

	seedCashFlowEntry(t, db, companyID, "JE-CF-OPEN", time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), models.LedgerSourceOpeningBalance, 1, []models.JournalLine{
		{CompanyID: companyID, AccountID: bankID, Debit: decimal.RequireFromString("100.00"), Memo: "Opening cash"},
		{CompanyID: companyID, AccountID: equityID, Credit: decimal.RequireFromString("100.00"), Memo: "Opening cash"},
	})
	seedCashFlowEntry(t, db, companyID, "JE-CF-IN", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), models.LedgerSourceCustomerReceipt, 1, []models.JournalLine{
		{CompanyID: companyID, AccountID: bankID, Debit: decimal.RequireFromString("300.00"), Memo: "Customer receipt"},
		{CompanyID: companyID, AccountID: revenueID, Credit: decimal.RequireFromString("300.00"), Memo: "Customer receipt"},
	})
	seedCashFlowEntry(t, db, companyID, "JE-CF-OUT", time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC), models.LedgerSourcePayment, 1, []models.JournalLine{
		{CompanyID: companyID, AccountID: expenseID, Debit: decimal.RequireFromString("80.00"), Memo: "Vendor payment"},
		{CompanyID: companyID, AccountID: bankID, Credit: decimal.RequireFromString("80.00"), Memo: "Vendor payment"},
	})
}

func seedCashFlowEntry(t *testing.T, db *gorm.DB, companyID uint, journalNo string, entryDate time.Time, sourceType models.LedgerSourceType, sourceID uint, lines []models.JournalLine) {
	t.Helper()

	entry := models.JournalEntry{
		CompanyID:    companyID,
		EntryDate:    entryDate,
		JournalNo:    journalNo,
		Status:       models.JournalEntryStatusPosted,
		SourceType:   sourceType,
		SourceID:     sourceID,
		CreatedAt:    time.Now().UTC(),
		ExchangeRate: decimal.NewFromInt(1),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	for i := range lines {
		lines[i].JournalEntryID = entry.ID
	}
	if err := db.Create(&lines).Error; err != nil {
		t.Fatal(err)
	}
}

func TestCashFlowPageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Cash Flow React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedCashFlowFixture(t, db, companyID)

	resp := performRequest(t, app, "/reports/cash-flow?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Cash Flow page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="cash-flow-island"`,
		`data-gb-react="cash-flow"`,
		`data-api-url="/api/reports/cash-flow?`,
		`from=2026-04-01`,
		`to=2026-04-30`,
		`/static/react/cash_flow.js?v=1`,
		`Operating Bank`,
		`Where Cash Came From`,
		`Where Cash Went`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestCashFlowAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Cash Flow API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedCashFlowFixture(t, db, companyID)

	otherCompanyID := seedCompany(t, db, "Other Cash Flow Co")
	seedJournalAccount(t, db, otherCompanyID, "1099", "Other Secret Bank", models.RootAsset, models.DetailBank)

	resp := performRequest(t, app, "/api/reports/cash-flow?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Cash Flow API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Totals struct {
			OpeningCash  string `json:"opening_cash"`
			TotalInflow  string `json:"total_inflow"`
			TotalOutflow string `json:"total_outflow"`
			NetChange    string `json:"net_change"`
			ClosingCash  string `json:"closing_cash"`
		} `json:"totals"`
		Accounts []struct {
			AccountCode    string `json:"account_code"`
			AccountName    string `json:"account_name"`
			OpeningBalance string `json:"opening_balance"`
			TotalInflow    string `json:"total_inflow"`
			TotalOutflow   string `json:"total_outflow"`
			ClosingBalance string `json:"closing_balance"`
			DrillURL       string `json:"drill_url"`
		} `json:"accounts"`
		Sources struct {
			Inflows []struct {
				SourceLabel string `json:"source_label"`
				Net         string `json:"net"`
			} `json:"inflows"`
			Outflows []struct {
				SourceLabel string `json:"source_label"`
				Net         string `json:"net"`
			} `json:"outflows"`
		} `json:"sources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	if payload.From != "2026-04-01" || payload.To != "2026-04-30" {
		t.Fatalf("unexpected report range: %#v", payload)
	}
	if payload.Totals.OpeningCash != "100.00" || payload.Totals.TotalInflow != "300.00" || payload.Totals.TotalOutflow != "80.00" || payload.Totals.NetChange != "220.00" || payload.Totals.ClosingCash != "320.00" {
		t.Fatalf("unexpected totals: %#v", payload.Totals)
	}
	if len(payload.Accounts) != 1 || payload.Accounts[0].AccountCode != "1000" || payload.Accounts[0].AccountName != "Operating Bank" || payload.Accounts[0].ClosingBalance != "320.00" {
		t.Fatalf("unexpected accounts: %#v", payload.Accounts)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"Receipt", "Payment", "/reports/account-transactions?account_id="} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "Other Secret Bank") || strings.Contains(body, "1099") {
		t.Fatalf("API payload leaked another company's account: %s", body)
	}
}
