// 遵循project_guide.md
package services

// receipt_service.go — Phase H slice H.2 CRUD + lifecycle for the
// inbound Receipt document.
//
// Scope lock (H.2)
// ----------------
// This file implements the document-layer surface for Receipt: it
// persists, reads, updates (draft only), lists, and flips status
// (draft→posted, posted→voided). It does NOT:
//
//   - Produce inventory movements
//   - Form or retire cost layers
//   - Call ReceiveStock / any inventory IN verb
//   - Write a JournalEntry
//   - Touch GR/IR
//   - Read companies.receipt_required
//   - Couple with Bill in any way
//   - Enforce source-identity linkage (PO references are stored only)
//
// PostReceipt / VoidReceipt in H.2 are pure status flips with audit.
// The consumer that turns Post into actual inventory truth is
// ReceiveStockFromReceipt in H.3; until then, posting a Receipt
// leaves inventory_* tables completely untouched. Tests in
// receipt_service_test.go lock this boundary at the CI level — any
// accidental H.3 slip shows up as a failed "no inventory effect"
// assertion.
//
// Audit surface
// -------------
// Post and Void each write exactly one audit row. Actions:
//   - `receipt.posted`   (draft → posted)
//   - `receipt.voided`   (posted → voided)
// Create / Update / Delete do NOT write audit in H.2 — they are
// standard document-layer CRUD on a pre-posting draft, with no
// cross-module state change worth recording. If a later slice needs
// draft-level audit (e.g. for regulated-industry compliance), it
// can bolt on without reshaping this surface.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// Error sentinels are prefixed `ErrInboundReceipt*` to namespace away
// from the existing AR-side `ErrReceipt*` family
// (customer_receipt_service.go), which governs customer payments. The
// domain meanings are unrelated: customer receipt = cash from
// customer; inbound receipt = goods from vendor.
var (
	// ErrInboundReceiptNotFound — lookup by (CompanyID, ID) missed.
	// Returned wrapped for errors.Is matching; handlers map to 404.
	ErrInboundReceiptNotFound = errors.New("receipt: not found")

	// ErrInboundReceiptNotDraft — attempted a draft-only operation
	// (update, delete) on a receipt that has moved out of draft.
	ErrInboundReceiptNotDraft = errors.New("receipt: operation requires status=draft")

	// ErrInboundReceiptNotPosted — attempted to void a receipt that
	// is not currently in posted status.
	ErrInboundReceiptNotPosted = errors.New("receipt: void requires status=posted")

	// ErrInboundReceiptAlreadyPosted — attempted to post a receipt
	// that is not in draft status (already posted, or already voided).
	ErrInboundReceiptAlreadyPosted = errors.New("receipt: post requires status=draft")

	// ErrInboundReceiptWarehouseRequired — a receipt must land
	// somewhere; WarehouseID is required on create.
	ErrInboundReceiptWarehouseRequired = errors.New("receipt: WarehouseID required")

	// ErrInboundReceiptDateRequired — receipt_date required so that
	// back-dated receipts are deliberate rather than silent.
	ErrInboundReceiptDateRequired = errors.New("receipt: ReceiptDate required")

	// ErrInboundReceiptLineProductRequired — every line names a
	// product/service.
	ErrInboundReceiptLineProductRequired = errors.New("receipt: line requires ProductServiceID")

	// ErrInboundReceiptCrossCompanyReference — an ID on the input
	// (vendor / warehouse / product_service / purchase_order /
	// purchase_order_line) resolves to a row that belongs to a
	// different company than the receipt being created or updated.
	// Rejected before any write so that the Receipt document cannot
	// establish a cross-tenant reference that H.3 / H.5 would later
	// have to detect after the fact. No FK constraint enforces this
	// (PO is not yet first-class enough to FK against, and multi-
	// company joins are allowed at the schema level for legacy
	// reasons), so the service layer is the boundary.
	ErrInboundReceiptCrossCompanyReference = errors.New("receipt: referenced entity belongs to a different company")
)

