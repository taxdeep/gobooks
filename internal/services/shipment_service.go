// 遵循project_guide.md
package services

// shipment_service.go — Phase I slice I.2 CRUD + lifecycle for the
// outbound Shipment document (sell-side mirror of receipt_service.go
// at its H.2 shape).
//
// Scope lock (I.2)
// ----------------
// This file implements the document-layer surface for Shipment: it
// persists, reads, updates (draft only), lists, and flips status
// (draft→posted, posted→voided). It does NOT:
//
//   - Produce inventory movements
//   - Consume cost layers / read moving-average cost
//   - Call IssueStock / any inventory OUT verb
//   - Write a JournalEntry
//   - Touch COGS / Inventory accounts
//   - Create a waiting_for_invoice operational item
//   - Read companies.shipment_required
//   - Couple with Invoice or SalesOrder beyond storing reservation IDs
//   - Enforce source-identity linkage (SO references are stored only)
//
// PostShipment / VoidShipment in I.2 are pure status flips with audit.
// The consumer that turns Post into actual issue truth + COGS JE is
// IssueStockFromShipment in I.3; until then, posting a Shipment
// leaves inventory_* tables and the GL completely untouched. Tests
// in shipment_service_test.go lock this boundary at the CI level —
// any accidental I.3 slip shows up as a failed "no inventory effect"
// assertion.
//
// Audit surface
// -------------
// Post and Void each write exactly one audit row. Actions:
//   - `shipment.posted`   (draft → posted)
//   - `shipment.voided`   (posted → voided)
// Create / Update / Delete do NOT write audit in I.2 — they are
// standard document-layer CRUD on a pre-posting draft, with no
// cross-module state change worth recording. If a later slice needs
// draft-level audit (e.g. for regulated-industry compliance), it can
// bolt on without reshaping this surface.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// Error sentinels for the outbound Shipment document. Named plainly
// (`ErrShipment*`) because there is no existing Shipment-domain name
// to collide with — unlike Phase H's Receipt which had to namespace
// away from the AR-side CustomerReceipt family.
var (
	// ErrShipmentNotFound — lookup by (CompanyID, ID) missed.
	// Returned wrapped for errors.Is matching; handlers map to 404.
	ErrShipmentNotFound = errors.New("shipment: not found")

	// ErrShipmentNotDraft — attempted a draft-only operation
	// (update, delete) on a shipment that has moved out of draft.
	ErrShipmentNotDraft = errors.New("shipment: operation requires status=draft")

	// ErrShipmentNotPosted — attempted to void a shipment that is
	// not currently in posted status.
	ErrShipmentNotPosted = errors.New("shipment: void requires status=posted")

	// ErrShipmentAlreadyPosted — attempted to post a shipment that
	// is not in draft status (already posted, or already voided).
	ErrShipmentAlreadyPosted = errors.New("shipment: post requires status=draft")

	// ErrShipmentWarehouseRequired — a shipment has to leave from
	// somewhere; WarehouseID is required on create.
	ErrShipmentWarehouseRequired = errors.New("shipment: WarehouseID required")

	// ErrShipmentDateRequired — ShipDate required so that back-dated
	// shipments are deliberate rather than silent.
	ErrShipmentDateRequired = errors.New("shipment: ShipDate required")

	// ErrShipmentLineProductRequired — every line names a
	// product/service.
	ErrShipmentLineProductRequired = errors.New("shipment: line requires ProductServiceID")

	// ErrShipmentCrossCompanyReference — an ID on the input
	// (customer / warehouse / product_service / sales_order /
	// sales_order_line) resolves to a row that belongs to a
	// different company than the shipment being created or updated.
	// Rejected before any write so that the Shipment document cannot
	// establish a cross-tenant reference that I.3 / I.5 would later
	// have to detect after the fact. No FK constraint enforces this
	// at the schema layer (SO reservation is accepted without FK per
	// the I.2 scope lock), so the service layer is the boundary.
	ErrShipmentCrossCompanyReference = errors.New("shipment: referenced entity belongs to a different company")
)

