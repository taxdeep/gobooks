// 遵循project_guide.md
package services

// company_receipt_required.go — Phase H slice H.1 audited admin surface
// for the companies.receipt_required capability rail.
//
// Rail, not an operational switch
// -------------------------------
// H.1 installs the column (migration 068) and this flip surface so
// later slices can branch on the flag once each is verified. Nothing
// reads the flag in H.1 — no Bill, no Receipt, no inventory handler.
// The admin surface exists so that when H.2–H.5 land incrementally,
// the consumer is already wired to an audited state change rather
// than being retrofitted.
//
// Reviewer contract: if a PR adds a reader of `receipt_required` as
// part of H.1, it is out of scope and must move to H.2 or later.
// H.1's rail is dormant by design.
//
// Operational enablement of `receipt_required=true` is blocked until
// Phase H.5 closes (INVENTORY_MODULE_API.md §Phase H Border 1).
// Flipping a real company before H.5 produces a half-bridged state
// that is strictly worse than Phase G; this is a discipline-enforced
// rule, not a code guard.

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ChangeCompanyReceiptRequiredInput is the input to the rail flip.
// Required=true opts the company into the Phase H Receipt-first
// inbound model; Required=false leaves the company on the Phase G
// Bill-forms-inventory legacy path.
//
// H.1 does NOT add new sentinel errors. Enabling is unconditional
// beyond the company existing; disabling is likewise unconditional
// because no consumer reads the flag yet — there is nothing to
// orphan by flipping it off. Consumer-aware guards (e.g. "cannot
// disable while unmatched Receipts exist") belong in the slice that
// introduces the consumer, not in this rail.
type ChangeCompanyReceiptRequiredInput struct {
	CompanyID   uint
	Required    bool
	Actor       string
	ActorUserID *uuid.UUID
}

// ChangeCompanyReceiptRequired flips companies.receipt_required.
//
// Semantics (mirrors G.1 ChangeCompanyTrackingCapability):
//   - No-op when the company is already in the target state: neither
//     side-effect nor audit row.
//   - On effective change: the column is persisted and exactly one
//     audit row is written, action =
//     "company.receipt_required.enabled" or
//     "company.receipt_required.disabled".
//   - Before/after state on the audit row records the flipped value.
//
// Deliberately NOT in this surface:
//   - Any validation involving the presence of Receipts, Bills, or
//     matched/unmatched GR/IR balances. Those constraints land with
//     their respective consumers in H.3 / H.5. Wiring them here would
//     couple H.1 to later-slice data shapes that do not exist yet.
//   - Any "operational safety" guard that blocks a flip on a real
//     company before H.5. Code cannot distinguish a test company
//     from a real company; Border 1 is enforced by review discipline,
//     not by this function.
func ChangeCompanyReceiptRequired(db *gorm.DB, in ChangeCompanyReceiptRequiredInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("services.ChangeCompanyReceiptRequired: CompanyID required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var company models.Company
		if err := tx.Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}
		if company.ReceiptRequired == in.Required {
			return nil // no-op
		}

		if err := tx.Model(&models.Company{}).
			Where("id = ?", in.CompanyID).
			Update("receipt_required", in.Required).Error; err != nil {
			return fmt.Errorf("persist receipt_required: %w", err)
		}

		cid := in.CompanyID
		action := "company.receipt_required.enabled"
		if !in.Required {
			action = "company.receipt_required.disabled"
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
			map[string]any{"receipt_required": company.ReceiptRequired},
			map[string]any{"receipt_required": in.Required},
		)
		return nil
	})
}
