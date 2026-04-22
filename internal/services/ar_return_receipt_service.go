// 遵循project_guide.md
package services

// ar_return_receipt_service.go — Phase I slice I.6a.2: CRUD + lifecycle
// for the AR-return physical-truth document (customer returns stock
// items, goes back into the warehouse).
//
// Scope lock (I.6a.2)
// -------------------
// This file implements the document-layer surface AND the inventory /
// JE wiring for ARReturnReceipt:
//
//   - Create / Get / Update (draft) / List / Delete (draft) — document
//     persistence with Q8 save-time CreditNote-link enforcement.
//   - Post — rail-aware. Under `companies.shipment_required=true`, runs
//     inventory.ReceiveStock at the TRACED original-sale cost and
//     books the Dr Inventory / Cr COGS JE. Under
//     `shipment_required=false` (legacy), it is a pure status flip
//     (IN.5's CreditNote retains movement ownership — Return Receipt
//     is optional under legacy per charter §3.4).
//   - Void — Q5 **document-local** reversal. Reverses its own
//     inventory_movements rows and own JE only. Does NOT cascade to
//     the paired CreditNote; operators void each document separately.
//
// What this slice (I.6a.2) does NOT do
// ------------------------------------
// - **CreditNote controlled-mode retrofit.** Under
//   `shipment_required=true`, CreditNote.Post still rejects stock
//   lines with `ErrCreditNoteStockItemRequiresReturnReceipt` (IN.5
//   behavior unchanged). Flipping that rejection into an acceptance
//   with per-line coverage (Q6) lands in I.6a.3.
// - **Rule4DocARReturnReceipt dispatch.** The Rule #4 movement-owner
//   table flip (CreditNote surrenders → ARReturnReceipt becomes owner
//   under controlled mode) is I.6a.3's job.
// - **Full-coverage enforcement at CN post.** Q6 check lives on the
//   CreditNote side; no enforcement happens here on the
//   ARReturnReceipt side. Multiple ARReturnReceipts may link to one
//   CreditNote line until the CN post runs the coverage check.
// - **UI / shortcut action.** The "Create matching Return Receipt"
//   button on CreditNote detail (Q4 pattern) is I.6a.4.
// - **Pilot enablement / runbook.** I.6a.5.
//
// Identity chain wired at post time
// ---------------------------------
//   InvoiceLine → CreditNoteLine → ARReturnReceiptLine → inventory_movement
//
// Traced cost: the Dr Inventory amount is the ORIGINAL sale's
// snapshot cost (read from the original Invoice's inventory_movement
// via CreditNoteLine.OriginalInvoiceLineID — the IN.5 field).
// Partial returns supported by construction (the cost is a rate, so
// it applies to any qty subset of the original sale). This matches
// IN.5's authoritative-cost semantic: March's COGS reverses at
// March's cost, never at today's drifted weighted average.
//
// Warehouse choice
// ----------------
// Unlike IN.5's CreditNote-direct path (which receives goods back at
// the ORIGINAL warehouse the sale left from), ARReturnReceipt
// captures an explicit WarehouseID on the document header. The
// inventory.ReceiveStock call uses THAT warehouse — so a customer
// who ships returns to a different warehouse than the original
// shipment works correctly. This is a deliberate correctness
// improvement made possible by the physical-truth document shape.
//
// Audit surface
// -------------
// Post and Void each write exactly one audit row:
//   - `ar_return_receipt.posted`   (draft → posted)
//   - `ar_return_receipt.voided`   (posted → voided)
// Create / Update / Delete do NOT write audit at I.6a.2 — standard
// pre-posting CRUD with no cross-module state change worth recording.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// Error sentinels. Prefixed `ErrARReturnReceipt*` to namespace cleanly
// from the AR CustomerReceipt family (`ErrReceipt*` in
// customer_receipt_service.go) and the Phase H Receipt family
// (`ErrInboundReceipt*` in receipt_service.go).
var (
	// ErrARReturnReceiptNotFound — lookup by (CompanyID, ID) missed.
	ErrARReturnReceiptNotFound = errors.New("ar_return_receipt: not found")

	// ErrARReturnReceiptNotDraft — attempted a draft-only operation
	// (update, delete) on a return receipt that has moved out of draft.
	ErrARReturnReceiptNotDraft = errors.New("ar_return_receipt: operation requires status=draft")

	// ErrARReturnReceiptNotPosted — attempted to void a return receipt
	// that is not currently posted.
	ErrARReturnReceiptNotPosted = errors.New("ar_return_receipt: void requires status=posted")

	// ErrARReturnReceiptAlreadyPosted — attempted to post a return
	// receipt that is not in draft (already posted or voided).
	ErrARReturnReceiptAlreadyPosted = errors.New("ar_return_receipt: post requires status=draft")

	// ErrARReturnReceiptWarehouseRequired — the document must land
	// somewhere.
	ErrARReturnReceiptWarehouseRequired = errors.New("ar_return_receipt: WarehouseID required")

	// ErrARReturnReceiptDateRequired — ReturnDate required so back-
	// dated returns are deliberate rather than silent.
	ErrARReturnReceiptDateRequired = errors.New("ar_return_receipt: ReturnDate required")

	// ErrARReturnReceiptLineProductRequired — every line names a
	// product/service.
	ErrARReturnReceiptLineProductRequired = errors.New("ar_return_receipt: line requires ProductServiceID")

	// ErrARReturnReceiptCreditNoteRequired — charter Q8: standalone
	// Return Receipt rejected. A draft-or-posted CreditNote link is
	// required at save time. Enforced at service, not schema (schema
	// keeps credit_note_id nullable per Q7 hard rule #1, orphan rows
	// recoverable).
	ErrARReturnReceiptCreditNoteRequired = errors.New("ar_return_receipt: CreditNoteID required (standalone Return Receipt rejected by charter Q8)")

	// ErrARReturnReceiptCreditNoteVoided — the linked CreditNote is in
	// voided status. Q8 requires a "draft-or-posted" link; voided is
	// neither. Separate from CreditNoteRequired so the remediation
	// message can tell the operator exactly why the link is invalid.
	ErrARReturnReceiptCreditNoteVoided = errors.New("ar_return_receipt: linked CreditNote is voided (Q8 requires draft-or-posted)")

	// ErrARReturnReceiptCrossCompanyReference — an ID on the input
	// (customer / warehouse / product_service / credit_note /
	// credit_note_line) resolves to a row that belongs to a different
	// company than the return receipt being created or updated.
	// Rejected before any write.
	ErrARReturnReceiptCrossCompanyReference = errors.New("ar_return_receipt: referenced entity belongs to a different company")

	// ErrARReturnReceiptLineCreditNoteLineMismatch — a line's
	// CreditNoteLineID points at a CreditNoteLine whose parent
	// CreditNote differs from the header's CreditNoteID. The identity
	// chain would be broken at post time; reject at save.
	ErrARReturnReceiptLineCreditNoteLineMismatch = errors.New("ar_return_receipt: line CreditNoteLineID belongs to a different CreditNote than the header")

	// ErrARReturnReceiptLineMissingOriginalInvoiceLine — at post time,
	// every stock-item line must resolve to a CreditNoteLine carrying
	// OriginalInvoiceLineID (the IN.5 trace). Without it, cost cannot
	// be traced from the original sale movement.
	ErrARReturnReceiptLineMissingOriginalInvoiceLine = errors.New("ar_return_receipt: stock-item line's CreditNoteLine has no OriginalInvoiceLineID — cost trace broken")

	// ErrARReturnReceiptLineOriginalMovementNotFound — the traced
	// original invoice movement could not be located. Data integrity
	// issue; operator should escalate.
	ErrARReturnReceiptLineOriginalMovementNotFound = errors.New("ar_return_receipt: could not locate original invoice movement for traced cost")
)

