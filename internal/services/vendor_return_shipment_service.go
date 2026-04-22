// 遵循project_guide.md
package services

// vendor_return_shipment_service.go — Phase I slice I.6b.2: CRUD +
// lifecycle for the AP-return physical-truth document (we ship
// stock back to a vendor).
//
// Scope lock (I.6b.2)
// -------------------
// This file implements the document-layer surface AND the
// inventory / JE wiring for VendorReturnShipment:
//
//   - Create / Get / Update (draft) / List / Delete (draft) —
//     document persistence with Q8 save-time VCN-link enforcement.
//   - Post — rail-aware. Under `companies.receipt_required=true`,
//     runs `inventory.IssueVendorReturn` (narrow verb from I.6b.2a)
//     at the TRACED original-receipt cost and books the
//     Dr AP / Cr Inventory JE. Under `receipt_required=false`
//     (legacy), it is a pure status flip (IN.6a's VCN retains
//     movement ownership — VRS is optional under legacy per
//     charter §3.4).
//   - Void — Q5 **document-local** reversal. Reverses ONLY its own
//     inventory_movements rows and own JE. Does NOT cascade to the
//     paired VendorCreditNote.
//
// Accounting asymmetry vs AR side (intentional)
// ---------------------------------------------
// On the AR side (I.6a), ARR books the physical leg (Dr Inventory /
// Cr COGS) and CN books the financial leg (Dr Revenue / Cr AR).
// That split works because the original sale has 4 distinct legs
// across two pairs of accounts (Inventory+COGS, Revenue+AR).
//
// On the AP side, the original Bill has 2 legs only
// (Dr Inventory / Cr AP). The reversal naturally self-balances on
// ONE document: VRS books Dr AP + Cr Inventory at traced cost.
// VCN under controlled mode (I.6b.3) becomes a commercial-document
// marker for stock lines — JE effect is owned entirely by VRS.
//
// This is a deliberate break from ARR/CN symmetry, documented
// here and in ledger_entry.go (LedgerSourceVendorReturnShipment
// comment).
//
// What this slice (I.6b.2) does NOT do
// ------------------------------------
// - **VCN controlled-mode retrofit.** Under receipt_required=true,
//   VCN.Post still rejects stock lines with
//   `ErrVendorCreditNoteStockItemRequiresReturnReceipt` (IN.6a
//   behaviour unchanged). Flipping that rejection + requiring
//   exact per-line VRS coverage (Q6) lands in I.6b.3.
// - **Rule4DocVendorReturnShipment dispatch.** The Rule #4
//   movement-owner table flip (VCN surrenders → VRS becomes owner
//   under controlled mode) is I.6b.3's job. I.6b.2 only adds the
//   post-time invariant on VRS itself (owner path).
// - **VCN posted-void symmetry extension.** Today VCN is draft-
//   void only (pre-IN.6a constraint). Extending posted-void with
//   cascade-free reversal (Q5 symmetry) lands in I.6b.3 alongside
//   the VCN retrofit.
// - **UI / shortcut action.** "Create Return to Vendor" button on
//   VCN detail page is I.6b.4.
// - **Pilot enablement / runbook.** I.6b.5.
//
// Identity chain wired at post time
// ---------------------------------
//   BillLine → VendorCreditNoteLine → VendorReturnShipmentLine → inventory_movement
//
// Traced cost: read from the ORIGINAL Bill's inventory_movement
// via VendorCreditNoteLine.OriginalBillLineID (IN.6a field).
// Partial returns supported by construction (rate × qty).
//
// Warehouse choice
// ----------------
// VRS captures WarehouseID on the document header (where the goods
// physically leave from). `inventory.IssueVendorReturn` uses that
// warehouse. This differs from the ORIGINAL Bill's receipt
// warehouse only if goods were transferred between receipt and
// return — rare but possible.
//
// Audit surface
// -------------
// Post and Void each write exactly one audit row:
//   - `vendor_return_shipment.posted`   (draft → posted)
//   - `vendor_return_shipment.voided`   (posted → voided)

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// Error sentinels. Prefixed `ErrVendorReturnShipment*` to namespace
// cleanly from IN.6a's VCN family (`ErrVendorCreditNote*`) and from
// the ARR family (`ErrARReturnReceipt*`).
var (
	ErrVendorReturnShipmentNotFound                = errors.New("vendor_return_shipment: not found")
	ErrVendorReturnShipmentNotDraft                = errors.New("vendor_return_shipment: operation requires status=draft")
	ErrVendorReturnShipmentNotPosted               = errors.New("vendor_return_shipment: void requires status=posted")
	ErrVendorReturnShipmentAlreadyPosted           = errors.New("vendor_return_shipment: post requires status=draft")
	ErrVendorReturnShipmentWarehouseRequired       = errors.New("vendor_return_shipment: WarehouseID required")
	ErrVendorReturnShipmentDateRequired            = errors.New("vendor_return_shipment: ShipDate required")
	ErrVendorReturnShipmentLineProductRequired     = errors.New("vendor_return_shipment: line requires ProductServiceID")
	ErrVendorReturnShipmentVCNRequired             = errors.New("vendor_return_shipment: VendorCreditNoteID required (standalone Return Shipment rejected by charter Q8)")
	ErrVendorReturnShipmentVCNVoided               = errors.New("vendor_return_shipment: linked VendorCreditNote is voided (Q8 requires draft-or-posted)")
	ErrVendorReturnShipmentCrossCompanyReference   = errors.New("vendor_return_shipment: referenced entity belongs to a different company")
	ErrVendorReturnShipmentLineVCNLineMismatch     = errors.New("vendor_return_shipment: line VendorCreditNoteLineID belongs to a different VCN than the header")
	ErrVendorReturnShipmentLineMissingOriginalBill = errors.New("vendor_return_shipment: stock-item line's VendorCreditNoteLine has no OriginalBillLineID — cost trace broken")
	ErrVendorReturnShipmentLineOriginalMovementNotFound = errors.New("vendor_return_shipment: could not locate original bill movement for traced cost")
	ErrVendorReturnShipmentVCNMissingBillAnchor    = errors.New("vendor_return_shipment: linked VendorCreditNote has no BillID — cost trace requires an anchor bill")
)