// CreateReceiptInput captures the fields a caller may set when
// creating a new Receipt in draft state. Fields not listed here are
// populated by the service (ID, CreatedAt/UpdatedAt, Status='draft').
type CreateReceiptInput struct {
	CompanyID       uint
	ReceiptNumber   string
	VendorID        *uint
	WarehouseID     uint
	ReceiptDate     time.Time
	Memo            string
	Reference       string
	PurchaseOrderID *uint

	Lines []CreateReceiptLineInput

	// Audit actor (only consumed by Post/Void; stored here for
	// callers that prefer to thread actor through the input struct
	// uniformly).
	Actor       string
	ActorUserID *uuid.UUID
}

// CreateReceiptLineInput captures the fields a caller may set on a
// receipt line at creation time.
type CreateReceiptLineInput struct {
	SortOrder           int
	ProductServiceID    uint
	Description         string
	Qty                 decimal.Decimal
	Unit                string
	UnitCost            decimal.Decimal
	LotNumber           string
	LotExpiryDate       *time.Time
	PurchaseOrderLineID *uint
}

// UpdateReceiptInput captures the mutable fields on a draft Receipt.
// Lines are replaced wholesale when provided (nil = leave lines
// untouched); a caller that wants to clear all lines passes an
// explicit empty slice. Status is not a mutable field — Post/Void
// own the status transitions.
type UpdateReceiptInput struct {
	ReceiptNumber   *string
	VendorID        *uint
	WarehouseID     *uint
	ReceiptDate     *time.Time
	Memo            *string
	Reference       *string
	PurchaseOrderID *uint
	Lines           []CreateReceiptLineInput
	ReplaceLines    bool
}

// ListReceiptsFilter narrows a company's receipts by status and date
// window. All fields optional; zero-values mean "unfiltered on this
// dimension".
type ListReceiptsFilter struct {
	Status    ReceiptStatus
	FromDate  *time.Time
	ToDate    *time.Time
	VendorID  *uint
	Limit     int
	Offset    int
}

// ReceiptStatus mirrors models.ReceiptStatus to keep service callers
// from needing to import models directly for the filter struct.
type ReceiptStatus = models.ReceiptStatus

