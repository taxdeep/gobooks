package services

import (
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

var (
	ErrExpenseNotFound            = errors.New("expense not found")
	ErrExpenseDateRequired        = errors.New("expense date is required")
	ErrExpenseDescriptionRequired = errors.New("description is required")
	ErrExpenseAmountInvalid       = errors.New("amount must be greater than zero")
	ErrExpenseCurrencyRequired    = errors.New("currency is required")
	ErrExpenseAccountRequired     = errors.New("expense account is required")
	ErrExpenseAccountInvalid      = errors.New("expense account is not valid for this company")
	ErrExpenseVendorInvalid       = errors.New("vendor is not valid for this company")
)

type ExpenseInput struct {
	CompanyID          uint
	TaskID             *uint
	BillableCustomerID *uint
	IsBillable         bool

	ExpenseDate      time.Time
	Description      string
	Amount           decimal.Decimal
	CurrencyCode     string
	VendorID         *uint
	ExpenseAccountID *uint
	Notes            string
}

type ExpenseListFilter struct {
	CompanyID uint
	TaskID    *uint
}

func CreateExpense(db *gorm.DB, in ExpenseInput) (*models.Expense, error) {
	expense, err := upsertExpense(db, 0, in)
	if err != nil {
		return nil, err
	}
	return expense, nil
}

func UpdateExpense(db *gorm.DB, companyID, expenseID uint, in ExpenseInput) (*models.Expense, error) {
	in.CompanyID = companyID
	return upsertExpense(db, expenseID, in)
}

func GetExpenseByID(db *gorm.DB, companyID, expenseID uint) (*models.Expense, error) {
	var expense models.Expense
	err := db.
		Preload("Task.Customer").
		Preload("BillableCustomer").
		Preload("Vendor").
		Preload("ExpenseAccount").
		Where("id = ? AND company_id = ?", expenseID, companyID).
		First(&expense).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrExpenseNotFound
	}
	if err != nil {
		return nil, err
	}
	return &expense, nil
}

func ListExpenses(db *gorm.DB, filter ExpenseListFilter) ([]models.Expense, error) {
	var expenses []models.Expense
	q := db.
		Preload("Task.Customer").
		Preload("BillableCustomer").
		Preload("Vendor").
		Preload("ExpenseAccount").
		Where("company_id = ?", filter.CompanyID)
	if filter.TaskID != nil && *filter.TaskID > 0 {
		q = q.Where("task_id = ?", *filter.TaskID)
	}
	if err := q.Order("expense_date desc, id desc").Find(&expenses).Error; err != nil {
		return nil, err
	}
	return expenses, nil
}

func upsertExpense(db *gorm.DB, expenseID uint, in ExpenseInput) (*models.Expense, error) {
	if err := validateExpenseInput(db, in); err != nil {
		return nil, err
	}
	linkage, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:          in.CompanyID,
		TaskID:             in.TaskID,
		BillableCustomerID: in.BillableCustomerID,
		IsBillable:         in.IsBillable,
	})
	if err != nil {
		return nil, err
	}

	var savedID uint
	err = db.Transaction(func(tx *gorm.DB) error {
		var expense models.Expense
		if expenseID > 0 {
			if err := tx.Where("id = ? AND company_id = ?", expenseID, in.CompanyID).First(&expense).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrExpenseNotFound
				}
				return err
			}
		} else {
			expense = models.Expense{CompanyID: in.CompanyID}
		}

		expense.TaskID = linkage.TaskID
		expense.BillableCustomerID = linkage.BillableCustomerID
		expense.IsBillable = linkage.IsBillable
		expense.ReinvoiceStatus = linkage.ReinvoiceStatus
		expense.ExpenseDate = in.ExpenseDate
		expense.Description = strings.TrimSpace(in.Description)
		expense.Amount = in.Amount.RoundBank(2)
		expense.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
		expense.VendorID = in.VendorID
		expense.ExpenseAccountID = in.ExpenseAccountID
		expense.Notes = strings.TrimSpace(in.Notes)

		if expenseID > 0 {
			if err := tx.Save(&expense).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(&expense).Error; err != nil {
				return err
			}
		}
		savedID = expense.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetExpenseByID(db, in.CompanyID, savedID)
}

func validateExpenseInput(db *gorm.DB, in ExpenseInput) error {
	if in.CompanyID == 0 {
		return ErrExpenseNotFound
	}
	if in.ExpenseDate.IsZero() {
		return ErrExpenseDateRequired
	}
	if strings.TrimSpace(in.Description) == "" {
		return ErrExpenseDescriptionRequired
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return ErrExpenseAmountInvalid
	}
	if strings.TrimSpace(in.CurrencyCode) == "" {
		return ErrExpenseCurrencyRequired
	}
	if in.ExpenseAccountID == nil || *in.ExpenseAccountID == 0 {
		return ErrExpenseAccountRequired
	}

	var count int64
	if err := db.Model(&models.Account{}).
		Where("id = ? AND company_id = ? AND is_active = true", *in.ExpenseAccountID, in.CompanyID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrExpenseAccountInvalid
	}

	if in.VendorID != nil && *in.VendorID > 0 {
		count = 0
		if err := db.Model(&models.Vendor{}).
			Where("id = ? AND company_id = ?", *in.VendorID, in.CompanyID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrExpenseVendorInvalid
		}
	}

	return nil
}
