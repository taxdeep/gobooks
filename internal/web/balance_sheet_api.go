package web

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

type balanceSheetAPIResponse struct {
	AsOf     string                   `json:"as_of"`
	Totals   balanceSheetAPITotals    `json:"totals"`
	Sections []balanceSheetAPISection `json:"sections"`
}

type balanceSheetAPITotals struct {
	Assets               string `json:"assets"`
	Liabilities          string `json:"liabilities"`
	Equity               string `json:"equity"`
	LiabilitiesAndEquity string `json:"liabilities_and_equity"`
	Difference           string `json:"difference"`
	Balanced             bool   `json:"balanced"`
}

type balanceSheetAPISection struct {
	Title string                `json:"title"`
	Root  string                `json:"root"`
	Total string                `json:"total"`
	Rows  []balanceSheetAPILine `json:"rows"`
}

type balanceSheetAPILine struct {
	AccountID   uint   `json:"account_id"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	Detail      string `json:"detail"`
	Amount      string `json:"amount"`
	DrillURL    string `json:"drill_url"`
}

func (s *Server) handleBalanceSheetAPI(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "active company required"})
	}

	co := s.loadReportCompanyInfo(companyID)
	_, asOfStr := resolveAsOfDate(c.Query("period"), c.Query("as_of"), co.FiscalYearEnd)
	asOf, err := time.Parse("2006-01-02", asOfStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid as-of date"})
	}

	report, err := services.BalanceSheetReport(s.DB, companyID, asOf)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not run balance sheet"})
	}
	return c.JSON(balanceSheetAPIFromReport(report, asOfStr))
}

func balanceSheetAPIFromReport(report services.BalanceSheet, asOf string) balanceSheetAPIResponse {
	liabilitiesAndEquity := report.TotalLiabilities.Add(report.TotalEquity)
	difference := report.TotalAssets.Sub(liabilitiesAndEquity).Abs()
	return balanceSheetAPIResponse{
		AsOf: asOf,
		Totals: balanceSheetAPITotals{
			Assets:               reportDecimalString(report.TotalAssets),
			Liabilities:          reportDecimalString(report.TotalLiabilities),
			Equity:               reportDecimalString(report.TotalEquity),
			LiabilitiesAndEquity: reportDecimalString(liabilitiesAndEquity),
			Difference:           reportDecimalString(difference),
			Balanced:             difference.LessThan(decimal.RequireFromString("0.005")),
		},
		Sections: []balanceSheetAPISection{
			balanceSheetAPISectionFromLines("Assets", models.RootAsset, report.TotalAssets, report.Assets, report.AsOf),
			balanceSheetAPISectionFromLines("Liabilities", models.RootLiability, report.TotalLiabilities, report.Liabilities, report.AsOf),
			balanceSheetAPISectionFromLines("Equity", models.RootEquity, report.TotalEquity, report.Equity, report.AsOf),
		},
	}
}

func balanceSheetAPISectionFromLines(title string, root models.RootAccountType, total decimal.Decimal, lines []services.BalanceSheetLine, asOf time.Time) balanceSheetAPISection {
	section := balanceSheetAPISection{
		Title: title,
		Root:  string(root),
		Total: reportDecimalString(total),
		Rows:  make([]balanceSheetAPILine, 0, len(lines)),
	}
	for _, line := range lines {
		section.Rows = append(section.Rows, balanceSheetAPILine{
			AccountID:   line.AccountID,
			AccountCode: line.Code,
			AccountName: line.Name,
			Detail:      line.Detail,
			Amount:      reportDecimalString(line.Amount),
			DrillURL:    services.AccountDrillURL(line.AccountID, time.Time{}, asOf),
		})
	}
	return section
}