// ── Input / filter types ─────────────────────────────────────────────────────

type CreateVendorReturnShipmentInput struct {
	CompanyID                  uint
	VendorReturnShipmentNumber string
	VendorID                   *uint
	WarehouseID                uint
	ShipDate                   time.Time
	Memo                       string
	Reference                  string
	VendorCreditNoteID         *uint

	Lines []CreateVendorReturnShipmentLineInput

	Actor       string
	ActorUserID *uuid.UUID
}

type CreateVendorReturnShipmentLineInput struct {
	SortOrder              int
	ProductServiceID       uint
	Description            string
	Qty                    decimal.Decimal
	Unit                   string
	VendorCreditNoteLineID *uint
}

type UpdateVendorReturnShipmentInput struct {
	VendorReturnShipmentNumber *string
	VendorID                   *uint
	WarehouseID                *uint
	ShipDate                   *time.Time
	Memo                       *string
	Reference                  *string
	VendorCreditNoteID         *uint
	Lines                      []CreateVendorReturnShipmentLineInput
	ReplaceLines               bool
}

type ListVendorReturnShipmentsFilter struct {
	Status             models.VendorReturnShipmentStatus
	FromDate           *time.Time
	ToDate             *time.Time
	VendorID           *uint
	VendorCreditNoteID *uint
	Limit              int
	Offset             int
}

// ── CRUD ─────────────────────────────────────────────────────────────────────

