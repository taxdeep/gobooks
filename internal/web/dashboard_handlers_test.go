package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

func TestDashboardPageMountsReactIslandWithFallback(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Dashboard Island Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/", rawToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{
		`id="dashboard-island"`,
		`data-gb-react="dashboard"`,
		`data-api-url="/api/dashboard/overview"`,
		`data-dashboard-fallback`,
		`/static/react/dashboard.js?v=1`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected dashboard HTML to contain %q", want)
		}
	}
}

func TestDashboardOverviewReturnsCompanyScopedTasksSuggestionsAndWidgets(t *testing.T) {
	db := testRouteDB(t)
	if err := db.AutoMigrate(&models.JournalEntry{}, &models.JournalLine{}); err != nil {
		t.Fatal(err)
	}
	companyID := seedCompany(t, db, "Dashboard API Co")
	otherCompanyID := seedCompany(t, db, "Other Dashboard Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedMembership(t, db, user.ID, otherCompanyID)

	now := time.Now().UTC()
	revenueAccount := models.Account{
		CompanyID:         companyID,
		Code:              "4000",
		Name:              "Sales Revenue",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailOperatingRevenue,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeBaseOnly,
	}
	expenseAccount := models.Account{
		CompanyID:         companyID,
		Code:              "6000",
		Name:              "Office Expense",
		RootAccountType:   models.RootExpense,
		DetailAccountType: models.DetailOfficeExpense,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeBaseOnly,
	}
	assetAccount := models.Account{
		CompanyID:         companyID,
		Code:              "1000",
		Name:              "Cash",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeBaseOnly,
	}
	if err := db.Create(&[]models.Account{revenueAccount, expenseAccount, assetAccount}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("company_id = ? AND code = ?", companyID, "4000").First(&revenueAccount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("company_id = ? AND code = ?", companyID, "6000").First(&expenseAccount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("company_id = ? AND code = ?", companyID, "1000").First(&assetAccount).Error; err != nil {
		t.Fatal(err)
	}
	je := models.JournalEntry{
		CompanyID:               companyID,
		EntryDate:               now,
		Status:                  models.JournalEntryStatusPosted,
		TransactionCurrencyCode: "CAD",
		ExchangeRate:            decimal.NewFromInt(1),
		ExchangeRateDate:        now,
		ExchangeRateSource:      "identity",
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]models.JournalLine{
		{CompanyID: companyID, JournalEntryID: je.ID, AccountID: assetAccount.ID, Debit: decimal.NewFromInt(875)},
		{CompanyID: companyID, JournalEntryID: je.ID, AccountID: expenseAccount.ID, Debit: decimal.NewFromInt(125)},
		{CompanyID: companyID, JournalEntryID: je.ID, AccountID: revenueAccount.ID, Credit: decimal.NewFromInt(1000)},
	}).Error; err != nil {
		t.Fatal(err)
	}
	dueDate := now.AddDate(0, 0, 3)
	if err := db.Create(&models.ActionCenterTask{
		CompanyID:      companyID,
		AssignedUserID: &user.ID,
		TaskType:       "bills_due_soon",
		SourceEngine:   "ap_engine",
		SourceType:     "rule",
		Title:          "Review bills due soon",
		Reason:         "There is 1 bill due soon.",
		EvidenceJSON:   `{"count":1,"total_amount":"120.00"}`,
		Priority:       models.ActionTaskPriorityMedium,
		DueDate:        &dueDate,
		ActionURL:      "/banking/pay-bills",
		Status:         models.ActionTaskStatusOpen,
		Fingerprint:    "test:bills_due_soon",
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ActionCenterTask{
		CompanyID:    otherCompanyID,
		TaskType:     "invoices_overdue",
		SourceEngine: "ar_engine",
		SourceType:   "rule",
		Title:        "Other task",
		Reason:       "Should not leak.",
		Priority:     models.ActionTaskPriorityHigh,
		Status:       models.ActionTaskStatusOpen,
		Fingerprint:  "other:invoices_overdue",
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DashboardWidgetSuggestion{
		CompanyID:    companyID,
		UserID:       &user.ID,
		WidgetKey:    "ap_aging",
		Title:        "Add AP Aging to dashboard",
		Reason:       "You opened AP Aging several times.",
		EvidenceJSON: `{"report_key":"ap_aging","open_count":3}`,
		Confidence:   decimal.RequireFromString("0.8000"),
		Source:       models.DashboardSuggestionSourceSystem,
		Status:       models.DashboardSuggestionPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DashboardWidgetSuggestion{
		CompanyID:  otherCompanyID,
		WidgetKey:  "ar_aging",
		Title:      "Other suggestion",
		Reason:     "Should not leak.",
		Confidence: decimal.RequireFromString("0.9000"),
		Source:     models.DashboardSuggestionSourceSystem,
		Status:     models.DashboardSuggestionPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	position := 1
	if err := db.Create(&models.DashboardUserWidget{
		CompanyID:  companyID,
		UserID:     &user.ID,
		WidgetKey:  "cash_balance",
		Title:      "Cash Balance",
		ConfigJSON: `{}`,
		Position:   &position,
		Source:     models.DashboardWidgetSourceUser,
		Active:     true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/api/dashboard/overview", rawToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, resp.StatusCode, string(body))
	}

	var got struct {
		KPIs  []dashboardTestKPI `json:"kpis"`
		Tasks []struct {
			Title     string         `json:"title"`
			ActionURL string         `json:"action_url"`
			Evidence  map[string]any `json:"evidence"`
		} `json:"tasks"`
		Suggestions []struct {
			Title    string         `json:"title"`
			Evidence map[string]any `json:"evidence"`
		} `json:"suggestions"`
		Widgets []struct {
			WidgetKey string `json:"widget_key"`
		} `json:"widgets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if len(got.KPIs) == 0 {
		t.Fatal("expected KPI payload")
	}
	expensesKPI := findDashboardTestKPI(got.KPIs, "expenses")
	if expensesKPI == nil {
		t.Fatalf("expected expenses KPI, got %+v", got.KPIs)
	}
	if strings.Contains(expensesKPI.Value, "-") {
		t.Fatalf("expected expenses to display as a positive cost, got %q", expensesKPI.Value)
	}
	if !strings.Contains(expensesKPI.Href, "/reports/income-statement?from=") || !strings.Contains(expensesKPI.Href, "#expenses") {
		t.Fatalf("expected expenses KPI to link to income statement expenses, got %q", expensesKPI.Href)
	}
	netIncomeKPI := findDashboardTestKPI(got.KPIs, "net_income")
	if netIncomeKPI == nil || !strings.Contains(netIncomeKPI.Href, "#net-income") {
		t.Fatalf("expected net income KPI report link, got %+v", netIncomeKPI)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Title != "Review bills due soon" {
		t.Fatalf("expected one company-scoped task, got %+v", got.Tasks)
	}
	if got.Tasks[0].ActionURL != "/banking/pay-bills" {
		t.Fatalf("expected corrected pay bills action URL, got %q", got.Tasks[0].ActionURL)
	}
	if got.Tasks[0].Evidence["count"] == nil {
		t.Fatalf("expected task evidence to be exposed, got %+v", got.Tasks[0].Evidence)
	}
	if len(got.Suggestions) != 1 || got.Suggestions[0].Title != "Add AP Aging to dashboard" {
		t.Fatalf("expected one company-scoped suggestion, got %+v", got.Suggestions)
	}
	if got.Suggestions[0].Evidence["report_key"] != "ap_aging" {
		t.Fatalf("expected suggestion evidence to be exposed, got %+v", got.Suggestions[0].Evidence)
	}
	if len(got.Widgets) != 1 || got.Widgets[0].WidgetKey != "cash_balance" {
		t.Fatalf("expected one active company-scoped widget, got %+v", got.Widgets)
	}
}

type dashboardTestKPI struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Href  string `json:"href"`
}

func findDashboardTestKPI(kpis []dashboardTestKPI, key string) *dashboardTestKPI {
	for i := range kpis {
		if kpis[i].Key == key {
			return &kpis[i]
		}
	}
	return nil
}