// CreateReceipt persists a new Receipt and its lines in draft state.
// Runs in a single transaction. Returns the created Receipt with
// lines populated.
func CreateReceipt(db *gorm.DB, in CreateReceiptInput) (*models.Receipt, error) {
	if in.CompanyID == 0 {
		return nil, fmt.Errorf("services.CreateReceipt: CompanyID required")
	}
	if in.WarehouseID == 0 {
		return nil, ErrInboundReceiptWarehouseRequired
	}
	if in.ReceiptDate.IsZero() {
		return nil, ErrInboundReceiptDateRequired
	}
	for i, ln := range in.Lines {
		if ln.ProductServiceID == 0 {
			return nil, fmt.Errorf("%w: line[%d]", ErrInboundReceiptLineProductRequired, i)
		}
	}

	var created models.Receipt
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := validateReceiptHeaderScope(tx, in.CompanyID,
			in.VendorID, in.WarehouseID, in.PurchaseOrderID); err != nil {
			return err
		}
		if err := validateReceiptLinesScope(tx, in.CompanyID, in.Lines); err != nil {
			return err
		}

		r := models.Receipt{
			CompanyID:       in.CompanyID,
			ReceiptNumber:   in.ReceiptNumber,
			VendorID:        in.VendorID,
			WarehouseID:     in.WarehouseID,
			ReceiptDate:     in.ReceiptDate,
			Status:          models.ReceiptStatusDraft,
			Memo:            in.Memo,
			Reference:       in.Reference,
			PurchaseOrderID: in.PurchaseOrderID,
		}
		if err := tx.Create(&r).Error; err != nil {
			return fmt.Errorf("create receipt: %w", err)
		}

		for _, ln := range in.Lines {
			rl := models.ReceiptLine{
				CompanyID:           in.CompanyID,
				ReceiptID:           r.ID,
				SortOrder:           ln.SortOrder,
				ProductServiceID:    ln.ProductServiceID,
				Description:         ln.Description,
				Qty:                 ln.Qty,
				Unit:                ln.Unit,
				UnitCost:            ln.UnitCost,
				LotNumber:           ln.LotNumber,
				LotExpiryDate:       ln.LotExpiryDate,
				PurchaseOrderLineID: ln.PurchaseOrderLineID,
			}
			if err := tx.Create(&rl).Error; err != nil {
				return fmt.Errorf("create receipt line: %w", err)
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

// GetReceipt loads a Receipt by (CompanyID, ID) with its lines
// preloaded. The company scope is enforced — a receipt from a
// different company returns ErrInboundReceiptNotFound even if the ID
// matches, preventing cross-tenant leakage.
func GetReceipt(db *gorm.DB, companyID, id uint) (*models.Receipt, error) {
	var r models.Receipt
	err := db.Preload("Lines").
		Where("company_id = ? AND id = ?", companyID, id).
		First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInboundReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load receipt: %w", err)
	}
	return &r, nil
}

// UpdateReceipt mutates a draft Receipt. Any field left nil on the
// input is preserved. Post-draft receipts are refused with
// ErrInboundReceiptNotDraft — the state machine guards that editing a
// posted or voided document requires its own reversal path.
func UpdateReceipt(db *gorm.DB, companyID, id uint, in UpdateReceiptInput) (*models.Receipt, error) {
	var updated models.Receipt
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ReceiptStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrInboundReceiptNotDraft, r.Status)
		}

		if in.ReceiptNumber != nil {
			r.ReceiptNumber = *in.ReceiptNumber
		}
		if in.VendorID != nil {
			r.VendorID = in.VendorID
		}
		if in.WarehouseID != nil {
			if *in.WarehouseID == 0 {
				return ErrInboundReceiptWarehouseRequired
			}
			r.WarehouseID = *in.WarehouseID
		}
		if in.ReceiptDate != nil {
			if in.ReceiptDate.IsZero() {
				return ErrInboundReceiptDateRequired
			}
			r.ReceiptDate = *in.ReceiptDate
		}
		if in.Memo != nil {
			r.Memo = *in.Memo
		}
		if in.Reference != nil {
			r.Reference = *in.Reference
		}
		if in.PurchaseOrderID != nil {
			r.PurchaseOrderID = in.PurchaseOrderID
		}

		if err := validateReceiptHeaderScope(tx, companyID,
			r.VendorID, r.WarehouseID, r.PurchaseOrderID); err != nil {
			return err
		}
		if in.ReplaceLines {
			if err := validateReceiptLinesScope(tx, companyID, in.Lines); err != nil {
				return err
			}
		}

		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save receipt: %w", err)
		}

		if in.ReplaceLines {
			if err := tx.Where("receipt_id = ?", r.ID).
				Delete(&models.ReceiptLine{}).Error; err != nil {
				return fmt.Errorf("delete old lines: %w", err)
			}
			for _, ln := range in.Lines {
				if ln.ProductServiceID == 0 {
					return ErrInboundReceiptLineProductRequired
				}
				rl := models.ReceiptLine{
					CompanyID:           companyID,
					ReceiptID:           r.ID,
					SortOrder:           ln.SortOrder,
					ProductServiceID:    ln.ProductServiceID,
					Description:         ln.Description,
					Qty:                 ln.Qty,
					Unit:                ln.Unit,
					UnitCost:            ln.UnitCost,
					LotNumber:           ln.LotNumber,
					LotExpiryDate:       ln.LotExpiryDate,
					PurchaseOrderLineID: ln.PurchaseOrderLineID,
				}
				if err := tx.Create(&rl).Error; err != nil {
					return fmt.Errorf("create receipt line: %w", err)
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

// ListReceipts returns a company's receipts ordered by ReceiptDate
// descending, then ID descending. Lines are NOT preloaded — the list
// surface is header-level. Callers needing line data call GetReceipt
// per row.
func ListReceipts(db *gorm.DB, companyID uint, filter ListReceiptsFilter) ([]models.Receipt, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("services.ListReceipts: CompanyID required")
	}
	q := db.Model(&models.Receipt{}).
		Where("company_id = ?", companyID)
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.FromDate != nil {
		q = q.Where("receipt_date >= ?", *filter.FromDate)
	}
	if filter.ToDate != nil {
		q = q.Where("receipt_date <= ?", *filter.ToDate)
	}
	if filter.VendorID != nil {
		q = q.Where("vendor_id = ?", *filter.VendorID)
	}
	q = q.Order("receipt_date DESC, id DESC")
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}
	var rows []models.Receipt
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	return rows, nil
}

// PostReceipt flips a draft Receipt to posted and, under Phase H
// Receipt-first semantics (companies.receipt_required=true), drives
// the full Receipt → receive truth → inventory effect chain and
// books the business-document-layer JE (Dr Inventory / Cr GR/IR).
//
// Flag gating (H.3):
//   - receipt_required=false (legacy default): status flip + audit
//     only. Byte-identical to H.2 behavior. Legacy companies continue
//     on the Bill-forms-inventory path; Receipt under flag=false is
//     effectively a dress-rehearsal document with no system truth.
//   - receipt_required=true: the full Receipt-first flow runs.
//     Receive truth lands as inventory_movements rows with
//     source_type='receipt'; inventory effect propagates through the
//     inventory module's standard cost-layer / balance machinery;
//     the JE is constructed from the base-currency values returned
//     by inventory.ReceiveStock and linked back via receipts.journal_entry_id.
//
// Writes exactly one audit row (`receipt.posted`) in both branches.
func PostReceipt(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.Receipt, error) {
	var out models.Receipt
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ReceiptStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrInboundReceiptAlreadyPosted, r.Status)
		}

		// Re-read the company inside the tx to pick up the latest
		// receipt_required flag (its admin surface commits before
		// PostReceipt begins; read-then-act is safe inside this tx).
		var company models.Company
		if err := tx.Where("id = ?", companyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		// Preload lines with ProductService resolved so
		// CreateReceiptMovements / JE builder can read inventory
		// accounts without a second round trip.
		if err := tx.Preload("Lines.ProductService").
			First(r, r.ID).Error; err != nil {
			return fmt.Errorf("preload receipt: %w", err)
		}

		var postedJEID *uint
		if company.ReceiptRequired {
			jeID, err := postReceiptReceiveTruthAndJE(tx, companyID, *r, actor)
			if err != nil {
				return err
			}
			postedJEID = jeID
		}

		now := time.Now().UTC()
		r.Status = models.ReceiptStatusPosted
		r.PostedAt = &now
		r.JournalEntryID = postedJEID
		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save receipt: %w", err)
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"receipt.posted",
			"receipt",
			r.ID,
			actorOrSystem(actor),
			map[string]any{
				"receipt_number":   r.ReceiptNumber,
				"journal_entry_id": nilableUintAsAny(postedJEID),
				"receipt_required": company.ReceiptRequired,
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ReceiptStatusDraft)},
			map[string]any{"status": string(models.ReceiptStatusPosted)},
		)
		out = *r
		return tx.Preload("Lines").First(&out, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// postReceiptReceiveTruthAndJE runs the H.3 flag=true branch of
// PostReceipt: project receipt lines to receive-truth via
// CreateReceiptMovements, then build the JE (Dr Inventory per line,
// Cr GR/IR total) and post it through the standard journal pipeline.
// Returns the created JournalEntry ID (may be nil when the receipt
// has no stock-item lines — legit: a receipt of pure services has no
// inventory accrual to book).
func postReceiptReceiveTruthAndJE(tx *gorm.DB, companyID uint, receipt models.Receipt, actor string) (*uint, error) {
	results, err := CreateReceiptMovements(tx, receipt)
	if err != nil {
		return nil, fmt.Errorf("create receipt movements: %w", err)
	}
	if len(results) == 0 {
		// No stock lines → no inventory accrual → no JE. Still a
		// valid posted Receipt (e.g. a service-only delivery note).
		return nil, nil
	}

	grirAccountID, err := resolveGRIRAccount(tx, companyID)
	if err != nil {
		return nil, err
	}
	frags, err := buildReceiptPostingFragments(results, grirAccountID, receipt.ReceiptNumber)
	if err != nil {
		return nil, err
	}
	if len(frags) == 0 {
		return nil, nil
	}
	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return nil, fmt.Errorf("aggregate journal lines: %w", err)
	}
	// Double-entry balance check: sums must equal in base currency.
	debitSum := sumPostingDebits(jeLines)
	creditSum := sumPostingCredits(jeLines)
	if !debitSum.Equal(creditSum) {
		return nil, fmt.Errorf(
			"receipt JE imbalance: debit %s, credit %s",
			debitSum.StringFixed(2), creditSum.StringFixed(2),
		)
	}

	je := models.JournalEntry{
		CompanyID:  companyID,
		EntryDate:  receipt.ReceiptDate,
		JournalNo:  receipt.ReceiptNumber,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceReceipt,
		SourceID:   receipt.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&je).Error, "create receipt journal entry"); err != nil {
		return nil, fmt.Errorf("create journal entry: %w", err)
	}

	createdLines := make([]models.JournalLine, 0, len(jeLines))
	for _, jl := range jeLines {
		line := models.JournalLine{
			CompanyID:      companyID,
			JournalEntryID: je.ID,
			AccountID:      jl.AccountID,
			TxDebit:        jl.Debit,
			TxCredit:       jl.Credit,
			Debit:          jl.Debit,
			Credit:         jl.Credit,
			Memo:           jl.Memo,
		}
		// Vendor linkage carried at party level so the GR/IR line is
		// traceable back to the supplier on reports.
		if receipt.VendorID != nil && *receipt.VendorID != 0 {
			line.PartyType = models.PartyTypeVendor
			line.PartyID = *receipt.VendorID
		}
		if err := tx.Create(&line).Error; err != nil {
			return nil, fmt.Errorf("create receipt journal line: %w", err)
		}
		createdLines = append(createdLines, line)
	}

	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: je,
		Lines:        createdLines,
		SourceType:   models.LedgerSourceReceipt,
		SourceID:     receipt.ID,
	}); err != nil {
		return nil, fmt.Errorf("project receipt to ledger: %w", err)
	}
	return &je.ID, nil
}

