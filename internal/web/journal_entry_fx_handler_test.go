package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

func testJournalRouteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testRouteDB(t)
	if err := db.AutoMigrate(
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.Currency{},
		&models.CompanyCurrency{},
		&models.ExchangeRate{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedJournalCompanyContext(t *testing.T, db *gorm.DB) (uint, string) {
	t.Helper()
	companyID := seedCompany(t, db, "Journal FX Co")
	if err := db.Model(&models.Company{}).Where("id = ?", companyID).Updates(map[string]any{
		"base_currency_code":         "CAD",
		"multi_currency_enabled":     true,
		"account_code_length":        4,
		"account_code_length_locked": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for _, cur := range []models.Currency{
		{Code: "CAD", Name: "Canadian Dollar", Symbol: "$", IsActive: true},
		{Code: "USD", Name: "US Dollar", Symbol: "$", IsActive: true},
	} {
		if err := db.Where("code = ?", cur.Code).FirstOrCreate(&cur).Error; err != nil {
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

func snippetAround(body, marker string, span int) string {
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	end := idx + span
	if end > len(body) {
		end = len(body)
	}
	return body[idx:end]
}

func seedJournalAccount(t *testing.T, db *gorm.DB, companyID uint, code, name string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()
	acc := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              name,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

func seedJournalCustomer(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()
	customer := models.Customer{CompanyID: companyID, Name: name}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	return customer.ID
}

func seedJournalVendor(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()
	vendor := models.Vendor{CompanyID: companyID, Name: name}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	return vendor.ID
}

func seedJournalFXMigrationAppliedAt(t *testing.T, db *gorm.DB, appliedAt time.Time) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT OR REPLACE INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		"048_journal_entry_fx_support.sql",
		appliedAt.UTC(),
	).Error; err != nil {
		t.Fatal(err)
	}
}

func parseJSONBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func performJournalFormRequest(t *testing.T, app *fiber.App, path string, form url.Values, rawToken string) *http.Response {
	t.Helper()
	csrf := newCSRFToken(t)
	if form == nil {
		form = url.Values{}
	}
	form.Set(CSRFFormField, csrf)
	cookies := []*http.Cookie{
		{Name: CSRFCookieName, Value: csrf, Path: "/"},
	}
	if rawToken != "" {
		cookies = append(cookies, &http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	}
	return performSecurityRequest(
		t,
		app,
		http.MethodPost,
		path,
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		cookies...,
	)
}

func TestJournalEntryFormSuggestsEditableNumberAndPostAutoAssigns(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)

	formResp := performRequest(t, app, "/journal-entry", token)
	if formResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected form page, got %d", formResp.StatusCode)
	}
	formBodyBytes, err := io.ReadAll(formResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = formResp.Body.Close()
	formBody := string(formBodyBytes)
	if !strings.Contains(formBody, `data-default-journal-no="JE-0001"`) {
		t.Fatalf("expected suggested JE number in form, got %q", formBody)
	}
	if !strings.Contains(formBody, `name="suggested_journal_no" value="JE-0001"`) {
		t.Fatalf("expected suggested JE number to post with the form, got %q", formBody)
	}

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {""},
		"transaction_currency_code": {"CAD"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[0][credit]":          {""},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][debit]":           {""},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}

	var je models.JournalEntry
	if err := db.Where("company_id = ? AND journal_no = ?", companyID, "JE-0001").First(&je).Error; err != nil {
		t.Fatalf("expected auto-assigned journal entry number: %v", err)
	}

	nextResp := performRequest(t, app, "/journal-entry", token)
	if nextResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected second form page, got %d", nextResp.StatusCode)
	}
	nextBodyBytes, err := io.ReadAll(nextResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = nextResp.Body.Close()
	if !strings.Contains(string(nextBodyBytes), `data-default-journal-no="JE-0002"`) {
		t.Fatalf("expected JE number counter to advance, got %q", string(nextBodyBytes))
	}
}

func TestJournalEntryPost_BaseCurrencyStoresExplicitSnapshotAndProjectsLedger(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-BASE-1"},
		"transaction_currency_code": {"CAD"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[0][credit]":          {""},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][debit]":           {""},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}

	var je models.JournalEntry
	if err := db.Preload("Lines").Where("company_id = ? AND journal_no = ?", companyID, "JE-BASE-1").First(&je).Error; err != nil {
		t.Fatalf("load journal entry: %v", err)
	}
	if je.TransactionCurrencyCode != "CAD" {
		t.Fatalf("expected explicit CAD transaction currency, got %q", je.TransactionCurrencyCode)
	}
	if !je.ExchangeRate.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("expected identity rate 1, got %s", je.ExchangeRate)
	}
	if je.ExchangeRateSource != services.JournalEntryExchangeRateSourceIdentity {
		t.Fatalf("expected identity source, got %q", je.ExchangeRateSource)
	}
	if len(je.Lines) != 2 {
		t.Fatalf("expected 2 journal lines, got %d", len(je.Lines))
	}
	for _, line := range je.Lines {
		if !line.TxDebit.Equal(line.Debit) || !line.TxCredit.Equal(line.Credit) {
			t.Fatal("base-currency JE should store tx amounts equal to base amounts")
		}
	}
	var ledgerCount int64
	if err := db.Model(&models.LedgerEntry{}).Where("journal_entry_id = ?", je.ID).Count(&ledgerCount).Error; err != nil {
		t.Fatalf("count ledger entries: %v", err)
	}
	if ledgerCount != 2 {
		t.Fatalf("expected manual JE to project to ledger, got %d entries", ledgerCount)
	}
}

func TestJournalEntryPost_ForeignCurrencyStoresSnapshotAndDerivedBaseAmounts(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	rateRow, err := services.UpsertExchangeRate(db, services.UpsertExchangeRateInput{
		Base:     "USD",
		Target:   "CAD",
		Rate:     decimal.RequireFromString("1.37000000"),
		RateType: "spot",
		Source:   services.ExchangeRateRowSourceProviderFetched,
		Date:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-USD-1"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceProviderFetched},
		"exchange_rate":             {"1.37000000"},
		"exchange_rate_date":        {"2026-04-10"},
		"exchange_rate_snapshot_id": {decimal.NewFromInt(int64(rateRow.ID)).String()},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[0][credit]":          {""},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][debit]":           {""},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}

	var je models.JournalEntry
	if err := db.Preload("Lines").Where("company_id = ? AND journal_no = ?", companyID, "JE-USD-1").First(&je).Error; err != nil {
		t.Fatalf("load journal entry: %v", err)
	}
	if je.TransactionCurrencyCode != "USD" {
		t.Fatalf("expected USD transaction currency, got %q", je.TransactionCurrencyCode)
	}
	if !je.ExchangeRate.Equal(decimal.RequireFromString("1.37000000")) {
		t.Fatalf("expected stored rate 1.37, got %s", je.ExchangeRate)
	}
	if je.ExchangeRateSource != services.JournalEntryExchangeRateSourceProviderFetched {
		t.Fatalf("expected provider_fetched source, got %q", je.ExchangeRateSource)
	}
	for _, line := range je.Lines {
		if !line.TxDebit.Add(line.TxCredit).Equal(decimal.RequireFromString("100.00")) {
			t.Fatalf("expected tx amount 100.00 on each foreign JE side, got %s/%s", line.TxDebit, line.TxCredit)
		}
		if !line.Debit.Add(line.Credit).Equal(decimal.RequireFromString("137.00")) {
			t.Fatalf("expected derived base amount 137.00 on each foreign JE side, got %s/%s", line.Debit, line.Credit)
		}
	}
}

func TestJournalEntryPost_InvalidRateRejected(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-BAD-RATE"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceManual},
		"exchange_rate":             {"0"},
		"exchange_rate_date":        {"2026-04-10"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected validation re-render, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(bodyBytes)), "valid exchange rate greater than 0") {
		t.Fatalf("expected invalid-rate message, got %q", string(bodyBytes))
	}
	var count int64
	if err := db.Model(&models.JournalEntry{}).Where("company_id = ? AND journal_no = ?", companyID, "JE-BAD-RATE").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expected invalid-rate JE to leave no persisted journal entry")
	}
}

func TestJournalEntryPost_TxImbalanceRejected(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-TX-IMBALANCE"},
		"transaction_currency_code": {"CAD"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"90.00"},
	}, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected validation re-render, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bodyBytes), "Total debits must equal total credits.") {
		t.Fatalf("expected tx-imbalance message, got %q", string(bodyBytes))
	}
}

// Tx-balanced entries whose per-line base-currency conversion produces a
// rounding residual are NOT rejected: the FX conversion engine (see
// fx_conversion_engine.go) absorbs the residual into the last debit/credit
// line via the anchor pattern, so the JE saves successfully with base
// totals in balance. Engine-level behavior is covered by
// TestConvertJournalLineAmounts_BaseImbalanceAbsorbedByAnchor; this test is
// the integration guard at the handler boundary.
//
// Entry: USD 0.01 + USD 0.01 debits vs USD 0.02 credit at rate 1.5.
// Per-line rounding: 0.02 + 0.02 debit vs 0.03 credit → residual +0.01.
// Anchor absorbs on the last debit: 0.02 - 0.01 = 0.01.
// Final base totals: debit 0.03 = credit 0.03.
func TestJournalEntryPost_BaseImbalanceAbsorbedByAnchor(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	rateRow, err := services.UpsertExchangeRate(db, services.UpsertExchangeRateInput{
		Base:     "USD",
		Target:   "CAD",
		Rate:     decimal.RequireFromString("1.50000000"),
		RateType: "spot",
		Source:   services.ExchangeRateRowSourceProviderFetched,
		Date:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-ANCHOR-ABSORB"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceProviderFetched},
		"exchange_rate":             {"1.50000000"},
		"exchange_rate_date":        {"2026-04-10"},
		"exchange_rate_snapshot_id": {decimal.NewFromInt(int64(rateRow.ID)).String()},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"0.01"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][debit]":           {"0.01"},
		"lines[2][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[2][credit]":          {"0.02"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 303 redirect (save succeeds after anchor absorption), got %d; body=%q",
			resp.StatusCode, string(body))
	}

	// Verify the persisted JE has balanced base totals.
	var je models.JournalEntry
	if err := db.Where("company_id = ? AND journal_no = ?", companyID, "JE-ANCHOR-ABSORB").First(&je).Error; err != nil {
		t.Fatalf("reload JE: %v", err)
	}
	var lines []models.JournalLine
	if err := db.Where("journal_entry_id = ?", je.ID).Find(&lines).Error; err != nil {
		t.Fatalf("reload JE lines: %v", err)
	}

	var baseDebit, baseCredit decimal.Decimal
	for _, l := range lines {
		baseDebit = baseDebit.Add(l.Debit)
		baseCredit = baseCredit.Add(l.Credit)
	}
	if !baseDebit.Equal(baseCredit) {
		t.Fatalf("base totals did not balance after anchor absorption: debit=%s credit=%s",
			baseDebit, baseCredit)
	}
}

func TestJournalEntryPost_ManualOverridePersistsWithoutMutatingSharedRates(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-MANUAL-FX"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceManual},
		"exchange_rate":             {"1.44000000"},
		"exchange_rate_date":        {"2026-04-10"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}

	var je models.JournalEntry
	if err := db.Where("company_id = ? AND journal_no = ?", companyID, "JE-MANUAL-FX").First(&je).Error; err != nil {
		t.Fatalf("load manual JE: %v", err)
	}
	if je.ExchangeRateSource != services.JournalEntryExchangeRateSourceManual {
		t.Fatalf("expected manual snapshot source, got %q", je.ExchangeRateSource)
	}
	if !je.ExchangeRate.Equal(decimal.RequireFromString("1.44000000")) {
		t.Fatalf("expected manual rate 1.44, got %s", je.ExchangeRate)
	}
	var rateCount int64
	if err := db.Model(&models.ExchangeRate{}).
		Where("base_currency_code = ? AND target_currency_code = ?", "USD", "CAD").
		Count(&rateCount).Error; err != nil {
		t.Fatal(err)
	}
	if rateCount != 0 {
		t.Fatalf("expected manual JE override to avoid mutating shared exchange-rate rows, got %d rows", rateCount)
	}
}

func TestJournalEntryPost_SavePathNeverBuildsProvider(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	rateRow, err := services.UpsertExchangeRate(db, services.UpsertExchangeRateInput{
		Base:     "USD",
		Target:   "CAD",
		Rate:     decimal.RequireFromString("1.33000000"),
		RateType: "spot",
		Source:   services.ExchangeRateRowSourceProviderFetched,
		Date:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	prevBuilder := buildExchangeRateProvider
	defer func() { buildExchangeRateProvider = prevBuilder }()
	providerBuilt := false
	buildExchangeRateProvider = func() services.ExchangeRateProvider {
		providerBuilt = true
		return &stubProviderForEndpoint{}
	}

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-NO-PROVIDER-SAVE"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceProviderFetched},
		"exchange_rate":             {"1.33000000"},
		"exchange_rate_date":        {"2026-04-10"},
		"exchange_rate_snapshot_id": {decimal.NewFromInt(int64(rateRow.ID)).String()},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}
	if providerBuilt {
		t.Fatal("expected JE save path to avoid constructing the live exchange-rate provider")
	}
}

func TestJournalEntryPost_AccountCompanyIsolationIsEnforced(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	otherCompanyID := seedCompany(t, db, "Other Account Co")
	if err := db.Model(&models.Company{}).Where("id = ?", otherCompanyID).Update("base_currency_code", "CAD").Error; err != nil {
		t.Fatal(err)
	}
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	outsiderRevenueID := seedJournalAccount(t, db, otherCompanyID, "4000", "Other Revenue", models.RootRevenue, models.DetailServiceRevenue)

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-ACCOUNT-FAIL"},
		"transaction_currency_code": {"CAD"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(outsiderRevenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected validation re-render, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bodyBytes), "accounts do not belong to this company") {
		t.Fatalf("expected account isolation message, got %q", string(bodyBytes))
	}
}

func TestJournalEntryPost_PartyCompanyIsolationIsEnforced(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	otherCompanyID := seedCompany(t, db, "Other Co")
	if err := db.Model(&models.Company{}).Where("id = ?", otherCompanyID).Update("base_currency_code", "CAD").Error; err != nil {
		t.Fatal(err)
	}
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	outsiderCustomerID := seedJournalCustomer(t, db, otherCompanyID, "Wrong Customer")

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-PARTY-FAIL"},
		"transaction_currency_code": {"CAD"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[0][party]":           {"customer:" + decimal.NewFromInt(int64(outsiderCustomerID)).String()},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected validation re-render, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bodyBytes), "customers do not belong to this company") {
		t.Fatalf("expected party isolation message, got %q", string(bodyBytes))
	}
	var count int64
	if err := db.Model(&models.JournalEntry{}).Where("company_id = ? AND journal_no = ?", companyID, "JE-PARTY-FAIL").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expected rejected JE to leave no persisted journal entry")
	}
}

func TestJournalEntryPost_PartyDeclaredTypeMismatchReturnsStableValidationMessage(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	customerID := seedJournalCustomer(t, db, companyID, "Typed Wrong")

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-PARTY-TYPE-FAIL"},
		"transaction_currency_code": {"CAD"},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[0][party]":           {"vendor:" + decimal.NewFromInt(int64(customerID)).String()},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected validation re-render, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "selected vendors are invalid for this company") {
		t.Fatalf("expected stable declared-type message, got %q", body)
	}
	if strings.Contains(strings.ToLower(body), "record not found") {
		t.Fatalf("expected normalized validation contract, got raw ORM error in %q", body)
	}
}