// CreateARReturnReceiptInput captures the fields a caller may set
// when creating a new ARReturnReceipt in draft state. Fields not
// listed here are populated by the service (ID, CreatedAt/UpdatedAt,
// Status='draft').
type CreateARReturnReceiptInput struct {
	CompanyID           uint
	ReturnReceiptNumber string
	CustomerID          *uint
	WarehouseID         uint
	ReturnDate          time.Time
	Memo                string
	Reference           string
	// CreditNoteID is required at save time per charter Q8 — no
	// standalone Return Receipts. Nullability here is a carrier shape
	// for inputs; the service rejects nil / zero on write.
	CreditNoteID *uint

	Lines []CreateARReturnReceiptLineInput

	Actor       string
	ActorUserID *uuid.UUID
}

// CreateARReturnReceiptLineInput captures the fields a caller may set
// on a line at creation time. No UnitCost — cost is authoritative
// from the inventory module (traced at post time).
type CreateARReturnReceiptLineInput struct {
	SortOrder        int
	ProductServiceID uint
	Description      string
	Qty              decimal.Decimal
	Unit             string
	// CreditNoteLineID is the per-line commercial link. Required at
	// post time (Q7 hard rule #2); optional at draft create so an
	// operator can stage lines before finalising identities.
	CreditNoteLineID *uint
}