func CreateVendorReturnShipment(db *gorm.DB, in CreateVendorReturnShipmentInput) (*models.VendorReturnShipment, error) {
	if in.CompanyID == 0 {
		return nil, fmt.Errorf("services.CreateVendorReturnShipment: CompanyID required")
	}
	if in.WarehouseID == 0 {
		return nil, ErrVendorReturnShipmentWarehouseRequired
	}
	if in.ShipDate.IsZero() {
		return nil, ErrVendorReturnShipmentDateRequired
	}
	if in.VendorCreditNoteID == nil || *in.VendorCreditNoteID == 0 {
		return nil, ErrVendorReturnShipmentVCNRequired
	}
	for i, ln := range in.Lines {
		if ln.ProductServiceID == 0 {
			return nil, fmt.Errorf("%w: line[%d]", ErrVendorReturnShipmentLineProductRequired, i)
		}
	}

	var created models.VendorReturnShipment
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := validateVRSHeaderScope(tx, in.CompanyID,
			in.VendorID, in.WarehouseID, in.VendorCreditNoteID); err != nil {
			return err
		}
		if err := validateVRSLinesScope(tx, in.CompanyID,
			*in.VendorCreditNoteID, in.Lines); err != nil {
			return err
		}

		r := models.VendorReturnShipment{
			CompanyID:                  in.CompanyID,
			VendorReturnShipmentNumber: in.VendorReturnShipmentNumber,
			VendorID:                   in.VendorID,
			WarehouseID:                in.WarehouseID,
			ShipDate:                   in.ShipDate,
			Status:                     models.VendorReturnShipmentStatusDraft,
			Memo:                       in.Memo,
			Reference:                  in.Reference,
			VendorCreditNoteID:         in.VendorCreditNoteID,
		}
		if err := tx.Create(&r).Error; err != nil {
			return fmt.Errorf("create vendor_return_shipment: %w", err)
		}

		for _, ln := range in.Lines {
			rl := models.VendorReturnShipmentLine{
				CompanyID:              in.CompanyID,
				VendorReturnShipmentID: r.ID,
				SortOrder:              ln.SortOrder,
				ProductServiceID:       ln.ProductServiceID,
				Description:            ln.Description,
				Qty:                    ln.Qty,
				Unit:                   ln.Unit,
				VendorCreditNoteLineID: ln.VendorCreditNoteLineID,
			}
			if err := tx.Create(&rl).Error; err != nil {
				return fmt.Errorf("create vendor_return_shipment line: %w", err)
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

func GetVendorReturnShipment(db *gorm.DB, companyID, id uint) (*models.VendorReturnShipment, error) {
	var r models.VendorReturnShipment
	err := db.Preload("Lines").
		Where("company_id = ? AND id = ?", companyID, id).
		First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorReturnShipmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor_return_shipment: %w", err)
	}
	return &r, nil
}

func UpdateVendorReturnShipment(db *gorm.DB, companyID, id uint, in UpdateVendorReturnShipmentInput) (*models.VendorReturnShipment, error) {
	var updated models.VendorReturnShipment
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadVRSForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.VendorReturnShipmentStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrVendorReturnShipmentNotDraft, r.Status)
		}

		if in.VendorReturnShipmentNumber != nil {
			r.VendorReturnShipmentNumber = *in.VendorReturnShipmentNumber
		}
		if in.VendorID != nil {
			r.VendorID = in.VendorID
		}
		if in.WarehouseID != nil {
			if *in.WarehouseID == 0 {
				return ErrVendorReturnShipmentWarehouseRequired
			}
			r.WarehouseID = *in.WarehouseID
		}
		if in.ShipDate != nil {
			if in.ShipDate.IsZero() {
				return ErrVendorReturnShipmentDateRequired
			}
			r.ShipDate = *in.ShipDate
		}
		if in.Memo != nil {
			r.Memo = *in.Memo
		}
		if in.Reference != nil {
			r.Reference = *in.Reference
		}
		if in.VendorCreditNoteID != nil {
			if *in.VendorCreditNoteID == 0 {
				return ErrVendorReturnShipmentVCNRequired
			}
			r.VendorCreditNoteID = in.VendorCreditNoteID
		}

		if r.VendorCreditNoteID == nil || *r.VendorCreditNoteID == 0 {
			return ErrVendorReturnShipmentVCNRequired
		}

		if err := validateVRSHeaderScope(tx, companyID,
			r.VendorID, r.WarehouseID, r.VendorCreditNoteID); err != nil {
			return err
		}
		if in.ReplaceLines {
			if err := validateVRSLinesScope(tx, companyID,
				*r.VendorCreditNoteID, in.Lines); err != nil {
				return err
			}
		}

		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save vendor_return_shipment: %w", err)
		}

		if in.ReplaceLines {
			if err := tx.Where("vendor_return_shipment_id = ?", r.ID).
				Delete(&models.VendorReturnShipmentLine{}).Error; err != nil {
				return fmt.Errorf("delete old lines: %w", err)
			}
			for _, ln := range in.Lines {
				if ln.ProductServiceID == 0 {
					return ErrVendorReturnShipmentLineProductRequired
				}
				rl := models.VendorReturnShipmentLine{
					CompanyID:              companyID,
					VendorReturnShipmentID: r.ID,
					SortOrder:              ln.SortOrder,
					ProductServiceID:       ln.ProductServiceID,
					Description:            ln.Description,
					Qty:                    ln.Qty,
					Unit:                   ln.Unit,
					VendorCreditNoteLineID: ln.VendorCreditNoteLineID,
				}
				if err := tx.Create(&rl).Error; err != nil {
					return fmt.Errorf("create vendor_return_shipment line: %w", err)
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

func ListVendorReturnShipments(db *gorm.DB, companyID uint, filter ListVendorReturnShipmentsFilter) ([]models.VendorReturnShipment, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("services.ListVendorReturnShipments: CompanyID required")
	}
	q := db.Model(&models.VendorReturnShipment{}).
		Where("company_id = ?", companyID)
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.FromDate != nil {
		q = q.Where("ship_date >= ?", *filter.FromDate)
	}
	if filter.ToDate != nil {
		q = q.Where("ship_date <= ?", *filter.ToDate)
	}
	if filter.VendorID != nil {
		q = q.Where("vendor_id = ?", *filter.VendorID)
	}
	if filter.VendorCreditNoteID != nil {
		q = q.Where("vendor_credit_note_id = ?", *filter.VendorCreditNoteID)
	}
	q = q.Order("ship_date DESC, id DESC")
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}
	var rows []models.VendorReturnShipment
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list vendor_return_shipments: %w", err)
	}
	return rows, nil
}

