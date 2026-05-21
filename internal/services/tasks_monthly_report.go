// filepath: internal/services/tasks_monthly_report.go
package services

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

var (
	ErrTaskReportCompanyRequired = errors.New("company is required")
	ErrTaskReportYearInvalid     = errors.New("report year is invalid")
	ErrTaskReportMonthInvalid    = errors.New("report month is invalid")
)

// MonthlyTaskSummary represents billable hours/amounts for a given month
type MonthlyTaskSummary struct {
	Year                int
	Month               int
	MonthName           string
	TotalTasks          int
	BillableTasksCount  int
	CompletedCount      int
	InvoicedCount       int
	TotalQuantity       decimal.Decimal
	TotalBillableAmount decimal.Decimal
	ByCustomer          map[string]*CustomerTaskSummary
}

type CustomerTaskSummary struct {
	CustomerID   uint
	CustomerName string
	TaskCount    int
	Quantity     decimal.Decimal
	Amount       decimal.Decimal
}

// GenerateMonthlyTaskReport generates a monthly summary of billable tasks
func GenerateMonthlyTaskReport(db *gorm.DB, companyID uint, year, month int) (*MonthlyTaskSummary, error) {
	startDate, endDate, err := taskMonthBounds(companyID, year, month)
	if err != nil {
		return nil, err
	}

	summary := &MonthlyTaskSummary{
		Year:       year,
		Month:      month,
		MonthName:  startDate.Format("January 2006"),
		ByCustomer: make(map[string]*CustomerTaskSummary),
	}

	var tasks []struct {
		ID           uint
		CustomerID   uint
		CustomerName string
		Status       models.TaskStatus
		Quantity     decimal.Decimal
		Rate         decimal.Decimal
		IsBillable   bool
	}

	// Query tasks for the month
	err = db.Table("tasks").
		Select("tasks.id, tasks.customer_id, customers.name as customer_name, tasks.status, tasks.quantity, tasks.rate, tasks.is_billable").
		Joins("LEFT JOIN customers ON tasks.customer_id = customers.id AND customers.company_id = tasks.company_id").
		Where("tasks.company_id = ?", companyID).
		Where("tasks.task_date >= ? AND tasks.task_date < ?", startDate, endDate).
		Order("tasks.task_date DESC").
		Scan(&tasks).Error
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	taskIDs := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	taskLineAmounts := map[uint]struct {
		Quantity decimal.Decimal
		Amount   decimal.Decimal
	}{}
	if len(taskIDs) > 0 {
		var lines []models.TaskLine
		if err := db.
			Where("company_id = ? AND task_id IN ? AND is_billable = ?", companyID, taskIDs, true).
			Find(&lines).Error; err != nil {
			return nil, err
		}
		for _, line := range lines {
			amount := taskLineAmounts[line.TaskID]
			amount.Quantity = amount.Quantity.Add(line.Quantity)
			amount.Amount = amount.Amount.Add(line.BillableAmount())
			taskLineAmounts[line.TaskID] = amount
		}
	}

	// Aggregate by customer
	totalQty := decimal.NewFromInt(0)
	totalAmount := decimal.NewFromInt(0)

	for _, task := range tasks {
		summary.TotalTasks++

		if task.IsBillable {
			quantity, billableAmount := taskReportQuantityAndAmount(task.ID, task.Quantity, task.Rate, taskLineAmounts)
			totalAmount = totalAmount.Add(billableAmount)
			totalQty = totalQty.Add(quantity)
			summary.BillableTasksCount++
		}

		if task.Status == models.TaskStatusCompleted {
			summary.CompletedCount++
		} else if task.Status == models.TaskStatusInvoiced {
			summary.InvoicedCount++
		}

		// Group by customer
		key := fmt.Sprintf("%d-%s", task.CustomerID, task.CustomerName)
		if _, exists := summary.ByCustomer[key]; !exists {
			summary.ByCustomer[key] = &CustomerTaskSummary{
				CustomerID:   task.CustomerID,
				CustomerName: task.CustomerName,
				Quantity:     decimal.NewFromInt(0),
				Amount:       decimal.NewFromInt(0),
			}
		}

		cust := summary.ByCustomer[key]
		cust.TaskCount++
		if task.IsBillable {
			quantity, billableAmount := taskReportQuantityAndAmount(task.ID, task.Quantity, task.Rate, taskLineAmounts)
			cust.Quantity = cust.Quantity.Add(quantity)
			cust.Amount = cust.Amount.Add(billableAmount)
		}
	}

	summary.TotalQuantity = totalQty
	summary.TotalBillableAmount = totalAmount

	return summary, nil
}

// ListTasksByMonth lists all tasks in a specific month (used for detailed report)
func ListTasksByMonth(db *gorm.DB, companyID uint, year, month int) ([]models.Task, error) {
	startDate, endDate, err := taskMonthBounds(companyID, year, month)
	if err != nil {
		return nil, err
	}

	var tasks []models.Task
	err = db.
		Where("company_id = ?", companyID).
		Where("task_date >= ? AND task_date < ?", startDate, endDate).
		Preload("Customer").
		Preload("ProductService").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc, id asc") }).
		Preload("Lines.ProductService").
		Order("task_date DESC").
		Find(&tasks).Error

	return tasks, err
}

func taskReportQuantityAndAmount(taskID uint, legacyQuantity, legacyRate decimal.Decimal, lineAmounts map[uint]struct {
	Quantity decimal.Decimal
	Amount   decimal.Decimal
}) (decimal.Decimal, decimal.Decimal) {
	if amount, ok := lineAmounts[taskID]; ok {
		return amount.Quantity, amount.Amount
	}
	return legacyQuantity, legacyQuantity.Mul(legacyRate)
}

func taskMonthBounds(companyID uint, year, month int) (time.Time, time.Time, error) {
	if companyID == 0 {
		return time.Time{}, time.Time{}, ErrTaskReportCompanyRequired
	}
	if year < 1900 || year > 9999 {
		return time.Time{}, time.Time{}, ErrTaskReportYearInvalid
	}
	if month < 1 || month > 12 {
		return time.Time{}, time.Time{}, ErrTaskReportMonthInvalid
	}

	startDate := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	return startDate, startDate.AddDate(0, 1, 0), nil
}
