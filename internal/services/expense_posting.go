// 遵循project_guide.md
package services

// expense_posting.go — IN.2 business-document-layer orchestration
// that wires a posted Expense to inventory movements (on stock-item
// lines, legacy mode) and to the Expense JE (Dr per-line accounts /
// Cr PaymentAccount).
//
// Rule #4 in effect
// -----------------
// Stock-item expense line MUST form an inventory movement OR be
// rejected loudly. Which path applies is decided by the company's
// workflow mode:
//
//   - Legacy (`receipt_required=false`): Expense is the movement
//     owner for its stock lines. CreateExpenseMovements runs each
//     stock line through inventory.ReceiveStock and the resulting
//     cost flows back into the JE's Dr-Inventory leg.
//   - Controlled (`receipt_required=true`): Expense is not a legal
//     inbound surface. PostExpense rejects stock-item lines with
//     ErrExpenseStockItemRequiresReceipt before any side effect.
//     Pure-expense lines (no stock item) remain allowed.
//
// GL shape for a legacy-mode Expense with mixed lines
// ---------------------------------------------------
//
//   Dr InventoryAsset (per stock line, amount = unit_cost_base
//                     returned by inventory.ReceiveStock × qty)
//   Dr ExpenseAccount (per pure-expense line, amount = line.Amount)
//   Cr PaymentAccount (sum of all line totals — the Bank / CC / Cash
//                     the operator swiped)
//
// No PPV account. No GR/IR. Expense is a one-shot transaction: cash
// already left the account when the operator entered the record;
// there is no vendor-AP accrual to reconcile later.
//
// Cost authority
// --------------
// Stock-line Dr-Inventory amounts come from the inventory module's
// ReceiveStock return value (InventoryValueBase), NOT from the
// ExpenseLine.Amount column. Under moving-average this is unit_cost
// × qty where unit_cost may differ from what the operator typed if
// the item was already in stock with a different average (inventory
// module owns the cost — "Hard Rule #9: authoritative cost
// principle", §2.9). For FIFO the first-layer entry matches exactly
// what inventory accepted, so typed and booked values agree.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

var (
	// ErrExpenseStockItemRequiresReceipt — Rule #4 Q2 invariant.
	// Controlled mode (`receipt_required=true`) closes the Expense
	// backdoor; operator must use Receipt for any inbound inventory.
	// Message is explicit so CS can copy-paste the remediation
	// ("use a Receipt instead of an Expense for this stock item").
	ErrExpenseStockItemRequiresReceipt = errors.New(
		"expense: stock-item line not allowed when receipt_required=true — " +
			"route inbound inventory through a Receipt instead (controlled mode)")

	// ErrExpensePaymentAccountRequiredForPost — PostExpense needs a
	// credit target. Draft expenses can omit PaymentAccountID; posting
	// requires it so the JE has a complete double-entry shape.
	ErrExpensePaymentAccountRequiredForPost = errors.New(
		"expense: payment account is required to post — set the Bank / Credit Card / Cash account on the expense first")

	// ErrExpenseNotDraft — PostExpense rejects a non-draft row.
	ErrExpenseNotDraft = errors.New("expense: post requires status=draft")

	// ErrExpenseNotPosted — VoidExpense rejects a non-posted row.
	ErrExpenseNotPosted = errors.New("expense: void requires status=posted")

	// ErrExpenseWarehouseRequiredForStockLine — stock-item line on
	// Expense needs a warehouse for the inventory movement. Post
	// validation reads WarehouseID on the header (default falls back
	// to the company default warehouse); if neither is present, fail
	// loud instead of silently picking a random warehouse.
	ErrExpenseWarehouseRequiredForStockLine = errors.New(
		"expense: stock-item line requires a warehouse — set the header Warehouse field or configure a company default")

	// ErrExpenseStockInventoryAccountMissing — Dr-Inventory leg can't
	// post without InventoryAccountID on the ProductService. Same
	// failure class as ErrInboundReceiptInventoryAccountMissing.
	ErrExpenseStockInventoryAccountMissing = errors.New(
		"expense: stock-item line has no inventory_account_id — configure the product/service")
)

// expenseMovementResult pairs an ExpenseLine with the inventory
// module's return value. Enough for the JE-fragment-builder step
// that follows to read the authoritative cost.
type expenseMovementResult struct {
	Line   models.ExpenseLine
	Result inventory.ReceiveStockResult
}

