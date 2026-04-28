// 遵循project_guide.md
package web

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"balanciz/internal/services"
)

func csvFilename(reportType string) string {
	return fmt.Sprintf("balanciz_%s_%s.csv", reportType, time.Now().Format("20060102_150405"))
}

func setCsvHeaders(c *fiber.Ctx, filename string) {
	c.Set("Content-Type", "text/csv; charset=utf-8")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
}

func setExcelHeaders(c *fiber.Ctx, filename string) {
	c.Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
}

// ── Financial statement CSV exports ──────────────────────────────────────────

func (s *Server) handleExportTrialBalanceCSV(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	fromDate, toDate, _, _, errMsg := parseReportRange(c.Query("from"), c.Query("to"))
	if errMsg != "" {
		return c.Status(fiber.StatusBadRequest).SendString(errMsg)
	}
	rows, totalDebits, totalCredits, err := services.TrialBalance(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("could not run report")
	}
	var buf bytes.Buffer
	if err := services.ExportTrialBalanceCSV(fromDate, toDate, rows, totalDebits, totalCredits, &buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	setCsvHeaders(c, csvFilename("trial_balance"))
	_, err = c.Write(buf.Bytes())
	return err
}

func (s *Server) handleExportIncomeStatementCSV(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	fromDate, toDate, _, _, errMsg := parseReportRange(c.Query("from"), c.Query("to"))
	if errMsg != "" {
		return c.Status(fiber.StatusBadRequest).SendString(errMsg)
	}
	report, err := services.IncomeStatementReport(s.DB, companyID, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("could not run report")
	}
	var buf bytes.Buffer
	if err := services.ExportIncomeStatementCSV(report, &buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	setCsvHeaders(c, csvFilename("income_statement"))
	_, err = c.Write(buf.Bytes())
	return err
}

func (s *Server) handleExportBalanceSheetCSV(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	asOfStr := strings.TrimSpace(c.Query("as_of"))
	if asOfStr == "" {
		asOfStr = time.Now().Format("2006-01-02")
	}
	asOf, err := time.Parse("2006-01-02", asOfStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("as_of must be a valid date")
	}
	report, err := services.BalanceSheetReport(s.DB, companyID, asOf)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("could not run report")
	}
	var buf bytes.Buffer
	if err := services.ExportBalanceSheetCSV(report, &buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	setCsvHeaders(c, csvFilename("balance_sheet"))
	_, err = c.Write(buf.Bytes())
	return err
}

// ── Clearing exports ─────────────────────────────────────────────────────────

func (s *Server) handleExportARAgingCSV(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	asOfStr := strings.TrimSpace(c.Query("as_of"))
	if asOfStr == "" {
		asOfStr = time.Now().Format("2006-01-02")
	}
	asOf, err := time.Parse("2006-01-02", asOfStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("as_of must be a valid date")
	}
	report, err := services.BuildARAgingReport(s.DB, companyID, asOf)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("could not run report")
	}
	var buf bytes.Buffer
	if err := services.ExportARAgingCSV(report, &buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	setCsvHeaders(c, csvFilename("ar_aging"))
	_, err = c.Write(buf.Bytes())
	return err
}

func (s *Server) handleExportAccountTransactionsCSV(c *fiber.Ctx) error {
	report, fromDate, toDate, errStatus, errMsg := s.accountTransactionsExportReport(c)
	if errMsg != "" {
		return c.Status(errStatus).SendString(errMsg)
	}
	var buf bytes.Buffer
	if err := services.ExportAccountTransactionsCSV(report, fromDate, toDate, &buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	setCsvHeaders(c, services.AccountTransactionsExportFilename(report, "csv"))
	_, err := c.Write(buf.Bytes())
	return err
}

func (s *Server) handleExportAccountTransactionsXLSX(c *fiber.Ctx) error {
	report, fromDate, toDate, errStatus, errMsg := s.accountTransactionsExportReport(c)
	if errMsg != "" {
		return c.Status(errStatus).SendString(errMsg)
	}
	var buf bytes.Buffer
	if err := services.ExportAccountTransactionsXLSX(report, fromDate, toDate, &buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	setExcelHeaders(c, services.AccountTransactionsExportFilename(report, "xlsx"))
	_, err := c.Write(buf.Bytes())
	return err
}

func (s *Server) handleExportAccountTransactionsPDF(c *fiber.Ctx) error {
	report, fromDate, toDate, errStatus, errMsg := s.accountTransactionsExportReport(c)
	if errMsg != "" {
		return c.Status(errStatus).SendString(errMsg)
	}
	if !services.PDFGeneratorAvailable() {
		return c.Status(fiber.StatusServiceUnavailable).SendString("PDF generation is not available")
	}
	pdfBytes, err := services.RenderAccountTransactionsPDF(c.Context(), report, fromDate, toDate)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).SendString("PDF generation failed: " + err.Error())
	}
	return sendPDFResponse(c, pdfBytes, services.AccountTransactionsExportFilename(report, "pdf"))
}

func (s *Server) accountTransactionsExportReport(c *fiber.Ctx) (*services.AccountTransactionsReport, time.Time, time.Time, int, string) {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return nil, time.Time{}, time.Time{}, fiber.StatusBadRequest, "company required"
	}
	accountID64, err := strconv.ParseUint(c.Query("account_id"), 10, 64)
	if err != nil || accountID64 == 0 {
		return nil, time.Time{}, time.Time{}, fiber.StatusBadRequest, "account_id query param required"
	}
	fromDate, toDate, _, _, errMsg := parseReportRange(c.Query("from"), c.Query("to"))
	if errMsg != "" {
		return nil, time.Time{}, time.Time{}, fiber.StatusBadRequest, errMsg
	}
	report, err := services.BuildAccountTransactionsReport(s.DB, companyID, uint(accountID64), fromDate, toDate)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fiber.StatusInternalServerError, "could not run report"
	}
	return report, fromDate, toDate, 0, ""
}

