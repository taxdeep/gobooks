// 遵循project_guide.md
package web

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/logging"
	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"

	"github.com/shopspring/decimal"
)

func (s *Server) handleDashboard(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// Dashboard MVP: calculate a lightweight P&L + expenses breakdown and
	// show recent revenue trend. We keep everything simple and non-dense.
	now := time.Now()
	fromDate := now.AddDate(0, 0, -30)
	toDate := now

	toMoneyVM := func(d decimal.Decimal) pages.MoneyVM {
		return pages.MoneyVM{
			Value:      pages.Money(d),
			IsPositive: d.GreaterThanOrEqual(decimal.Zero),
		}
	}

	vm := pages.DashboardVM{
		HasCompany:   true,
		RangeLabel:   "Last 30 days",
		RevenueTrend: []pages.RevenueTrendPointVM{},
	}

	// Profit & Loss summary (and expenses list are derived from the same report).
	if report, err := services.IncomeStatementReport(s.DB, companyID, fromDate, toDate); err == nil {
		vm.PnL.Revenue = toMoneyVM(report.TotalRevenue)
		// Expenses are typically outflows; show as negative so we can color red.
		vm.PnL.Expenses = toMoneyVM(report.TotalExpenses.Neg())
		vm.PnL.NetIncome = toMoneyVM(report.NetIncome)

		vm.Expenses.Total = vm.PnL.Expenses
		// Top expense accounts by absolute value (now already positive cost -> we negate for display).
		// Note: report.Expenses is naturally "expense" accounts (expense/cost_of_sales not included).
		top := report.Expenses
		if len(top) > 6 {
			top = top[:6]
		}
		vm.Expenses.TopLines = make([]pages.ExpenseLineVM, 0, len(top))
		for _, l := range top {
			amt := l.Amount.Neg()
			vm.Expenses.TopLines = append(vm.Expenses.TopLines, pages.ExpenseLineVM{
				Account: l.Name,
				Amount:  toMoneyVM(amt),
			})
		}
	}

	// Optional: revenue trend for last 3 calendar months.
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for i := 2; i >= 0; i-- {
		ms := monthStart.AddDate(0, -i, 0)
		me := ms.AddDate(0, 1, -1)
		rep, err := services.IncomeStatementReport(s.DB, companyID, ms, me)
		if err != nil {
			continue
		}
		vm.RevenueTrend = append(vm.RevenueTrend, pages.RevenueTrendPointVM{
			Label: ms.Format("2006-01"),
			Revenue: pages.MoneyVM{
				Value:      pages.Money(rep.TotalRevenue),
				IsPositive: rep.TotalRevenue.GreaterThanOrEqual(decimal.Zero),
			},
		})
	}

	// Right column: bank accounts list (best-effort MVP heuristic).
	var assetAccounts []models.Account
	if err := s.DB.Where("company_id = ? AND root_account_type = ?", companyID, models.RootAsset).Order("code asc").Limit(50).Find(&assetAccounts).Error; err == nil {
		bankAccounts := make([]models.Account, 0, len(assetAccounts))
		for _, a := range assetAccounts {
			if a.DetailAccountType == models.DetailBank || strings.Contains(strings.ToLower(a.Name), "bank") {
				bankAccounts = append(bankAccounts, a)
			}
		}
		if len(bankAccounts) == 0 {
			bankAccounts = assetAccounts
		}
		if len(bankAccounts) > 5 {
			bankAccounts = bankAccounts[:5]
		}
		vm.BankAccounts = make([]pages.BankAccountVM, 0, len(bankAccounts))
		for _, a := range bankAccounts {
			vm.BankAccounts = append(vm.BankAccounts, pages.BankAccountVM{
				Code: a.Code,
				Name: a.Name,
			})
		}
	}

	return pages.Dashboard(vm).Render(c.Context(), c)
}

func (s *Server) handleSetupForm(c *fiber.Ctx) error {
	return pages.Setup(pages.SetupViewModel{
		Active: "Setup",
		Values: pages.SetupFormValues{
			AccountCodeLength: "4",
		},
		Errors: pages.SetupFormErrors{},
	}).Render(c.Context(), c)
}

func (s *Server) handleReportsHub(c *fiber.Ctx) error {
	companyID, hasCompany := ActiveCompanyIDFromCtx(c)
	user := UserFromCtx(c)

	vm := pages.ReportsHubVM{
		HasCompany: hasCompany,
		Categories: buildReportsHubCategories(),
	}
	// Favourites only exist when both user + company are resolved.
	// Pre-company users (mid-onboarding) get the categorized list with
	// no stars filled in.
	if hasCompany && user != nil {
		favs, err := services.ListUserReportFavourites(s.DB, user.ID, companyID)
		if err == nil {
			vm.Favourites = favs
			vm.FavouriteEntries = collectFavouriteEntries(favs)
		}
	}
	return pages.ReportsHub(vm).Render(c.Context(), c)
}

