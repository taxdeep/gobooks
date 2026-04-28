// 遵循project_guide.md
package web

import (
	"log/slog"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// handleSalesTaxReport renders the Sales Tax Report summary page.
// Route: GET /reports/sales-tax
func (s *Server) handleSalesTaxReport(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset:         preset,
		From:           fromStr,
		To:             toStr,
		FiscalYearEnd:  co.FiscalYearEnd,
		CompanyName:    co.Name,
		ReportTitle:    "Sales Tax Report",
		FormAction:     "/reports/sales-tax",
		Mode:           "period",
		CSVExportURL:   "/reports/account-transactions/export.csv",
		ExcelExportURL: "/reports/account-transactions/export.xlsx",
		PDFExportURL:   "/reports/account-transactions/export.pdf",
	}

	vm := pages.SalesTaxReportVM{
		HasCompany: true,
		Preset:     preset,
		DateFrom:   fromStr,
		DateTo:     toStr,
		DateLabel:  formatDateRangeLabel(fromStr, toStr),
		Toolbar:    toolbar,
	}

	if errMsg != "" {
		vm.FormError = errMsg
		return pages.SalesTaxReport(vm).Render(c.Context(), c)
	}

	summaryRows, balanceRows, summaryTotals, balanceTotals, err :=
		services.BuildSalesTaxReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		slog.Error("sales_tax_report.build", "company_id", companyID, "error", err)
		vm.FormError = "Could not run report."
		return pages.SalesTaxReport(vm).Render(c.Context(), c)
	}

	vm.SummaryRows = summaryRows
	vm.BalanceRows = balanceRows
	vm.SummaryTotals = summaryTotals
	vm.BalanceTotals = balanceTotals

	slog.Info("report.render",
		"type", "sales_tax",
		"company_id", companyID,
		"from", fromStr,
		"to", toStr,
	)
	return pages.SalesTaxReport(vm).Render(c.Context(), c)
}

// handleAccountTransactions renders the per-account transaction ledger.
// Route: GET /reports/account-transactions
func (s *Server) handleAccountTransactions(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accountIDRaw := c.Query("account_id")
	accountID64, err := strconv.ParseUint(accountIDRaw, 10, 64)
	if err != nil || accountID64 == 0 {
		return c.Redirect("/reports/sales-tax", fiber.StatusSeeOther)
	}
	accountID := uint(accountID64)

	co := s.loadReportCompanyInfo(companyID)
	preset, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)

	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)

	toolbar := pages.ReportToolbarVM{
		Preset:        preset,
		From:          fromStr,
		To:            toStr,
		FiscalYearEnd: co.FiscalYearEnd,
		CompanyName:   co.Name,
		ReportTitle:   "Account Transactions",
		FormAction:    "/reports/account-transactions",
		Mode:          "period",
		// account_id MUST go through HiddenInputs — appending it to the
		// action's query string doesn't survive form submit (browsers
		// replace the action's query with form inputs on GET submit).
		HiddenInputs: []pages.ReportToolbarHiddenInput{
			{Name: "account_id", Value: accountIDRaw},
		},
	}

	vm := pages.AccountTransactionsVM{
		HasCompany: true,
		Preset:     preset,
		DateFrom:   fromStr,
		DateTo:     toStr,
		AccountID:  accountID,
		Toolbar:    toolbar,
	}

	if errMsg != "" {
		vm.FormError = errMsg
		return pages.AccountTransactions(vm).Render(c.Context(), c)
	}

	report, err := services.BuildAccountTransactionsReport(s.DB, companyID, accountID, fromDate, toDate)
	if err != nil {
		slog.Error("account_transactions.build", "company_id", companyID, "account_id", accountID, "error", err)
		vm.FormError = "Account not found or could not load transactions."
		return pages.AccountTransactions(vm).Render(c.Context(), c)
	}

	vm.Report = report
	slog.Info("report.render",
		"type", "account_transactions",
		"company_id", companyID,
		"account_id", accountID,
		"from", fromStr,
		"to", toStr,
	)
	return pages.AccountTransactions(vm).Render(c.Context(), c)
}

// formatDateRangeLabel converts "2026-01-01" + "2026-04-13" → "Jan 01, 2026 to Apr 13, 2026".
func formatDateRangeLabel(from, to string) string {
	f := formatMonthDay(from)
	t := formatMonthDay(to)
	if f == "" || t == "" {
		return from + " to " + to
	}
	return f + " to " + t
}

func formatMonthDay(s string) string {
	// s is "YYYY-MM-DD"
	if len(s) != 10 {
		return s
	}
	months := [...]string{
		"", "Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
	}
	m, err1 := strconv.Atoi(s[5:7])
	d, err2 := strconv.Atoi(s[8:10])
	if err1 != nil || err2 != nil || m < 1 || m > 12 {
		return s
	}
	day := strconv.Itoa(d)
	if len(day) == 1 {
		day = "0" + day
	}
	return months[m] + " " + day + ", " + s[:4]
}