// UpdateARReturnReceiptInput captures the mutable fields on a draft
// ARReturnReceipt. Lines are replaced wholesale when ReplaceLines is
// true; nil / false means leave lines untouched.
type UpdateARReturnReceiptInput struct {
	ReturnReceiptNumber *string
	CustomerID          *uint
	WarehouseID         *uint
	ReturnDate          *time.Time
	Memo                *string
	Reference           *string
	CreditNoteID        *uint
	Lines               []CreateARReturnReceiptLineInput
	ReplaceLines        bool
}

// ListARReturnReceiptsFilter narrows a company's return receipts.
type ListARReturnReceiptsFilter struct {
	Status       models.ARReturnReceiptStatus
	FromDate     *time.Time
	ToDate       *time.Time
	CustomerID   *uint
	CreditNoteID *uint
	Limit        int
	Offset       int
}

// CreateARReturnReceipt persists a new ARReturnReceipt and its lines
// in draft state. Runs in a single transaction. Enforces charter Q8
// (CreditNote link required at save time) and cross-company scope
// on all referenced IDs. Returns the created document with lines
// populated.
func CreateARReturnReceipt(db *gorm.DB, in CreateARReturnReceiptInput) (*models.ARReturnReceipt, error) {
	if in.CompanyID == 0 {
		return nil, fmt.Errorf("services.CreateARReturnReceipt: CompanyID required")
	}
	if in.WarehouseID == 0 {
		return nil, ErrARReturnReceiptWarehouseRequired
	}
	if in.ReturnDate.IsZero() {
		return nil, ErrARReturnReceiptDateRequired
	}
	if in.CreditNoteID == nil || *in.CreditNoteID == 0 {
		return nil, ErrARReturnReceiptCreditNoteRequired
	}
	for i, ln := range in.Lines {
		if ln.ProductServiceID == 0 {
			return nil, fmt.Errorf("%w: line[%d]", ErrARReturnReceiptLineProductRequired, i)
		}
	}

	var created models.ARReturnReceipt
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := validateARReturnReceiptHeaderScope(tx, in.CompanyID,
			in.CustomerID, in.WarehouseID, in.CreditNoteID); err != nil {
			return err
		}
		if err := validateARReturnReceiptLinesScope(tx, in.CompanyID,
			*in.CreditNoteID, in.Lines); err != nil {
			return err
		}

		r := models.ARReturnReceipt{
			CompanyID:           in.CompanyID,
			ReturnReceiptNumber: in.ReturnReceiptNumber,
			CustomerID:          in.CustomerID,
			WarehouseID:         in.WarehouseID,
			ReturnDate:          in.ReturnDate,
			Status:              models.ARReturnReceiptStatusDraft,
			Memo:                in.Memo,
			Reference:           in.Reference,
			CreditNoteID:        in.CreditNoteID,
		}
		if err := tx.Create(&r).Error; err != nil {
			return fmt.Errorf("create ar_return_receipt: %w", err)
		}

		for _, ln := range in.Lines {
			rl := models.ARReturnReceiptLine{
				CompanyID:         in.CompanyID,
				ARReturnReceiptID: r.ID,
				SortOrder:         ln.SortOrder,
				ProductServiceID:  ln.ProductServiceID,
				Description:       ln.Description,
				Qty:               ln.Qty,
				Unit:              ln.Unit,
				CreditNoteLineID:  ln.CreditNoteLineID,
			}
			if err := tx.Create(&rl).Error; err != nil {
				return fmt.Errorf("create ar_return_receipt line: %w", err)
			}
		}
		created = r
		return tx.Preload("Lines").First(&created, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// GetARReturnReceipt loads an ARReturnReceipt by (CompanyID, ID) with
// its lines preloaded. Cross-tenant access returns
// ErrARReturnReceiptNotFound.
func GetARReturnReceipt(db *gorm.DB, companyID, id uint) (*models.ARReturnReceipt, error) {
	var r models.ARReturnReceipt
	err := db.Preload("Lines").
		Where("company_id = ? AND id = ?", companyID, id).
		First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrARReturnReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load ar_return_receipt: %w", err)
	}
	return &r, nil
}

// UpdateARReturnReceipt mutates a draft ARReturnReceipt. Any field
// left nil on the input is preserved. Post-draft receipts are refused
// with ErrARReturnReceiptNotDraft. Q8 save-time CreditNote link is
// re-validated if changed.
func UpdateARReturnReceipt(db *gorm.DB, companyID, id uint, in UpdateARReturnReceiptInput) (*models.ARReturnReceipt, error) {
	var updated models.ARReturnReceipt
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadARReturnReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ARReturnReceiptStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrARReturnReceiptNotDraft, r.Status)
		}

		if in.ReturnReceiptNumber != nil {
			r.ReturnReceiptNumber = *in.ReturnReceiptNumber
		}
		if in.CustomerID != nil {
			r.CustomerID = in.CustomerID
		}
		if in.WarehouseID != nil {
			if *in.WarehouseID == 0 {
				return ErrARReturnReceiptWarehouseRequired
			}
			r.WarehouseID = *in.WarehouseID
		}
		if in.ReturnDate != nil {
			if in.ReturnDate.IsZero() {
				return ErrARReturnReceiptDateRequired
			}
			r.ReturnDate = *in.ReturnDate
		}
		if in.Memo != nil {
			r.Memo = *in.Memo
		}
		if in.Reference != nil {
			r.Reference = *in.Reference
		}
		if in.CreditNoteID != nil {
			if *in.CreditNoteID == 0 {
				return ErrARReturnReceiptCreditNoteRequired
			}
			r.CreditNoteID = in.CreditNoteID
		}

		// Post-mutation, the CreditNote link must still be present
		// and valid per Q8. Re-check (the initial check + this ensures
		// an update that clears CreditNoteID is rejected).
		if r.CreditNoteID == nil || *r.CreditNoteID == 0 {
			return ErrARReturnReceiptCreditNoteRequired
		}

		if err := validateARReturnReceiptHeaderScope(tx, companyID,
			r.CustomerID, r.WarehouseID, r.CreditNoteID); err != nil {
			return err
		}
		if in.ReplaceLines {
			if err := validateARReturnReceiptLinesScope(tx, companyID,
				*r.CreditNoteID, in.Lines); err != nil {
				return err
			}
		}

		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save ar_return_receipt: %w", err)
		}

		if in.ReplaceLines {
			if err := tx.Where("ar_return_receipt_id = ?", r.ID).
				Delete(&models.ARReturnReceiptLine{}).Error; err != nil {
				return fmt.Errorf("delete old lines: %w", err)
			}
			for _, ln := range in.Lines {
				if ln.ProductServiceID == 0 {
					return ErrARReturnReceiptLineProductRequired
				}
				rl := models.ARReturnReceiptLine{
					CompanyID:         companyID,
					ARReturnReceiptID: r.ID,
					SortOrder:         ln.SortOrder,
					ProductServiceID:  ln.ProductServiceID,
					Description:       ln.Description,
					Qty:               ln.Qty,
					Unit:              ln.Unit,
					CreditNoteLineID:  ln.CreditNoteLineID,
				}
				if err := tx.Create(&rl).Error; err != nil {
					return fmt.Errorf("create ar_return_receipt line: %w", err)
				}
			}
		}

		updated = *r
		return tx.Preload("Lines").First(&updated, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// ListARReturnReceipts returns a company's return receipts ordered by
// ReturnDate desc, ID desc. Lines are NOT preloaded — callers wanting
// line data call GetARReturnReceipt per row.
func ListARReturnReceipts(db *gorm.DB, companyID uint, filter ListARReturnReceiptsFilter) ([]models.ARReturnReceipt, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("services.ListARReturnReceipts: CompanyID required")
	}
	q := db.Model(&models.ARReturnReceipt{}).
		Where("company_id = ?", companyID)
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.FromDate != nil {
		q = q.Where("return_date >= ?", *filter.FromDate)
	}
	if filter.ToDate != nil {
		q = q.Where("return_date <= ?", *filter.ToDate)
	}
	if filter.CustomerID != nil {
		q = q.Where("customer_id = ?", *filter.CustomerID)
	}
	if filter.CreditNoteID != nil {
		q = q.Where("credit_note_id = ?", *filter.CreditNoteID)
	}
	q = q.Order("return_date DESC, id DESC")
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}
	var rows []models.ARReturnReceipt
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list ar_return_receipts: %w", err)
	}
	return rows, nil
}

