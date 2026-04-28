package services

import (
	"errors"
	"time"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

type taskInvoiceReleaseMode int

const (
	taskInvoiceReleaseKeepReferences taskInvoiceReleaseMode = iota
	taskInvoiceReleaseClearReferences
)

var ErrTaskGeneratedDraftReadOnly = errors.New("this draft was generated from Billable Work. Delete the draft and regenerate from Billable Work to change the included work")

// HasActiveTaskInvoiceSources reports whether the invoice currently owns any
// active task_invoice_sources rows. Historical rows (voided_at != NULL) are
// intentionally ignored so deleted/voided drafts can be regenerated later.
func HasActiveTaskInvoiceSources(db *gorm.DB, companyID, invoiceID uint) (bool, error) {
	if companyID == 0 || invoiceID == 0 {
		return false, nil
	}

	var count int64
	if err := db.Model(&models.TaskInvoiceSource{}).
		Where("company_id = ? AND invoice_id = ? AND voided_at IS NULL", companyID, invoiceID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// releaseTaskInvoiceSourcesForInvoice marks active task_invoice_sources rows as
// historical and restores the current-linkage cache on their source rows.
//
// keepReferences:
//   - used for invoice void, where the historical invoice still exists and the
//     bridge row may keep invoice_id / invoice_line_id for audit lookup.
//
// clearReferences:
//   - used for draft delete, where the draft header and lines are about to be
//     permanently deleted. The bridge history remains, but the deleted invoice
//     references are cleared first.
func releaseTaskInvoiceSourcesForInvoice(tx *gorm.DB, companyID, invoiceID uint, mode taskInvoiceReleaseMode) error {
	var bridges []models.TaskInvoiceSource
	if err := tx.
		Where("company_id = ? AND invoice_id = ? AND voided_at IS NULL", companyID, invoiceID).
		Find(&bridges).Error; err != nil {
		return err
	}
	if len(bridges) == 0 {
		return nil
	}

	now := time.Now().UTC()
	taskIDs := make([]uint, 0)
	expenseIDs := make([]uint, 0)
	billLineIDs := make([]uint, 0)
	bridgeIDs := make([]uint, 0, len(bridges))

	for _, bridge := range bridges {
		bridgeIDs = append(bridgeIDs, bridge.ID)
		switch bridge.SourceType {
		case models.TaskInvoiceSourceTask:
			taskIDs = append(taskIDs, bridge.SourceID)
		case models.TaskInvoiceSourceExpense:
			expenseIDs = append(expenseIDs, bridge.SourceID)
		case models.TaskInvoiceSourceBillLine:
			billLineIDs = append(billLineIDs, bridge.SourceID)
		}
	}

	bridgeUpdates := map[string]any{"voided_at": now}
	if mode == taskInvoiceReleaseClearReferences {
		bridgeUpdates["invoice_id"] = nil
		bridgeUpdates["invoice_line_id"] = nil
	}
	if err := tx.Model(&models.TaskInvoiceSource{}).
		Where("id IN ?", bridgeIDs).
		Updates(bridgeUpdates).Error; err != nil {
		return err
	}

	if len(taskIDs) > 0 {
		if err := tx.Model(&models.Task{}).
			Where("company_id = ? AND id IN ?", companyID, dedupeUintIDs(taskIDs)).
			Updates(map[string]any{
				"status":          string(models.TaskStatusCompleted),
				"invoice_id":      nil,
				"invoice_line_id": nil,
			}).Error; err != nil {
			return err
		}
	}
	if len(expenseIDs) > 0 {
		if err := tx.Model(&models.Expense{}).
			Where("company_id = ? AND id IN ?", companyID, dedupeUintIDs(expenseIDs)).
			Updates(map[string]any{
				"reinvoice_status": string(models.ReinvoiceStatusUninvoiced),
				"invoice_id":       nil,
				"invoice_line_id":  nil,
			}).Error; err != nil {
			return err
		}
	}
	if len(billLineIDs) > 0 {
		if err := tx.Model(&models.BillLine{}).
			Where("company_id = ? AND id IN ?", companyID, dedupeUintIDs(billLineIDs)).
			Updates(map[string]any{
				"reinvoice_status": string(models.ReinvoiceStatusUninvoiced),
				"invoice_id":       nil,
				"invoice_line_id":  nil,
			}).Error; err != nil {
			return err
		}
	}

	return nil
}