// buildReportsHubCategories transforms the registry into the per-
// category VM slices the templ iterates. One pass; cheap.
func buildReportsHubCategories() []pages.ReportsHubCategoryVM {
	cats := services.Categories()
	out := make([]pages.ReportsHubCategoryVM, 0, len(cats))
	for _, c := range cats {
		entries := services.ReportsByCategory(c)
		items := make([]pages.ReportsHubItemVM, 0, len(entries))
		for _, e := range entries {
			items = append(items, pages.ReportsHubItemVM{
				Key:   e.Key,
				Title: e.Title,
				Desc:  e.Desc,
				Href:  e.Href,
			})
		}
		out = append(out, pages.ReportsHubCategoryVM{
			Key:         string(c),
			Label:       services.ReportCategoryLabel(c),
			Description: services.ReportCategoryDescription(c),
			Items:       items,
		})
	}
	return out
}

// collectFavouriteEntries returns the registry rows for keys the user
// has starred, in registry order. Used by the templ's Favourites
// section so the order is stable across renders.
func collectFavouriteEntries(favs map[string]bool) []pages.ReportsHubItemVM {
	if len(favs) == 0 {
		return nil
	}
	out := []pages.ReportsHubItemVM{}
	for _, e := range services.AllReports() {
		if favs[e.Key] {
			out = append(out, pages.ReportsHubItemVM{
				Key:   e.Key,
				Title: e.Title,
				Desc:  e.Desc,
				Href:  e.Href,
			})
		}
	}
	return out
}

// handleSalesByCustomer renders the Sales-by-Customer summary —
// posted invoices grouped by customer with count + total + average.
// Click a customer name to drill into /customers/:id workspace.
func (s *Server) handleSalesByCustomer(c *fiber.Ctx) error {
	return s.handleCounterpartySummary(c, counterpartySummaryConfig{
		ReportTitle:    "Sales by Customer",
		FormAction:     "/reports/sales-by-customer",
		ColumnLabel:    "Customer",
		DrillURLPrefix: "/customers/",
		EmptyHint:      "No invoices in this period.",
		Build:          services.BuildSalesByCustomerReport,
	})
}

// handleExpenseByVendor renders the Expense-by-Vendor summary —
// posted bills + expenses grouped by vendor with count + total +
// average. Click a vendor name to drill into /vendors/:id workspace.
func (s *Server) handleExpenseByVendor(c *fiber.Ctx) error {
	return s.handleCounterpartySummary(c, counterpartySummaryConfig{
		ReportTitle:    "Expense by Vendor",
		FormAction:     "/reports/expense-by-vendor",
		ColumnLabel:    "Vendor",
		DrillURLPrefix: "/vendors/",
		EmptyHint:      "No bills or expenses in this period.",
		Build:          services.BuildExpenseByVendorReport,
	})
}

// counterpartySummaryConfig parameterises handleCounterpartySummary
// so Sales-by-Customer + Expense-by-Vendor share the boilerplate
// (toolbar wiring, date parsing, error rendering, VM construction).
type counterpartySummaryConfig struct {
	ReportTitle    string
	FormAction     string
	ColumnLabel    string
	DrillURLPrefix string
	EmptyHint      string
	Build          func(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (*services.CounterpartySummaryReport, error)
}

func (s *Server) handleCounterpartySummary(c *fiber.Ctx, cfg counterpartySummaryConfig) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)
	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, From: fromStr, To: toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   cfg.ReportTitle,
		FormAction:    cfg.FormAction,
		Mode:          "period",
	}

	baseVM := pages.CounterpartySummaryVM{
		HasCompany:     true,
		From:           fromStr,
		To:             toStr,
		Toolbar:        toolbar,
		PageTitle:      cfg.ReportTitle,
		ColumnLabel:    cfg.ColumnLabel,
		DrillURLPrefix: cfg.DrillURLPrefix,
		EmptyHint:      cfg.EmptyHint,
	}

	if errMsg != "" {
		baseVM.FormError = errMsg
		return pages.CounterpartySummary(baseVM).Render(c.Context(), c)
	}

	report, err := cfg.Build(s.DB, companyID, fromDate, toDate)
	if err != nil {
		baseVM.FormError = "Could not build " + cfg.ReportTitle + "."
		return pages.CounterpartySummary(baseVM).Render(c.Context(), c)
	}
	baseVM.Report = report
	return pages.CounterpartySummary(baseVM).Render(c.Context(), c)
}

