package web

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

type cashFlowAPIResponse struct {
	From     string                 `json:"from"`
	To       string                 `json:"to"`
	Totals   cashFlowAPITotals      `json:"totals"`
	Accounts []cashFlowAPIAccount   `json:"accounts"`
	Sources  cashFlowAPISourceGroup `json:"sources"`
}

type cashFlowAPITotals struct {
	OpeningCash  string `json:"opening_cash"`
	TotalInflow  string `json:"total_inflow"`
	TotalOutflow string `json:"total_outflow"`
	NetChange    string `json:"net_change"`
	ClosingCash  string `json:"closing_cash"`
}

type cashFlowAPIAccount struct {
	AccountID      uint   `json:"account_id"`
	AccountCode    string `json:"account_code"`
	AccountName    string `json:"account_name"`
	OpeningBalance string `json:"opening_balance"`
	TotalInflow    string `json:"total_inflow"`
	TotalOutflow   string `json:"total_outflow"`
	ClosingBalance string `json:"closing_balance"`
	DrillURL       string `json:"drill_url"`
}

type cashFlowAPISourceGroup struct {
	Inflows  []cashFlowAPISourceRow `json:"inflows"`
	Outflows []cashFlowAPISourceRow `json:"outflows"`
}

type cashFlowAPISourceRow struct {
	SourceType  string `json:"source_type"`
	SourceLabel string `json:"source_label"`
	Inflow      string `json:"inflow"`
	Outflow     string `json:"outflow"`
	Net         string `json:"net"`
}

func (s *Server) handleCashFlowAPI(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "active company required"})
	}

	co := s.loadReportCompanyInfo(companyID)
	_, fromStr, toStr := resolvePeriodDates(c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)
	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)
	if errMsg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": errMsg})
	}

	report, err := services.BuildCashFlowReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not build cash flow summary"})
	}
	return c.JSON(cashFlowAPIFromReport(report, fromStr, toStr))
}

func cashFlowAPIFromReport(report *services.CashFlowReport, from, to string) cashFlowAPIResponse {
	if report == nil {
		return cashFlowAPIResponse{From: from, To: to}
	}
	payload := cashFlowAPIResponse{
		From: from,
		To:   to,
		Totals: cashFlowAPITotals{
			OpeningCash:  reportDecimalString(report.OpeningCash),
			TotalInflow:  reportDecimalString(report.TotalInflow),
			TotalOutflow: reportDecimalString(report.TotalOutflow),
			NetChange:    reportDecimalString(report.NetChange),
			ClosingCash:  reportDecimalString(report.ClosingCash),
		},
		Accounts: make([]cashFlowAPIAccount, 0, len(report.Accounts)),
		Sources: cashFlowAPISourceGroup{
			Inflows:  cashFlowAPISourceRows(report.InflowBySource),
			Outflows: cashFlowAPISourceRows(report.OutflowBySource),
		},
	}
	for _, account := range report.Accounts {
		payload.Accounts = append(payload.Accounts, cashFlowAPIAccount{
			AccountID:      account.AccountID,
			AccountCode:    account.AccountCode,
			AccountName:    account.AccountName,
			OpeningBalance: reportDecimalString(account.OpeningBalance),
			TotalInflow:    reportDecimalString(account.TotalInflow),
			TotalOutflow:   reportDecimalString(account.TotalOutflow),
			ClosingBalance: reportDecimalString(account.ClosingBalance),
			DrillURL:       services.AccountDrillURL(account.AccountID, report.FromDate, report.ToDate),
		})
	}
	return payload
}

func cashFlowAPISourceRows(rows []services.CashFlowSourceRow) []cashFlowAPISourceRow {
	out := make([]cashFlowAPISourceRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, cashFlowAPISourceRow{
			SourceType:  row.SourceType,
			SourceLabel: row.SourceLabel,
			Inflow:      reportDecimalString(row.Inflow),
			Outflow:     reportDecimalString(row.Outflow),
			Net:         reportDecimalString(row.Net),
		})
	}
	return out
}