// PostARReturnReceipt flips a draft ARReturnReceipt to posted and,
// under Phase I.6a Return-Receipt semantics
// (`companies.shipment_required=true`), drives the full return
// receipt → receive truth → inventory effect chain and books the
// Dr Inventory / Cr COGS JE at the TRACED original-sale cost.
//
// Rail gating (charter §3.4):
//   - shipment_required=false (legacy default): status flip + audit
//     only. IN.5's CreditNote remains the movement owner under
//     legacy; creating an ARReturnReceipt is a physical-tracking
//     convenience with no system truth.
//   - shipment_required=true: the full Return-Receipt-first flow
//     runs. Receive truth lands as inventory_movements rows with
//     source_type='ar_return_receipt'; the JE is constructed from
//     the base-currency values returned by inventory.ReceiveStock
//     and linked back via ar_return_receipts.journal_entry_id.
//
// Note: I.6a.3 will flip the CreditNote controlled-mode rejection
// into an acceptance-with-coverage path. In I.6a.2 the CreditNote
// post side still rejects stock lines — so a posted ARReturnReceipt
// under controlled mode awaits I.6a.3 before it has a fully working
// end-to-end flow. This slice ships the ARReturnReceipt half
// independently so reviewers can evaluate it in isolation.
//
// Writes exactly one audit row (`ar_return_receipt.posted`) in both
// branches.
func PostARReturnReceipt(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.ARReturnReceipt, error) {
	var out models.ARReturnReceipt
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadARReturnReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ARReturnReceiptStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrARReturnReceiptAlreadyPosted, r.Status)
		}

		// Re-read company inside tx to pick up latest
		// shipment_required flag (admin surface commits before
		// PostARReturnReceipt begins; read-then-act is safe here).
		var company models.Company
		if err := tx.Where("id = ?", companyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		// Preload lines with ProductService + CreditNoteLine so the
		// posting helper can read inventory / COGS accounts and trace
		// the original invoice movement without extra round trips.
		if err := tx.Preload("Lines.ProductService").
			Preload("Lines.CreditNoteLine").
			Preload("CreditNote").
			First(r, r.ID).Error; err != nil {
			return fmt.Errorf("preload ar_return_receipt: %w", err)
		}

		var postedJEID *uint
		if company.ShipmentRequired {
			jeID, err := postARReturnReceiptReceiveTruthAndJE(tx, companyID, *r)
			if err != nil {
				return err
			}
			postedJEID = jeID
		}

		now := time.Now().UTC()
		r.Status = models.ARReturnReceiptStatusPosted
		r.PostedAt = &now
		r.JournalEntryID = postedJEID
		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save ar_return_receipt: %w", err)
		}

		// I.6a.3 Rule #4 post-time invariant. Under
		// shipment_required=true this document IS the movement owner
		// (Rule4DocARReturnReceipt) and MUST have produced at least
		// one inventory_movements row per stock-item line. Under
		// shipment_required=false it is NOT the owner — assertion
		// expects zero rows. Either way, the dispatch + assertion
		// catches any future regression that decouples post from
		// movement formation.
		stockLineCount := 0
		for _, ln := range r.Lines {
			if ln.ProductService != nil && ln.ProductService.IsStockItem {
				stockLineCount++
			}
		}
		if err := AssertRule4PostTimeInvariant(tx, companyID,
			Rule4DocARReturnReceipt, r.ID, stockLineCount,
			Rule4WorkflowState{
				ReceiptRequired:  company.ReceiptRequired,
				ShipmentRequired: company.ShipmentRequired,
			},
		); err != nil {
			return err
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"ar_return_receipt.posted",
			"ar_return_receipt",
			r.ID,
			actorOrSystem(actor),
			map[string]any{
				"return_receipt_number": r.ReturnReceiptNumber,
				"credit_note_id":        nilableUintAsAny(r.CreditNoteID),
				"journal_entry_id":      nilableUintAsAny(postedJEID),
				"shipment_required":     company.ShipmentRequired,
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ARReturnReceiptStatusDraft)},
			map[string]any{"status": string(models.ARReturnReceiptStatusPosted)},
		)
		out = *r
		return tx.Preload("Lines").First(&out, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// VoidARReturnReceipt flips a posted ARReturnReceipt to voided.
// Document-local per charter Q5 — reverses ONLY its own inventory
// movements and own JE. Does NOT cascade to the paired CreditNote;
// operators void each document separately per their audit trail
// preference.
//
// When the receipt was posted under shipment_required=true (presence
// of journal_entry_id is the signal), also reverses:
//  1. The inventory movements via ReverseARReturnReceiptMovements
//     (wrapper over reverseDocumentMovements).
//  2. The JE — reversal JE posted (debit/credit swapped), original
//     JE flips to reversed, ledger entries flip, reversal projected.
//
// When posted without a JE (flag=false, or flag=true with no stock
// lines), void is a status flip + audit only.
//
// Writes exactly one audit row (`ar_return_receipt.voided`).
func VoidARReturnReceipt(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.ARReturnReceipt, error) {
	var out models.ARReturnReceipt
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadARReturnReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ARReturnReceiptStatusPosted {
			return fmt.Errorf("%w: current=%s", ErrARReturnReceiptNotPosted, r.Status)
		}

		var reversedJEID *uint
		if r.JournalEntryID != nil {
			if err := tx.Preload("Lines.ProductService").
				First(r, r.ID).Error; err != nil {
				return fmt.Errorf("preload ar_return_receipt for void: %w", err)
			}
			jeID, err := voidARReturnReceiptReverseJEAndMovements(tx, companyID, *r)
			if err != nil {
				return err
			}
			reversedJEID = jeID
		}

		now := time.Now().UTC()
		r.Status = models.ARReturnReceiptStatusVoided
		r.VoidedAt = &now
		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save ar_return_receipt: %w", err)
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"ar_return_receipt.voided",
			"ar_return_receipt",
			r.ID,
			actorOrSystem(actor),
			map[string]any{
				"return_receipt_number": r.ReturnReceiptNumber,
				"original_je_id":        nilableUintAsAny(r.JournalEntryID),
				"reversal_je_id":        nilableUintAsAny(reversedJEID),
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ARReturnReceiptStatusPosted)},
			map[string]any{"status": string(models.ARReturnReceiptStatusVoided)},
		)
		out = *r
		return tx.Preload("Lines").First(&out, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteARReturnReceipt removes a draft receipt and its lines.
// Non-draft receipts refuse deletion (trace must stay for audit).
func DeleteARReturnReceipt(db *gorm.DB, companyID, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		r, err := loadARReturnReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ARReturnReceiptStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrARReturnReceiptNotDraft, r.Status)
		}
		if err := tx.Where("ar_return_receipt_id = ?", r.ID).
			Delete(&models.ARReturnReceiptLine{}).Error; err != nil {
			return fmt.Errorf("delete ar_return_receipt lines: %w", err)
		}
		if err := tx.Delete(r).Error; err != nil {
			return fmt.Errorf("delete ar_return_receipt: %w", err)
		}
		return nil
	})
}