// CreateShipmentInput captures the fields a caller may set when
// creating a new Shipment in draft state. Fields not listed here are
// populated by the service (ID, CreatedAt/UpdatedAt, Status='draft').
type CreateShipmentInput struct {
	CompanyID      uint
	ShipmentNumber string
	CustomerID     *uint
	WarehouseID    uint
	ShipDate       time.Time
	Memo           string
	Reference      string
	SalesOrderID   *uint

	Lines []CreateShipmentLineInput

	// Audit actor (only consumed by Post/Void; stored here for
	// callers that prefer to thread actor through the input struct
	// uniformly).
	Actor       string
	ActorUserID *uuid.UUID
}

// CreateShipmentLineInput captures the fields a caller may set on a
// shipment line at creation time. Note the absence of UnitCost — see
// the file-level comment and models/shipment.go on why outbound cost
// is authoritative from the inventory module, never declared here.
type CreateShipmentLineInput struct {
	SortOrder        int
	ProductServiceID uint
	Description      string
	Qty              decimal.Decimal
	Unit             string
	SalesOrderLineID *uint
}

// UpdateShipmentInput captures the mutable fields on a draft Shipment.
// Lines are replaced wholesale when ReplaceLines is true (nil Lines
// with ReplaceLines=true clears all lines); ReplaceLines=false leaves
// lines untouched regardless of the Lines field. Status is not a
// mutable field — Post/Void own the status transitions.
type UpdateShipmentInput struct {
	ShipmentNumber *string
	CustomerID     *uint
	WarehouseID    *uint
	ShipDate       *time.Time
	Memo           *string
	Reference      *string
	SalesOrderID   *uint
	Lines          []CreateShipmentLineInput
	ReplaceLines   bool
}

// ListShipmentsFilter narrows a company's shipments by status and
// date window. All fields optional; zero-values mean "unfiltered on
// this dimension".
type ListShipmentsFilter struct {
	Status     ShipmentStatus
	FromDate   *time.Time
	ToDate     *time.Time
	CustomerID *uint
	Limit      int
	Offset     int
}

// ShipmentStatus mirrors models.ShipmentStatus to keep service callers
// from needing to import models directly for the filter struct.
type ShipmentStatus = models.ShipmentStatus