// VoidReceipt flips a posted Receipt to voided. When the receipt was
// posted under receipt_required=true (presence of journal_entry_id
// is the signal), also reverses:
//   1. The inventory movements (receive truth) via ReverseReceiptMovements
//      — the inventory module books reversal movements; balances /
//      cost layers unwind internally through the same machinery that
//      supports VoidBill.
//   2. The JE — a reversal JE is posted (debit/credit swapped),
//      original JE flips to status=reversed, ledger entries for the
//      original are marked reversed, and the reversal JE is projected
//      to the ledger.
//
// When the receipt was posted without a JE (flag=false, or flag=true
// with no stock lines), void is a status flip + audit only, matching
// the H.2 behavior.
//
// Writes exactly one audit row (`receipt.voided`) regardless of
// branch.
func VoidReceipt(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.Receipt, error) {
	var out models.Receipt
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ReceiptStatusPosted {
			return fmt.Errorf("%w: current=%s", ErrInboundReceiptNotPosted, r.Status)
		}

		// If the receipt had inventory effect at post time (JE linked),
		// reverse the JE + movements. Otherwise, pure status flip.
		var reversedJEID *uint
		if r.JournalEntryID != nil {
			// Load the receipt with lines for the reversal helper.
			// ReceiptLine rows carry enough identity for
			// reverseDocumentMovements to find the original movements
			// by (company, source_type='receipt', source_id=r.ID).
			if err := tx.Preload("Lines.ProductService").
				First(r, r.ID).Error; err != nil {
				return fmt.Errorf("preload receipt for void: %w", err)
			}
			jeID, err := voidReceiptReverseJEAndMovements(tx, companyID, *r)
			if err != nil {
				return err
			}
			reversedJEID = jeID
		}

		now := time.Now().UTC()
		r.Status = models.ReceiptStatusVoided
		r.VoidedAt = &now
		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save receipt: %w", err)
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"receipt.voided",
			"receipt",
			r.ID,
			actorOrSystem(actor),
			map[string]any{
				"receipt_number":    r.ReceiptNumber,
				"original_je_id":    nilableUintAsAny(r.JournalEntryID),
				"reversal_je_id":    nilableUintAsAny(reversedJEID),
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ReceiptStatusPosted)},
			map[string]any{"status": string(models.ReceiptStatusVoided)},
		)
		out = *r
		return tx.Preload("Lines").First(&out, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteReceipt removes a draft receipt and its lines. Non-draft
// receipts (posted, voided) are refused — their trace must stay for
// audit continuity.
func DeleteReceipt(db *gorm.DB, companyID, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		r, err := loadReceiptForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.ReceiptStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrInboundReceiptNotDraft, r.Status)
		}
		if err := tx.Where("receipt_id = ?", r.ID).
			Delete(&models.ReceiptLine{}).Error; err != nil {
			return fmt.Errorf("delete receipt lines: %w", err)
		}
		if err := tx.Delete(r).Error; err != nil {
			return fmt.Errorf("delete receipt: %w", err)
		}
		return nil
	})
}

