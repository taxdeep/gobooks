package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/services"
)

type generalLedgerAPIResponse struct {
	From         string                    `json:"from"`
	To           string                    `json:"to"`
	SectionCount int                       `json:"section_count"`
	RowCount     int                       `json:"row_count"`
	Totals       generalLedgerAPITotals    `json:"totals"`
	Sections     []generalLedgerAPISection `json:"sections"`
}

type generalLedgerAPITotals struct {
	Debits  string `json:"debits"`
	Credits string `json:"credits"`
}

type generalLedgerAPISection struct {
	AccountID       uint                  `json:"account_id"`
	AccountCode     string                `json:"account_code"`
	AccountName     string                `json:"account_name"`
	AccountRootType string                `json:"account_root_type"`
	DetailType      string                `json:"detail_type"`
	StartingBalance string                `json:"starting_balance"`
	TotalDebits     string                `json:"total_debits"`
	TotalCredits    string                `json:"total_credits"`
	EndingBalance   string                `json:"ending_balance"`
	Rows            []generalLedgerAPIRow `json:"rows"`
}

type generalLedgerAPIRow struct {
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

func (s *Server) handleGeneralLedgerAPI(c *fiber.Ctx) error {
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

	report, err := services.BuildGeneralLedgerReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not build General Ledger"})
	}

	return c.JSON(generalLedgerAPIFromReport(report, fromStr, toStr))
}

func generalLedgerAPIFromReport(report *services.GeneralLedgerReport, from, to string) generalLedgerAPIResponse {
	if report == nil {
		return generalLedgerAPIResponse{From: from, To: to}
	}
	resp := generalLedgerAPIResponse{
		From:         from,
		To:           to,
		SectionCount: len(report.Sections),
		Sections:     make([]generalLedgerAPISection, 0, len(report.Sections)),
	}
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero
	for _, section := range report.Sections {
		apiSection := generalLedgerAPISection{
			AccountID:       section.AccountID,
			AccountCode:     section.AccountCode,
			AccountName:     section.AccountName,
			AccountRootType: section.AccountRootType,
			DetailType:      section.DetailType,
			StartingBalance: reportDecimalString(section.StartingBalance),
			TotalDebits:     reportDecimalString(section.TotalDebits),
			TotalCredits:    reportDecimalString(section.TotalCredits),
			EndingBalance:   reportDecimalString(section.EndingBalance),
			Rows:            make([]generalLedgerAPIRow, 0, len(section.Rows)),
		}
		totalDebits = totalDebits.Add(section.TotalDebits)
		totalCredits = totalCredits.Add(section.TotalCredits)
		resp.RowCount += len(section.Rows)
		for idx, row := range section.Rows {
			apiSection.Rows = append(apiSection.Rows, generalLedgerAPIRow{
				Key:                  section.AccountCode + ":" + row.Date + ":" + row.JournalNo + ":" + reportDecimalString(row.Debit) + ":" + reportDecimalString(row.Credit) + ":" + decimal.NewFromInt(int64(idx)).String(),
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
		resp.Sections = append(resp.Sections, apiSection)
	}
	resp.Totals = generalLedgerAPITotals{
		Debits:  reportDecimalString(totalDebits),
		Credits: reportDecimalString(totalCredits),
	}
	return resp
}
