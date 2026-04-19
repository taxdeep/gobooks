// 遵循project_guide.md
package services

// receipt_posting.go — Phase H slice H.3: business-document-layer
// orchestration that wires a posted Receipt to receive truth (via
// inventory.ReceiveStock) and to the GR/IR journal.
//
// Three-layer split (H.3 boundary lock)
// -------------------------------------
//
//   POSTED RECEIPT          (business document — models.Receipt)
//         │
//         │  CreateReceiptMovements projects each line into a
//         │  ReceiveStockInput. The inventory module owns the
//         │  create-movement verb (ReceiveStock), which returns a
//         │  receive-truth record ID and authoritative cost.
//         ▼
//   RECEIVE TRUTH           (inventory_movements row, source_type='receipt')
//         │
//         │  Internal to the inventory module: cost layer / balance
//         │  machinery (unchanged from Phase D/E/F). The business-
//         │  document layer never mutates inventory_balances or
//         │  cost layers directly.
//         ▼
//   INVENTORY EFFECT        (inventory_balances, inventory_cost_layers,
//                            inventory_lots, inventory_serial_units)
//
// Separately, the business-document layer reads the ReceiveStockResult
// and constructs a journal: Dr Inventory (per line, amount =
// InventoryValueBase returned by the inventory module) / Cr GR/IR
// (summed, booked to companies.gr_ir_clearing_account_id). The
// inventory package has zero knowledge of the GR/IR account, the JE,
// or the ledger projection — Hard Rule #3 is enforced here by
// construction and (separately) locked by an import-guard test.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
)

var (
	// ErrGRIRAccountNotConfigured — PostReceipt ran under
	// receipt_required=true on a company whose gr_ir_clearing_account_id
	// is not set. Admin must configure via
	// ChangeCompanyGRIRClearingAccount before the Receipt-first flow
	// can complete. Distinct from ErrGRIRClearingAccountInvalid
	// (which rejects bad assignment) so the remediation is
	// unambiguous: one says "set it", the other says "pick a
	// different account".
	ErrGRIRAccountNotConfigured = errors.New("gr_ir_clearing: companies.gr_ir_clearing_account_id not configured — PostReceipt under receipt_required=true requires it")

	// ErrInboundReceiptInventoryAccountMissing — a stock-item line on
	// the receipt has no inventory_account_id configured on its
	// ProductService. The debit side of the GR/IR journal cannot be
	// booked without it, so post fails early with a clear pointer at
	// the product-service catalog as the remediation site.
	ErrInboundReceiptInventoryAccountMissing = errors.New("receipt: stock-item line has no inventory_account_id — configure the product/service")
)

// receiveTruthResult pairs a single line's inventory return with its
// source line — enough for the JE construction step that follows.
type receiveTruthResult struct {
	Line   models.ReceiptLine
	Result inventory.ReceiveStockResult
}

// CreateReceiptMovements is the receipt-side facade over
// inventory.ReceiveStock. 1:1 with CreatePurchaseMovements (bill
// side) so both document kinds share the receive-truth verb but
// project their own shape.
//
// Iterates each stock-item line on the receipt and books one
// inventory.ReceiveStock call. Non-stock lines are skipped. Returns
// the per-line (ReceiptLine, ReceiveStockResult) pairs so the
// caller can aggregate cost for the JE in base currency.
//
// The function is GL-agnostic: no journal entry, no fragments, no
// account IDs touched. GL construction is the next step in the
// pipeline, owned by PostReceipt.
//
// Tracked items: LotNumber + LotExpiryDate on the ReceiptLine are
// forwarded to ReceiveStock per Phase F2 rules. Serial-tracked
// items are likewise forwarded once ReceiptLine grows a serial
// surface; for H.3 they fail loud at validateInboundTracking
// (the F2 guard), matching the Bill-side behavior from G.4.
func CreateReceiptMovements(tx *gorm.DB, receipt models.Receipt) ([]receiveTruthResult, error) {
	if len(receipt.Lines) == 0 {
		return nil, nil
	}

	// Pick a fresh idempotency-key version for this post attempt so a
	// voided-and-re-posted receipt does not collide with its prior
	// keys (mirrors bill posting).
	version, err := nextIdempotencyVersion(tx, receipt.CompanyID, "receipt", receipt.ID)
	if err != nil {
		return nil, fmt.Errorf("pick idempotency version: %w", err)
	}

	out := make([]receiveTruthResult, 0, len(receipt.Lines))
	for _, line := range receipt.Lines {
		if line.ProductService == nil {
			// Not preloaded — caller (PostReceipt) preloads ProductService
			// before invoking; a nil here is a bug, not a non-stock line.
			return nil, fmt.Errorf("receipt line %d: ProductService not preloaded", line.ID)
		}
		if !line.ProductService.IsStockItem {
			continue
		}

		lineID := line.ID
		in := inventory.ReceiveStockInput{
			CompanyID:      receipt.CompanyID,
			ItemID:         line.ProductServiceID,
			WarehouseID:    receipt.WarehouseID,
			Quantity:       line.Qty,
			MovementDate:   receipt.ReceiptDate,
			UnitCost:       line.UnitCost,
			ExchangeRate:   decimal.NewFromInt(1),
			SourceType:     string(models.LedgerSourceReceipt),
			SourceID:       receipt.ID,
			SourceLineID:   &lineID,
			IdempotencyKey: fmt.Sprintf("receipt:%d:line:%d:v%d", receipt.ID, line.ID, version),
			Memo:           "Receipt: " + receipt.ReceiptNumber,
		}
		if line.LotNumber != "" {
			in.LotNumber = line.LotNumber
		}
		if line.LotExpiryDate != nil {
			in.ExpiryDate = line.LotExpiryDate
		}

		result, err := inventory.ReceiveStock(tx, in)
		if err != nil {
			return nil, fmt.Errorf("receive stock for item %d: %w", line.ProductServiceID, translateInventoryErr(err))
		}
		out = append(out, receiveTruthResult{Line: line, Result: *result})
	}
	return out, nil
}