// validateReceiptHeaderScope verifies that every non-nil reference ID
// on the Receipt header resolves to a row belonging to the same
// company. No FK constraints enforce this at the DB layer (PO is not
// yet first-class FK-able and multi-company joins are legal at the
// schema level for legacy reasons), so the service is the boundary.
//
// Checks:
//   - Vendor (optional): vendors.company_id == companyID
//   - Warehouse (required): warehouses.company_id == companyID
//   - PurchaseOrder (optional): purchase_orders.company_id == companyID
//
// Returns ErrInboundReceiptCrossCompanyReference on mismatch, wrapped
// with the offending entity name so logs pinpoint the fault.
func validateReceiptHeaderScope(tx *gorm.DB, companyID uint, vendorID *uint, warehouseID uint, purchaseOrderID *uint) error {
	if vendorID != nil && *vendorID != 0 {
		if err := requireSameCompany(tx, &models.Vendor{}, "vendor",
			*vendorID, companyID); err != nil {
			return err
		}
	}
	if warehouseID != 0 {
		if err := requireSameCompany(tx, &models.Warehouse{}, "warehouse",
			warehouseID, companyID); err != nil {
			return err
		}
	}
	if purchaseOrderID != nil && *purchaseOrderID != 0 {
		if err := requireSameCompany(tx, &models.PurchaseOrder{}, "purchase_order",
			*purchaseOrderID, companyID); err != nil {
			return err
		}
	}
	return nil
}

