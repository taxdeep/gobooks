// 遵循project_guide.md
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
	ErrTaskCustomerRequired         = errors.New("customer is required")
	ErrTaskTitleRequired            = errors.New("title is required")
	ErrTaskDateRequired             = errors.New("task date is required")
	ErrTaskUnitTypeRequired         = errors.New("unit type is required")
	ErrTaskUnitTypeInvalid          = errors.New("unit type is invalid")
	ErrTaskCurrencyRequired         = errors.New("currency is required")
	ErrTaskQuantityNegative         = errors.New("quantity must be zero or greater")
	ErrTaskRateNegative             = errors.New("rate must be zero or greater")
	ErrTaskNotFound                 = errors.New("task not found")
	ErrTaskCustomerInvalid          = errors.New("customer is not valid for this company")
	ErrTaskCompletedReadOnly        = errors.New("completed tasks can only update notes")
	ErrTaskCancelledReadOnly        = errors.New("cancelled tasks cannot be edited")
	ErrTaskInvoicedReadOnly         = errors.New("invoiced tasks cannot be edited")
	ErrTaskCompleteRequiresOpen     = errors.New("only open tasks can be completed")
	ErrTaskCancelRequiresOpenOrDone = errors.New("only open or completed tasks can be cancelled")
)

type TaskInput struct {
	CompanyID    uint
	CustomerID   uint
	Title        string
	TaskDate     time.Time
	Quantity     decimal.Decimal
	UnitType     string
	Rate         decimal.Decimal
	CurrencyCode string
	IsBillable   bool
	Notes        string
}

type TaskListFilter struct {
	CompanyID  uint
	CustomerID *uint
	Status     *models.TaskStatus
	From       *time.Time
	To         *time.Time
}

func CreateTask(db *gorm.DB, in TaskInput) (*models.Task, error) {
	if err := validateTaskInput(db, in); err != nil {
		return nil, err
	}

	task := models.Task{
		CompanyID:    in.CompanyID,
		CustomerID:   in.CustomerID,
		Title:        strings.TrimSpace(in.Title),
		TaskDate:     in.TaskDate,
		Quantity:     in.Quantity,
		UnitType:     strings.TrimSpace(in.UnitType),
		Rate:         in.Rate,
		CurrencyCode: strings.ToUpper(strings.TrimSpace(in.CurrencyCode)),
		IsBillable:   in.IsBillable,
		Status:       models.TaskStatusOpen,
		Notes:        strings.TrimSpace(in.Notes),
	}

	if err := db.Create(&task).Error; err != nil {
		return nil, err
	}
	return GetTaskByID(db, in.CompanyID, task.ID)
}