// CreateShipment persists a new Shipment and its lines in draft state.
// Runs in a single transaction. Returns the created Shipment with
// lines populated.
func CreateShipment(db *gorm.DB, in CreateShipmentInput) (*models.Shipment, error) {
	if in.CompanyID == 0 {
		return nil, fmt.Errorf("services.CreateShipment: CompanyID required")
	}
	if in.WarehouseID == 0 {
		return nil, ErrShipmentWarehouseRequired
	}
	if in.ShipDate.IsZero() {
		return nil, ErrShipmentDateRequired
	}
	for i, ln := range in.Lines {
		if ln.ProductServiceID == 0 {
			return nil, fmt.Errorf("%w: line[%d]", ErrShipmentLineProductRequired, i)
		}
	}

	var created models.Shipment
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := validateShipmentHeaderScope(tx, in.CompanyID,
			in.CustomerID, in.WarehouseID, in.SalesOrderID); err != nil {
			return err
		}
		if err := validateShipmentLinesScope(tx, in.CompanyID, in.Lines); err != nil {
			return err
		}

		s := models.Shipment{
			CompanyID:      in.CompanyID,
			ShipmentNumber: in.ShipmentNumber,
			CustomerID:     in.CustomerID,
			WarehouseID:    in.WarehouseID,
			ShipDate:       in.ShipDate,
			Status:         models.ShipmentStatusDraft,
			Memo:           in.Memo,
			Reference:      in.Reference,
			SalesOrderID:   in.SalesOrderID,
		}
		if err := tx.Create(&s).Error; err != nil {
			return fmt.Errorf("create shipment: %w", err)
		}

		for _, ln := range in.Lines {
			sl := models.ShipmentLine{
				CompanyID:        in.CompanyID,
				ShipmentID:       s.ID,
				SortOrder:        ln.SortOrder,
				ProductServiceID: ln.ProductServiceID,
				Description:      ln.Description,
				Qty:              ln.Qty,
				Unit:             ln.Unit,
				SalesOrderLineID: ln.SalesOrderLineID,
			}
			if err := tx.Create(&sl).Error; err != nil {
				return fmt.Errorf("create shipment line: %w", err)
			}
		}
		created = s
		return tx.Preload("Lines").First(&created, s.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// GetShipment loads a Shipment by (CompanyID, ID) with its lines
// preloaded. The company scope is enforced — a shipment from a
// different company returns ErrShipmentNotFound even if the ID
// matches, preventing cross-tenant leakage.
func GetShipment(db *gorm.DB, companyID, id uint) (*models.Shipment, error) {
	var s models.Shipment
	err := db.Preload("Lines").
		Where("company_id = ? AND id = ?", companyID, id).
		First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrShipmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load shipment: %w", err)
	}
	return &s, nil
}

// UpdateShipment mutates a draft Shipment. Any field left nil on the
// input is preserved. Post-draft shipments are refused with
// ErrShipmentNotDraft — the state machine guards that editing a
// posted or voided document requires its own reversal path.
func UpdateShipment(db *gorm.DB, companyID, id uint, in UpdateShipmentInput) (*models.Shipment, error) {
	var updated models.Shipment
	err := db.Transaction(func(tx *gorm.DB) error {
		s, err := loadShipmentForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if s.Status != models.ShipmentStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrShipmentNotDraft, s.Status)
		}

		if in.ShipmentNumber != nil {
			s.ShipmentNumber = *in.ShipmentNumber
		}
		if in.CustomerID != nil {
			s.CustomerID = in.CustomerID
		}
		if in.WarehouseID != nil {
			if *in.WarehouseID == 0 {
				return ErrShipmentWarehouseRequired
			}
			s.WarehouseID = *in.WarehouseID
		}
		if in.ShipDate != nil {
			if in.ShipDate.IsZero() {
				return ErrShipmentDateRequired
			}
			s.ShipDate = *in.ShipDate
		}
		if in.Memo != nil {
			s.Memo = *in.Memo
		}
		if in.Reference != nil {
			s.Reference = *in.Reference
		}
		if in.SalesOrderID != nil {
			s.SalesOrderID = in.SalesOrderID
		}

		if err := validateShipmentHeaderScope(tx, companyID,
			s.CustomerID, s.WarehouseID, s.SalesOrderID); err != nil {
			return err
		}
		if in.ReplaceLines {
			if err := validateShipmentLinesScope(tx, companyID, in.Lines); err != nil {
				return err
			}
		}

		if err := tx.Save(s).Error; err != nil {
			return fmt.Errorf("save shipment: %w", err)
		}

		if in.ReplaceLines {
			if err := tx.Where("shipment_id = ?", s.ID).
				Delete(&models.ShipmentLine{}).Error; err != nil {
				return fmt.Errorf("delete old lines: %w", err)
			}
			for _, ln := range in.Lines {
				if ln.ProductServiceID == 0 {
					return ErrShipmentLineProductRequired
				}
				sl := models.ShipmentLine{
					CompanyID:        companyID,
					ShipmentID:       s.ID,
					SortOrder:        ln.SortOrder,
					ProductServiceID: ln.ProductServiceID,
					Description:      ln.Description,
					Qty:              ln.Qty,
					Unit:             ln.Unit,
					SalesOrderLineID: ln.SalesOrderLineID,
				}
				if err := tx.Create(&sl).Error; err != nil {
					return fmt.Errorf("create shipment line: %w", err)
				}
			}
		}

		updated = *s
		return tx.Preload("Lines").First(&updated, s.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// ListShipments returns a company's shipments ordered by ShipDate
// descending, then ID descending. Lines are NOT preloaded — the list
// surface is header-level. Callers needing line data call GetShipment
// per row.
func ListShipments(db *gorm.DB, companyID uint, filter ListShipmentsFilter) ([]models.Shipment, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("services.ListShipments: CompanyID required")
	}
	q := db.Model(&models.Shipment{}).
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
	if filter.CustomerID != nil {
		q = q.Where("customer_id = ?", *filter.CustomerID)
	}
	q = q.Order("ship_date DESC, id DESC")
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}
	var rows []models.Shipment
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list shipments: %w", err)
	}
	return rows, nil
}