// validateARReturnReceiptHeaderScope verifies every non-nil header
// reference resolves to a same-company row AND the linked CreditNote
// is not voided (Q8). Returns the wrapped error with an entity label
// so logs pinpoint the fault.
func validateARReturnReceiptHeaderScope(tx *gorm.DB, companyID uint, customerID *uint, warehouseID uint, creditNoteID *uint) error {
	if customerID != nil && *customerID != 0 {
		if err := requireARReturnReceiptSameCompany(tx, &models.Customer{}, "customer",
			*customerID, companyID); err != nil {
			return err
		}
	}
	if warehouseID != 0 {
		if err := requireARReturnReceiptSameCompany(tx, &models.Warehouse{}, "warehouse",
			warehouseID, companyID); err != nil {
			return err
		}
	}
	if creditNoteID != nil && *creditNoteID != 0 {
		if err := requireARReturnReceiptSameCompany(tx, &models.CreditNote{}, "credit_note",
			*creditNoteID, companyID); err != nil {
			return err
		}
		// Q8: draft-or-posted link only. Voided CreditNote rejected.
		var status string
		if err := tx.Model(&models.CreditNote{}).
			Select("status").
			Where("id = ?", *creditNoteID).
			Limit(1).
			Scan(&status).Error; err != nil {
			return fmt.Errorf("load credit_note status: %w", err)
		}
		if status == string(models.CreditNoteStatusVoided) {
			return fmt.Errorf("%w: credit_note id=%d status=%s",
				ErrARReturnReceiptCreditNoteVoided, *creditNoteID, status)
		}
	}
	return nil
}