func TestJournalEntryPost_ForeignCurrencyAcceptsEarlierShownSnapshotAfterLaterSameDayRefresh(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	firstSnapshot, err := services.UpsertExchangeRate(db, services.UpsertExchangeRateInput{
		Base:     "USD",
		Target:   "CAD",
		Rate:     decimal.RequireFromString("1.33000000"),
		RateType: "spot",
		Source:   services.ExchangeRateRowSourceProviderFetched,
		Date:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.UpsertExchangeRate(db, services.UpsertExchangeRateInput{
		Base:     "USD",
		Target:   "CAD",
		Rate:     decimal.RequireFromString("1.34000000"),
		RateType: "spot",
		Source:   services.ExchangeRateRowSourceProviderFetched,
		Date:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	resp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-USD-SNAPSHOT-A"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceProviderFetched},
		"exchange_rate":             {"1.33000000"},
		"exchange_rate_date":        {"2026-04-10"},
		"exchange_rate_snapshot_id": {decimal.NewFromInt(int64(firstSnapshot.ID)).String()},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"100.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"100.00"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", resp.StatusCode)
	}

	var je models.JournalEntry
	if err := db.Where("company_id = ? AND journal_no = ?", companyID, "JE-USD-SNAPSHOT-A").First(&je).Error; err != nil {
		t.Fatalf("load JE saved with earlier snapshot: %v", err)
	}
	if !je.ExchangeRate.Equal(decimal.RequireFromString("1.33000000")) {
		t.Fatalf("expected earlier shown snapshot rate 1.33 to persist, got %s", je.ExchangeRate)
	}
}

func TestJournalEntryReverse_CopiesSnapshotExactly(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	rateRow, err := services.UpsertExchangeRate(db, services.UpsertExchangeRateInput{
		Base:     "USD",
		Target:   "CAD",
		Rate:     decimal.RequireFromString("1.25000000"),
		RateType: "spot",
		Source:   services.ExchangeRateRowSourceProviderFetched,
		Date:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	postResp := performJournalFormRequest(t, app, "/journal-entry", url.Values{
		"entry_date":                {"2026-04-10"},
		"journal_no":                {"JE-REV-BASE"},
		"transaction_currency_code": {"USD"},
		"exchange_rate_source":      {services.JournalEntryExchangeRateSourceProviderFetched},
		"exchange_rate":             {"1.25000000"},
		"exchange_rate_date":        {"2026-04-10"},
		"exchange_rate_snapshot_id": {decimal.NewFromInt(int64(rateRow.ID)).String()},
		"lines[0][account_id]":      {decimal.NewFromInt(int64(cashID)).String()},
		"lines[0][debit]":           {"80.00"},
		"lines[1][account_id]":      {decimal.NewFromInt(int64(revenueID)).String()},
		"lines[1][credit]":          {"80.00"},
	}, token)
	if postResp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected post redirect, got %d", postResp.StatusCode)
	}

	var original models.JournalEntry
	if err := db.Preload("Lines").Where("company_id = ? AND journal_no = ?", companyID, "JE-REV-BASE").First(&original).Error; err != nil {
		t.Fatalf("load original JE: %v", err)
	}

	reverseResp := performJournalFormRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(original.ID)).String()+"/reverse", url.Values{
		"reverse_date": {"2026-04-11"},
	}, token)
	if reverseResp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected reverse redirect, got %d", reverseResp.StatusCode)
	}

	var reversal models.JournalEntry
	if err := db.Preload("Lines").Where("reversed_from_id = ?", original.ID).First(&reversal).Error; err != nil {
		t.Fatalf("load reversal JE: %v", err)
	}
	if reversal.TransactionCurrencyCode != original.TransactionCurrencyCode ||
		!reversal.ExchangeRate.Equal(original.ExchangeRate) ||
		!reversal.ExchangeRateDate.Equal(original.ExchangeRateDate) ||
		reversal.ExchangeRateSource != original.ExchangeRateSource {
		t.Fatal("expected reversal header to copy snapshot exactly")
	}
	if len(original.Lines) != len(reversal.Lines) {
		t.Fatalf("expected same line count on reversal, got %d vs %d", len(original.Lines), len(reversal.Lines))
	}
	for i := range original.Lines {
		if !reversal.Lines[i].TxDebit.Equal(original.Lines[i].TxCredit) || !reversal.Lines[i].TxCredit.Equal(original.Lines[i].TxDebit) {
			t.Fatal("expected reversal tx amounts to swap exactly")
		}
	}
}

