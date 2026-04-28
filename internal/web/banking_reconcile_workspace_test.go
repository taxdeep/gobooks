package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestBankReconcileWorkModeMountsReactWorkspaceWithReversalMetadata(t *testing.T) {
	db := testRouteDB(t)
	migrateBankReconcileWorkspaceTestTables(t, db)

	companyID := seedCompany(t, db, "Reconcile React Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	bankAccount, offsetAccount := seedReconcileAccounts(t, db, companyID)
	origJE, _ := seedReconcileJE(t, db, companyID, bankAccount.ID, offsetAccount.ID, "JE-ORIG", nil, decimal.NewFromInt(100))
	seedReconcileJE(t, db, companyID, bankAccount.ID, offsetAccount.ID, "JE-REV", &origJE.ID, decimal.NewFromInt(-100))

	app := testRouteApp(t, db)
	resp := performRequest(t, app, fmt.Sprintf("/banking/reconcile?account_id=%d&statement_date=2026-04-30&ending_balance=0.00", bankAccount.ID), rawToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{
		`data-gb-react="bank-reconcile"`,
		`/static/react/bank_reconcile.js?v=1`,
		`is_reversal_pair`,
		`/journal-entry/`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected reconciliation workspace HTML to contain %q", want)
		}
	}
}

func TestBankReconcileDraftAPIPersistsOnlyValidCompanyAccountLines(t *testing.T) {
	db := testRouteDB(t)
	migrateBankReconcileWorkspaceTestTables(t, db)

	companyID := seedCompany(t, db, "Reconcile Draft Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	bankAccount, offsetAccount := seedReconcileAccounts(t, db, companyID)
	_, bankLine := seedReconcileJE(t, db, companyID, bankAccount.ID, offsetAccount.ID, "JE-DRAFT", nil, decimal.NewFromInt(25))

	app := testRouteApp(t, db)
	payload, err := json.Marshal(map[string]any{
		"account_id":        fmt.Sprintf("%d", bankAccount.ID),
		"statement_date":    "2026-04-30",
		"ending_balance":    "25.00",
		"selected_line_ids": []string{fmt.Sprintf("%d", bankLine.ID), "999999"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "/api/banking/reconcile/draft", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CSRFHeaderName, "csrf-reconcile-draft")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-reconcile-draft", Path: "/"})

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var draft models.ReconciliationDraft
	if err := db.Where("company_id = ? AND account_id = ?", companyID, bankAccount.ID).First(&draft).Error; err != nil {
		t.Fatalf("expected draft row, got %v", err)
	}
	var selected []string
	if err := json.Unmarshal([]byte(draft.SelectedLineIDs), &selected); err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0] != fmt.Sprintf("%d", bankLine.ID) {
		t.Fatalf("expected only valid line id %d, got %v", bankLine.ID, selected)
	}
}

func migrateBankReconcileWorkspaceTestTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.Reconciliation{},
		&models.ReconciliationDraft{},
		&models.ReconciliationMatchSuggestion{},
		&models.ReconciliationMatchSuggestionLine{},
		&models.ReconciliationMemory{},
	); err != nil {
		t.Fatal(err)
	}
}

func seedReconcileAccounts(t *testing.T, db *gorm.DB, companyID uint) (models.Account, models.Account) {
	t.Helper()
	bank := models.Account{
		CompanyID:         companyID,
		Code:              "1000",
		Name:              "Operating Bank",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeBaseOnly,
	}
	offset := models.Account{
		CompanyID:         companyID,
		Code:              "4000",
		Name:              "Offset",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailOperatingRevenue,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeBaseOnly,
	}
	if err := db.Create(&[]models.Account{bank, offset}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("company_id = ? AND code = ?", companyID, bank.Code).First(&bank).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("company_id = ? AND code = ?", companyID, offset.Code).First(&offset).Error; err != nil {
		t.Fatal(err)
	}
	return bank, offset
}

func seedReconcileJE(t *testing.T, db *gorm.DB, companyID, bankAccountID, offsetAccountID uint, journalNo string, reversedFromID *uint, bankAmount decimal.Decimal) (models.JournalEntry, models.JournalLine) {
	t.Helper()
	entryDate := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	sourceType := models.LedgerSourceManual
	if reversedFromID != nil {
		sourceType = models.LedgerSourceReversal
	}
	je := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               entryDate,
		JournalNo:               journalNo,
		Status:                  models.JournalEntryStatusPosted,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        entryDate,
		ExchangeRateSource:      "identity",
		SourceType:              sourceType,
		ReversedFromID:          reversedFromID,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}

	abs := bankAmount.Abs()
	bankLine := models.JournalLine{CompanyID: companyID, JournalEntryID: je.ID, AccountID: bankAccountID, Memo: journalNo}
	offsetLine := models.JournalLine{CompanyID: companyID, JournalEntryID: je.ID, AccountID: offsetAccountID, Memo: journalNo}
	if bankAmount.IsNegative() {
		bankLine.Credit = abs
		offsetLine.Debit = abs
	} else {
		bankLine.Debit = abs
		offsetLine.Credit = abs
	}
	if err := db.Create(&[]models.JournalLine{bankLine, offsetLine}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("journal_entry_id = ? AND account_id = ?", je.ID, bankAccountID).First(&bankLine).Error; err != nil {
		t.Fatal(err)
	}
	return je, bankLine
}
