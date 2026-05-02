package web

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

type trialBalanceAPIResponse struct {
	From     string                   `json:"from"`
	To       string                   `json:"to"`
	RowCount int                      `json:"row_count"`
	Totals   trialBalanceAPITotals    `json:"totals"`
	Sections []trialBalanceAPISection `json:"sections"`
}

type trialBalanceAPITotals struct {
	Debits     string `json:"debits"`
	Credits    string `json:"credits"`
	Difference string `json:"difference"`
	Balanced   bool   `json:"balanced"`
}

type trialBalanceAPISection struct {
	Title   string               `json:"title"`
	Root    string               `json:"root"`
	Debits  string               `json:"debits"`
	Credits string               `json:"credits"`
	Rows    []trialBalanceAPIRow `json:"rows"`
}

type trialBalanceAPIRow struct {
	AccountID      uint   `json:"account_id"`
	AccountCode    string `json:"account_code"`
	AccountName    string `json:"account_name"`
	Classification string `json:"classification"`
	Root           string `json:"root"`
	Detail         string `json:"detail"`
	Debit          string `json:"debit"`
	Credit         string `json:"credit"`
	DrillURL       string `json:"drill_url"`
}

func (s *Server) handleTrialBalanceAPI(c *fiber.Ctx) error {
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

	rows, totalDebits, totalCredits, err := services.TrialBalance(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not run trial balance"})
	}

	return c.JSON(trialBalanceAPIFromRows(rows, totalDebits, totalCredits, fromStr, toStr, fromDate, toDate))
}

func trialBalanceAPIFromRows(rows []services.TrialBalanceRow, totalDebits, totalCredits decimal.Decimal, from, to string, fromDate, toDate time.Time) trialBalanceAPIResponse {
	resp := trialBalanceAPIResponse{
		From:     from,
		To:       to,
		RowCount: len(rows),
		Sections: make([]trialBalanceAPISection, 0, 6),
	}
	roots := []struct {
		title string
		root  models.RootAccountType
	}{
		{"Assets", models.RootAsset},
		{"Liabilities", models.RootLiability},
		{"Equity", models.RootEquity},
		{"Revenue", models.RootRevenue},
		{"Cost of Sales", models.RootCostOfSales},
		{"Expenses", models.RootExpense},
	}
	for _, root := range roots {
		section := trialBalanceAPISection{
			Title: root.title,
			Root:  string(root.root),
			Rows:  make([]trialBalanceAPIRow, 0),
		}
		sectionDebits := decimal.Zero
		sectionCredits := decimal.Zero
		for _, row := range rows {
			if row.Root != string(root.root) {
				continue
			}
			sectionDebits = sectionDebits.Add(row.Debit)
			sectionCredits = sectionCredits.Add(row.Credit)
			section.Rows = append(section.Rows, trialBalanceAPIRow{
				AccountID:      row.AccountID,
				AccountCode:    row.Code,
				AccountName:    row.Name,
				Classification: row.Classification,
				Root:           row.Root,
				Detail:         row.Detail,
				Debit:          reportDecimalString(row.Debit),
				Credit:         reportDecimalString(row.Credit),
				DrillURL:       services.AccountDrillURL(row.AccountID, fromDate, toDate),
			})
		}
		section.Debits = reportDecimalString(sectionDebits)
		section.Credits = reportDecimalString(sectionCredits)
		resp.Sections = append(resp.Sections, section)
	}
	difference := totalDebits.Sub(totalCredits).Abs()
	resp.Totals = trialBalanceAPITotals{
		Debits:     reportDecimalString(totalDebits),
		Credits:    reportDecimalString(totalCredits),
		Difference: reportDecimalString(difference),
		Balanced:   difference.LessThan(decimal.RequireFromString("0.005")),
	}
	return resp
}