func TestJournalEntryDetail_LegacyBaseJournalEntryStillReadsAsIdentity(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	seedJournalFXMigrationAppliedAt(t, db, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC))
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	legacyJE := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		JournalNo:               "JE-LEGACY-BASE",
		Status:                  models.JournalEntryStatusPosted,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&legacyJE).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("100.00"), Debit: decimal.RequireFromString("100.00")},
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("100.00"), Credit: decimal.RequireFromString("100.00")},
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(legacyJE.ID)).String(), token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected detail page, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "1 CAD = 1 CAD") {
		t.Fatalf("expected legacy base JE to keep honest identity display, got %q", body)
	}
	if strings.Contains(body, "Unavailable (legacy)") {
		t.Fatalf("legacy base JE should not be marked unavailable, got %q", body)
	}
}

func TestJournalEntryDetail_LegacyForeignJournalEntryReconstructsLinkedInvoiceSnapshot(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	seedJournalFXMigrationAppliedAt(t, db, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC))
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	customerID := seedJournalCustomer(t, db, companyID, "Legacy Customer")
	invoice := models.Invoice{
		CompanyID:      companyID,
		InvoiceNumber:  "INV-LEGACY-USD",
		CustomerID:     customerID,
		InvoiceDate:    time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusIssued,
		CurrencyCode:   "USD",
		ExchangeRate:   decimal.RequireFromString("1.25000000"),
		Amount:         decimal.RequireFromString("100.00"),
		AmountBase:     decimal.RequireFromString("125.00"),
		BalanceDue:     decimal.RequireFromString("100.00"),
		BalanceDueBase: decimal.RequireFromString("125.00"),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	legacyJE := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               invoice.InvoiceDate,
		JournalNo:               "JE-LEGACY-USD",
		Status:                  models.JournalEntryStatusPosted,
		SourceType:              models.LedgerSourceInvoice,
		SourceID:                invoice.ID,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        invoice.InvoiceDate,
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&legacyJE).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("125.00"), Debit: decimal.RequireFromString("125.00"), PartyType: models.PartyTypeCustomer, PartyID: customerID},
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("125.00"), Credit: decimal.RequireFromString("125.00"), PartyType: models.PartyTypeCustomer, PartyID: customerID},
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(legacyJE.ID)).String(), token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected detail page, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "Unavailable (legacy)") {
		t.Fatalf("expected legacy foreign JE to be labeled unavailable/reconstructed, got %q", body)
	}
	if !strings.Contains(body, "1 USD = 1.25 CAD") {
		t.Fatalf("expected reconstructed foreign FX rate, got %q", body)
	}
	if !strings.Contains(body, "Original transaction-currency line amounts were not persisted") {
		t.Fatalf("expected legacy FX note, got %q", body)
	}
	if strings.Contains(body, ">125.00</td><td class=\"py-3 pr-4 font-mono tabular-nums\">125.00</td>") {
		t.Fatalf("legacy foreign JE should not display backfilled tx amounts as historical truth, got %q", body)
	}
	if !strings.Contains(body, ">—</td>") {
		t.Fatalf("expected unavailable tx amount placeholders, got %q", body)
	}
}