// handleCashFlow renders the Cash Flow Summary report — actual cash
// account movements grouped by source. Not a GAAP indirect-method
// statement of cash flows; the operator-friendly "where did my cash
// move" view that's more useful for small business decisions.
func (s *Server) handleCashFlow(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, From: fromStr, To: toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "Cash Flow Summary",
		FormAction:    "/reports/cash-flow",
		Mode:          "period",
	}

	if errMsg != "" {
		return pages.CashFlow(pages.CashFlowVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			FormError:  errMsg,
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	report, err := services.BuildCashFlowReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return pages.CashFlow(pages.CashFlowVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			FormError:  "Could not build Cash Flow Summary.",
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	return pages.CashFlow(pages.CashFlowVM{
		HasCompany: true,
		From:       fromStr,
		To:         toStr,
		Report:     report,
		Toolbar:    toolbar,
	}).Render(c.Context(), c)
}

// handleGeneralLedger renders the General Ledger report — every
// account's posting trail in one continuous document. Reuses the
// Account Transactions row shape so the drill-through (Type / # link
// / Name) behaves identically between the two surfaces.
func (s *Server) handleGeneralLedger(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, From: fromStr, To: toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "General Ledger",
		FormAction:    "/reports/general-ledger",
		Mode:          "period",
	}

	if errMsg != "" {
		return pages.GeneralLedger(pages.GeneralLedgerVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			FormError:  errMsg,
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	report, err := services.BuildGeneralLedgerReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return pages.GeneralLedger(pages.GeneralLedgerVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			FormError:  "Could not build General Ledger.",
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	return pages.GeneralLedger(pages.GeneralLedgerVM{
		HasCompany: true,
		From:       fromStr,
		To:         toStr,
		Report:     report,
		Toolbar:    toolbar,
	}).Render(c.Context(), c)
}

// handleReportFavouriteToggle handles the star/unstar POST from the
// Reports hub. Idempotent — clicking on an already-starred report
// removes the favourite, clicking on a non-starred report adds it.
//
// Always redirects back to /reports so the operator sees the updated
// star state without manual navigation.
func (s *Server) handleReportFavouriteToggle(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	reportKey := strings.TrimSpace(c.FormValue("report_key"))
	if _, err := services.ToggleReportFavourite(s.DB, user.ID, companyID, reportKey); err != nil {
		// ErrUnknownReportKey or DB failure — log and bounce back to the
		// hub. The hub still renders correctly without the toggle
		// applying; better to show the page than 500 the user.
		slog.Warn("report favourite toggle failed", "user_id", user.ID, "company_id", companyID, "report_key", reportKey, "err", err)
	}
	return c.Redirect("/reports", fiber.StatusSeeOther)
}

// reportCompanyInfo holds the company fields needed by report handlers.
type reportCompanyInfo struct {
	Name          string
	FiscalYearEnd string
}

// loadReportCompanyInfo loads the company name and fiscal year end for report use.
// Returns safe defaults on DB error.
func (s *Server) loadReportCompanyInfo(companyID uint) reportCompanyInfo {
	var co models.Company
	if err := s.DB.Select("id, name, fiscal_year_end").First(&co, companyID).Error; err != nil {
		return reportCompanyInfo{FiscalYearEnd: "12-31"}
	}
	return reportCompanyInfo{Name: co.Name, FiscalYearEnd: co.FiscalYearEnd}
}

// resolvePeriodDates resolves the effective from/to date strings from a period
// preset plus any explicitly supplied from/to values.
//
//   - If from/to are both present, they are used as-is (preset is just echoed).
//   - If preset is non-custom and dates are missing, they are computed server-side.
//   - If nothing is supplied (first page load), PresetLastMonth is the default.
func resolvePeriodDates(presetRaw, fromRaw, toRaw, fyEnd string) (preset, from, to string) {
	fromRaw = strings.TrimSpace(fromRaw)
	toRaw = strings.TrimSpace(toRaw)
	presetRaw = strings.TrimSpace(presetRaw)

	// If explicit from+to supplied, use them; preset is carried through for display.
	if fromRaw != "" && toRaw != "" {
		if presetRaw == "" {
			presetRaw = string(services.PresetCustom)
		}
		return presetRaw, fromRaw, toRaw
	}

	// Determine the preset to compute.
	p := services.ReportPreset(presetRaw)
	if p == "" || p == services.PresetCustom {
		p = services.PresetLastMonth // first-load default
	}
	result := services.ComputeReportPeriod(p, fyEnd, time.Now())
	if result.From.IsZero() {
		return string(services.PresetCustom), fromRaw, toRaw
	}
	return string(p), result.From.Format("2006-01-02"), result.To.Format("2006-01-02")
}

// resolveAsOfDate resolves the effective as_of date from a period preset or
// explicit value. For Balance Sheet, the preset's To date serves as the single
// point-in-time "as of" date rather than a range boundary.
func resolveAsOfDate(presetRaw, asOfRaw, fyEnd string) (preset, asOf string) {
	return resolveAsOfDateAt(presetRaw, asOfRaw, fyEnd, time.Now())
}

func resolveAsOfDateAt(presetRaw, asOfRaw, fyEnd string, now time.Time) (preset, asOf string) {
	asOfRaw = strings.TrimSpace(asOfRaw)
	presetRaw = strings.TrimSpace(presetRaw)

	if asOfRaw != "" {
		if presetRaw == "" {
			presetRaw = string(services.PresetCustom)
		}
		return presetRaw, asOfRaw
	}

	p := services.ReportPreset(presetRaw)
	if p == "" || p == services.PresetCustom {
		p = services.PresetLastMonth
	}
	result := services.ComputeReportPeriod(p, fyEnd, now)
	if result.To.IsZero() {
		return string(services.PresetCustom), now.Format("2006-01-02")
	}
	return string(p), result.To.Format("2006-01-02")
}

func (s *Server) handleTrialBalance(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, From: fromStr, To: toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "Trial Balance",
		FormAction:    "/reports/trial-balance",
		CSVExportURL:  "/reports/trial-balance/export.csv",
		Mode:          "period",
	}

	if errMsg != "" {
		return pages.TrialBalance(pages.TrialBalanceVM{
			HasCompany:   true,
			From:         fromStr,
			To:           toStr,
			Rows:         []services.TrialBalanceRow{},
			TotalDebits:  "0.00",
			TotalCredits: "0.00",
			FormError:    errMsg,
			Toolbar:      toolbar,
		}).Render(c.Context(), c)
	}

	rows, totalDebits, totalCredits, err := services.TrialBalance(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return pages.TrialBalance(pages.TrialBalanceVM{
			HasCompany:   true,
			From:         fromStr,
			To:           toStr,
			Rows:         []services.TrialBalanceRow{},
			TotalDebits:  "0.00",
			TotalCredits: "0.00",
			FormError:    "Could not run report.",
			Toolbar:      toolbar,
		}).Render(c.Context(), c)
	}

	return pages.TrialBalance(pages.TrialBalanceVM{
		HasCompany:   true,
		From:         fromStr,
		To:           toStr,
		FromTime:     fromDate,
		ToTime:       toDate,
		Rows:         rows,
		TotalDebits:  pages.Money(totalDebits),
		TotalCredits: pages.Money(totalCredits),
		Toolbar:      toolbar,
	}).Render(c.Context(), c)
}

func (s *Server) handleIncomeStatement(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, From: fromStr, To: toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "Income Statement",
		FormAction:    "/reports/income-statement",
		CSVExportURL:  "/reports/income-statement/export.csv",
		Mode:          "period",
	}

	if errMsg != "" {
		return pages.IncomeStatement(pages.IncomeStatementVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			Report:     services.IncomeStatement{FromDate: fromDate, ToDate: toDate},
			FormError:  errMsg,
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	report, source, err := s.ReportCache.GetIncomeStatement(companyID, fromDate, toDate, func() (services.IncomeStatement, error) {
		return services.IncomeStatementReport(s.DB, companyID, fromDate, toDate)
	})
	if err != nil {
		return pages.IncomeStatement(pages.IncomeStatementVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			Report:     services.IncomeStatement{FromDate: fromDate, ToDate: toDate},
			FormError:  "Could not run report.",
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}
	toolbar.Source = source
	toolbar.FreshnessLabel = reportFreshnessLabel(source)
	slog.Info("report.render",
		"type", "income_statement",
		"company_id", companyID,
		"from", fromStr,
		"to", toStr,
		"source", source,
	)

	return pages.IncomeStatement(pages.IncomeStatementVM{
		HasCompany: true,
		From:       fromStr,
		To:         toStr,
		Report:     report,
		Toolbar:    toolbar,
	}).Render(c.Context(), c)
}

func (s *Server) handleBalanceSheet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, asOfStr := resolveAsOfDate(
		c.Query("period"), c.Query("as_of"), co.FiscalYearEnd)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, AsOf: asOfStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "Balance Sheet",
		FormAction:    "/reports/balance-sheet",
		CSVExportURL:  "/reports/balance-sheet/export.csv",
		Mode:          "asof",
	}

	asOf, err := time.Parse("2006-01-02", asOfStr)
	if err != nil {
		return pages.BalanceSheet(pages.BalanceSheetVM{
			HasCompany: true,
			AsOf:       asOfStr,
			Report:     services.BalanceSheet{AsOf: time.Now()},
			FormError:  "As of date must be a valid date.",
			AsOfTime:   time.Now(),
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	report, err := services.BalanceSheetReport(s.DB, companyID, asOf)
	if err != nil {
		return pages.BalanceSheet(pages.BalanceSheetVM{
			HasCompany: true,
			AsOf:       asOfStr,
			Report:     services.BalanceSheet{AsOf: asOf},
			FormError:  "Could not run report.",
			AsOfTime:   asOf,
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	return pages.BalanceSheet(pages.BalanceSheetVM{
		HasCompany: true,
		AsOf:       asOfStr,
		Report:     report,
		AsOfTime:   asOf,
		Toolbar:    toolbar,
	}).Render(c.Context(), c)
}

func (s *Server) handleARAgingReport(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, asOfStr := resolveAsOfDate(
		c.Query("period"), c.Query("as_of"), co.FiscalYearEnd)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, AsOf: asOfStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "A/R Aging",
		FormAction:    "/reports/ar-aging",
		CSVExportURL:  "/reports/ar-aging/export.csv",
		Mode:          "asof",
	}

	asOf, err := time.Parse("2006-01-02", asOfStr)
	if err != nil {
		return pages.ARAging(pages.ARAgingVM{
			HasCompany: true,
			AsOf:       asOfStr,
			Report:     services.ARAgingReport{AsOf: time.Now()},
			FormError:  "As of date must be a valid date.",
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	report, source, err := s.ReportCache.GetARAgingReport(companyID, asOf, func() (services.ARAgingReport, error) {
		return services.BuildARAgingReport(s.DB, companyID, asOf)
	})
	if err != nil {
		return pages.ARAging(pages.ARAgingVM{
			HasCompany: true,
			AsOf:       asOfStr,
			Report:     services.ARAgingReport{AsOf: asOf},
			FormError:  "Could not run report.",
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}
	toolbar.Source = source
	toolbar.FreshnessLabel = reportFreshnessLabel(source)
	slog.Info("report.render",
		"type", "ar_aging",
		"company_id", companyID,
		"as_of", asOfStr,
		"source", source,
	)

	return pages.ARAging(pages.ARAgingVM{
		HasCompany: true,
		AsOf:       asOfStr,
		Report:     report,
		Toolbar:    toolbar,
	}).Render(c.Context(), c)
}

func reportFreshnessLabel(source string) string {
	switch source {
	case "cache":
		return "Freshness: cached for up to 5 minutes"
	case "recomputed":
		return "Freshness: recomputed just now"
	case "mixed":
		return "Freshness: mixed result"
	default:
		return ""
	}
}

func (s *Server) handleJournalEntryReport(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset: preset, From: fromStr, To: toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "Journal Entries",
		FormAction:    "/reports/journal-entries",
		Mode:          "period",
	}
	if errMsg != "" {
		return pages.JournalEntryReport(pages.JournalEntryReportVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			Entries:    nil,
			FormError:  errMsg,
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	entries, err := services.JournalEntryReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return pages.JournalEntryReport(pages.JournalEntryReportVM{
			HasCompany: true,
			From:       fromStr,
			To:         toStr,
			Entries:    nil,
			FormError:  "Could not run report.",
			Toolbar:    toolbar,
		}).Render(c.Context(), c)
	}

	return pages.JournalEntryReport(pages.JournalEntryReportVM{
		HasCompany: true,
		From:       fromStr,
		To:         toStr,
		Entries:    entries,
		Toolbar:    toolbar,
	}).Render(c.Context(), c)
}

func parseReportRange(fromRaw, toRaw string) (time.Time, time.Time, string, string, string) {
	// Defaults: last 30 days.
	now := time.Now()
	toStr := strings.TrimSpace(toRaw)
	if toStr == "" {
		toStr = now.Format("2006-01-02")
	}
	toDate, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return time.Time{}, time.Time{}, strings.TrimSpace(fromRaw), toStr, "To date must be a valid date."
	}

	fromStr := strings.TrimSpace(fromRaw)
	if fromStr == "" {
		fromStr = toDate.AddDate(0, 0, -30).Format("2006-01-02")
	}
	fromDate, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, fromStr, toStr, "From date must be a valid date."
	}

	if fromDate.After(toDate) {
		return time.Time{}, time.Time{}, fromStr, toStr, "From date must be before To date."
	}

	return fromDate, toDate, fromStr, toStr, ""
}

func defaultBusinessTypeForEntity(entity models.EntityType) models.BusinessType {
	switch entity {
	case models.EntityTypeLLP:
		return models.BusinessTypeProfessionalCorp
	default:
		// Keep setup simple: default to Retail for Personal/Incorporated.
		return models.BusinessTypeRetail
	}
}

func within53Weeks(a, b time.Time) bool {
	days := int(a.Sub(b).Hours() / 24)
	if days < 0 {
		days = -days
	}
	return days <= 371
}

func (s *Server) handleSetupSubmit(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	sess := SessionFromCtx(c)
	if user == nil || sess == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	// Read form fields.
	name := strings.TrimSpace(c.FormValue("company_name"))
	entityTypeRaw := strings.TrimSpace(c.FormValue("entity_type"))
	addressLine := strings.TrimSpace(c.FormValue("address_line"))
	city := strings.TrimSpace(c.FormValue("city"))
	province := strings.TrimSpace(c.FormValue("province"))
	postalCode := NormalizePostalCode(c.FormValue("postal_code"))
	country := strings.TrimSpace(c.FormValue("country"))
	businessNumber := strings.TrimSpace(c.FormValue("business_number"))
	industry := strings.TrimSpace(c.FormValue("industry"))
	incorporatedDateRaw := strings.TrimSpace(c.FormValue("incorporated_date"))
	incorporatedDate := incorporatedDateRaw
	if norm := NormalizeIncorporatedDate(incorporatedDateRaw); norm != "" {
		incorporatedDate = norm
	}
	fiscalYearEndRaw := strings.TrimSpace(c.FormValue("fiscal_year_end"))
	fiscalYearEnd := fiscalYearEndRaw
	if norm := NormalizeFiscalYearEnd(fiscalYearEndRaw); norm != "" {
		fiscalYearEnd = norm
	}
	accountCodeLengthRaw := strings.TrimSpace(c.FormValue("account_code_length"))

	values := pages.SetupFormValues{
		CompanyName:       name,
		EntityType:        entityTypeRaw,
		AddressLine:       addressLine,
		City:              city,
		Province:          province,
		PostalCode:        postalCode,
		Country:           country,
		BusinessNumber:    businessNumber,
		Industry:          industry,
		IncorporatedDate:  incorporatedDate,
		FiscalYearEnd:     fiscalYearEnd,
		AccountCodeLength: accountCodeLengthRaw,
	}

	errs := validateSetupCompanyForm(values)
	if errs.HasAny() {
		return pages.Setup(pages.SetupViewModel{
			Active: "Setup",
			Values: values,
			Errors: errs,
		}).Render(c.Context(), c)
	}

	// ── Plan quota check: max owned companies ──────────────────────────────
	// Load the user's plan and count how many companies they already own.
	var fullUser models.User
	if err := s.DB.Preload("Plan").First(&fullUser, "id = ?", user.ID).Error; err != nil {
		return pages.Setup(pages.SetupViewModel{
			Active: "Setup",
			Values: values,
			Errors: pages.SetupFormErrors{Form: "Could not verify your account. Please try again."},
		}).Render(c.Context(), c)
	}
	// Plan.ID == 0 means the plan system is not yet set up (e.g. fresh migration in
	// progress). In that case we skip the quota check to avoid blocking company creation.
	if fullUser.Plan.ID != 0 && fullUser.Plan.MaxOwnedCompanies != -1 {
		var ownedCount int64
		s.DB.Model(&models.CompanyMembership{}).
			Where("user_id = ? AND role = ? AND is_active = true", user.ID, models.CompanyRoleOwner).
			Count(&ownedCount)
		if int(ownedCount) >= fullUser.Plan.MaxOwnedCompanies {
			return pages.Setup(pages.SetupViewModel{
				Active: "Setup",
				Values: values,
				Errors: pages.SetupFormErrors{
					Form: "You have reached the maximum number of companies for your plan (" +
						strconv.Itoa(fullUser.Plan.MaxOwnedCompanies) + "). " +
						"Contact support to upgrade.",
				},
			}).Render(c.Context(), c)
		}
	}
	// ── end quota check ────────────────────────────────────────────────────

	codeLen, _ := ParseAccountCodeLengthChoice(values.AccountCodeLength)

	entityType, businessType, industryValue, err := parseSetupCompanyForm(values)
	if err != nil {
		return pages.Setup(pages.SetupViewModel{
			Active: "Setup",
			Values: values,
			Errors: pages.SetupFormErrors{Form: "Could not read company details. Please try again."},
		}).Render(c.Context(), c)
	}

	// Save company + import default COA in one transaction.
	var setupCompanyID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		company := models.Company{
			Name:                    name,
			EntityType:              entityType,
			BusinessType:            businessType,
			AddressLine:             addressLine,
			City:                    city,
			Province:                province,
			PostalCode:              postalCode,
			Country:                 country,
			BusinessNumber:          businessNumber,
			Industry:                industryValue,
			IncorporatedDate:        incorporatedDate,
			FiscalYearEnd:           fiscalYearEnd,
			AccountCodeLength:       codeLen,
			AccountCodeLengthLocked: false,
		}

		if err := tx.Create(&company).Error; err != nil {
			return err
		}
		setupCompanyID = company.ID

		membership := models.CompanyMembership{
			ID:        uuid.New(),
			UserID:    user.ID,
			CompanyID: company.ID,
			Role:      models.CompanyRoleOwner,
			IsActive:  true,
		}
		if err := tx.Create(&membership).Error; err != nil {
			return err
		}

		if err := services.CreateDefaultAccountsForCompany(tx, company.ID, codeLen); err != nil {
			return err
		}
		if err := tx.Model(&models.Company{}).Where("id = ?", company.ID).Update("account_code_length_locked", true).Error; err != nil {
			return err
		}
		// Batch 1 – Task module: seed TASK_LABOR and TASK_REIM system items.
		if err := services.EnsureSystemTaskItems(tx, company.ID); err != nil {
			return err
		}
		if err := tx.Model(&models.Session{}).Where("id = ?", sess.ID).Update("active_company_id", company.ID).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		// Safe error shown to user; details can be logged later.
		return pages.Setup(pages.SetupViewModel{
			Active: "Setup",
			Values: values,
			Errors: pages.SetupFormErrors{
				Form: "Could not save setup. Please try again.",
			},
		}).Render(c.Context(), c)
	}
	sess.ActiveCompanyID = &setupCompanyID
	details := map[string]any{
		"company_name":  name,
		"entity_type":   entityTypeRaw,
		"business_type": string(businessType),
		"company_id":    setupCompanyID,
	}
	cid := setupCompanyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "setup.completed", "company", setupCompanyID, actor, details, &cid, &uid)

	// Setup done. Redirect to dashboard (guard middleware will allow now).
	// Support both normal form submit and potential HTMX submit.
	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/", fiber.StatusSeeOther)
}

func (s *Server) handleAuditLog(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filterQ := strings.TrimSpace(c.Query("q"))
	filterAction := strings.TrimSpace(c.Query("action"))
	filterEntity := strings.TrimSpace(c.Query("entity"))
	filterFrom := strings.TrimSpace(c.Query("from"))
	filterTo := strings.TrimSpace(c.Query("to"))

	page := 1
	if pageRaw := strings.TrimSpace(c.Query("page")); pageRaw != "" {
		if p, err := services.ParseUint(pageRaw); err == nil && p > 0 {
			page = int(p)
		}
	}

	const pageSize = 50
	offset := (page - 1) * pageSize

	base := s.DB.Model(&models.AuditLog{}).Where("company_id = ?", companyID)
	if filterQ != "" {
		like := "%" + filterQ + "%"
		base = base.Where(
			"LOWER(action) LIKE LOWER(?) OR LOWER(entity_type) LIKE LOWER(?) OR LOWER(details_json) LIKE LOWER(?)",
			like, like, like,
		)
	}
	if filterAction != "" {
		base = base.Where("action = ?", filterAction)
	}
	if filterEntity != "" {
		base = base.Where("entity_type = ?", filterEntity)
	}
	if filterFrom != "" {
		if d, err := time.Parse("2006-01-02", filterFrom); err == nil {
			base = base.Where("created_at >= ?", d)
		}
	}
	if filterTo != "" {
		if d, err := time.Parse("2006-01-02", filterTo); err == nil {
			base = base.Where("created_at < ?", d.AddDate(0, 0, 1))
		}
	}

	var total int64
	_ = base.Count(&total).Error

	var rows []models.AuditLog
	_ = base.Order("created_at desc, id desc").Offset(offset).Limit(pageSize).Find(&rows).Error

	var actions []string
	_ = s.DB.Model(&models.AuditLog{}).Where("company_id = ?", companyID).Distinct().Order("action asc").Pluck("action", &actions).Error
	var entities []string
	_ = s.DB.Model(&models.AuditLog{}).Where("company_id = ?", companyID).Distinct().Order("entity_type asc").Pluck("entity_type", &entities).Error

	vm := pages.AuditLogVM{
		HasCompany: true,
		Items:      rows,

		FilterQ:      filterQ,
		FilterAction: filterAction,
		FilterEntity: filterEntity,
		FilterFrom:   filterFrom,
		FilterTo:     filterTo,
		Actions:      actions,
		Entities:     entities,

		Page:       page,
		PrevPage:   page - 1,
		NextPage:   page + 1,
		HasPrev:    page > 1,
		HasNext:    int64(offset+pageSize) < total,
		TotalCount: total,
	}

	return pages.AuditLog(vm).Render(c.Context(), c)
}

func (s *Server) handleAIConnectGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var company models.Company
	if err := s.DB.Where("id = ?", companyID).First(&company).Error; err != nil {
		return pages.AIConnect(pages.AIConnectVM{
			HasCompany: false,
			FormError:  "Company not found. Please run setup first.",
			Breadcrumb: breadcrumbSettingsAIConnect(),
		}).Render(c.Context(), c)
	}

	row, err := services.LoadAIConnectionSettings(s.DB, company.ID)
	if err != nil {
		return pages.AIConnect(pages.AIConnectVM{
			HasCompany: true,
			FormError:  "Could not load AI connection settings.",
			Breadcrumb: breadcrumbSettingsAIConnect(),
		}).Render(c.Context(), c)
	}

	vm := aiConnectVMFromRow(row, !AIConnectEditableFromCtx(c))
	vm.HasCompany = true
	vm.Saved = c.Query("saved") == "1"
	vm.Tested = c.Query("tested") == "1"
	return pages.AIConnect(vm).Render(c.Context(), c)
}

func (s *Server) handleAIConnectPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	if !AIConnectEditableFromCtx(c) {
		return fiber.NewError(fiber.StatusForbidden, "Forbidden")
	}

	var company models.Company
	if err := s.DB.Where("id = ?", companyID).First(&company).Error; err != nil {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	provider, err := services.ParseAIProvider(c.FormValue("provider"))
	if err != nil {
		row, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
		vm := aiConnectVMFromRow(row, false)
		vm.HasCompany = true
		vm.FormError = "Invalid provider."
		return pages.AIConnect(vm).Render(c.Context(), c)
	}

	enabled := c.FormValue("enabled") == "true"
	vision := c.FormValue("vision_enabled") == "true"
	apiKey := strings.TrimSpace(c.FormValue("api_key"))
	baseURL := strings.TrimSpace(c.FormValue("api_base_url"))
	model := strings.TrimSpace(c.FormValue("model_name"))

	rowBefore, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
	beforeSnap := services.AIConnectionAuditSnapshot(rowBefore)

	if err := services.UpsertAIConnectionSettings(s.DB, company.ID, provider, baseURL, apiKey, model, enabled, vision); err != nil {
		logging.L().Warn("ai_connect save failed", "err", err.Error(), "company_id", company.ID)
		row, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
		vm := aiConnectVMFromRow(row, false)
		vm.HasCompany = true
		vm.FormError = aiConnectSaveErrorMessage(err)
		vm.Provider = provider
		vm.APIBaseURL = baseURL
		vm.ModelName = model
		vm.Enabled = enabled
		vm.VisionEnabled = vision
		vm.HasAPIKey = row.APIKey != "" || apiKey != ""
		vm.APIKeyHint = services.MaskAPIKey(row.APIKey)
		return pages.AIConnect(vm).Render(c.Context(), c)
	}

	rowAfter, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
	afterSnap := services.AIConnectionAuditSnapshot(rowAfter)
	cid := company.ID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContextDetails(s.DB, "settings.ai_connect.saved", "settings", company.ID, actor, map[string]any{
		"company_id": company.ID,
	}, &cid, &uid, beforeSnap, afterSnap)

	return c.Redirect("/settings/ai-connect?saved=1", fiber.StatusSeeOther)
}

func (s *Server) handleAIConnectTestPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	if !AIConnectEditableFromCtx(c) {
		return fiber.NewError(fiber.StatusForbidden, "Forbidden")
	}

	var company models.Company
	if err := s.DB.Where("id = ?", companyID).First(&company).Error; err != nil {
		return c.Redirect("/setup", fiber.StatusSeeOther)
	}

	rowBefore, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
	beforeSnap := services.AIConnectionAuditSnapshot(rowBefore)

	ok, msg, skipped, err := services.RunAIConnectionTest(s.DB, company.ID)
	if err != nil {
		row, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
		vm := aiConnectVMFromRow(row, false)
		vm.HasCompany = true
		vm.FormError = "Could not run connection test."
		return pages.AIConnect(vm).Render(c.Context(), c)
	}
	if skipped {
		row, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
		vm := aiConnectVMFromRow(row, false)
		vm.HasCompany = true
		vm.FormError = msg
		return pages.AIConnect(vm).Render(c.Context(), c)
	}

	rowAfter, _ := services.LoadAIConnectionSettings(s.DB, company.ID)
	afterSnap := services.AIConnectionAuditSnapshot(rowAfter)
	cid := company.ID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContextDetails(s.DB, "settings.ai_connect.tested", "settings", company.ID, actor, map[string]any{
		"ok":         ok,
		"message":    msg,
		"company_id": company.ID,
	}, &cid, &uid, beforeSnap, afterSnap)

	return c.Redirect("/settings/ai-connect?tested=1", fiber.StatusSeeOther)
}

func aiConnectVMFromRow(row models.AIConnectionSettings, readOnly bool) pages.AIConnectVM {
	vm := pages.AIConnectVM{
		Breadcrumb:    breadcrumbSettingsAIConnect(),
		ReadOnly:      readOnly,
		Provider:      row.Provider,
		APIBaseURL:    row.APIBaseURL,
		ModelName:     row.ModelName,
		Enabled:       row.Enabled,
		VisionEnabled: row.VisionEnabled,
		HasAPIKey:     row.APIKey != "",
		APIKeyHint:    services.MaskAPIKey(row.APIKey),
	}
	if vm.Provider == "" {
		vm.Provider = models.AIProviderOpenAICompatible
	}
	if row.LastTestAt != nil {
		vm.HasLastTest = true
		vm.LastTestAtFormatted = row.LastTestAt.Format(time.RFC3339)
		vm.LastTestOK = row.LastTestOK
		vm.LastTestMessage = row.LastTestMessage
	}
	return vm
}
