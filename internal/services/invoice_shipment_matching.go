// 遵循project_guide.md
package services

// invoice_shipment_matching.go — Phase I slice I.5 Invoice↔Shipment
// matching: closes waiting_for_invoice queue items when a posted
// Invoice's line carries shipment_line_id.
//
// Role in the pipeline
// --------------------
// At Invoice post under shipment_required=true, every invoice line
// with a non-nil ShipmentLineID resolves — inside the posting
// transaction — to exactly one open waiting_for_invoice row, which
// transitions to 'closed' with the invoice line recorded as the
// resolver. Non-matching linkages fail loud and roll back the whole
// post.
//
// At Invoice void, the symmetric helper reopens every
// waiting_for_invoice row that this invoice had closed. Rows that
// were already voided by an intervening Shipment void are left
// voided — a voided source invalidates downstream match history.
//
// Scope locks
// -----------
//   - I.5 is 1:1 atomic: one ShipmentLine → one invoice line → one
//     WFI row. Partial invoicing of a shipment line is not supported.
//   - ShipmentLineID is meaningful ONLY under shipment_required=true.
//     Under flag=false, lines carrying the column are rejected so the
//     field cannot drift into the legacy Invoice-forms-COGS flow.
//   - No FK at the schema layer (migration 078 leaves it nullable and
//     index-only). Cross-tenant / posted-status guards are enforced
//     here at the service boundary.

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

var (
	// ErrInvoiceShipmentLineInFlagOffContext — invoice line carries
	// a shipment_line_id under companies.shipment_required=false. The
	// linkage is meaningless there (legacy COGS path runs on its own
	// terms) so we reject to prevent silent drift of the column into
	// the legacy flow.
	ErrInvoiceShipmentLineInFlagOffContext = errors.New("invoice line: shipment_line_id set under shipment_required=false — not allowed; flip the rail or clear the field")

	// ErrInvoiceShipmentLineCrossCompany — invoice line's
	// shipment_line_id resolves to a ShipmentLine in a different
	// company. Rejected before the WFI close attempt so the error
	// message is unambiguous.
	ErrInvoiceShipmentLineCrossCompany = errors.New("invoice line: shipment_line_id belongs to a different company")

	// ErrInvoiceShipmentLineNotFound — invoice line's
	// shipment_line_id does not resolve to any ShipmentLine (row
	// missing entirely, not tenant-mismatched).
	ErrInvoiceShipmentLineNotFound = errors.New("invoice line: shipment_line_id does not resolve to any ShipmentLine")

	// ErrInvoiceShipmentNotPosted — invoice line's shipment is not
	// in status=posted. Matching against a draft, voided, or
	// otherwise non-posted Shipment is rejected because the WFI
	// row exists only for posted shipments under flag=true.
	ErrInvoiceShipmentNotPosted = errors.New("invoice line: parent shipment is not in posted status")
)

// closeWaitingForInvoiceMatches walks inv.Lines and closes the
// matching WFI row for every line with a non-nil ShipmentLineID.
// Called from PostInvoice inside the posting transaction, after the
// JE + ledger have been projected.
//
// Invariant: shipment_line_id is usable ONLY under
// shipment_required=true. Under flag=false we refuse the column
// loudly, matching the Phase I.B scope (the linkage exists because
// Shipment-first is active, not because the invoice happens to
// reference a historical shipment).
func closeWaitingForInvoiceMatches(tx *gorm.DB, companyID uint, inv models.Invoice, shipmentRequired bool) error {
	for i, line := range inv.Lines {
		if line.ShipmentLineID == nil || *line.ShipmentLineID == 0 {
			continue
		}
		if !shipmentRequired {
			return fmt.Errorf("%w: line[%d] id=%d", ErrInvoiceShipmentLineInFlagOffContext, i, line.ID)
		}

		// Cross-company + posted-status guard on the ShipmentLine.
		var shipLine models.ShipmentLine
		if err := tx.Where("id = ?", *line.ShipmentLineID).
			First(&shipLine).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: line[%d] shipment_line_id=%d",
					ErrInvoiceShipmentLineNotFound, i, *line.ShipmentLineID)
			}
			return fmt.Errorf("load shipment line %d: %w", *line.ShipmentLineID, err)
		}
		if shipLine.CompanyID != companyID {
			return fmt.Errorf("%w: line[%d] shipment_line_id=%d belongs to company=%d, invoice company=%d",
				ErrInvoiceShipmentLineCrossCompany, i, *line.ShipmentLineID,
				shipLine.CompanyID, companyID)
		}
		var ship models.Shipment
		if err := tx.Where("id = ? AND company_id = ?", shipLine.ShipmentID, companyID).
			First(&ship).Error; err != nil {
			return fmt.Errorf("load shipment %d: %w", shipLine.ShipmentID, err)
		}
		if ship.Status != models.ShipmentStatusPosted {
			return fmt.Errorf("%w: line[%d] shipment_line_id=%d parent shipment status=%s",
				ErrInvoiceShipmentNotPosted, i, *line.ShipmentLineID, ship.Status)
		}

		// Close the WFI row. Returns ErrWaitingForInvoiceNotFound if
		// there is no open row for this shipment line — which happens
		// when the line was already matched by another invoice or
		// when the shipment was voided between its post and this
		// invoice's post.
		if err := CloseWaitingForInvoiceItemByShipmentLine(tx, companyID,
			*line.ShipmentLineID, inv.ID, line.ID); err != nil {
			return fmt.Errorf("close WFI for invoice line[%d] id=%d: %w", i, line.ID, err)
		}
	}
	return nil
}

// reopenWaitingForInvoiceMatchesOnVoid is the VoidInvoice
// counterpart. Flips every WFI row that this invoice had closed
// back to 'open', clearing the resolution identity fields. Rows
// in status='voided' (because their source Shipment was voided
// first) are left voided.
//
// No-op when the invoice has no WFI linkage (e.g. legacy
// flag=false invoice). Safe to call unconditionally from
// VoidInvoice.
func reopenWaitingForInvoiceMatchesOnVoid(tx *gorm.DB, companyID, invoiceID uint) error {
	return ReopenWaitingForInvoiceItemByInvoice(tx, companyID, invoiceID)
}