// CreateExpenseMovements is the expense-side facade over
// inventory.ReceiveStock. Peer of CreateReceiptMovements (H.3) and
// CreatePurchaseMovements (Phase G legacy bill path).
//
// Iterates each line of the preloaded expense. Stock-item lines
// (ProductService.IsStockItem=true) are booked via ReceiveStock
// using line.Qty + line.UnitPrice from ExpenseLine columns. Lines
// that point at a non-stock item OR have no ProductService are
// silently skipped (they are Rule #4-compliant pure-expense lines).
//
// Warehouse routing: warehouseID is the header-level WarehouseID
// resolved by the caller (PostExpense) with a fallback to the
// company default. Expense has no per-line warehouse column — all
// stock lines on a single Expense land in the same warehouse.
//
// Returns one result per stock-item line booked. Empty slice for
// pure-expense Expenses.
func CreateExpenseMovements(tx *gorm.DB, expense models.Expense, warehouseID uint) ([]expenseMovementResult, error) {
	if warehouseID == 0 {
		return nil, ErrExpenseWarehouseRequiredForStockLine
	}
	if len(expense.Lines) == 0 {
		return nil, nil
	}

	version, err := nextIdempotencyVersion(tx, expense.CompanyID, "expense", expense.ID)
	if err != nil {
		return nil, fmt.Errorf("pick idempotency version: %w", err)
	}

	out := make([]expenseMovementResult, 0, len(expense.Lines))
	for _, line := range expense.Lines {
		if line.ProductService == nil || !line.ProductService.IsStockItem {
			continue
		}
		if !line.Qty.IsPositive() {
			continue
		}

		lineID := line.ID
		in := inventory.ReceiveStockInput{
			CompanyID:      expense.CompanyID,
			ItemID:         *line.ProductServiceID,
			WarehouseID:    warehouseID,
			Quantity:       line.Qty,
			MovementDate:   expense.ExpenseDate,
			UnitCost:       line.UnitPrice,
			ExchangeRate:   decimal.NewFromInt(1),
			SourceType:     string(models.LedgerSourceExpense),
			SourceID:       expense.ID,
			SourceLineID:   &lineID,
			IdempotencyKey: fmt.Sprintf("expense:%d:line:%d:v%d", expense.ID, line.ID, version),
			Memo:           "Expense: " + expense.Description,
		}
		result, err := inventory.ReceiveStock(tx, in)
		if err != nil {
			return nil, fmt.Errorf("receive stock for item %d: %w", *line.ProductServiceID, translateInventoryErr(err))
		}
		out = append(out, expenseMovementResult{Line: line, Result: *result})
	}
	return out, nil
}

// ReverseExpenseMovements reverses every original expense movement
// for a voided Expense. Thin wrapper around reverseDocumentMovements
// — same shape as ReverseReceiptMovements / ReverseShipmentMovements.
func ReverseExpenseMovements(tx *gorm.DB, companyID uint, expense models.Expense) error {
	return reverseDocumentMovements(tx, companyID, reverseDocumentScope{
		sourceType:         string(models.LedgerSourceExpense),
		sourceID:           expense.ID,
		reversalSourceType: "expense_reversal",
		movementDate:       expense.ExpenseDate,
		memo:               "Void: " + expense.Description,
		reason:             inventory.ReversalReasonCancellation,
	})
}

// buildExpensePostingFragments assembles the JE fragments for a
// legacy-mode posted Expense:
//
//   - Per stock-item line: Dr InventoryAsset at the
//     InventoryValueBase returned by inventory (authoritative cost,
//     may differ from line.UnitPrice × qty under moving-average if
//     the item already has stock with a different weighted avg).
//   - Per pure-expense line: Dr line.ExpenseAccountID at line.Amount.
//   - Cr PaymentAccount at the sum of all line contributions.
//
// Caller (PostExpense) must supply paymentAccountID (post-time
// validated non-zero). stockResults may be empty when the Expense
// is pure-expense.
func buildExpensePostingFragments(
	expense models.Expense,
	stockResults []expenseMovementResult,
	paymentAccountID uint,
) ([]PostingFragment, error) {
	// Map stock-line IDs to their authoritative-cost result for
	// O(1) dispatch during line iteration.
	stockByLine := make(map[uint]decimal.Decimal, len(stockResults))
	for _, r := range stockResults {
		if r.Line.ProductService == nil || r.Line.ProductService.InventoryAccountID == nil {
			return nil, fmt.Errorf("%w: line=%d item=%d",
				ErrExpenseStockInventoryAccountMissing, r.Line.ID,
				func() uint {
					if r.Line.ProductServiceID != nil {
						return *r.Line.ProductServiceID
					}
					return 0
				}())
		}
		stockByLine[r.Line.ID] = r.Result.InventoryValueBase
	}

	var frags []PostingFragment
	creditTotal := decimal.Zero

	for _, line := range expense.Lines {
		// Stock-line route: Dr Inventory at authoritative cost.
		if invVal, ok := stockByLine[line.ID]; ok {
			if !invVal.IsPositive() {
				// zero-cost stock line contributes nothing either side
				continue
			}
			frags = append(frags, PostingFragment{
				AccountID: *line.ProductService.InventoryAccountID,
				Debit:     invVal,
				Memo:      "Inventory in (expense): " + line.Description,
			})
			creditTotal = creditTotal.Add(invVal)
			continue
		}

		// Pure-expense route: Dr ExpenseAccount at line.Amount.
		if line.ExpenseAccountID == nil || *line.ExpenseAccountID == 0 {
			// Save path already rejects this; guarded here too so
			// a manually-crafted row doesn't slip through.
			return nil, fmt.Errorf("expense line %d has no expense account", line.ID)
		}
		amt := line.Amount
		if !amt.IsPositive() {
			continue
		}
		frags = append(frags, PostingFragment{
			AccountID: *line.ExpenseAccountID,
			Debit:     amt,
			Memo:      "Expense: " + line.Description,
		})
		creditTotal = creditTotal.Add(amt)
	}

	if creditTotal.IsPositive() {
		frags = append(frags, PostingFragment{
			AccountID: paymentAccountID,
			Credit:    creditTotal,
			Memo:      "Paid: " + expense.ExpenseNumber,
		})
	}
	return frags, nil
}