func TestJournalEntryDetail_LegacyForeignJournalEntryWithoutLinkedSourceShowsUnavailable(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	seedJournalFXMigrationAppliedAt(t, db, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC))
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	legacyJE := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		JournalNo:               "JE-LEGACY-MISSING-SOURCE",
		Status:                  models.JournalEntryStatusPosted,
		SourceType:              models.LedgerSourceInvoice,
		SourceID:                999999,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&legacyJE).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("125.00"), Debit: decimal.RequireFromString("125.00")},
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("125.00"), Credit: decimal.RequireFromString("125.00")},
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(legacyJE.ID)).String(), token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected detail page, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "Historical FX details are unavailable") {
		t.Fatalf("expected explicit unavailable note, got %q", body)
	}
	if strings.Contains(body, "1 CAD = 1 CAD") {
		t.Fatalf("legacy foreign JE without source should not be falsified as identity, got %q", body)
	}
}

func TestJournalEntryList_LegacyForeignJournalEntriesRenderHonestFXSummaries(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	seedJournalFXMigrationAppliedAt(t, db, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC))
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	customerID := seedJournalCustomer(t, db, companyID, "Legacy Customer")
	invoice := models.Invoice{
		CompanyID:      companyID,
		InvoiceNumber:  "INV-LIST-LEGACY-USD",
		CustomerID:     customerID,
		InvoiceDate:    time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusIssued,
		CurrencyCode:   "USD",
		ExchangeRate:   decimal.RequireFromString("1.25000000"),
		Amount:         decimal.RequireFromString("100.00"),
		AmountBase:     decimal.RequireFromString("125.00"),
		BalanceDue:     decimal.RequireFromString("100.00"),
		BalanceDueBase: decimal.RequireFromString("125.00"),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	reconstructable := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               invoice.InvoiceDate,
		JournalNo:               "JE-LIST-LEGACY-USD",
		Status:                  models.JournalEntryStatusPosted,
		SourceType:              models.LedgerSourceInvoice,
		SourceID:                invoice.ID,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        invoice.InvoiceDate,
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&reconstructable).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: reconstructable.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("125.00"), Debit: decimal.RequireFromString("125.00")},
		{CompanyID: companyID, JournalEntryID: reconstructable.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("125.00"), Credit: decimal.RequireFromString("125.00")},
	}).Error; err != nil {
		t.Fatal(err)
	}
	unavailable := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		JournalNo:               "JE-LIST-LEGACY-MISSING",
		Status:                  models.JournalEntryStatusPosted,
		SourceType:              models.LedgerSourceInvoice,
		SourceID:                999999,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&unavailable).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: unavailable.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("125.00"), Debit: decimal.RequireFromString("125.00")},
		{CompanyID: companyID, JournalEntryID: unavailable.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("125.00"), Credit: decimal.RequireFromString("125.00")},
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, "/journal-entry/list", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected list page, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	reconstructableSnippet := snippetAround(body, "JE-LIST-LEGACY-USD", 1800)
	if reconstructableSnippet == "" {
		t.Fatalf("expected reconstructable legacy JE in list, got %q", body)
	}
	if !strings.Contains(reconstructableSnippet, ">USD</div>") {
		t.Fatalf("expected reconstructable legacy JE to show resolved USD currency, got %q", reconstructableSnippet)
	}
	if strings.Contains(reconstructableSnippet, ">CAD</div>") {
		t.Fatalf("legacy list row should not surface raw backfilled CAD semantics, got %q", reconstructableSnippet)
	}
	if !strings.Contains(reconstructableSnippet, "Unavailable (legacy)") {
		t.Fatalf("expected reconstructable legacy JE to carry honest legacy source label, got %q", reconstructableSnippet)
	}
	if strings.Contains(reconstructableSnippet, services.LegacyForeignJournalEntryReversalBlockedMessage) {
		t.Fatalf("reconstructable legacy JE should remain reversible once the shared resolver reconstructs header truth, got %q", reconstructableSnippet)
	}

	unavailableSnippet := snippetAround(body, "JE-LIST-LEGACY-MISSING", 1800)
	if unavailableSnippet == "" {
		t.Fatalf("expected unavailable legacy JE in list, got %q", body)
	}
	if strings.Contains(unavailableSnippet, ">CAD</div>") {
		t.Fatalf("legacy missing-source JE should not show backfilled CAD as trustworthy tx currency, got %q", unavailableSnippet)
	}
	if !strings.Contains(unavailableSnippet, "Unavailable (legacy)") {
		t.Fatalf("expected unavailable legacy JE to show explicit unavailable label, got %q", unavailableSnippet)
	}
	if !strings.Contains(unavailableSnippet, services.LegacyForeignJournalEntryReversalBlockedMessage) {
		t.Fatalf("unreconstructable legacy JE should share the stable reversal block reason, got %q", unavailableSnippet)
	}
}

