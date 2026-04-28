// 遵循project_guide.md
package services

// company_shipment_required.go — Phase I slice I.1 audited admin
// surface for the companies.shipment_required capability rail
// (sell-side mirror of company_receipt_required.go from H.1).
//
// Rail, not an operational switch
// -------------------------------
// I.1 installs the column (migration 075) and this flip surface so
// later slices can branch on the flag once each is verified. Nothing
// reads the flag in I.1 — no Invoice, no Shipment, no inventory
// handler. The admin surface exists so that when I.2–I.5 land
// incrementally, the consumer is already wired to an audited state
// change rather than being retrofitted.
//
// Reviewer contract: if a PR adds a reader of `shipment_required` as
// part of I.1, it is out of scope and must move to I.2 or later.
// I.1's rail is dormant by design.
//
// Operational enablement of `shipment_required=true` is blocked until
// Phase I closes under the I.B scope selection (INVENTORY_MODULE_API.md
// §Phase I capability-gate rule). Flipping a real company before I.5
// produces a half-bridged state — Shipment can form cost, but Invoice
// cannot match against shipped qty and `waiting_for_invoice` cannot
// resolve cleanly — that is strictly worse than legacy.

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ChangeCompanyShipmentRequiredInput is the input to the rail flip.
// Required=true opts the company into the Phase I (I.B) Shipment-
// first sell-side model; Required=false leaves the company on the
// legacy Invoice-forms-COGS path.
//
// I.1 does NOT add new sentinel errors. Enabling is unconditional
// beyond the company existing; disabling is likewise unconditional
// because no consumer reads the flag yet — there is nothing to
// orphan by flipping it off. Consumer-aware guards (e.g. "cannot
// disable while unmatched `waiting_for_invoice` items exist") belong
// in the slice that introduces the consumer, not in this rail.
type ChangeCompanyShipmentRequiredInput struct {
	CompanyID   uint
	Required    bool
	Actor       string
	ActorUserID *uuid.UUID
}

// ChangeCompanyShipmentRequired flips companies.shipment_required.
//
// Semantics (mirrors H.1 ChangeCompanyReceiptRequired exactly):
//   - No-op when the company is already in the target state: neither
//     side-effect nor audit row.
//   - On effective change: the column is persisted and exactly one
//     audit row is written, action =
//     "company.shipment_required.enabled" or
//     "company.shipment_required.disabled".
//   - Before/after state on the audit row records the flipped value.
//
// Deliberately NOT in this surface:
//   - Any validation involving the presence of Shipments, Invoices,
//     or unmatched waiting_for_invoice items. Those constraints land
//     with their respective consumers in I.3 / I.5. Wiring them here
//     would couple I.1 to later-slice data shapes that do not exist
//     yet.
//   - Any "operational safety" guard that blocks a flip on a real
//     company before I.5. Code cannot distinguish a test company
//     from a real company; the capability-gate rule is enforced by
//     review discipline, not by this function.
func ChangeCompanyShipmentRequired(db *gorm.DB, in ChangeCompanyShipmentRequiredInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("services.ChangeCompanyShipmentRequired: CompanyID required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var company models.Company
		if err := tx.Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}
		if company.ShipmentRequired == in.Required {
			return nil // no-op
		}

		if err := tx.Model(&models.Company{}).
			Where("id = ?", in.CompanyID).
			Update("shipment_required", in.Required).Error; err != nil {
			return fmt.Errorf("persist shipment_required: %w", err)
		}

		cid := in.CompanyID
		action := "company.shipment_required.enabled"
		if !in.Required {
			action = "company.shipment_required.disabled"
		}
		TryWriteAuditLogWithContextDetails(
			tx,
			action,
			"company",
			company.ID,
			actorOrSystem(in.Actor),
			map[string]any{"company_name": company.Name},
			&cid,
			in.ActorUserID,
			map[string]any{"shipment_required": company.ShipmentRequired},
			map[string]any{"shipment_required": in.Required},
		)
		return nil
	})
}
