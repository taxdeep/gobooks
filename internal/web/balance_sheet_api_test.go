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

func seedBalanceSheetPostedEntry(t *testing.T, db *gorm.DB, companyID uint, journalNo string) {
	t.Helper()

	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	equityID := seedJournalAccount(t, db, companyID, "3000", "Owner Equity", models.RootEquity, models.DetailOwnerContribution)

	entry := models.JournalEntry{
		CompanyID:    companyID,
		EntryDate:    time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		JournalNo:    journalNo,
		Status:       models.JournalEntryStatusPosted,
		CreatedAt:    time.Now().UTC(),
		ExchangeRate: decimal.NewFromInt(1),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	lines := []models.JournalLine{
		{
			CompanyID:      companyID,
			JournalEntryID: entry.ID,
			AccountID:      cashID,
			Debit:          decimal.RequireFromString("125.00"),
			Memo:           "Balance sheet fixture",
		},
		{
			CompanyID:      companyID,
			JournalEntryID: entry.ID,
			AccountID:      equityID,
			Credit:         decimal.RequireFromString("125.00"),
			Memo:           "Balance sheet fixture",
		},
	}
	if err := db.Create(&lines).Error; err != nil {
		t.Fatal(err)
	}
}

func TestBalanceSheetPageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Balance React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedBalanceSheetPostedEntry(t, db, companyID, "JE-BS-001")

	resp := performRequest(t, app, "/reports/balance-sheet?as_of=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Balance Sheet page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="balance-sheet-island"`,
		`data-gb-react="balance-sheet"`,
		`data-api-url="/api/reports/balance-sheet?`,
		`as_of=2026-04-30`,
		`/static/react/balance_sheet.js?v=1`,
		`Assets`,
		`Total Liabilities + Equity`,
		`Cash`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestBalanceSheetAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "Balance API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedBalanceSheetPostedEntry(t, db, companyID, "JE-BS-API")

	otherCompanyID := seedCompany(t, db, "Other Balance Co")
	seedJournalAccount(t, db, otherCompanyID, "1999", "Other Secret Asset", models.RootAsset, models.DetailOtherCurrentAsset)

	resp := performRequest(t, app, "/api/reports/balance-sheet?as_of=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected Balance Sheet API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		AsOf   string `json:"as_of"`
		Totals struct {
			Assets               string `json:"assets"`
			Liabilities          string `json:"liabilities"`
			Equity               string `json:"equity"`
			LiabilitiesAndEquity string `json:"liabilities_and_equity"`
			Difference           string `json:"difference"`
			Balanced             bool   `json:"balanced"`
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

	if payload.AsOf != "2026-04-30" {
		t.Fatalf("unexpected as-of date: %#v", payload)
	}
	if payload.Totals.Assets != "125.00" || payload.Totals.Liabilities != "0.00" || payload.Totals.Equity != "125.00" || payload.Totals.LiabilitiesAndEquity != "125.00" || payload.Totals.Difference != "0.00" || !payload.Totals.Balanced {
		t.Fatalf("unexpected totals: %#v", payload.Totals)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"1000", "Cash", "3000", "Owner Equity", "/reports/account-transactions?account_id="} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "Other Secret Asset") || strings.Contains(body, "1999") {
		t.Fatalf("API payload leaked another company's account: %s", body)
	}
}
