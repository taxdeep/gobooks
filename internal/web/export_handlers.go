// 遵循project_guide.md
package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"gobooks/internal/services"
)

func csvFilename(reportType string) string {
	return fmt.Sprintf("gobooks_%s_%s.csv", reportType, time.Now().Format("20060102_150405"))
}

func setCsvHeaders(c *fiber.Ctx, filename string) {
	c.Set("Content-Type", "text/csv; charset=utf-8")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
}

// ── Clearing exports ─────────────────────────────────────────────────────────

func (s *Server) handleExportClearingSummary(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company required")
	}
	setCsvHeaders(c, csvFilename("clearing_summary"))
	return services.ExportClearingSummaryCSV(s.DB, companyID, c)
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
	setCsvHeaders(c, csvFilename("clearing_movements"))
	return services.ExportClearingMovementsCSV(s.DB, companyID, uint(channelID), c)
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