// ReverseReceiptMovements reverses every original receipt movement
// for a voided receipt. Thin wrapper around the shared
// reverseDocumentMovements helper (same helper drives
// ReverseSaleMovements / ReversePurchaseMovements).
func ReverseReceiptMovements(tx *gorm.DB, companyID uint, receipt models.Receipt) error {
	return reverseDocumentMovements(tx, companyID, reverseDocumentScope{
		sourceType:         string(models.LedgerSourceReceipt),
		sourceID:           receipt.ID,
		reversalSourceType: "receipt_reversal",
		movementDate:       receipt.ReceiptDate,
		memo:               "Void: " + receipt.ReceiptNumber,
		reason:             inventory.ReversalReasonCancellation,
	})
}

// buildReceiptPostingFragments constructs the JE fragments for a
// posted receipt: Dr Inventory (per-line, amount = base-currency
// InventoryValueBase returned by inventory.ReceiveStock) / Cr GR/IR
// (aggregated total).
//
// The inventory module returned authoritative base-currency values.
// The business-document layer takes those as-is — it does NOT
// recompute from unit_cost × qty, because the inventory module may
// have applied landed-cost allocation, exchange-rate snapshot, or
// other adjustments the document layer is not privy to. This
// mirrors Phase E0's "COGS from IssueStock return value, not preview"
// principle, applied to the inbound side.
//
// Returned fragments are not balanced individually; callers run them
// through AggregateJournalLines + the double-entry balance check the
// same way bill_post does.
func buildReceiptPostingFragments(results []receiveTruthResult, grirAccountID uint, receiptNumber string) ([]PostingFragment, error) {
	var frags []PostingFragment
	grirCredit := decimal.Zero
	for _, r := range results {
		if r.Line.ProductService == nil || r.Line.ProductService.InventoryAccountID == nil {
			return nil, fmt.Errorf("%w: line=%d item=%d",
				ErrInboundReceiptInventoryAccountMissing, r.Line.ID, r.Line.ProductServiceID)
		}
		value := r.Result.InventoryValueBase
		if !value.IsPositive() {
			// Zero-value lines (qty=0 or unit_cost=0) contribute
			// nothing to either side — skip silently rather than
			// producing a zero-amount fragment.
			continue
		}
		frags = append(frags, PostingFragment{
			AccountID: *r.Line.ProductService.InventoryAccountID,
			Debit:     value,
			Credit:    decimal.Zero,
			Memo:      "Inventory in (receipt): " + r.Line.Description,
		})
		grirCredit = grirCredit.Add(value)
	}
	if !grirCredit.IsPositive() {
		return nil, nil
	}
	frags = append(frags, PostingFragment{
		AccountID: grirAccountID,
		Debit:     decimal.Zero,
		Credit:    grirCredit,
		Memo:      "GR/IR clearing: " + receiptNumber,
	})
	return frags, nil
}

// resolveGRIRAccount returns the company's configured GR/IR clearing
// account or ErrGRIRAccountNotConfigured. Company is re-read inside
// the caller's transaction so that a concurrent config change (via
// ChangeCompanyGRIRClearingAccount) is picked up — the admin surface
// commits before PostReceipt begins.
func resolveGRIRAccount(tx *gorm.DB, companyID uint) (uint, error) {
	var company models.Company
	if err := tx.Where("id = ?", companyID).First(&company).Error; err != nil {
		return 0, fmt.Errorf("load company: %w", err)
	}
	if company.GRIRClearingAccountID == nil {
		return 0, ErrGRIRAccountNotConfigured
	}
	return *company.GRIRClearingAccountID, nil
}