func TestJournalEntryReverse_LegacyForeignJournalEntryUsesResolvedSnapshotAndHonestUnavailableTxAmounts(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	seedJournalFXMigrationAppliedAt(t, db, time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC))
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	customerID := seedJournalCustomer(t, db, companyID, "Legacy Customer")
	invoice := models.Invoice{
		CompanyID:      companyID,
		InvoiceNumber:  "INV-REV-LEGACY-USD",
		CustomerID:     customerID,
		InvoiceDate:    time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusIssued,
		CurrencyCode:   "USD",
		ExchangeRate:   decimal.RequireFromString("1.25000000"),
		Amount:         decimal.RequireFromString("100.00"),
		AmountBase:     decimal.RequireFromString("125.00"),
		BalanceDue:     decimal.RequireFromString("100.00"),
		BalanceDueBase: decimal.RequireFromString("125.00"),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	legacyJE := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               invoice.InvoiceDate,
		JournalNo:               "JE-REV-LEGACY-USD",
		Status:                  models.JournalEntryStatusPosted,
		SourceType:              models.LedgerSourceInvoice,
		SourceID:                invoice.ID,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        invoice.InvoiceDate,
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&legacyJE).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("125.00"), Debit: decimal.RequireFromString("125.00")},
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("125.00"), Credit: decimal.RequireFromString("125.00")},
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJournalFormRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(legacyJE.ID)).String()+"/reverse", url.Values{
		"reverse_date": {"2026-04-11"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected reverse redirect, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Location"), "reversed=1") {
		t.Fatalf("expected successful reversal redirect, got %q", resp.Header.Get("Location"))
	}

	var reversal models.JournalEntry
	if err := db.Preload("Lines").Where("reversed_from_id = ?", legacyJE.ID).First(&reversal).Error; err != nil {
		t.Fatalf("load reversal JE: %v", err)
	}
	if reversal.TransactionCurrencyCode != "USD" {
		t.Fatalf("expected reconstructed USD header on legacy reversal, got %q", reversal.TransactionCurrencyCode)
	}
	if !reversal.ExchangeRate.Equal(decimal.RequireFromString("1.25000000")) {
		t.Fatalf("expected reconstructed legacy rate 1.25, got %s", reversal.ExchangeRate)
	}
	if reversal.ExchangeRateSource != services.JournalEntryExchangeRateSourceLegacyUnavailable {
		t.Fatalf("expected legacy-unavailable source on honest legacy reversal, got %q", reversal.ExchangeRateSource)
	}
	for _, line := range reversal.Lines {
		if !line.TxDebit.IsZero() || !line.TxCredit.IsZero() {
			t.Fatalf("expected honest legacy reversal to leave unavailable tx line amounts as zero, got %s/%s", line.TxDebit, line.TxCredit)
		}
	}

	detailResp := performRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(reversal.ID)).String(), token)
	if detailResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected reversal detail page, got %d", detailResp.StatusCode)
	}
	defer detailResp.Body.Close()
	bodyBytes, err := io.ReadAll(detailResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "1 USD = 1.25 CAD") {
		t.Fatalf("expected reversal detail to show reconstructed header truth, got %q", body)
	}
	if !strings.Contains(body, "Original transaction-currency line amounts were unavailable") &&
		!strings.Contains(body, "only base-currency line amounts are shown") {
		t.Fatalf("expected honest legacy-unavailable note on reversal detail, got %q", body)
	}
	if strings.Count(body, "—</td>") < 2 {
		t.Fatalf("expected unavailable tx amount placeholders on reversal detail, got %q", body)
	}
}

