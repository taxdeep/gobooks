// 遵循project_guide.md
package services

// waiting_for_invoice_service.go — Phase I slice I.3 operational
// queue management for models.WaitingForInvoiceItem.
//
// Lifecycle surface (all callers are inside document transactions):
//
//	CreateWaitingForInvoiceItems    — Shipment post (I.3)
//	VoidWaitingForInvoiceItemsByShipment
//	                                — Shipment void (I.3)
//	CloseWaitingForInvoiceItem      — Invoice post + match (I.5)
//	ReopenWaitingForInvoiceItem     — Invoice void + match (I.5)
//	ListOpenWaitingForInvoiceItems  — operational dashboards
//
// All mutators are idempotent with respect to their target state:
// closing an already-closed item is a no-op (not an error), mirroring
// the flag-flip helpers elsewhere in the codebase. This makes the
// Shipment void path safe to re-run if the outer transaction retries.
//
// Scope locks:
//   - No JE is written by any function here.
//   - No inventory_movements row is touched. WFI is strictly the
//     operational queue; issue truth is owned by shipment_posting.go.
//   - No Invoice-side state is mutated from this file. I.5's Invoice
//     post calls CloseWaitingForInvoiceItem; the reverse call exists
//     only so Invoice void can reopen.

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

var (
	// ErrWaitingForInvoiceNotFound — lookup by (CompanyID, ID) missed.
	ErrWaitingForInvoiceNotFound = errors.New("waiting_for_invoice: not found")

	// ErrWaitingForInvoiceNotOpen — attempted to close an item that is
	// not currently open (already closed, or voided). Indicates a
	// matching-side logic bug rather than user input; surfaces loud.
	ErrWaitingForInvoiceNotOpen = errors.New("waiting_for_invoice: close requires status=open")

	// ErrWaitingForInvoiceNotClosed — attempted to reopen an item that
	// is not currently closed. Invoice void can only reopen items
	// that the corresponding Invoice post had closed.
	ErrWaitingForInvoiceNotClosed = errors.New("waiting_for_invoice: reopen requires status=closed")
)

// CreateWaitingForInvoiceItems inserts one open WFI row per entry in
// results (filtered to stock-item, positive-qty lines — the caller
// has already done the filtering by virtue of CreateShipmentMovements
// returning only productive lines).
//
// Caller: PostShipment under shipment_required=true, after
// CreateShipmentMovements has returned its authoritative cost figures.
// Runs inside the caller's transaction.
func CreateWaitingForInvoiceItems(tx *gorm.DB, shipment models.Shipment, results []issueTruthResult) error {
	for _, r := range results {
		unitCost := r.Result.UnitCostBase
		// If CreateShipmentMovements filtered zero-qty lines, qty is
		// positive here; but guard defensively so a future refactor
		// can't leak a zero-qty WFI row into operations.
		if !r.Line.Qty.IsPositive() {
			continue
		}

		item := models.WaitingForInvoiceItem{
			CompanyID:        shipment.CompanyID,
			ShipmentID:       shipment.ID,
			ShipmentLineID:   r.Line.ID,
			ProductServiceID: r.Line.ProductServiceID,
			WarehouseID:      shipment.WarehouseID,
			CustomerID:       shipment.CustomerID,
			SalesOrderID:     shipment.SalesOrderID,
			SalesOrderLineID: r.Line.SalesOrderLineID,
			QtyPending:       r.Line.Qty,
			UnitCostBase:     unitCost,
			ShipDate:         shipment.ShipDate,
			Status:           models.WaitingForInvoiceStatusOpen,
		}
		if err := tx.Create(&item).Error; err != nil {
			return fmt.Errorf("create waiting_for_invoice item: %w", err)
		}
	}
	return nil
}

// VoidWaitingForInvoiceItemsByShipment transitions every WFI row
// attached to a given shipment to 'voided', regardless of current
// status. Called from VoidShipment when the shipment was posted
// under shipment_required=true.
//
// Rows already closed (matched to an Invoice) are flipped to 'voided'
// too — the void of a Shipment invalidates any matching Invoice
// linkage by definition. The Invoice itself is not auto-voided here;
// that cross-document guard lands with I.5 when Invoice is the
// consumer of the linkage.
//
// No-op when no rows exist for the shipment (e.g. service-only or
// zero-qty shipments that produced no WFI rows in the first place).
func VoidWaitingForInvoiceItemsByShipment(tx *gorm.DB, companyID, shipmentID uint) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":      models.WaitingForInvoiceStatusVoided,
		"resolved_at": &now,
		"updated_at":  now,
	}
	if err := tx.Model(&models.WaitingForInvoiceItem{}).
		Where("company_id = ? AND shipment_id = ? AND status <> ?",
			companyID, shipmentID, models.WaitingForInvoiceStatusVoided).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("void waiting_for_invoice items: %w", err)
	}
	return nil
}

