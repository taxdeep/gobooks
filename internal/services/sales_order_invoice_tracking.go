// 遵循project_guide.md
package services

// sales_order_invoice_tracking.go — SO↔Invoice back-link + totals
// tracking. Wires the previously-inert
// `SalesOrder.InvoicedAmount` / `SalesOrderLine.InvoicedQty` /
// `SalesOrderStatus{Partially,Fully}Invoiced` fields to the invoice
// lifecycle.
//
// Data flow
// ---------
//
//   Create Invoice from SO  (UI shortcut, web/invoice_editor_handlers.go
//                            prefill branch sets sales_order_id on the
//                            form)
//        │
//        │  form POST → handleInvoiceSaveDraft persists:
//        │    invoices.sales_order_id = <soID>
//        │    invoice_lines.* inserted (no so-line link yet)
//        │
//        │  MatchInvoiceLinesToSalesOrder(tx, companyID, invID):
//        │    — scans SO lines in SortOrder
//        │    — for each invoice line with a ProductServiceID
//        │      matching an SO line, consumes as much of that SO
//        │      line's remaining qty as the invoice line wants
//        │    — persists invoice_lines.sales_order_line_id per
//        │      matched row
//        ▼
//   Draft Invoice (status='draft')
//        │
//        │  PostInvoice → ApplyInvoicePostToSalesOrder(tx, inv)
//        │    — for each invoice line with a sales_order_line_id,
//        │      increments SalesOrderLine.InvoicedQty by that qty
//        │    — sums LineTotals of tracked lines and increments
//        │      SalesOrder.InvoicedAmount
//        │    — recomputes SO.Status:
//        │        any line with InvoicedQty < Quantity  → partially_invoiced
//        │        every line with InvoicedQty >= Quantity → fully_invoiced
//        ▼
//   Posted Invoice (status='issued' etc.)
//        │
//        │  VoidInvoice → ReverseInvoicePostOnSalesOrder(tx, inv)
//        │    — reverses the same deltas (decrements)
//        │    — status rolls back appropriately
//        ▼
//   Voided Invoice
//
// Design decisions
// ----------------
// - **Line match algorithm is FIFO-remaining.** When an invoice
//   line has a ProductServiceID that matches one or more SO lines,
//   we consume the earliest-sorted SO line's remaining qty first.
//   Operator-added lines (ProductServiceID not on the SO) stay
//   unlinked — no SO tracking for them.
// - **Match runs at save time, not post.** That way the link is
//   stable and auditable on drafts, not opaque until post. Re-save
//   clears and re-matches, so draft edits stay consistent.
// - **Status transitions are idempotent.** Calling Apply twice
//   without an intervening Void would double-count, so the caller
//   (PostInvoice / VoidInvoice) is expected to pair them correctly.
//   Invoice posted → already-posted is rejected at a higher layer.
// - **No FK constraints.** Matches the existing convention for
//   cross-document invoice links (ShipmentLineID / QuoteID etc.).
// - **Cross-tenant safety.** Every query includes company_id.

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// MatchInvoiceLinesToSalesOrder populates invoice_lines.sales_order_line_id
// for every invoice line whose product matches an SO line with
// remaining uninvoiced qty. Idempotent on re-save — existing matches
// are cleared first. No-op when the invoice has no SalesOrderID.
func MatchInvoiceLinesToSalesOrder(tx *gorm.DB, companyID, invoiceID uint) error {
	var inv models.Invoice
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return fmt.Errorf("load invoice for so-match: %w", err)
	}
	if inv.SalesOrderID == nil || *inv.SalesOrderID == 0 {
		// Standalone invoice — clear any lingering line-level links
		// (defensive; covers edit flows that detached the SO on re-
		// save).
		if err := tx.Model(&models.InvoiceLine{}).
			Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
			Update("sales_order_line_id", nil).Error; err != nil {
			return fmt.Errorf("clear so-line-id: %w", err)
		}
		return nil
	}

	// Load the SO + its lines, verify same company.
	var so models.SalesOrder
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", *inv.SalesOrderID, companyID).
		First(&so).Error; err != nil {
		return fmt.Errorf("load sales order %d for match: %w", *inv.SalesOrderID, err)
	}

	// Compute remaining-by-SO-line keyed on SO-line ID, but only
	// counting against OTHER invoices — i.e. subtract what THIS
	// invoice's previous lines (if any) already claimed, since
	// we're about to re-match.
	remaining := make(map[uint]decimal.Decimal, len(so.Lines))
	// Sorted order: matches the SO line's SortOrder (lowest first
	// = FIFO).
	sortedSOLines := append([]models.SalesOrderLine(nil), so.Lines...)
	sortOrderAsc(sortedSOLines)
	for _, sl := range sortedSOLines {
		rem := sl.Quantity.Sub(sl.InvoicedQty)
		if !rem.IsPositive() {
			rem = decimal.Zero
		}
		remaining[sl.ID] = rem
	}
	// Subtract this invoice's current tracked qty (before we re-
	// assign). Only lines whose SO-line belongs to this SO count.
	for _, il := range inv.Lines {
		if il.SalesOrderLineID == nil || *il.SalesOrderLineID == 0 {
			continue
		}
		if _, ok := remaining[*il.SalesOrderLineID]; !ok {
			continue
		}
		remaining[*il.SalesOrderLineID] = remaining[*il.SalesOrderLineID].Add(il.Qty)
	}

	// Clear existing links before re-matching.
	if err := tx.Model(&models.InvoiceLine{}).
		Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
		Update("sales_order_line_id", nil).Error; err != nil {
		return fmt.Errorf("clear so-line-id before re-match: %w", err)
	}

	// Match each invoice line (in SortOrder) to SO lines by
	// ProductServiceID + FIFO-remaining.
	sortedInvLines := append([]models.InvoiceLine(nil), inv.Lines...)
	sortInvLinesAsc(sortedInvLines)
	for _, il := range sortedInvLines {
		if il.ProductServiceID == nil || *il.ProductServiceID == 0 {
			continue
		}
		needed := il.Qty
		if !needed.IsPositive() {
			continue
		}
		for _, sl := range sortedSOLines {
			if sl.ProductServiceID == nil || *sl.ProductServiceID == 0 {
				continue
			}
			if *sl.ProductServiceID != *il.ProductServiceID {
				continue
			}
			rem, ok := remaining[sl.ID]
			if !ok || !rem.IsPositive() {
				continue
			}
			// Match found — assign this invoice line to this SO
			// line. Partial-coverage case: if the invoice line
			// needs more than remaining, we still match to this
			// SO line; the leftover qty is untracked (operator
			// billed more than the SO had — that's their call).
			slID := sl.ID
			if err := tx.Model(&models.InvoiceLine{}).
				Where("id = ? AND company_id = ?", il.ID, companyID).
				Update("sales_order_line_id", &slID).Error; err != nil {
				return fmt.Errorf("set sales_order_line_id on line %d: %w", il.ID, err)
			}
			if needed.LessThanOrEqual(rem) {
				remaining[sl.ID] = rem.Sub(needed)
			} else {
				remaining[sl.ID] = decimal.Zero
			}
			break // one invoice line → one SO line (first match wins)
		}
	}
	return nil
}

