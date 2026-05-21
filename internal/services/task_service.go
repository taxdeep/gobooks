// 遵循project_guide.md
package services

import (
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
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
	ErrTaskLineRequired             = errors.New("at least one task service line is required")
	ErrTaskNotFound                 = errors.New("task not found")
	ErrTaskCustomerInvalid          = errors.New("customer is not valid for this company")
	ErrTaskServiceItemInvalid       = errors.New("service item must be an active service-type item for this company")
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
	// ProductServiceID optionally links the task to a service item from the
	// Products & Services catalogue.  nil = use TASK_LABOR default when billing.
	ProductServiceID *uint
	Lines            []TaskLineInput
}

type TaskLineInput struct {
	ProductServiceID *uint
	Description      string
	Quantity         decimal.Decimal
	Rate             decimal.Decimal
	IsBillable       bool
}

type TaskListFilter struct {
	CompanyID  uint
	CustomerID *uint
	Status     *models.TaskStatus
	From       *time.Time
	To         *time.Time
}

func CreateTask(db *gorm.DB, in TaskInput) (*models.Task, error) {
	in.UnitType = normalizeTaskUnitType(in.UnitType)
	in.Lines = normalizeTaskLineInputs(in)
	if err := validateTaskInput(db, in); err != nil {
		return nil, err
	}

	var task models.Task
	err := db.Transaction(func(tx *gorm.DB) error {
		task = models.Task{
			CompanyID:    in.CompanyID,
			CustomerID:   in.CustomerID,
			Title:        strings.TrimSpace(in.Title),
			TaskDate:     in.TaskDate,
			UnitType:     strings.TrimSpace(in.UnitType),
			CurrencyCode: strings.ToUpper(strings.TrimSpace(in.CurrencyCode)),
			IsBillable:   in.IsBillable,
			Status:       models.TaskStatusOpen,
			Notes:        strings.TrimSpace(in.Notes),
		}
		syncTaskLegacyFromLines(&task, in.Lines)
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		return replaceTaskLines(tx, &task, in.Lines)
	})
	if err != nil {
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
		if strings.TrimSpace(in.UnitType) == "" {
			in.UnitType = normalizeTaskUnitType(task.UnitType)
		} else {
			in.UnitType = normalizeTaskUnitType(in.UnitType)
		}
		in.Lines = normalizeTaskLineInputs(in)

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
			task.UnitType = normalizeTaskUnitType(in.UnitType)
			task.Rate = in.Rate
			task.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
			task.IsBillable = in.IsBillable
			task.Notes = strings.TrimSpace(in.Notes)
			syncTaskLegacyFromLines(task, in.Lines)
		}

		if err := tx.Save(task).Error; err != nil {
			return err
		}
		if task.Status == models.TaskStatusOpen {
			if err := replaceTaskLines(tx, task, in.Lines); err != nil {
				return err
			}
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
		Preload("ProductService").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc, id asc") }).
		Preload("Lines.ProductService").
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
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc, id asc") }).
		Preload("Lines.ProductService").
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
	err := applyLockForUpdate(
		db.Where("id = ? AND company_id = ?", taskID, companyID),
	).First(&task).Error
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
	unitType := normalizeTaskUnitType(in.UnitType)
	if !models.IsValidTaskUnitType(unitType) {
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
	if len(in.Lines) == 0 {
		return ErrTaskLineRequired
	}
	for _, line := range in.Lines {
		if line.Quantity.IsNegative() {
			return ErrTaskQuantityNegative
		}
		if line.Rate.IsNegative() {
			return ErrTaskRateNegative
		}
		if line.ProductServiceID != nil {
			if err := validateTaskServiceItem(db, in.CompanyID, *line.ProductServiceID); err != nil {
				return err
			}
		}
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

	if in.ProductServiceID != nil {
		if err := validateTaskServiceItem(db, in.CompanyID, *in.ProductServiceID); err != nil {
			return err
		}
	}
	return nil
}

func validateTaskServiceItem(db *gorm.DB, companyID, itemID uint) error {
	var item models.ProductService
	err := db.
		Where("id = ? AND company_id = ? AND type = ?",
			itemID, companyID, models.ProductServiceTypeService).
		Where("is_active = true").
		First(&item).Error
	if err != nil {
		return ErrTaskServiceItemInvalid
	}
	return nil
}

func normalizeTaskUnitType(unitType string) string {
	unitType = strings.TrimSpace(unitType)
	if unitType == "" {
		return models.TaskUnitTypeHour
	}
	return unitType
}

func normalizeTaskLineInputs(in TaskInput) []TaskLineInput {
	lines := make([]TaskLineInput, 0, len(in.Lines))
	for _, line := range in.Lines {
		if line.Quantity.IsZero() && line.Rate.IsZero() && line.ProductServiceID == nil && strings.TrimSpace(line.Description) == "" {
			continue
		}
		desc := strings.TrimSpace(line.Description)
		if desc == "" {
			desc = strings.TrimSpace(in.Title)
		}
		lines = append(lines, TaskLineInput{
			ProductServiceID: line.ProductServiceID,
			Description:      desc,
			Quantity:         line.Quantity,
			Rate:             line.Rate,
			IsBillable:       in.IsBillable,
		})
	}
	if len(lines) == 0 {
		lines = append(lines, TaskLineInput{
			ProductServiceID: in.ProductServiceID,
			Description:      strings.TrimSpace(in.Title),
			Quantity:         in.Quantity,
			Rate:             in.Rate,
			IsBillable:       in.IsBillable,
		})
	}
	return lines
}

func syncTaskLegacyFromLines(task *models.Task, lines []TaskLineInput) {
	if len(lines) == 0 {
		return
	}
	task.ProductServiceID = lines[0].ProductServiceID
	task.Quantity = lines[0].Quantity
	task.Rate = lines[0].Rate
}

func replaceTaskLines(tx *gorm.DB, task *models.Task, lines []TaskLineInput) error {
	if err := tx.Where("company_id = ? AND task_id = ?", task.CompanyID, task.ID).Delete(&models.TaskLine{}).Error; err != nil {
		return err
	}
	for i, in := range lines {
		snap := SnapshotLineUOM(tx, task.CompanyID, in.ProductServiceID, LineUOMSell, in.Quantity, "", decimal.Zero)
		line := models.TaskLine{
			CompanyID:        task.CompanyID,
			TaskID:           task.ID,
			ProductServiceID: in.ProductServiceID,
			Description:      strings.TrimSpace(in.Description),
			Quantity:         in.Quantity,
			Rate:             in.Rate,
			LineUOM:          snap.LineUOM,
			LineUOMFactor:    snap.LineUOMFactor,
			QtyInStockUOM:    snap.QtyInStockUOM,
			SortOrder:        uint(i + 1),
			IsBillable:       in.IsBillable,
		}
		if err := tx.Create(&line).Error; err != nil {
			return err
		}
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
	// ProductServiceID is part of the billing configuration; treat as core.
	switch {
	case task.ProductServiceID == nil && in.ProductServiceID != nil:
		return true
	case task.ProductServiceID != nil && in.ProductServiceID == nil:
		return true
	case task.ProductServiceID != nil && in.ProductServiceID != nil && *task.ProductServiceID != *in.ProductServiceID:
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