// PostShipment flips a draft Shipment to posted and, under Phase I
// Shipment-first semantics (companies.shipment_required=true), drives
// the full Shipment → issue truth → inventory effect chain, books the
// business-document-layer JE (Dr COGS / Cr Inventory per line), and
// creates waiting_for_invoice queue items for every stock-item line.
//
// Flag gating (I.3):
//   - shipment_required=false (legacy default): status flip + audit
//     only. Byte-identical to I.2 behavior. Legacy companies continue
//     on the Invoice-forms-COGS path; Shipment under flag=false is
//     effectively a dress-rehearsal document with no system truth.
//   - shipment_required=true: the full Shipment-first flow runs.
//     Issue truth lands as inventory_movements rows with
//     source_type='shipment'; inventory effect propagates through the
//     inventory module's standard cost-layer / balance machinery;
//     the JE is constructed from the base-currency values returned
//     by inventory.IssueStock and linked back via
//     shipments.journal_entry_id; one waiting_for_invoice row is
//     inserted per stock-item line for I.5's Invoice match to close.
//
// Writes exactly one audit row (`shipment.posted`) in both branches.
func PostShipment(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.Shipment, error) {
	var out models.Shipment
	err := db.Transaction(func(tx *gorm.DB) error {
		s, err := loadShipmentForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if s.Status != models.ShipmentStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrShipmentAlreadyPosted, s.Status)
		}

		// Re-read the company inside the tx to pick up the latest
		// shipment_required flag (its admin surface commits before
		// PostShipment begins; read-then-act is safe inside this tx).
		var company models.Company
		if err := tx.Where("id = ?", companyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		// Preload lines with ProductService resolved so
		// CreateShipmentMovements / JE builder can read inventory /
		// COGS accounts without a second round trip.
		if err := tx.Preload("Lines.ProductService").
			First(s, s.ID).Error; err != nil {
			return fmt.Errorf("preload shipment: %w", err)
		}

		var postedJEID *uint
		if company.ShipmentRequired {
			jeID, err := postShipmentIssueTruthAndJE(tx, companyID, *s)
			if err != nil {
				return err
			}
			postedJEID = jeID
		}

		now := time.Now().UTC()
		s.Status = models.ShipmentStatusPosted
		s.PostedAt = &now
		s.JournalEntryID = postedJEID
		if err := tx.Save(s).Error; err != nil {
			return fmt.Errorf("save shipment: %w", err)
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"shipment.posted",
			"shipment",
			s.ID,
			actorOrSystem(actor),
			map[string]any{
				"shipment_number":    s.ShipmentNumber,
				"journal_entry_id":   nilableUintAsAny(postedJEID),
				"shipment_required":  company.ShipmentRequired,
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ShipmentStatusDraft)},
			map[string]any{"status": string(models.ShipmentStatusPosted)},
		)
		out = *s
		return tx.Preload("Lines").First(&out, s.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// postShipmentIssueTruthAndJE runs the I.3 flag=true branch of
// PostShipment: project shipment lines to issue-truth via
// CreateShipmentMovements, build the JE (Dr COGS / Cr Inventory per
// line from authoritative cost), post it through the standard
// journal pipeline, and create one waiting_for_invoice row per
// stock-item line. Returns the created JournalEntry ID (may be nil
// when the shipment has no stock-item lines — legit: a shipment
// of pure services has no issue truth to book).
func postShipmentIssueTruthAndJE(tx *gorm.DB, companyID uint, shipment models.Shipment) (*uint, error) {
	results, err := CreateShipmentMovements(tx, shipment)
	if err != nil {
		return nil, fmt.Errorf("create shipment movements: %w", err)
	}
	if len(results) == 0 {
		// No stock lines → no inventory effect → no JE, no WFI row.
		return nil, nil
	}

	frags, err := buildShipmentPostingFragments(results)
	if err != nil {
		return nil, err
	}
	if len(frags) == 0 {
		// All results contributed zero-cost fragments (unlikely but
		// possible under degenerate FIFO state) — skip JE creation
		// but still create WFI rows: goods left the warehouse even
		// if the peeled cost rounded to zero.
		if err := CreateWaitingForInvoiceItems(tx, shipment, results); err != nil {
			return nil, err
		}
		return nil, nil
	}
	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return nil, fmt.Errorf("aggregate journal lines: %w", err)
	}
	debitSum := sumPostingDebits(jeLines)
	creditSum := sumPostingCredits(jeLines)
	if !debitSum.Equal(creditSum) {
		return nil, fmt.Errorf(
			"shipment JE imbalance: debit %s, credit %s",
			debitSum.StringFixed(2), creditSum.StringFixed(2),
		)
	}

	je := models.JournalEntry{
		CompanyID:  companyID,
		EntryDate:  shipment.ShipDate,
		JournalNo:  shipment.ShipmentNumber,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceShipment,
		SourceID:   shipment.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&je).Error, "create shipment journal entry"); err != nil {
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
		// Customer linkage (if set) carried at party level so the
		// COGS/Inventory lines trace back to the buyer on drilldown.
		if shipment.CustomerID != nil && *shipment.CustomerID != 0 {
			line.PartyType = models.PartyTypeCustomer
			line.PartyID = *shipment.CustomerID
		}
		if err := tx.Create(&line).Error; err != nil {
			return nil, fmt.Errorf("create shipment journal line: %w", err)
		}
		createdLines = append(createdLines, line)
	}

	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: je,
		Lines:        createdLines,
		SourceType:   models.LedgerSourceShipment,
		SourceID:     shipment.ID,
	}); err != nil {
		return nil, fmt.Errorf("project shipment to ledger: %w", err)
	}

	if err := CreateWaitingForInvoiceItems(tx, shipment, results); err != nil {
		return nil, err
	}

	return &je.ID, nil
}

