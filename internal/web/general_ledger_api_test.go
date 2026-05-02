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

func seedGeneralLedgerPostedEntry(t *testing.T, db *gorm.DB, companyID uint, journalNo string) {
	t.Helper()

	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailSalesRevenue)
	customer := models.Customer{CompanyID: companyID, Name: "GL Customer"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}

	entry := models.JournalEntry{
		CompanyID:    companyID,
		EntryDate:    time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		JournalNo:    journalNo,
		Status:       models.JournalEntryStatusPosted,
		CreatedAt:    time.Now().UTC(),
		SourceType:   "",
		SourceID:     0,
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
			Memo:           "GL fixture sale",
			PartyType:      models.PartyTypeCustomer,
			PartyID:        customer.ID,
		},
		{
			CompanyID:      companyID,
			JournalEntryID: entry.ID,
			AccountID:      revenueID,
			Credit:         decimal.RequireFromString("125.00"),
			Memo:           "GL fixture sale",
			PartyType:      models.PartyTypeCustomer,
			PartyID:        customer.ID,
		},
	}
	if err := db.Create(&lines).Error; err != nil {
		t.Fatal(err)
	}
}

func TestGeneralLedgerPageMountsReactExplorerWithFallback(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "GL React Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-GL-001")

	resp := performRequest(t, app, "/reports/general-ledger?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected General Ledger page, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		`id="general-ledger-island"`,
		`data-gb-react="general-ledger"`,
		`data-api-url="/api/reports/general-ledger?`,
		`from=2026-04-01`,
		`to=2026-04-30`,
		`/static/react/general_ledger.js?v=1`,
		`JE-GL-001`,
		`Cash`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected page to contain %q, got %q", want, body)
		}
	}
}

func TestGeneralLedgerAPIReturnsCompanyScopedJSON(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID := seedCompany(t, db, "GL API Co")
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedGeneralLedgerPostedEntry(t, db, companyID, "JE-GL-API")

	otherCompanyID := seedCompany(t, db, "Other GL Co")
	seedGeneralLedgerPostedEntry(t, db, otherCompanyID, "JE-OTHER")

	resp := performRequest(t, app, "/api/reports/general-ledger?from=2026-04-01&to=2026-04-30", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected General Ledger API response, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var payload struct {
		From         string `json:"from"`
		To           string `json:"to"`
		SectionCount int    `json:"section_count"`
		RowCount     int    `json:"row_count"`
		Totals       struct {
			Debits  string `json:"debits"`
			Credits string `json:"credits"`
		} `json:"totals"`
		Sections []struct {
			AccountCode string `json:"account_code"`
			AccountName string `json:"account_name"`
			Rows        []struct {
				DocumentNumber   string `json:"document_number"`
				DocumentURL      string `json:"document_url"`
				CounterpartyName string `json:"counterparty_name"`
				Description      string `json:"description"`
				Debit            string `json:"debit"`
				Credit           string `json:"credit"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	if payload.From != "2026-04-01" || payload.To != "2026-04-30" {
		t.Fatalf("unexpected report range: %#v", payload)
	}
	if payload.SectionCount != 2 || payload.RowCount != 2 {
		t.Fatalf("expected two account sections and two ledger rows, got sections=%d rows=%d", payload.SectionCount, payload.RowCount)
	}
	if payload.Totals.Debits != "125.00" || payload.Totals.Credits != "125.00" {
		t.Fatalf("unexpected totals: %#v", payload.Totals)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"JE-GL-API", "/journal-entry/", "GL Customer", "GL fixture sale"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected API payload to contain %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "JE-OTHER") {
		t.Fatalf("API payload leaked another company's journal entry: %s", body)
	}
}