// PostVendorReturnShipment flips draft → posted. Rail-aware — under
// receipt_required=true runs the full outflow + JE; under =false
// it's a status-flip only.
func PostVendorReturnShipment(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.VendorReturnShipment, error) {
	var out models.VendorReturnShipment
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadVRSForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.VendorReturnShipmentStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrVendorReturnShipmentAlreadyPosted, r.Status)
		}

		var company models.Company
		if err := tx.Where("id = ?", companyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		if err := tx.Preload("Lines.ProductService").
			Preload("Lines.VendorCreditNoteLine").
			Preload("VendorCreditNote").
			First(r, r.ID).Error; err != nil {
			return fmt.Errorf("preload vendor_return_shipment: %w", err)
		}

		var postedJEID *uint
		if company.ReceiptRequired {
			jeID, err := postVRSOutflowTruthAndJE(tx, companyID, *r)
			if err != nil {
				return err
			}
			postedJEID = jeID
		}

		now := time.Now().UTC()
		r.Status = models.VendorReturnShipmentStatusPosted
		r.PostedAt = &now
		r.JournalEntryID = postedJEID
		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save vendor_return_shipment: %w", err)
		}

		// I.6b.2 Rule #4 post-time invariant. Under
		// receipt_required=true VRS IS the movement owner
		// (Rule4DocVendorReturnShipment — dispatch added in I.6b.3;
		// for I.6b.2 the dispatch table still returns false here so
		// this assertion is effectively defensive / forward-looking).
		stockLineCount := 0
		for _, ln := range r.Lines {
			if ln.ProductService != nil && ln.ProductService.IsStockItem {
				stockLineCount++
			}
		}
		if err := AssertRule4PostTimeInvariant(tx, companyID,
			Rule4DocVendorReturnShipment, r.ID, stockLineCount,
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
			"vendor_return_shipment.posted",
			"vendor_return_shipment",
			r.ID,
			actorOrSystem(actor),
			map[string]any{
				"vendor_return_shipment_number": r.VendorReturnShipmentNumber,
				"vendor_credit_note_id":         nilableUintAsAny(r.VendorCreditNoteID),
				"journal_entry_id":              nilableUintAsAny(postedJEID),
				"receipt_required":              company.ReceiptRequired,
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.VendorReturnShipmentStatusDraft)},
			map[string]any{"status": string(models.VendorReturnShipmentStatusPosted)},
		)
		out = *r
		return tx.Preload("Lines").First(&out, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// VoidVendorReturnShipment flips posted → voided. Document-local per
// charter Q5 — reverses ONLY this document's movements + own JE.
func VoidVendorReturnShipment(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.VendorReturnShipment, error) {
	var out models.VendorReturnShipment
	err := db.Transaction(func(tx *gorm.DB) error {
		r, err := loadVRSForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.VendorReturnShipmentStatusPosted {
			return fmt.Errorf("%w: current=%s", ErrVendorReturnShipmentNotPosted, r.Status)
		}

		var reversedJEID *uint
		if r.JournalEntryID != nil {
			if err := tx.Preload("Lines.ProductService").
				First(r, r.ID).Error; err != nil {
				return fmt.Errorf("preload vendor_return_shipment for void: %w", err)
			}
			jeID, err := voidVRSReverseJEAndMovements(tx, companyID, *r)
			if err != nil {
				return err
			}
			reversedJEID = jeID
		}

		now := time.Now().UTC()
		r.Status = models.VendorReturnShipmentStatusVoided
		r.VoidedAt = &now
		if err := tx.Save(r).Error; err != nil {
			return fmt.Errorf("save vendor_return_shipment: %w", err)
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"vendor_return_shipment.voided",
			"vendor_return_shipment",
			r.ID,
			actorOrSystem(actor),
			map[string]any{
				"vendor_return_shipment_number": r.VendorReturnShipmentNumber,
				"original_je_id":                nilableUintAsAny(r.JournalEntryID),
				"reversal_je_id":                nilableUintAsAny(reversedJEID),
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.VendorReturnShipmentStatusPosted)},
			map[string]any{"status": string(models.VendorReturnShipmentStatusVoided)},
		)
		out = *r
		return tx.Preload("Lines").First(&out, r.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func DeleteVendorReturnShipment(db *gorm.DB, companyID, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		r, err := loadVRSForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if r.Status != models.VendorReturnShipmentStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrVendorReturnShipmentNotDraft, r.Status)
		}
		if err := tx.Where("vendor_return_shipment_id = ?", r.ID).
			Delete(&models.VendorReturnShipmentLine{}).Error; err != nil {
			return fmt.Errorf("delete vendor_return_shipment lines: %w", err)
		}
		if err := tx.Delete(r).Error; err != nil {
			return fmt.Errorf("delete vendor_return_shipment: %w", err)
		}
		return nil
	})
}