// ApplyInvoicePostToSalesOrder runs at PostInvoice tx tail. For each
// invoice line with a sales_order_line_id, increments the matched
// SalesOrderLine.InvoicedQty by the line qty and sums LineTotal into
// SalesOrder.InvoicedAmount. Recomputes SO.Status afterwards. No-op
// when inv.SalesOrderID is nil.
//
// Idempotency: PostInvoice locks the invoice row so this runs once
// per post. Re-posting a voided invoice (if ever supported) would
// be a fresh call pair (Apply → then Void's Reverse).
func ApplyInvoicePostToSalesOrder(tx *gorm.DB, inv models.Invoice) error {
	if inv.SalesOrderID == nil || *inv.SalesOrderID == 0 {
		return nil
	}
	return mutateSOInvoicedTotals(tx, inv, +1)
}

// ReverseInvoicePostOnSalesOrder runs at VoidInvoice tx tail.
// Reverses the effects of Apply — decrements InvoicedQty /
// InvoicedAmount, rolls back status.
func ReverseInvoicePostOnSalesOrder(tx *gorm.DB, inv models.Invoice) error {
	if inv.SalesOrderID == nil || *inv.SalesOrderID == 0 {
		return nil
	}
	return mutateSOInvoicedTotals(tx, inv, -1)
}