// validateReceiptLinesScope applies the same company-scope rule to
// each line's referenced IDs. Runs a single query per distinct ID
// encountered — small input sets so N+1 is acceptable; optimisable if
// lines grow into the hundreds.
//
// Checks:
//   - ProductService (required): product_services.company_id == companyID
//   - PurchaseOrderLine (optional): purchase_order_lines.company_id == companyID
func validateReceiptLinesScope(tx *gorm.DB, companyID uint, lines []CreateReceiptLineInput) error {
	for i, ln := range lines {
		if ln.ProductServiceID != 0 {
			if err := requireSameCompany(tx, &models.ProductService{}, "product_service",
				ln.ProductServiceID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
		}
		if ln.PurchaseOrderLineID != nil && *ln.PurchaseOrderLineID != 0 {
			if err := requireSameCompany(tx, &models.PurchaseOrderLine{}, "purchase_order_line",
				*ln.PurchaseOrderLineID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// requireSameCompany loads the row identified by id and confirms its
// CompanyID matches the expected value. Returns ErrInboundReceiptNotFound
// if the row is absent (distinct fault from cross-company), and
// ErrInboundReceiptCrossCompanyReference if present but owned by a
// different tenant. The `entity` string is a human label (e.g.
// "vendor") folded into the wrapped error so logs say exactly which
// reference offended.
func requireSameCompany(tx *gorm.DB, model any, entity string, id, companyID uint) error {
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
		// Row absent OR has company_id=0 (neither legitimate). Both
		// map to "not found" from the Receipt's perspective.
		return fmt.Errorf("%w: %s id=%d not found", ErrInboundReceiptNotFound, entity, id)
	}
	if found != companyID {
		return fmt.Errorf("%w: %s id=%d belongs to company=%d, receipt company=%d",
			ErrInboundReceiptCrossCompanyReference, entity, id, found, companyID)
	}
	return nil
}

// loadReceiptForUpdate fetches a Receipt scoped to the company and
// takes a row-level write lock (`SELECT ... FOR UPDATE` on PostgreSQL;
// no-op on SQLite — test DBs are single-writer anyway). Used by every
// lifecycle-mutating operation (Update, Post, Void, Delete) so
// concurrent flips on the same receipt serialise and cross-state
// races (e.g. two simultaneous PostReceipt calls) are rejected
// deterministically by the status check that immediately follows.
//
// In H.2 the mutations are document-layer only, but lifecycle truth
// itself deserves concurrency protection so that H.3 lands on a
// race-free foundation rather than inherit one.
//
// Returns ErrInboundReceiptNotFound when the row does not exist or
// belongs to another tenant (company scope enforced).
func loadReceiptForUpdate(tx *gorm.DB, companyID, id uint) (*models.Receipt, error) {
	var r models.Receipt
	err := applyLockForUpdate(tx.Where("company_id = ? AND id = ?", companyID, id)).
		First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInboundReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load receipt: %w", err)
	}
	return &r, nil
}

// voidReceiptReverseJEAndMovements reverses the JE and the inventory
// movements associated with a previously-posted Receipt. Mirrors the
// VoidBill reversal pattern so that the same void semantics apply
// regardless of which document produced the inbound. Returns the
// reversal JE ID for audit capture.
func voidReceiptReverseJEAndMovements(tx *gorm.DB, companyID uint, receipt models.Receipt) (*uint, error) {
	if receipt.JournalEntryID == nil {
		return nil, nil
	}

	// Load original JE with lines for the debit/credit swap.
	var origJE models.JournalEntry
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", *receipt.JournalEntryID, companyID).
		First(&origJE).Error; err != nil {
		return nil, fmt.Errorf("load original receipt JE: %w", err)
	}
	if len(origJE.Lines) == 0 {
		return nil, fmt.Errorf("original receipt JE %d has no lines", origJE.ID)
	}

	// Reversal JE header.
	reversalJE := models.JournalEntry{
		CompanyID:      companyID,
		EntryDate:      origJE.EntryDate,
		JournalNo:      "VOID-" + receipt.ReceiptNumber,
		ReversedFromID: &origJE.ID,
		Status:         models.JournalEntryStatusPosted,
		SourceType:     models.LedgerSourceReversal,
		SourceID:       receipt.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal receipt JE"); err != nil {
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

	// Mark original JE reversed.
	if err := tx.Model(&models.JournalEntry{}).
		Where("id = ? AND company_id = ?", origJE.ID, companyID).
		Update("status", models.JournalEntryStatusReversed).Error; err != nil {
		return nil, fmt.Errorf("mark original JE reversed: %w", err)
	}
	// Flip original ledger entries + project reversal.
	if err := MarkLedgerEntriesReversed(tx, companyID, origJE.ID); err != nil {
		return nil, fmt.Errorf("mark ledger entries reversed: %w", err)
	}
	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: reversalJE,
		Lines:        createdRevLines,
		SourceType:   models.LedgerSourceReversal,
		SourceID:     receipt.ID,
	}); err != nil {
		return nil, fmt.Errorf("project reversal to ledger: %w", err)
	}

	// Reverse inventory movements (receive truth → unwound; inventory
	// module internally unwinds balances and cost layers).
	if err := ReverseReceiptMovements(tx, companyID, receipt); err != nil {
		return nil, fmt.Errorf("reverse receipt movements: %w", err)
	}

	return &reversalJE.ID, nil
}
