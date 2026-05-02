package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

type accountTransactionsAPIResponse struct {
	From          string                      `json:"from"`
	To            string                      `json:"to"`
	AccountID     uint                        `json:"account_id"`
	AccountCode   string                      `json:"account_code"`
	AccountName   string                      `json:"account_name"`
	RootType      string                      `json:"root_type"`
	DetailType    string                      `json:"detail_type"`
	Starting      string                      `json:"starting_balance"`
	TotalDebits   string                      `json:"total_debits"`
	TotalCredits  string                      `json:"total_credits"`
	Ending        string                      `json:"ending_balance"`
	BalanceChange string                      `json:"balance_change"`
	RowCount      int                         `json:"row_count"`
	Rows          []accountTransactionsAPIRow `json:"rows"`
}

type accountTransactionsAPIRow struct {
	Key                  string `json:"key"`
	Date                 string `json:"date"`
	Type                 string `json:"type"`
	DocumentNumber       string `json:"document_number"`
	DocumentURL          string `json:"document_url"`
	CounterpartyName     string `json:"counterparty_name"`
	Description          string `json:"description"`
	Debit                string `json:"debit"`
	Credit               string `json:"credit"`
	Balance              string `json:"balance"`
	JournalNo            string `json:"journal_no"`
	TransactionTypeLabel string `json:"transaction_type_label"`
}

func (s *Server) handleAccountTransactionsAPI(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "active company required"})
	}

	accountID64, err := strconv.ParseUint(c.Query("account_id"), 10, 64)
	if err != nil || accountID64 == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "account_id is required"})
	}
	accountID := uint(accountID64)

	co := s.loadReportCompanyInfo(companyID)
	_, fromStr, toStr := resolvePeriodDates(
		c.Query("period"), c.Query("from"), c.Query("to"), co.FiscalYearEnd)
	fromDate, toDate, fromStr, toStr, errMsg := parseReportRange(fromStr, toStr)
	if errMsg != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": errMsg})
	}

	report, err := services.BuildAccountTransactionsReport(s.DB, companyID, accountID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "account not found or could not load transactions"})
	}

	return c.JSON(accountTransactionsAPIFromReport(report, fromStr, toStr))
}

func accountTransactionsAPIFromReport(report *services.AccountTransactionsReport, from, to string) accountTransactionsAPIResponse {
	if report == nil {
		return accountTransactionsAPIResponse{From: from, To: to}
	}
	resp := accountTransactionsAPIResponse{
		From:          from,
		To:            to,
		AccountID:     report.AccountID,
		AccountCode:   report.AccountCode,
		AccountName:   report.AccountName,
		RootType:      report.AccountRootType,
		DetailType:    report.DetailType,
		Starting:      reportDecimalString(report.StartingBalance),
		TotalDebits:   reportDecimalString(report.TotalDebits),
		TotalCredits:  reportDecimalString(report.TotalCredits),
		Ending:        reportDecimalString(report.EndingBalance),
		BalanceChange: reportDecimalString(report.EndingBalance.Sub(report.StartingBalance)),
		RowCount:      len(report.Rows),
		Rows:          make([]accountTransactionsAPIRow, 0, len(report.Rows)),
	}
	for idx, row := range report.Rows {
		resp.Rows = append(resp.Rows, accountTransactionsAPIRow{
			Key:                  report.AccountCode + ":" + row.Date + ":" + row.JournalNo + ":" + formatReportUint(uint(idx)),
			Date:                 row.Date,
			Type:                 row.TransactionTypeLabel,
			DocumentNumber:       row.DocumentNumber,
			DocumentURL:          row.DocumentURL,
			CounterpartyName:     row.CounterpartyName,
			Description:          row.Description,
			Debit:                reportDecimalString(row.Debit),
			Credit:               reportDecimalString(row.Credit),
			Balance:              reportDecimalString(row.Balance),
			JournalNo:            row.JournalNo,
			TransactionTypeLabel: row.TransactionTypeLabel,
		})
	}
	return resp
}