// CloseWaitingForInvoiceItem transitions an open WFI row to closed
// and records the invoice line that closed it. Called from I.5's
// Invoice post per invoice line with matching shipment_line_id.
//
// Refuses to close an already-closed item (would orphan the earlier
// match). Refuses to close a voided item.
func CloseWaitingForInvoiceItem(tx *gorm.DB, companyID, itemID, invoiceID, invoiceLineID uint) error {
	var item models.WaitingForInvoiceItem
	err := tx.Where("company_id = ? AND id = ?", companyID, itemID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrWaitingForInvoiceNotFound
	}
	if err != nil {
		return fmt.Errorf("load waiting_for_invoice: %w", err)
	}
	if item.Status != models.WaitingForInvoiceStatusOpen {
		return fmt.Errorf("%w: current=%s", ErrWaitingForInvoiceNotOpen, item.Status)
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"status":                   models.WaitingForInvoiceStatusClosed,
		"resolved_invoice_id":      invoiceID,
		"resolved_invoice_line_id": invoiceLineID,
		"resolved_at":              &now,
		"updated_at":               now,
	}
	if err := tx.Model(&models.WaitingForInvoiceItem{}).
		Where("id = ?", itemID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("close waiting_for_invoice: %w", err)
	}
	return nil
}

// CloseWaitingForInvoiceItemByShipmentLine finds the open WFI row for
// a given shipment_line and closes it. Convenience for I.5's Invoice
// post, which knows the shipment_line_id but not the WFI row ID.
//
// Returns ErrWaitingForInvoiceNotFound when no open row exists for
// the shipment line — this is the signal used by I.5's validation
// to reject invoice lines whose shipment_line_id is already-matched
// or nonexistent.
func CloseWaitingForInvoiceItemByShipmentLine(tx *gorm.DB, companyID, shipmentLineID, invoiceID, invoiceLineID uint) error {
	var item models.WaitingForInvoiceItem
	err := tx.Where("company_id = ? AND shipment_line_id = ? AND status = ?",
		companyID, shipmentLineID, models.WaitingForInvoiceStatusOpen).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrWaitingForInvoiceNotFound
	}
	if err != nil {
		return fmt.Errorf("load waiting_for_invoice by line: %w", err)
	}
	return CloseWaitingForInvoiceItem(tx, companyID, item.ID, invoiceID, invoiceLineID)
}

// ReopenWaitingForInvoiceItemByInvoice flips every WFI row that was
// closed by a given invoice back to 'open', clearing the resolution
// fields. Called from I.5's Invoice void path.
//
// Items voided for any other reason (e.g. their source shipment was
// voided first) are NOT reopened — a voided item stays voided.
func ReopenWaitingForInvoiceItemByInvoice(tx *gorm.DB, companyID, invoiceID uint) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":                   models.WaitingForInvoiceStatusOpen,
		"resolved_invoice_id":      nil,
		"resolved_invoice_line_id": nil,
		"resolved_at":              nil,
		"updated_at":               now,
	}
	if err := tx.Model(&models.WaitingForInvoiceItem{}).
		Where("company_id = ? AND resolved_invoice_id = ? AND status = ?",
			companyID, invoiceID, models.WaitingForInvoiceStatusClosed).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("reopen waiting_for_invoice: %w", err)
	}
	return nil
}

// ListOpenWaitingForInvoiceItems returns a company's open queue, most
// recently shipped first. Intended for operational dashboards.
func ListOpenWaitingForInvoiceItems(db *gorm.DB, companyID uint, limit, offset int) ([]models.WaitingForInvoiceItem, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("services.ListOpenWaitingForInvoiceItems: CompanyID required")
	}
	q := db.Model(&models.WaitingForInvoiceItem{}).
		Where("company_id = ? AND status = ?", companyID, models.WaitingForInvoiceStatusOpen).
		Order("ship_date DESC, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	var rows []models.WaitingForInvoiceItem
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list waiting_for_invoice: %w", err)
	}
	return rows, nil
}
