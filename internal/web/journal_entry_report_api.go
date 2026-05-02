package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/services"
)

type journalEntryReportAPIResponse struct {
	From       string                       `json:"from"`
	To         string                       `json:"to"`
	EntryCount int                          `json:"entry_count"`
	LineCount  int                          `json:"line_count"`
	Totals     journalEntryReportAPITotals  `json:"totals"`
	Entries    []journalEntryReportAPIEntry `json:"entries"`
}

type journalEntryReportAPITotals struct {
	Debits  string `json:"debits"`
	Credits string `json:"credits"`
}

type journalEntryReportAPIEntry struct {
	ID           uint                        `json:"id"`
	EntryDate    string                      `json:"entry_date"`
	JournalNo    string                      `json:"journal_no"`
	DocumentURL  string                      `json:"document_url"`
	ReversalNote string                      `json:"reversal_note"`
	LineCount    int                         `json:"line_count"`
	Debits       string                      `json:"debits"`
	Credits      string                      `json:"credits"`
	Lines        []journalEntryReportAPILine `json:"lines"`
}

type journalEntryReportAPILine struct {
	Key         string `json:"key"`
	Account     string `json:"account"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	Memo        string `json:"memo"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
}

func (s *Server) handleJournalEntryReportAPI(c *fiber.Ctx) error {
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

	entries, err := services.JournalEntryReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not run journal entries report"})
	}

	return c.JSON(journalEntryReportAPIFromEntries(entries, fromStr, toStr))
}

func journalEntryReportAPIFromEntries(entries []services.JournalEntryReportEntry, from, to string) journalEntryReportAPIResponse {
	resp := journalEntryReportAPIResponse{
		From:       from,
		To:         to,
		EntryCount: len(entries),
		Entries:    make([]journalEntryReportAPIEntry, 0, len(entries)),
	}
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero
	for _, entry := range entries {
		apiEntry := journalEntryReportAPIEntry{
			ID:           entry.ID,
			EntryDate:    entry.EntryDate,
			JournalNo:    entry.JournalNo,
			DocumentURL:  "/journal-entry/" + formatReportUint(entry.ID),
			ReversalNote: entry.ReversalNote,
			LineCount:    len(entry.Lines),
			Lines:        make([]journalEntryReportAPILine, 0, len(entry.Lines)),
		}
		entryDebits := decimal.Zero
		entryCredits := decimal.Zero
		for idx, line := range entry.Lines {
			entryDebits = entryDebits.Add(line.Debit)
			entryCredits = entryCredits.Add(line.Credit)
			resp.LineCount++
			apiEntry.Lines = append(apiEntry.Lines, journalEntryReportAPILine{
				Key:         formatReportUint(entry.ID) + ":" + formatReportUint(uint(idx)),
				Account:     line.AccountCode + " " + line.AccountName,
				AccountCode: line.AccountCode,
				AccountName: line.AccountName,
				Memo:        line.Memo,
				Debit:       reportDecimalString(line.Debit),
				Credit:      reportDecimalString(line.Credit),
			})
		}
		apiEntry.Debits = reportDecimalString(entryDebits)
		apiEntry.Credits = reportDecimalString(entryCredits)
		totalDebits = totalDebits.Add(entryDebits)
		totalCredits = totalCredits.Add(entryCredits)
		resp.Entries = append(resp.Entries, apiEntry)
	}
	resp.Totals = journalEntryReportAPITotals{
		Debits:  reportDecimalString(totalDebits),
		Credits: reportDecimalString(totalCredits),
	}
	return resp
}

func formatReportUint(n uint) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