func TestJournalEntryReverse_LegacyForeignJournalEntryWithoutResolvedSnapshotBlocksWithStableMessage(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)
	seedJournalFXMigrationAppliedAt(t, db, time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC))
	cashID := seedJournalAccount(t, db, companyID, "1000", "Cash", models.RootAsset, models.DetailBank)
	revenueID := seedJournalAccount(t, db, companyID, "4000", "Revenue", models.RootRevenue, models.DetailServiceRevenue)
	legacyJE := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		JournalNo:               "JE-REV-LEGACY-MISSING",
		Status:                  models.JournalEntryStatusPosted,
		SourceType:              models.LedgerSourceInvoice,
		SourceID:                999999,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		ExchangeRateSource:      services.JournalEntryExchangeRateSourceIdentity,
		CreatedAt:               time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&legacyJE).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: cashID, TxDebit: decimal.RequireFromString("125.00"), Debit: decimal.RequireFromString("125.00")},
		{CompanyID: companyID, JournalEntryID: legacyJE.ID, AccountID: revenueID, TxCredit: decimal.RequireFromString("125.00"), Credit: decimal.RequireFromString("125.00")},
	}).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJournalFormRequest(t, app, "/journal-entry/"+decimal.NewFromInt(int64(legacyJE.ID)).String()+"/reverse", url.Values{
		"reverse_date": {"2026-04-11"},
	}, token)
	if resp.StatusCode != fiber.StatusSeeOther {
		t.Fatalf("expected reverse redirect, got %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.Contains(location, "error=legacy-fx-unavailable") {
		t.Fatalf("expected stable legacy FX redirect, got %q", location)
	}
	var reversalCount int64
	if err := db.Model(&models.JournalEntry{}).Where("reversed_from_id = ?", legacyJE.ID).Count(&reversalCount).Error; err != nil {
		t.Fatal(err)
	}
	if reversalCount != 0 {
		t.Fatalf("expected blocked legacy FX reversal to create no reversal JE, got %d", reversalCount)
	}

	listResp := performRequest(t, app, "/journal-entry/list?error=legacy-fx-unavailable", token)
	if listResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected list page after blocked reversal, got %d", listResp.StatusCode)
	}
	defer listResp.Body.Close()
	bodyBytes, err := io.ReadAll(listResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bodyBytes), services.LegacyForeignJournalEntryReversalBlockedMessage) {
		t.Fatalf("expected stable blocked-reversal message, got %q", string(bodyBytes))
	}
}