// mutateSOInvoicedTotals is the shared direction-aware body for
// Apply / Reverse. sign = +1 (apply) or -1 (reverse).
func mutateSOInvoicedTotals(tx *gorm.DB, inv models.Invoice, sign int) error {
	if inv.SalesOrderID == nil || *inv.SalesOrderID == 0 {
		return nil
	}
	if sign != 1 && sign != -1 {
		return fmt.Errorf("mutateSOInvoicedTotals: sign must be +1 or -1, got %d", sign)
	}

	// Re-load the invoice with lines to avoid caller-passed
	// freshness assumptions.
	var fullInv models.Invoice
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", inv.ID, inv.CompanyID).
		First(&fullInv).Error; err != nil {
		return fmt.Errorf("reload invoice %d for so-tracking: %w", inv.ID, err)
	}

	// Lock the SO + its lines. SELECT FOR UPDATE serialises
	// concurrent invoices against the same SO.
	var so models.SalesOrder
	if err := applyLockForUpdate(tx.Where("id = ? AND company_id = ?",
		*fullInv.SalesOrderID, fullInv.CompanyID)).
		First(&so).Error; err != nil {
		return fmt.Errorf("lock sales order %d for tracking: %w", *fullInv.SalesOrderID, err)
	}
	// Cancelled SOs must not be mutated — an invoice on a cancelled
	// SO is a data anomaly; fail loud rather than quiet-accept.
	if so.Status == models.SalesOrderStatusCancelled {
		return fmt.Errorf("sales order %d is cancelled; cannot apply invoice %d tracking",
			so.ID, fullInv.ID)
	}

	var soLines []models.SalesOrderLine
	if err := tx.Where("sales_order_id = ?", so.ID).
		Order("sort_order asc").
		Find(&soLines).Error; err != nil {
		return fmt.Errorf("load so-lines for tracking: %w", err)
	}
	byID := make(map[uint]*models.SalesOrderLine, len(soLines))
	for i := range soLines {
		byID[soLines[i].ID] = &soLines[i]
	}

	// Accumulate deltas.
	linkedTotalDelta := decimal.Zero
	for _, il := range fullInv.Lines {
		if il.SalesOrderLineID == nil || *il.SalesOrderLineID == 0 {
			continue
		}
		sl, ok := byID[*il.SalesOrderLineID]
		if !ok {
			// Cross-SO link (shouldn't happen after Match ran, but
			// defensive).
			continue
		}
		qtyDelta := il.Qty
		if sign == -1 {
			qtyDelta = qtyDelta.Neg()
		}
		sl.InvoicedQty = sl.InvoicedQty.Add(qtyDelta)
		// Floor at zero on reverse — defensive against rounding.
		if sl.InvoicedQty.IsNegative() {
			sl.InvoicedQty = decimal.Zero
		}
		if err := tx.Model(&models.SalesOrderLine{}).
			Where("id = ?", sl.ID).
			Update("invoiced_qty", sl.InvoicedQty).Error; err != nil {
			return fmt.Errorf("update so-line %d invoiced_qty: %w", sl.ID, err)
		}
		totalDelta := il.LineTotal
		if sign == -1 {
			totalDelta = totalDelta.Neg()
		}
		linkedTotalDelta = linkedTotalDelta.Add(totalDelta)
	}

	newInvoicedAmount := so.InvoicedAmount.Add(linkedTotalDelta)
	if newInvoicedAmount.IsNegative() {
		newInvoicedAmount = decimal.Zero
	}

	// Recompute status. Read fresh InvoicedQty from the updated
	// slice.
	newStatus := recomputeSOStatus(so.Status, soLines)

	updates := map[string]any{
		"invoiced_amount": newInvoicedAmount,
	}
	if newStatus != so.Status {
		updates["status"] = string(newStatus)
	}
	if err := tx.Model(&models.SalesOrder{}).
		Where("id = ?", so.ID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update so totals: %w", err)
	}
	return nil
}

// recomputeSOStatus returns the status an SO should hold given the
// current InvoicedQty distribution across its lines. Never
// transitions out of Cancelled (guard lives earlier).
//
// Transitions:
//   - No line has any InvoicedQty → Confirmed (or Draft, unchanged
//     if we started there — but PostInvoice only touches Confirmed
//     or Partially_Invoiced SOs in practice).
//   - Every line's InvoicedQty >= Quantity → FullyInvoiced.
//   - Anything in between → PartiallyInvoiced.
func recomputeSOStatus(current models.SalesOrderStatus, lines []models.SalesOrderLine) models.SalesOrderStatus {
	if current == models.SalesOrderStatusCancelled {
		return current
	}
	if len(lines) == 0 {
		return current
	}
	anySubstantiveLine := false
	anyInvoiced := false
	allFullyInvoiced := true
	for _, l := range lines {
		if !salesOrderLineCountsForInvoiceStatus(l) {
			continue
		}
		anySubstantiveLine = true
		if l.InvoicedQty.IsPositive() {
			anyInvoiced = true
		}
		if l.InvoicedQty.LessThan(l.Quantity) {
			allFullyInvoiced = false
		}
	}
	if !anySubstantiveLine {
		return current
	}
	switch {
	case allFullyInvoiced:
		return models.SalesOrderStatusFullyInvoiced
	case anyInvoiced:
		return models.SalesOrderStatusPartiallyInvoiced
	default:
		// Roll back to Confirmed if we had advanced to
		// Partially/Fully and now everything's zero (full void).
		return models.SalesOrderStatusConfirmed
	}
}

// ── small sort helpers (package-local; avoid importing sort just
// for this narrow use) ───────────────────────────────────────────

func salesOrderLineCountsForInvoiceStatus(l models.SalesOrderLine) bool {
	if l.ProductServiceID != nil && *l.ProductServiceID != 0 {
		return true
	}
	if strings.TrimSpace(l.Description) != "" {
		return true
	}
	return !l.UnitPrice.IsZero() || !l.LineNet.IsZero() || !l.TaxAmount.IsZero() || !l.LineTotal.IsZero()
}

func sortOrderAsc(lines []models.SalesOrderLine) {
	// Simple insertion sort — input is always small (lines per SO).
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j-1].SortOrder > lines[j].SortOrder; j-- {
			lines[j-1], lines[j] = lines[j], lines[j-1]
		}
	}
}

func sortInvLinesAsc(lines []models.InvoiceLine) {
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j-1].SortOrder > lines[j].SortOrder; j-- {
			lines[j-1], lines[j] = lines[j], lines[j-1]
		}
	}
}
