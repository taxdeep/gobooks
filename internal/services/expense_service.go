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
	ErrExpenseNotFound              = errors.New("expense not found")
	ErrExpenseDateRequired          = errors.New("expense date is required")
	ErrExpenseDescriptionRequired   = errors.New("description is required")
	ErrExpenseAmountInvalid         = errors.New("amount must be greater than zero")
	ErrExpenseCurrencyRequired      = errors.New("currency is required")
	ErrExpenseAccountRequired       = errors.New("expense account is required")
	ErrExpenseAccountInvalid        = errors.New("expense account is not valid for this company")
	ErrExpenseVendorInvalid         = errors.New("vendor is not valid for this company")
	ErrExpensePaymentAccountInvalid = errors.New("payment account is not valid for this company")
	ErrExpensePaymentMethodRequired = errors.New("payment method is required when a payment account is selected")
	ErrExpensePaymentMethodInvalid  = errors.New("payment method is not valid")
	ErrExpenseLinesRequired         = errors.New("at least one expense line with a positive amount is required")
	ErrExpenseLineAccountRequired   = errors.New("each expense line must have an expense category")
	ErrExpenseLineAccountInvalid    = errors.New("one or more expense line categories are not valid for this company")
)

// ExpenseLineInput represents a single cost-category row within an expense.
// When Lines is non-empty on ExpenseInput, the service derives the expense's
// total Amount and primary ExpenseAccountID from the lines.
type ExpenseLineInput struct {
	Description      string
	Amount           decimal.Decimal
	ExpenseAccountID *uint
}

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

	// Lines replaces the single Amount/ExpenseAccountID when non-empty.
	// The service sums line amounts → Expense.Amount and uses lines[0].ExpenseAccountID
	// as Expense.ExpenseAccountID for backward-compat reporting joins.
	Lines []ExpenseLineInput

	// Payment settlement (all optional).
	PaymentAccountID *uint
	PaymentMethod    models.PaymentMethod
	PaymentReference string
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
		Preload("Lines", func(db *gorm.DB) *gorm.DB {
			return db.Order("line_order ASC, id ASC")
		}).
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
	// When lines are present, derive header Amount and ExpenseAccountID from them.
	if len(in.Lines) > 0 {
		total := decimal.Zero
		for _, l := range in.Lines {
			total = total.Add(l.Amount)
		}
		in.Amount = total
		in.ExpenseAccountID = in.Lines[0].ExpenseAccountID
		// Use first non-empty line description as header description if blank.
		if strings.TrimSpace(in.Description) == "" {
			for _, l := range in.Lines {
				if d := strings.TrimSpace(l.Description); d != "" {
					in.Description = d
					break
				}
			}
		}
	}

	if err := validateExpenseInput(db, in); err != nil {
		return nil, err
	}

	// Validate per-line accounts when lines are present.
	if len(in.Lines) > 0 {
		if err := validateExpenseLines(db, in); err != nil {
			return nil, err
		}
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
		expense.PaymentAccountID = in.PaymentAccountID
		expense.PaymentMethod = in.PaymentMethod
		expense.PaymentReference = strings.TrimSpace(in.PaymentReference)

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

		// Replace expense lines when the submission includes them.
		if len(in.Lines) > 0 {
			if err := tx.Where("expense_id = ?", savedID).Delete(&models.ExpenseLine{}).Error; err != nil {
				return err
			}
			for i, l := range in.Lines {
				line := models.ExpenseLine{
					ExpenseID:        savedID,
					LineOrder:        i,
					Description:      strings.TrimSpace(l.Description),
					Amount:           l.Amount.RoundBank(2),
					ExpenseAccountID: l.ExpenseAccountID,
				}
				if err := tx.Create(&line).Error; err != nil {
					return err
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetExpenseByID(db, in.CompanyID, savedID)
}

// validateExpenseLines checks per-line accounts exist and belong to the company.
func validateExpenseLines(db *gorm.DB, in ExpenseInput) error {
	for _, l := range in.Lines {
		if l.ExpenseAccountID == nil || *l.ExpenseAccountID == 0 {
			return ErrExpenseLineAccountRequired
		}
	}
	// Batch-check all distinct account IDs.
	seen := map[uint]bool{}
	ids := make([]uint, 0, len(in.Lines))
	for _, l := range in.Lines {
		if l.ExpenseAccountID != nil && *l.ExpenseAccountID > 0 && !seen[*l.ExpenseAccountID] {
			seen[*l.ExpenseAccountID] = true
			ids = append(ids, *l.ExpenseAccountID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var count int64
	if err := db.Model(&models.Account{}).
		Where("id IN ? AND company_id = ? AND root_account_type = ? AND is_active = true",
			ids, in.CompanyID, models.RootExpense).
		Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(ids) {
		return ErrExpenseLineAccountInvalid
	}
	return nil
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
		Where("id = ? AND company_id = ? AND root_account_type = ? AND is_active = true",
			*in.ExpenseAccountID, in.CompanyID, models.RootExpense).
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

	if in.PaymentAccountID != nil && *in.PaymentAccountID > 0 {
		count = 0
		if err := db.Model(&models.Account{}).
			Where("id = ? AND company_id = ? AND detail_account_type IN ? AND is_active = true",
				*in.PaymentAccountID, in.CompanyID,
				models.PaymentSourceDetailTypes()).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrExpensePaymentAccountInvalid
		}
		if in.PaymentMethod == "" {
			return ErrExpensePaymentMethodRequired
		}
	}

	if in.PaymentMethod != "" {
		if _, err := models.ParsePaymentMethod(string(in.PaymentMethod)); err != nil {
			return ErrExpensePaymentMethodInvalid
		}
	}

	return nil
}
