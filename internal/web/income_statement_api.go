package web

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

type incomeStatementAPIResponse struct {
	From     string                      `json:"from"`
	To       string                      `json:"to"`
	Source   string                      `json:"source"`
	Totals   incomeStatementAPITotals    `json:"totals"`
	Sections []incomeStatementAPISection `json:"sections"`
}

type incomeStatementAPITotals struct {
	Revenue     string `json:"revenue"`
	CostOfSales string `json:"cost_of_sales"`
	GrossProfit string `json:"gross_profit"`
	Expenses    string `json:"expenses"`
	NetIncome   string `json:"net_income"`
}

type incomeStatementAPISection struct {
	Title string                   `json:"title"`
	Root  string                   `json:"root"`
	Total string                   `json:"total"`
	Rows  []incomeStatementAPILine `json:"rows"`
}

type incomeStatementAPILine struct {
	AccountID   uint   `json:"account_id"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	Detail      string `json:"detail"`
	Amount      string `json:"amount"`
	DrillURL    string `json:"drill_url"`
}

func (s *Server) handleIncomeStatementAPI(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "active company required"})
	}

	co := s.loadReportCompanyInfo(companyID)
	_, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)
	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)
	if errMsg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": errMsg})
	}

	report, source, err := s.incomeStatementReport(companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not run income statement"})
	}
	return c.JSON(incomeStatementAPIFromReport(report, source, fromStr, toStr))
}

func (s *Server) incomeStatementReport(companyID uint, fromDate, toDate time.Time) (services.IncomeStatement, string, error) {
	if s.ReportCache == nil {
		report, err := services.IncomeStatementReport(s.DB, companyID, fromDate, toDate)
		return report, "recomputed", err
	}
	return s.ReportCache.GetIncomeStatement(companyID, fromDate, toDate, func() (services.IncomeStatement, error) {
		return services.IncomeStatementReport(s.DB, companyID, fromDate, toDate)
	})
}

func incomeStatementAPIFromReport(report services.IncomeStatement, source, from, to string) incomeStatementAPIResponse {
	return incomeStatementAPIResponse{
		From:   from,
		To:     to,
		Source: source,
		Totals: incomeStatementAPITotals{
			Revenue:     reportDecimalString(report.TotalRevenue),
			CostOfSales: reportDecimalString(report.TotalCostOfSales),
			GrossProfit: reportDecimalString(report.GrossProfit),
			Expenses:    reportDecimalString(report.TotalExpenses),
			NetIncome:   reportDecimalString(report.NetIncome),
		},
		Sections: []incomeStatementAPISection{
			incomeStatementAPISectionFromLines("Revenue", models.RootRevenue, report.TotalRevenue, report.Revenue, report.FromDate, report.ToDate),
			incomeStatementAPISectionFromLines("Cost of Sales", models.RootCostOfSales, report.TotalCostOfSales, report.CostOfSales, report.FromDate, report.ToDate),
			incomeStatementAPISectionFromLines("Expenses", models.RootExpense, report.TotalExpenses, report.Expenses, report.FromDate, report.ToDate),
		},
	}
}

func incomeStatementAPISectionFromLines(title string, root models.RootAccountType, total decimal.Decimal, lines []services.IncomeStatementLine, fromDate, toDate time.Time) incomeStatementAPISection {
	section := incomeStatementAPISection{
		Title: title,
		Root:  string(root),
		Total: reportDecimalString(total),
		Rows:  make([]incomeStatementAPILine, 0, len(lines)),
	}
	for _, line := range lines {
		section.Rows = append(section.Rows, incomeStatementAPILine{
			AccountID:   line.AccountID,
			AccountCode: line.Code,
			AccountName: line.Name,
			Detail:      line.Detail,
			Amount:      reportDecimalString(line.Amount),
			DrillURL:    services.AccountDrillURL(line.AccountID, fromDate, toDate),
		})
	}
	return section
}
