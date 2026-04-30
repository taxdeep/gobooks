package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

func TestAccountCurrencySelection(t *testing.T) {
	ctx := services.CompanyCurrencyContext{
		BaseCurrencyCode:       "CAD",
		MultiCurrencyEnabled:   true,
		AllowedCurrencyOptions: []string{"CAD", "USD"},
	}

	mode, code, err := accountCurrencySelection(ctx, models.DetailBank, "usd")
	if err != nil {
		t.Fatalf("expected USD bank currency to be accepted: %v", err)
	}
	if mode != models.CurrencyModeFixedForeign || code == nil || *code != "USD" {
		t.Fatalf("expected fixed_foreign USD, got mode=%q code=%v", mode, code)
	}

	mode, code, err = accountCurrencySelection(ctx, models.DetailOperatingExpense, "USD")
	if err != nil {
		t.Fatalf("non-currency detail should ignore currency selection: %v", err)
	}
	if mode != models.CurrencyModeBaseOnly || code != nil {
		t.Fatalf("expected base_only nil code for non-currency detail, got mode=%q code=%v", mode, code)
	}

	if _, _, err = accountCurrencySelection(ctx, models.DetailAccountsReceivable, "EUR"); err == nil {
		t.Fatal("expected invalid company currency to be rejected")
	}
}

func TestAccountHasJournalMovementIncludesVoidedEntries(t *testing.T) {
	db := testRouteDB(t)
	if err := db.AutoMigrate(&models.JournalEntry{}, &models.JournalLine{}); err != nil {
		t.Fatal(err)
	}

	companyID := seedCompany(t, db, "Movement Lock Co")
	acc := models.Account{
		CompanyID:         companyID,
		Code:              "1000",
		Name:              "USD Bank",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}

	server := &Server{DB: db}
	if server.accountHasJournalMovement(companyID, acc.ID) {
		t.Fatal("new account should not be locked before journal movement")
	}

	je := models.JournalEntry{
		CompanyID: companyID,
		EntryDate: time.Now(),
		Status:    models.JournalEntryStatusVoided,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	line := models.JournalLine{
		CompanyID:      companyID,
		JournalEntryID: je.ID,
		AccountID:      acc.ID,
		Debit:          decimal.NewFromInt(10),
		TxDebit:        decimal.NewFromInt(10),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	if !server.accountHasJournalMovement(companyID, acc.ID) {
		t.Fatal("voided journal movement should still lock account currency")
	}
}

func TestAccountCreateStoresForeignCurrencyForCurrencyAwareDetail(t *testing.T) {
	db := testRouteDB(t)
	seedAccountCurrencyTables(t, db)
	companyID, token := seedAccountCurrencyCompanySession(t, db)
	app := testRouteApp(t, db)

	form := url.Values{
		"code":                {"1000"},
		"name":                {"USD Bank"},
		"root_account_type":   {string(models.RootAsset)},
		"detail_account_type": {string(models.DetailBank)},
		"currency_code":       {"USD"},
	}
	resp := performAccountCurrencyFormRequest(t, app, http.MethodPost, "/accounts", form, token)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect after create, got %d", resp.StatusCode)
	}

	var acc models.Account
	if err := db.Where("company_id = ? AND code = ?", companyID, "1000").First(&acc).Error; err != nil {
		t.Fatal(err)
	}
	if acc.CurrencyMode != models.CurrencyModeFixedForeign || acc.CurrencyCode == nil || *acc.CurrencyCode != "USD" {
		t.Fatalf("expected fixed_foreign USD account, got mode=%q code=%v", acc.CurrencyMode, acc.CurrencyCode)
	}
}

func TestAccountUpdateRejectsCurrencyChangeAfterMovement(t *testing.T) {
	db := testRouteDB(t)
	seedAccountCurrencyTables(t, db)
	companyID, token := seedAccountCurrencyCompanySession(t, db)
	app := testRouteApp(t, db)

	usd := "USD"
	acc := models.Account{
		CompanyID:         companyID,
		Code:              "1000",
		Name:              "USD Bank",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeFixedForeign,
		CurrencyCode:      &usd,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	seedVoidedJournalLineForAccount(t, db, companyID, acc.ID)

	form := url.Values{
		"account_id":          {strconv.FormatUint(uint64(acc.ID), 10)},
		"name":                {"USD Bank"},
		"root_account_type":   {string(models.RootAsset)},
		"detail_account_type": {string(models.DetailBank)},
		"currency_code":       {"CAD"},
	}
	resp := performAccountCurrencyFormRequest(t, app, http.MethodPost, "/accounts/update", form, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected validation response, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)
	if !strings.Contains(body, "Currency cannot be changed") {
		t.Fatalf("expected currency lock message in response, got body: %s", body)
	}

	var after models.Account
	if err := db.First(&after, acc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CurrencyMode != models.CurrencyModeFixedForeign || after.CurrencyCode == nil || *after.CurrencyCode != "USD" {
		t.Fatalf("locked account currency changed unexpectedly: mode=%q code=%v", after.CurrencyMode, after.CurrencyCode)
	}
}

func seedAccountCurrencyTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&models.Currency{},
		&models.CompanyCurrency{},
		&models.JournalEntry{},
		&models.JournalLine{},
	); err != nil {
		t.Fatal(err)
	}
}

func seedAccountCurrencyCompanySession(t *testing.T, db *gorm.DB) (uint, string) {
	t.Helper()
	companyID := seedCompany(t, db, "Account Currency Co")
	if err := db.Model(&models.Company{}).Where("id = ?", companyID).Updates(map[string]any{
		"base_currency_code":     "CAD",
		"multi_currency_enabled": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for _, cur := range []models.Currency{
		{Code: "CAD", Name: "Canadian Dollar", Symbol: "$", IsActive: true},
		{Code: "USD", Name: "US Dollar", Symbol: "$", IsActive: true},
	} {
		if err := db.Create(&cur).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.CompanyCurrency{CompanyID: companyID, CurrencyCode: "USD", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	user, token := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	return companyID, token
}

func seedVoidedJournalLineForAccount(t *testing.T, db *gorm.DB, companyID uint, accountID uint) {
	t.Helper()
	je := models.JournalEntry{
		CompanyID: companyID,
		EntryDate: time.Now(),
		Status:    models.JournalEntryStatusVoided,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	line := models.JournalLine{
		CompanyID:      companyID,
		JournalEntryID: je.ID,
		AccountID:      accountID,
		Debit:          decimal.NewFromInt(10),
		TxDebit:        decimal.NewFromInt(10),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
}

func performAccountCurrencyFormRequest(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, method string, path string, form url.Values, rawToken string) *http.Response {
	t.Helper()
	csrf := newCSRFToken(t)
	form.Set(CSRFFormField, csrf)
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"})
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