func TestExchangeRateEndpoint_SameCurrencyIdentityAndProviderFetch(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	companyID, token := seedJournalCompanyContext(t, db)

	resp := performRequest(t, app, "/api/exchange-rate?transaction_currency_code=CAD&date=2026-04-10", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected same-currency identity response, got %d", resp.StatusCode)
	}
	identity := parseJSONBody(t, resp)
	if identity["exchange_rate_source"] != services.JournalEntryExchangeRateSourceIdentity {
		t.Fatalf("expected identity source, got %+v", identity)
	}
	if identity["exchange_rate"] != "1" {
		t.Fatalf("expected identity rate 1, got %+v", identity)
	}

	prevBuilder := buildExchangeRateProvider
	defer func() { buildExchangeRateProvider = prevBuilder }()
	buildExchangeRateProvider = func() services.ExchangeRateProvider {
		return &stubProviderForEndpoint{
			quote: services.ExchangeRateProviderQuote{
				BaseCurrencyCode:   "USD",
				TargetCurrencyCode: "CAD",
				Rate:               decimal.RequireFromString("1.33000000"),
				EffectiveDate:      time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			},
		}
	}

	resp = performRequest(t, app, "/api/exchange-rate?transaction_currency_code=USD&date=2026-04-10&allow_provider_fetch=1", token)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected provider-backed response, got %d", resp.StatusCode)
	}
	providerPayload := parseJSONBody(t, resp)
	if providerPayload["exchange_rate_source"] != services.JournalEntryExchangeRateSourceProviderFetched {
		t.Fatalf("expected provider_fetched source, got %+v", providerPayload)
	}
	var count int64
	if err := db.Model(&models.ExchangeRate{}).
		Where("base_currency_code = ? AND target_currency_code = ? AND source = ?", "USD", "CAD", services.ExchangeRateRowSourceProviderFetched).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected provider-backed endpoint fetch to persist locally for company %d, got %d rows", companyID, count)
	}
}

