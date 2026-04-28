package services

import (
	"errors"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

var (
	ErrTaskLinkageTaskNotFound      = errors.New("task is not valid for this company")
	ErrTaskLinkageTaskStatusInvalid = errors.New("only open or completed tasks can be linked")
	ErrTaskBillableCustomerMismatch = errors.New("billable customer must match the task customer")
)

// TaskCostLinkageInput carries the shared task-linkage fields used by both
// standalone expenses and bill lines.
//
// BillableCustomerID is optional at the API boundary because UI flows may hide
// it and let the service auto-derive it from the selected task. When it is
// provided, it must still match task.customer_id.
type TaskCostLinkageInput struct {
	CompanyID          uint
	TaskID             *uint
	BillableCustomerID *uint
	IsBillable         bool
}

// TaskCostLinkageResult is the normalized linkage truth persisted on the
// record after service-layer validation.
type TaskCostLinkageResult struct {
	TaskID             *uint
	BillableCustomerID *uint
	IsBillable         bool
	ReinvoiceStatus    models.ReinvoiceStatus
	Task               *models.Task
}

// ListSelectableTasks returns the tasks that may be linked from costs in the
// current batch: company-scoped and currently open or completed.
func ListSelectableTasks(db *gorm.DB, companyID uint) ([]models.Task, error) {
	var tasks []models.Task
	if err := db.
		Preload("Customer").
		Where("company_id = ? AND status IN ?", companyID, []models.TaskStatus{
			models.TaskStatusOpen,
			models.TaskStatusCompleted,
		}).
		Order("task_date desc, id desc").
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// NormalizeTaskCostLinkage enforces the shared Task linkage rules for
// task-linked costs:
//   - task must exist in the current company and be open/completed
//   - billable_customer_id, when present, must match task.customer_id
//   - task-linked costs inherit billable_customer_id from the task
//   - reinvoice_status is only meaningful for task-linked + billable rows
func NormalizeTaskCostLinkage(db *gorm.DB, in TaskCostLinkageInput) (TaskCostLinkageResult, error) {
	out := TaskCostLinkageResult{
		IsBillable:      in.IsBillable,
		ReinvoiceStatus: models.ReinvoiceStatusNone,
	}
	if in.TaskID == nil || *in.TaskID == 0 {
		return out, nil
	}

	var task models.Task
	err := db.
		Preload("Customer").
		Where("id = ? AND company_id = ?", *in.TaskID, in.CompanyID).
		First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, ErrTaskLinkageTaskNotFound
	}
	if err != nil {
		return out, err
	}

	switch task.Status {
	case models.TaskStatusOpen, models.TaskStatusCompleted:
	default:
		return out, ErrTaskLinkageTaskStatusInvalid
	}

	if in.BillableCustomerID != nil && *in.BillableCustomerID != 0 && *in.BillableCustomerID != task.CustomerID {
		return out, ErrTaskBillableCustomerMismatch
	}
	taskID := task.ID
	customerID := task.CustomerID
	out.Task = &task
	out.TaskID = &taskID
	out.BillableCustomerID = &customerID
	if in.IsBillable {
		out.ReinvoiceStatus = models.ReinvoiceStatusUninvoiced
	}
	return out, nil
}