// VoidShipment flips a posted Shipment to voided. When the shipment
// was posted under shipment_required=true (presence of
// journal_entry_id is the signal), also reverses:
//  1. The inventory movements (issue truth) via ReverseShipmentMovements
//     — the inventory module books reversal movements; balances /
//     cost layers unwind internally through the same machinery that
//     supports VoidInvoice.
//  2. The JE — a reversal JE is posted (debit/credit swapped),
//     original JE flips to status=reversed, ledger entries for the
//     original are marked reversed, and the reversal JE is projected
//     to the ledger.
//  3. Every waiting_for_invoice row attached to the shipment flips
//     to status='voided'. Rows already closed by a matching Invoice
//     (I.5) are also voided — voiding the source Shipment invalidates
//     the downstream match by definition.
//
// When the shipment was posted without a JE (flag=false, or flag=true
// with no stock lines), void is a status flip + audit only, matching
// the I.2 behavior.
//
// Writes exactly one audit row (`shipment.voided`) regardless of
// branch.
func VoidShipment(db *gorm.DB, companyID, id uint, actor string, actorUserID *uuid.UUID) (*models.Shipment, error) {
	var out models.Shipment
	err := db.Transaction(func(tx *gorm.DB) error {
		s, err := loadShipmentForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if s.Status != models.ShipmentStatusPosted {
			return fmt.Errorf("%w: current=%s", ErrShipmentNotPosted, s.Status)
		}

		var reversedJEID *uint
		if s.JournalEntryID != nil {
			if err := tx.Preload("Lines.ProductService").
				First(s, s.ID).Error; err != nil {
				return fmt.Errorf("preload shipment for void: %w", err)
			}
			jeID, err := voidShipmentReverseJEAndMovements(tx, companyID, *s)
			if err != nil {
				return err
			}
			reversedJEID = jeID
			if err := VoidWaitingForInvoiceItemsByShipment(tx, companyID, s.ID); err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		s.Status = models.ShipmentStatusVoided
		s.VoidedAt = &now
		if err := tx.Save(s).Error; err != nil {
			return fmt.Errorf("save shipment: %w", err)
		}
		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"shipment.voided",
			"shipment",
			s.ID,
			actorOrSystem(actor),
			map[string]any{
				"shipment_number":  s.ShipmentNumber,
				"original_je_id":   nilableUintAsAny(s.JournalEntryID),
				"reversal_je_id":   nilableUintAsAny(reversedJEID),
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ShipmentStatusPosted)},
			map[string]any{"status": string(models.ShipmentStatusVoided)},
		)
		out = *s
		return tx.Preload("Lines").First(&out, s.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// voidShipmentReverseJEAndMovements reverses the JE and the inventory
// movements associated with a previously-posted Shipment. Mirrors the
// VoidReceipt reversal pattern so that the same void semantics apply
// regardless of which document produced the outbound. Returns the
// reversal JE ID for audit capture.
func voidShipmentReverseJEAndMovements(tx *gorm.DB, companyID uint, shipment models.Shipment) (*uint, error) {
	if shipment.JournalEntryID == nil {
		return nil, nil
	}

	var origJE models.JournalEntry
	if err := tx.Preload("Lines").
		Where("id = ? AND company_id = ?", *shipment.JournalEntryID, companyID).
		First(&origJE).Error; err != nil {
		return nil, fmt.Errorf("load original shipment JE: %w", err)
	}
	if len(origJE.Lines) == 0 {
		return nil, fmt.Errorf("original shipment JE %d has no lines", origJE.ID)
	}

	reversalJE := models.JournalEntry{
		CompanyID:      companyID,
		EntryDate:      origJE.EntryDate,
		JournalNo:      "VOID-" + shipment.ShipmentNumber,
		ReversedFromID: &origJE.ID,
		Status:         models.JournalEntryStatusPosted,
		SourceType:     models.LedgerSourceReversal,
		SourceID:       shipment.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal shipment JE"); err != nil {
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
		SourceID:     shipment.ID,
	}); err != nil {
		return nil, fmt.Errorf("project reversal to ledger: %w", err)
	}

	if err := ReverseShipmentMovements(tx, companyID, shipment); err != nil {
		return nil, fmt.Errorf("reverse shipment movements: %w", err)
	}

	return &reversalJE.ID, nil
}

// DeleteShipment removes a draft shipment and its lines. Non-draft
// shipments (posted, voided) are refused — their trace must stay for
// audit continuity.
func DeleteShipment(db *gorm.DB, companyID, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		s, err := loadShipmentForUpdate(tx, companyID, id)
		if err != nil {
			return err
		}
		if s.Status != models.ShipmentStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrShipmentNotDraft, s.Status)
		}
		if err := tx.Where("shipment_id = ?", s.ID).
			Delete(&models.ShipmentLine{}).Error; err != nil {
			return fmt.Errorf("delete shipment lines: %w", err)
		}
		if err := tx.Delete(s).Error; err != nil {
			return fmt.Errorf("delete shipment: %w", err)
		}
		return nil
	})
}