// validateARReturnReceiptLinesScope applies company-scope to each
// line's refs AND verifies each CreditNoteLineID belongs to the
// ARReturnReceipt's linked CreditNote (identity-chain consistency
// at save time). One query per distinct ID — small input sets so
// N+1 is acceptable.
func validateARReturnReceiptLinesScope(tx *gorm.DB, companyID uint, creditNoteID uint, lines []CreateARReturnReceiptLineInput) error {
	for i, ln := range lines {
		if ln.ProductServiceID != 0 {
			if err := requireARReturnReceiptSameCompany(tx, &models.ProductService{}, "product_service",
				ln.ProductServiceID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
		}
		if ln.CreditNoteLineID != nil && *ln.CreditNoteLineID != 0 {
			if err := requireARReturnReceiptSameCompany(tx, &models.CreditNoteLine{}, "credit_note_line",
				*ln.CreditNoteLineID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
			// Chain-consistency: the CreditNoteLine must belong to the
			// header's CreditNote.
			var parentCN uint
			if err := tx.Model(&models.CreditNoteLine{}).
				Select("credit_note_id").
				Where("id = ?", *ln.CreditNoteLineID).
				Limit(1).
				Scan(&parentCN).Error; err != nil {
				return fmt.Errorf("line[%d]: load credit_note_line parent: %w", i, err)
			}
			if parentCN != creditNoteID {
				return fmt.Errorf("%w: line[%d] credit_note_line id=%d belongs to cn=%d, header cn=%d",
					ErrARReturnReceiptLineCreditNoteLineMismatch, i,
					*ln.CreditNoteLineID, parentCN, creditNoteID)
			}
		}
	}
	return nil
}

// requireARReturnReceiptSameCompany loads the row identified by id
// and confirms its CompanyID matches the expected value. Returns
// ErrARReturnReceiptNotFound on absence,
// ErrARReturnReceiptCrossCompanyReference on mismatch.
func requireARReturnReceiptSameCompany(tx *gorm.DB, model any, entity string, id, companyID uint) error {
	var found uint
	err := tx.Model(model).
		Select("company_id").
		Where("id = ?", id).
		Limit(1).
		Scan(&found).Error
	if err != nil {
		return fmt.Errorf("validate %s scope: %w", entity, err)
	}
	if found == 0 {
		return fmt.Errorf("%w: %s id=%d not found", ErrARReturnReceiptNotFound, entity, id)
	}
	if found != companyID {
		return fmt.Errorf("%w: %s id=%d belongs to company=%d, ar_return_receipt company=%d",
			ErrARReturnReceiptCrossCompanyReference, entity, id, found, companyID)
	}
	return nil
}

// loadARReturnReceiptForUpdate fetches an ARReturnReceipt scoped to
// the company with a row-level write lock. Used by every lifecycle-
// mutating operation so concurrent status flips serialise.
func loadARReturnReceiptForUpdate(tx *gorm.DB, companyID, id uint) (*models.ARReturnReceipt, error) {
	var r models.ARReturnReceipt
	err := applyLockForUpdate(tx.Where("company_id = ? AND id = ?", companyID, id)).
		First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrARReturnReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load ar_return_receipt: %w", err)
	}
	return &r, nil
}

// voidARReturnReceiptReverseJEAndMovements reverses the JE + inventory
// movements for a previously-posted ARReturnReceipt. Mirrors the
// VoidReceipt / VoidShipment reversal pattern so the same void
// semantics apply regardless of document.
//
// Document-local per charter Q5: ONLY this document's rows are
// touched. The paired CreditNote (and any movements it owns under
// IN.5 legacy mode) is not cascaded.
func voidARReturnReceiptReverseJEAndMovements(tx *gorm.DB, companyID uint, r models.ARReturnReceipt) (*uint, error) {
	if r.JournalEntryID == nil {
		return nil, nil
	}

	var origJE models.JournalEntry
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", *r.JournalEntryID, companyID).
		First(&origJE).Error; err != nil {
		return nil, fmt.Errorf("load original ar_return_receipt JE: %w", err)
	}
	if len(origJE.Lines) == 0 {
		return nil, fmt.Errorf("original ar_return_receipt JE %d has no lines", origJE.ID)
	}

	reversalJE := models.JournalEntry{
		CompanyID:      companyID,
		EntryDate:      origJE.EntryDate,
		JournalNo:      "VOID-" + r.ReturnReceiptNumber,
		ReversedFromID: &origJE.ID,
		Status:         models.JournalEntryStatusPosted,
		SourceType:     models.LedgerSourceReversal,
		SourceID:       r.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal ar_return_receipt JE"); err != nil {
		return nil, fmt.Errorf("create reversal JE: %w", err)
	}

	createdRevLines := make([]models.JournalLine, 0, len(origJE.Lines))
	for _, l := range origJE.Lines {
		line := models.JournalLine{
			CompanyID:      companyID,
			JournalEntryID: reversalJE.ID,
			AccountID:      l.AccountID,
			Debit:          l.Credit,
			Credit:         l.Debit,
			Memo:           "VOID: " + l.Memo,
			PartyType:      l.PartyType,
			PartyID:        l.PartyID,
		}
		if err := tx.Create(&line).Error; err != nil {
			return nil, fmt.Errorf("create reversal line: %w", err)
		}
		createdRevLines = append(createdRevLines, line)
	}

	if err := tx.Model(&models.JournalEntry{}).
		Where("id = ? AND company_id = ?", origJE.ID, companyID).
		Update("status", models.JournalEntryStatusReversed).Error; err != nil {
		return nil, fmt.Errorf("mark original JE reversed: %w", err)
	}
	if err := MarkLedgerEntriesReversed(tx, companyID, origJE.ID); err != nil {
		return nil, fmt.Errorf("mark ledger entries reversed: %w", err)
	}
	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: reversalJE,
		Lines:        createdRevLines,
		SourceType:   models.LedgerSourceReversal,
		SourceID:     r.ID,
	}); err != nil {
		return nil, fmt.Errorf("project reversal to ledger: %w", err)
	}

	if err := ReverseARReturnReceiptMovements(tx, companyID, r); err != nil {
		return nil, fmt.Errorf("reverse ar_return_receipt movements: %w", err)
	}

	return &reversalJE.ID, nil
}
