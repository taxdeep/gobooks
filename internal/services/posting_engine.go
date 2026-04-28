// 遵循project_guide.md
package services

// posting_engine.go — PostingEngine: the single coordinator for all posting lifecycle operations.
//
// Role and responsibilities:
//   PostingEngine is the authoritative entry point for any operation that transitions
//   a business document or journal entry between lifecycle states. It does NOT contain
//   accounting logic itself; instead it orchestrates the existing domain services:
//
//     fragment_builder.go  — fragment assembly (pure, no DB)
//     journal_aggregate.go — fragment aggregation by account + side
//     tax_service.go       — line-level tax calculation (pure, no DB)
//     journal_reverse.go   — reversal journal entry creation
//     invoice_void.go      — invoice void workflow
//     ledger.go            — ledger_entries projection (Phase 2)
//
// Current delegation model:
//   PostingEngine methods delegate to the existing package-level functions
//   (PostInvoice, PostBill, VoidInvoice, ReverseJournalEntry). This ensures the
//   existing invoice/bill posting flows are not broken while the engine structure
//   is established. Future phases will wire the fragment builder and ledger
//   projection directly through the engine.
//
// Company isolation:
//   Every method requires companyID as its first argument. All downstream calls
//   pass companyID explicitly; no method relies on state stored in the engine.
//
// Concurrency:
//   PostingEngine holds no mutable state. Multiple goroutines may share a single
//   instance safely. Concurrency control (SELECT FOR UPDATE + unique partial index)
//   is owned by the individual posting functions called by each method.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrSourceTypeNotSupported is returned when a VoidDocument or ReverseDocument
// call specifies a source type that is not yet handled by this engine version.
var ErrSourceTypeNotSupported = errors.New("posting engine: source type is not supported in this phase")

// ── Engine type ───────────────────────────────────────────────────────────────

// PostingEngine coordinates all posting, voiding, and reversal operations.
// Create one via NewPostingEngine; the same instance may be shared across requests.
type PostingEngine struct {
	db *gorm.DB
}

// NewPostingEngine returns a PostingEngine bound to db.
func NewPostingEngine(db *gorm.DB) *PostingEngine {
	return &PostingEngine{db: db}
}

// ── Posting ───────────────────────────────────────────────────────────────────

// PostInvoice transitions a draft invoice to posted status and creates its
// double-entry journal entry.
//
// Delegates to: services.PostInvoice (invoice_post.go).
// Future: will also call ProjectToLedger within the same transaction.
func (e *PostingEngine) PostInvoice(companyID, invoiceID uint, actor string, userID *uuid.UUID) error {
	return PostInvoice(e.db, companyID, invoiceID, actor, userID)
}

// PostBill transitions a draft bill to posted status and creates its
// double-entry journal entry.
//
// Delegates to: services.PostBill (bill_post.go).
// Future: will also call ProjectToLedger within the same transaction.
func (e *PostingEngine) PostBill(companyID, billID uint, actor string, userID *uuid.UUID) error {
	return PostBill(e.db, companyID, billID, actor, userID)
}

// ── Voiding ───────────────────────────────────────────────────────────────────

// VoidDocument cancels a posted business document by reversing its accounting
// and updating the document status to voided.
//
// Supported source types:
//   - LedgerSourceInvoice → delegates to services.VoidInvoice
//   - LedgerSourceBill    → not yet implemented (returns ErrSourceTypeNotSupported)
//
// sourceID is the primary key of the document in its respective table.
func (e *PostingEngine) VoidDocument(
	companyID uint,
	sourceType models.LedgerSourceType,
	sourceID uint,
	actor string,
	userID *uuid.UUID,
) error {
	switch sourceType {
	case models.LedgerSourceInvoice:
		return VoidInvoice(e.db, companyID, sourceID, actor, userID)

	case models.LedgerSourceBill:
		return VoidBill(e.db, companyID, sourceID, actor, userID)

	default:
		return fmt.Errorf("%w: %q", ErrSourceTypeNotSupported, sourceType)
	}
}

// ── Reversal ──────────────────────────────────────────────────────────────────

// ReverseDocument creates a reversal journal entry for the journal entry linked
// to a business document, without changing the document's own status.
//
// This is distinct from VoidDocument:
//   - VoidDocument   = reverse accounting AND mark the document as voided.
//   - ReverseDocument = create the reversal JE only; document status is unchanged.
//     Used for correcting manual journal entries or for accounting adjustments
//     where the source document lifecycle should not be affected.
//
// sourceType determines how the linked journal entry is resolved:
//   - LedgerSourceInvoice → loads invoices.journal_entry_id
//   - LedgerSourceBill    → loads bills.journal_entry_id
//   - LedgerSourceManual  → treats sourceID as the journal_entry_id directly
//
// Returns the new reversal journal entry ID on success.
func (e *PostingEngine) ReverseDocument(
	companyID uint,
	sourceType models.LedgerSourceType,
	sourceID uint,
	reverseDate time.Time,
	actor string,
) (uint, error) {
	jeID, err := e.resolveJournalEntryID(companyID, sourceType, sourceID)
	if err != nil {
		return 0, fmt.Errorf("reverse document: %w", err)
	}

	// Wrap ReverseJournalEntry in a transaction so that all writes
	// (reversal JE, lines, original JE status update, ledger projections)
	// are atomic. ReverseJournalEntry expects a transaction-scoped *gorm.DB.
	var reversalID uint
	if err := e.db.Transaction(func(tx *gorm.DB) error {
		var err error
		reversalID, err = ReverseJournalEntry(tx, companyID, jeID, reverseDate)
		return err
	}); err != nil {
		return 0, fmt.Errorf("reverse document: %w", err)
	}

	_ = actor // reserved for audit log in a future phase
	return reversalID, nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// resolveJournalEntryID finds the journal entry associated with a source document.
// For manual entries, sourceID is used as the journal entry ID directly.
func (e *PostingEngine) resolveJournalEntryID(
	companyID uint,
	sourceType models.LedgerSourceType,
	sourceID uint,
) (uint, error) {
	if sourceID == 0 {
		return 0, errors.New("sourceID must be non-zero")
	}

	switch sourceType {
	case models.LedgerSourceInvoice:
		var inv models.Invoice
		if err := e.db.
			Select("id, company_id, journal_entry_id").
			Where("id = ? AND company_id = ?", sourceID, companyID).
			First(&inv).Error; err != nil {
			return 0, fmt.Errorf("load invoice %d: %w", sourceID, err)
		}
		if inv.JournalEntryID == nil || *inv.JournalEntryID == 0 {
			return 0, fmt.Errorf("invoice %d has no linked journal entry", sourceID)
		}
		return *inv.JournalEntryID, nil

	case models.LedgerSourceBill:
		var bill models.Bill
		if err := e.db.
			Select("id, company_id, journal_entry_id").
			Where("id = ? AND company_id = ?", sourceID, companyID).
			First(&bill).Error; err != nil {
			return 0, fmt.Errorf("load bill %d: %w", sourceID, err)
		}
		if bill.JournalEntryID == nil || *bill.JournalEntryID == 0 {
			return 0, fmt.Errorf("bill %d has no linked journal entry", sourceID)
		}
		return *bill.JournalEntryID, nil

	case models.LedgerSourceManual:
		// For manual journal entries there is no source document;
		// sourceID is the journal_entry_id directly.
		return sourceID, nil

	default:
		return 0, fmt.Errorf("%w: %q", ErrSourceTypeNotSupported, sourceType)
	}
}