func (s *Server) handleExportClearingSummary(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	var buf bytes.Buffer
	if err := services.ExportClearingSummaryCSV(s.DB, companyID, &buf); err != nil {
		return c.Status(fiber.StatusConflict).SendString(err.Error())
	}
	setCsvHeaders(c, csvFilename("clearing_summary"))
	_, err := c.Write(buf.Bytes())
	return err
}

func (s *Server) handleExportClearingMovements(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	channelID, _ := strconv.ParseUint(c.Query("channel"), 10, 64)
	if channelID == 0 {
		return c.Status(fiber.StatusBadRequest).SendString("channel query param required")
	}
	var buf bytes.Buffer
	if err := services.ExportClearingMovementsCSV(s.DB, companyID, uint(channelID), &buf); err != nil {
		return c.Status(fiber.StatusConflict).SendString(err.Error())
	}
	setCsvHeaders(c, csvFilename("clearing_movements"))
	_, err := c.Write(buf.Bytes())
	return err
}

// ── Settlement exports ───────────────────────────────────────────────────────

func (s *Server) handleExportSettlementsList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	setCsvHeaders(c, csvFilename("settlements"))
	return services.ExportSettlementsListCSV(s.DB, companyID, c)
}

func (s *Server) handleExportSettlementLines(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Status(fiber.StatusBadRequest).SendString("settlement id required")
	}
	setCsvHeaders(c, csvFilename(fmt.Sprintf("settlement_%d_lines", id)))
	return services.ExportSettlementLinesCSV(s.DB, companyID, uint(id), c)
}

// ── Channel order exports ────────────────────────────────────────────────────

func (s *Server) handleExportChannelOrders(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	setCsvHeaders(c, csvFilename("channel_orders"))
	return services.ExportChannelOrdersListCSV(s.DB, companyID, c)
}

func (s *Server) handleExportChannelOrderLines(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Status(fiber.StatusBadRequest).SendString("order id required")
	}
	setCsvHeaders(c, csvFilename(fmt.Sprintf("channel_order_%d_lines", id)))
	return services.ExportChannelOrderLinesCSV(s.DB, companyID, uint(id), c)
}