// ── Scope validation ─────────────────────────────────────────────────────────

func validateVRSHeaderScope(tx *gorm.DB, companyID uint, vendorID *uint, warehouseID uint, vcnID *uint) error {
	if vendorID != nil && *vendorID != 0 {
		if err := requireVRSSameCompany(tx, &models.Vendor{}, "vendor",
			*vendorID, companyID); err != nil {
			return err
		}
	}
	if warehouseID != 0 {
		if err := requireVRSSameCompany(tx, &models.Warehouse{}, "warehouse",
			warehouseID, companyID); err != nil {
			return err
		}
	}
	if vcnID != nil && *vcnID != 0 {
		if err := requireVRSSameCompany(tx, &models.VendorCreditNote{}, "vendor_credit_note",
			*vcnID, companyID); err != nil {
			return err
		}
		var status string
		if err := tx.Model(&models.VendorCreditNote{}).
			Select("status").
			Where("id = ?", *vcnID).
			Limit(1).
			Scan(&status).Error; err != nil {
			return fmt.Errorf("load vendor_credit_note status: %w", err)
		}
		if status == string(models.VendorCreditNoteStatusVoided) {
			return fmt.Errorf("%w: vendor_credit_note id=%d status=%s",
				ErrVendorReturnShipmentVCNVoided, *vcnID, status)
		}
	}
	return nil
}