// validateShipmentHeaderScope verifies that every non-nil reference
// ID on the Shipment header resolves to a row belonging to the same
// company. No FK constraints enforce this at the DB layer (SO is
// reservation-only in I.2, and multi-company joins are legal at the
// schema level for legacy reasons), so the service is the boundary.
//
// Checks:
//   - Customer (optional): customers.company_id == companyID
//   - Warehouse (required): warehouses.company_id == companyID
//   - SalesOrder (optional): sales_orders.company_id == companyID
//
// Returns ErrShipmentCrossCompanyReference on mismatch, wrapped with
// the offending entity name so logs pinpoint the fault.
func validateShipmentHeaderScope(tx *gorm.DB, companyID uint, customerID *uint, warehouseID uint, salesOrderID *uint) error {
	if customerID != nil && *customerID != 0 {
		if err := requireShipmentSameCompany(tx, &models.Customer{}, "customer",
			*customerID, companyID); err != nil {
			return err
		}
	}
	if warehouseID != 0 {
		if err := requireShipmentSameCompany(tx, &models.Warehouse{}, "warehouse",
			warehouseID, companyID); err != nil {
			return err
		}
	}
	if salesOrderID != nil && *salesOrderID != 0 {
		if err := requireShipmentSameCompany(tx, &models.SalesOrder{}, "sales_order",
			*salesOrderID, companyID); err != nil {
			return err
		}
	}
	return nil
}

