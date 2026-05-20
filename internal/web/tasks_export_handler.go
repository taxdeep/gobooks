// filepath: internal/web/tasks_export_handler.go
package web

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"balanciz/internal/models"
	"balanciz/internal/services"

	"github.com/gofiber/fiber/v2"
)

// Export tasks as CSV
func (s *Server) handleTasksExportCSV(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "company context required",
		})
	}

	filter, err := taskExportFilterFromQuery(companyID, c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	// Get tasks matching filters
	tasks, err := services.ListTasks(s.DB, filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to export tasks",
		})
	}

	// Create CSV in memory
	csvBuf := &strings.Builder{}
	w := csv.NewWriter(csvBuf)

	// Write header
	w.Write([]string{
		"Task ID",
		"Date",
		"Customer",
		"Description",
		"Quantity",
		"Rate",
		"Currency",
		"Amount",
		"Billable",
		"Status",
		"Notes",
	})

	// Write data rows
	for _, task := range tasks {
		customerName := task.Customer.Name

		w.Write([]string{
			fmt.Sprintf("%d", task.ID),
			task.TaskDate.Format("2006-01-02"),
			customerName,
			task.Title,
			task.Quantity.String(),
			task.Rate.String(),
			task.CurrencyCode,
			task.BillableAmount().String(),
			fmt.Sprintf("%v", task.IsBillable),
			string(task.Status),
			task.Notes,
		})
	}

	w.Flush()

	// Set response headers for CSV download
	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"tasks-%s.csv\"", time.Now().Format("2006-01-02")))
	c.Set("Content-Length", fmt.Sprintf("%d", len(csvBuf.String())))

	return c.SendString(csvBuf.String())
}

func taskExportFilterFromQuery(companyID uint, c *fiber.Ctx) (services.TaskListFilter, error) {
	filter := services.TaskListFilter{CompanyID: companyID}

	yearStr := strings.TrimSpace(c.Query("year"))
	monthStr := strings.TrimSpace(c.Query("month"))
	if yearStr != "" || monthStr != "" {
		if yearStr == "" || monthStr == "" {
			return filter, fmt.Errorf("year and month are required together")
		}
		year, err := strconv.Atoi(yearStr)
		if err != nil || year < 1900 || year > 9999 {
			return filter, fmt.Errorf("report year is invalid")
		}
		month, err := strconv.Atoi(monthStr)
		if err != nil || month < 1 || month > 12 {
			return filter, fmt.Errorf("report month is invalid")
		}

		from := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(0, 1, -1)
		filter.From = &from
		filter.To = &to
	}

	filterCustomerID := strings.TrimSpace(c.Query("customer_id"))
	if filterCustomerID != "" {
		if id64, err := services.ParseUint(filterCustomerID); err == nil && id64 > 0 {
			id := uint(id64)
			filter.CustomerID = &id
		}
	}

	filterStatus := strings.TrimSpace(c.Query("status"))
	if filterStatus != "" {
		status := models.TaskStatus(filterStatus)
		for _, allowed := range models.AllTaskStatuses() {
			if allowed == status {
				filter.Status = &status
				break
			}
		}
	}

	if filterFrom := strings.TrimSpace(c.Query("from")); filterFrom != "" {
		if from, err := time.Parse("2006-01-02", filterFrom); err == nil {
			filter.From = &from
		}
	}
	if filterTo := strings.TrimSpace(c.Query("to")); filterTo != "" {
		if to, err := time.Parse("2006-01-02", filterTo); err == nil {
			filter.To = &to
		}
	}

	return filter, nil
}