func validateVRSLinesScope(tx *gorm.DB, companyID uint, vcnID uint, lines []CreateVendorReturnShipmentLineInput) error {
	for i, ln := range lines {
		if ln.ProductServiceID != 0 {
			if err := requireVRSSameCompany(tx, &models.ProductService{}, "product_service",
				ln.ProductServiceID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
		}
		if ln.VendorCreditNoteLineID != nil && *ln.VendorCreditNoteLineID != 0 {
			if err := requireVRSSameCompany(tx, &models.VendorCreditNoteLine{}, "vendor_credit_note_line",
				*ln.VendorCreditNoteLineID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
			var parentVCN uint
			if err := tx.Model(&models.VendorCreditNoteLine{}).
				Select("vendor_credit_note_id").
				Where("id = ?", *ln.VendorCreditNoteLineID).
				Limit(1).
				Scan(&parentVCN).Error; err != nil {
				return fmt.Errorf("line[%d]: load vcn_line parent: %w", i, err)
			}
			if parentVCN != vcnID {
				return fmt.Errorf("%w: line[%d] vcn_line id=%d belongs to vcn=%d, header vcn=%d",
					ErrVendorReturnShipmentLineVCNLineMismatch, i,
					*ln.VendorCreditNoteLineID, parentVCN, vcnID)
			}
		}
	}
	return nil
}

func requireVRSSameCompany(tx *gorm.DB, model any, entity string, id, companyID uint) error {
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
		return fmt.Errorf("%w: %s id=%d not found", ErrVendorReturnShipmentNotFound, entity, id)
	}
	if found != companyID {
		return fmt.Errorf("%w: %s id=%d belongs to company=%d, vendor_return_shipment company=%d",
			ErrVendorReturnShipmentCrossCompanyReference, entity, id, found, companyID)
	}
	return nil
}

func loadVRSForUpdate(tx *gorm.DB, companyID, id uint) (*models.VendorReturnShipment, error) {
	var r models.VendorReturnShipment
	err := applyLockForUpdate(tx.Where("company_id = ? AND id = ?", companyID, id)).
		First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorReturnShipmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor_return_shipment: %w", err)
	}
	return &r, nil
}

// voidVRSReverseJEAndMovements reverses the JE + inventory movements
// for a previously-posted VRS. Document-local per charter Q5.
func voidVRSReverseJEAndMovements(tx *gorm.DB, companyID uint, r models.VendorReturnShipment) (*uint, error) {
	if r.JournalEntryID == nil {
		return nil, nil
	}

	var origJE models.JournalEntry
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", *r.JournalEntryID, companyID).
		First(&origJE).Error; err != nil {
		return nil, fmt.Errorf("load original vendor_return_shipment JE: %w", err)
	}
	if len(origJE.Lines) == 0 {
		return nil, fmt.Errorf("original vendor_return_shipment JE %d has no lines", origJE.ID)
	}

	reversalJE := models.JournalEntry{
		CompanyID:      companyID,
		EntryDate:      origJE.EntryDate,
		JournalNo:      "VOID-" + r.VendorReturnShipmentNumber,
		ReversedFromID: &origJE.ID,
		Status:         models.JournalEntryStatusPosted,
		SourceType:     models.LedgerSourceReversal,
		SourceID:       r.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal vendor_return_shipment JE"); err != nil {
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

	if err := ReverseVRSMovements(tx, companyID, r); err != nil {
		return nil, fmt.Errorf("reverse vendor_return_shipment movements: %w", err)
	}

	return &reversalJE.ID, nil
}