// validateShipmentLinesScope applies the same company-scope rule to
// each line's referenced IDs. Runs one query per distinct reference
// — small input sets so N+1 is acceptable; optimisable if lines grow
// into the hundreds.
//
// Checks:
//   - ProductService (required): product_services.company_id == companyID
//   - SalesOrderLine (optional): resolved via parent sales_orders.company_id
//     because SalesOrderLine itself does not carry a company_id column
//     in its schema (mirrors the one-sided-join pattern used elsewhere
//     in the sell-side stack).
func validateShipmentLinesScope(tx *gorm.DB, companyID uint, lines []CreateShipmentLineInput) error {
	for i, ln := range lines {
		if ln.ProductServiceID != 0 {
			if err := requireShipmentSameCompany(tx, &models.ProductService{}, "product_service",
				ln.ProductServiceID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
		}
		if ln.SalesOrderLineID != nil && *ln.SalesOrderLineID != 0 {
			if err := requireSalesOrderLineCompany(tx, *ln.SalesOrderLineID, companyID); err != nil {
				return fmt.Errorf("line[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// requireShipmentSameCompany loads the row identified by id and
// confirms its CompanyID matches the expected value. Shipment
// analogue of receipt_service.requireSameCompany; kept file-local so
// the error wrapping can use shipment-specific sentinels without
// cross-domain refactor.
func requireShipmentSameCompany(tx *gorm.DB, model any, entity string, id, companyID uint) error {
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
		return fmt.Errorf("%w: %s id=%d not found", ErrShipmentNotFound, entity, id)
	}
	if found != companyID {
		return fmt.Errorf("%w: %s id=%d belongs to company=%d, shipment company=%d",
			ErrShipmentCrossCompanyReference, entity, id, found, companyID)
	}
	return nil
}

// requireSalesOrderLineCompany verifies a SalesOrderLine's parent SO
// belongs to the expected company. SalesOrderLine has no CompanyID
// column, so the join through sales_orders is the only authoritative
// path. Returns ErrShipmentNotFound if the line is absent or its
// parent missing, ErrShipmentCrossCompanyReference on tenant mismatch.
func requireSalesOrderLineCompany(tx *gorm.DB, lineID, companyID uint) error {
	var found uint
	err := tx.Table("sales_order_lines").
		Select("sales_orders.company_id").
		Joins("JOIN sales_orders ON sales_orders.id = sales_order_lines.sales_order_id").
		Where("sales_order_lines.id = ?", lineID).
		Limit(1).
		Scan(&found).Error
	if err != nil {
		return fmt.Errorf("validate sales_order_line scope: %w", err)
	}
	if found == 0 {
		return fmt.Errorf("%w: sales_order_line id=%d not found", ErrShipmentNotFound, lineID)
	}
	if found != companyID {
		return fmt.Errorf("%w: sales_order_line id=%d belongs to company=%d, shipment company=%d",
			ErrShipmentCrossCompanyReference, lineID, found, companyID)
	}
	return nil
}

// loadShipmentForUpdate fetches a Shipment scoped to the company and
// takes a row-level write lock (`SELECT ... FOR UPDATE` on PostgreSQL;
// no-op on SQLite — test DBs are single-writer anyway). Used by every
// lifecycle-mutating operation (Update, Post, Void, Delete) so
// concurrent flips on the same shipment serialise and cross-state
// races (e.g. two simultaneous PostShipment calls) are rejected
// deterministically by the status check that immediately follows.
//
// In I.2 the mutations are document-layer only, but lifecycle truth
// itself deserves concurrency protection so that I.3 lands on a
// race-free foundation rather than inherit one.
//
// Returns ErrShipmentNotFound when the row does not exist or belongs
// to another tenant (company scope enforced).
func loadShipmentForUpdate(tx *gorm.DB, companyID, id uint) (*models.Shipment, error) {
	var s models.Shipment
	err := applyLockForUpdate(tx.Where("company_id = ? AND id = ?", companyID, id)).
		First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrShipmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load shipment: %w", err)
	}
	return &s, nil
}
