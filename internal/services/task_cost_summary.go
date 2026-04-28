package services

import (
	"errors"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

type TaskCostSummary struct {
	Expenses               []models.Expense
	BillLines              []models.BillLine
	BillableExpenseAmount  decimal.Decimal
	NonBillableExpenseCost decimal.Decimal
}

// GetTaskCostSummary returns the currently linked task costs for UI display.
// Totals are operational summaries built from current document amounts; they
// are not invoice truth or a profitability report.
func GetTaskCostSummary(db *gorm.DB, companyID, taskID uint) (*TaskCostSummary, error) {
	var task models.Task
	if err := db.Select("id", "company_id").Where("id = ? AND company_id = ?", taskID, companyID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}

	summary := &TaskCostSummary{}
	if err := db.
		Preload("Vendor").
		Preload("ExpenseAccount").
		Preload("BillableCustomer").
		Where("company_id = ? AND task_id = ?", companyID, taskID).
		Order("expense_date desc, id desc").
		Find(&summary.Expenses).Error; err != nil {
		return nil, err
	}

	if err := db.
		Preload("Bill").
		Preload("Bill.Vendor").
		Preload("ExpenseAccount").
		Preload("BillableCustomer").
		Where("company_id = ? AND task_id = ?", companyID, taskID).
		Order("bill_id desc, sort_order asc, id desc").
		Find(&summary.BillLines).Error; err != nil {
		return nil, err
	}

	for _, exp := range summary.Expenses {
		if exp.IsBillable {
			summary.BillableExpenseAmount = summary.BillableExpenseAmount.Add(exp.Amount)
		} else {
			summary.NonBillableExpenseCost = summary.NonBillableExpenseCost.Add(exp.Amount)
		}
	}
	for _, line := range summary.BillLines {
		if line.IsBillable {
			summary.BillableExpenseAmount = summary.BillableExpenseAmount.Add(line.LineNet)
		} else {
			summary.NonBillableExpenseCost = summary.NonBillableExpenseCost.Add(line.LineNet)
		}
	}

	return summary, nil
}
