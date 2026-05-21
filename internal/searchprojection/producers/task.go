// éµå¾ªproject_guide.md
package producers

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"

	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/searchprojection"
)

const EntityTypeTask = "task"

// ProjectTask refreshes the search_documents row for one task. It is
// company-scoped like the accounting producers so task search cannot leak
// across tenants even when called from a shared handler path.
func ProjectTask(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, taskID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectTask: companyID is required")
	}
	var task models.Task
	err := db.Where("id = ? AND company_id = ?", taskID, companyID).
		Preload("Customer").
		Preload("ProductService").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc, id asc") }).
		Preload("Lines.ProductService").
		First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectTask: load %d for company %d: %w", taskID, companyID, err)
	}
	doc := TaskDocument(task)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectTask upsert failed",
			"task_id", taskID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteTaskProjection(ctx context.Context, p searchprojection.Projector, companyID, taskID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeTask, taskID)
}

func TaskDocument(task models.Task) searchprojection.Document {
	docDate := task.TaskDate
	customerName := ""
	if task.Customer.ID != 0 {
		customerName = task.Customer.Name
	}
	title := counterpartyTitle(customerName, "Customer", task.Title)

	subtitle := "Task"
	if !task.TaskDate.IsZero() {
		subtitle += " - " + task.TaskDate.Format("2006-01-02")
	}
	if task.IsBillable {
		subtitle += " - billable"
	}
	if task.ProductService != nil && task.ProductService.Name != "" {
		subtitle += " - " + task.ProductService.Name
	}
	memo := task.Title + " " + task.Notes
	for _, line := range task.Lines {
		memo += " " + line.Description
		if line.ProductService != nil {
			memo += " " + line.ProductService.Name
		}
	}

	return searchprojection.Document{
		CompanyID:  task.CompanyID,
		EntityType: EntityTypeTask,
		EntityID:   task.ID,
		DocNumber:  "TASK-" + strconv.FormatUint(uint64(task.ID), 10),
		Title:      title,
		Subtitle:   subtitle,
		Memo:       memo,
		DocDate:    &docDate,
		Amount:     task.BillableAmount().StringFixed(2),
		Currency:   task.CurrencyCode,
		Status:     string(task.Status),
		URLPath:    "/tasks/" + strconv.FormatUint(uint64(task.ID), 10),
	}
}