func UpdateTask(db *gorm.DB, companyID, taskID uint, in TaskInput) (*models.Task, error) {
	var updated *models.Task
	err := db.Transaction(func(tx *gorm.DB) error {
		task, err := loadTaskForUpdate(tx, companyID, taskID)
		if err != nil {
			return err
		}

		switch task.Status {
		case models.TaskStatusCancelled:
			return ErrTaskCancelledReadOnly
		case models.TaskStatusInvoiced:
			return ErrTaskInvoicedReadOnly
		case models.TaskStatusCompleted:
			if completedTaskCoreChanged(*task, in) {
				return ErrTaskCompletedReadOnly
			}
			task.Notes = strings.TrimSpace(in.Notes)
		default:
			if err := validateTaskInput(tx, in); err != nil {
				return err
			}
			task.CustomerID = in.CustomerID
			task.Title = strings.TrimSpace(in.Title)
			task.TaskDate = in.TaskDate
			task.Quantity = in.Quantity
			task.UnitType = strings.TrimSpace(in.UnitType)
			task.Rate = in.Rate
			task.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
			task.IsBillable = in.IsBillable
			task.Notes = strings.TrimSpace(in.Notes)
		}

		if err := tx.Save(task).Error; err != nil {
			return err
		}
		updated = task
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetTaskByID(db, companyID, updated.ID)
}

func CompleteTask(db *gorm.DB, companyID, taskID uint) (*models.Task, error) {
	return transitionTaskStatus(db, companyID, taskID, func(task *models.Task) error {
		if task.Status != models.TaskStatusOpen {
			return ErrTaskCompleteRequiresOpen
		}
		task.Status = models.TaskStatusCompleted
		return nil
	})
}

func CancelTask(db *gorm.DB, companyID, taskID uint) (*models.Task, error) {
	return transitionTaskStatus(db, companyID, taskID, func(task *models.Task) error {
		switch task.Status {
		case models.TaskStatusOpen, models.TaskStatusCompleted:
			task.Status = models.TaskStatusCancelled
			return nil
		default:
			return ErrTaskCancelRequiresOpenOrDone
		}
	})
}

func GetTaskByID(db *gorm.DB, companyID, taskID uint) (*models.Task, error) {
	var task models.Task
	err := db.
		Preload("Customer").
		Preload("Invoice").
		Preload("InvoiceLine").
		Where("id = ? AND company_id = ?", taskID, companyID).
		First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func ListTasks(db *gorm.DB, filter TaskListFilter) ([]models.Task, error) {
	if filter.CompanyID == 0 {
		return nil, ErrTaskNotFound
	}
	q := db.
		Preload("Customer").
		Preload("Invoice").
		Where("company_id = ?", filter.CompanyID)

	if filter.CustomerID != nil && *filter.CustomerID > 0 {
		q = q.Where("customer_id = ?", *filter.CustomerID)
	}
	if filter.Status != nil && *filter.Status != "" {
		q = q.Where("status = ?", *filter.Status)
	}
	if filter.From != nil {
		start := startOfDay(*filter.From)
		q = q.Where("task_date >= ?", start)
	}
	if filter.To != nil {
		endExclusive := startOfDay(*filter.To).AddDate(0, 0, 1)
		q = q.Where("task_date < ?", endExclusive)
	}

	var tasks []models.Task
	if err := q.Order("task_date desc, id desc").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func transitionTaskStatus(db *gorm.DB, companyID, taskID uint, fn func(task *models.Task) error) (*models.Task, error) {
	var updated *models.Task
	err := db.Transaction(func(tx *gorm.DB) error {
		task, err := loadTaskForUpdate(tx, companyID, taskID)
		if err != nil {
			return err
		}
		if err := fn(task); err != nil {
			return err
		}
		if err := tx.Save(task).Error; err != nil {
			return err
		}
		updated = task
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetTaskByID(db, companyID, updated.ID)
}

func loadTaskForUpdate(db *gorm.DB, companyID, taskID uint) (*models.Task, error) {
	var task models.Task
	err := db.Where("id = ? AND company_id = ?", taskID, companyID).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func validateTaskInput(db *gorm.DB, in TaskInput) error {
	if in.CompanyID == 0 || in.CustomerID == 0 {
		return ErrTaskCustomerRequired
	}
	if strings.TrimSpace(in.Title) == "" {
		return ErrTaskTitleRequired
	}
	if in.TaskDate.IsZero() {
		return ErrTaskDateRequired
	}
	if strings.TrimSpace(in.UnitType) == "" {
		return ErrTaskUnitTypeRequired
	}
	if !models.IsValidTaskUnitType(strings.TrimSpace(in.UnitType)) {
		return ErrTaskUnitTypeInvalid
	}
	if strings.TrimSpace(in.CurrencyCode) == "" {
		return ErrTaskCurrencyRequired
	}
	if in.Quantity.IsNegative() {
		return ErrTaskQuantityNegative
	}
	if in.Rate.IsNegative() {
		return ErrTaskRateNegative
	}

	var count int64
	if err := db.Model(&models.Customer{}).
		Where("id = ? AND company_id = ?", in.CustomerID, in.CompanyID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrTaskCustomerInvalid
	}
	return nil
}

func completedTaskCoreChanged(task models.Task, in TaskInput) bool {
	if task.CustomerID != in.CustomerID {
		return true
	}
	if strings.TrimSpace(task.Title) != strings.TrimSpace(in.Title) {
		return true
	}
	if !sameDate(task.TaskDate, in.TaskDate) {
		return true
	}
	if !task.Quantity.Equal(in.Quantity) {
		return true
	}
	if strings.TrimSpace(task.UnitType) != strings.TrimSpace(in.UnitType) {
		return true
	}
	if !task.Rate.Equal(in.Rate) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(task.CurrencyCode), strings.TrimSpace(in.CurrencyCode)) == false {
		return true
	}
	if task.IsBillable != in.IsBillable {
		return true
	}
	return false
}

func sameDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