type stubProviderForEndpoint struct {
	quote services.ExchangeRateProviderQuote
}

func (p *stubProviderForEndpoint) FetchRate(ctx context.Context, baseCurrencyCode, targetCurrencyCode string, date time.Time) (services.ExchangeRateProviderQuote, error) {
	return p.quote, nil
}

// TestExchangeRateEndpoint_TodayUnclosed_FallsBackToPreviousClosedDate tests the
// today/unclosed-date fallback contract: when the requested date is "today" and no
// local rate exists for that date, the provider is called and returns the most recent
// officially closed rate (which may carry yesterday's date). The endpoint must return
// that prior closed date — not today — and persist the row with that effective date.
func TestExchangeRateEndpoint_TodayUnclosed_FallsBackToPreviousClosedDate(t *testing.T) {
	db := testJournalRouteDB(t)
	app := testRouteApp(t, db)
	_, token := seedJournalCompanyContext(t, db)

	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	yesterdayStr := yesterday.Format("2006-01-02")

	// Stub provider returns yesterday's closed date regardless of the requested date —
	// simulating Frankfurter's behaviour when today's market has not yet closed.
	prevBuilder := buildExchangeRateProvider
	defer func() { buildExchangeRateProvider = prevBuilder }()
	buildExchangeRateProvider = func() services.ExchangeRateProvider {
		return &stubProviderForEndpoint{
			quote: services.ExchangeRateProviderQuote{
				BaseCurrencyCode:   "USD",
				TargetCurrencyCode: "CAD",
				Rate:               decimal.RequireFromString("1.42000000"),
				EffectiveDate:      yesterday,
			},
		}
	}

	// No local rate seeded for today or yesterday — forces provider path.
	resp := performRequest(t, app,
		"/api/exchange-rate?transaction_currency_code=USD&date="+today+"&allow_provider_fetch=1",
		token,
	)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	payload := parseJSONBody(t, resp)

	// Effective date returned must be yesterday (the actual closed date), not today.
	if payload["exchange_rate_date"] != yesterdayStr {
		t.Fatalf("expected exchange_rate_date=%q (prior closed date), got %q", yesterdayStr, payload["exchange_rate_date"])
	}

	// Row persisted in DB must carry yesterday's effective_date so future lookups
	// for today (effective_date <= today) will find it without re-fetching.
	var row models.ExchangeRate
	if err := db.
		Where("base_currency_code = ? AND target_currency_code = ?", "USD", "CAD").
		Order("effective_date DESC").
		First(&row).Error; err != nil {
		t.Fatalf("expected persisted exchange rate row: %v", err)
	}
	if !row.EffectiveDate.Equal(yesterday.Truncate(24 * time.Hour)) {
		t.Fatalf("persisted row effective_date=%q, want %q", row.EffectiveDate.Format("2006-01-02"), yesterdayStr)
	}
}
