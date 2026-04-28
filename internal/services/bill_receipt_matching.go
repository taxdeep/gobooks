// 遵循project_guide.md
package services

// bill_receipt_matching.go — Phase H slice H.5 matching engine.
//
// Role in the post pipeline (flag=true only)
// ------------------------------------------
//
//   BuildBillFragments         →  per-line Dr Expense, single Cr AP
//   (unchanged for flag=false)
//
//   AdjustBillFragmentsForGRIRClearing
//   (H.4 aggregate/blind clearing; still used for non-matched bill
//    lines and for bills with no matched lines at all)
//
//   ┌─ this file ──────────────────────────────────────────────────┐
//   │                                                               │
//   │  resolveBillLineMatchingContext(tx, bill)                     │
//   │     → map[bill_line_id] matchingContext{receipt, matched_qty, │
//   │        unmatched_qty, receipt_cost_base}                      │
//   │                                                               │
//   │  applyBillLineMatchingToFragments(frags, bill, ctx, grirID,   │
//   │        ppvID)                                                 │
//   │     → splits the matched bill line's single expense-debit     │
//   │       fragment into:                                          │
//   │         Dr GR/IR (matched_qty × receipt_cost)                 │
//   │         Dr/Cr PPV (matched_qty × variance) — omitted if zero  │
//   │         Dr GR/IR (unmatched_qty × bill_price)  — blind        │
//   │       Total debit equals the original line net (invariant).   │
//   │                                                               │
//   └───────────────────────────────────────────────────────────────┘
//
// Scope boundaries
// ----------------
// - Only bill_lines with receipt_line_id != nil are considered. Lines
//   without a receipt pointer stay on the H.4 blind path.
// - Only stock lines participate in matching. Non-stock lines with a
//   stray receipt_line_id (shouldn't happen per validation, but
//   defense-in-depth) are left on their expense account.
// - Receipt must be posted and in the same company; draft, voided,
//   and cross-tenant receipts are rejected by the validator BEFORE
//   the matcher runs.
// - Cumulative matching is computed dynamically (sum of posted
//   bill_lines.qty grouped by receipt_line_id, excluding the current
//   bill). No cached matched_qty on receipt_lines — avoids dual-write
//   sync bugs; N+1 of this shape is negligible at realistic scale.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

var (
	// ErrPPVAccountNotConfigured — matching engaged (at least one bill
	// line has receipt_line_id set) but the company's
	// purchase_price_variance_account_id is nil. Remediation: configure
	// via ChangeCompanyPPVAccount.
	ErrPPVAccountNotConfigured = errors.New("ppv: matched bill lines require a configured purchase_price_variance_account_id")

	// ErrBillLineReceiptRefInvalid — the referenced receipt line
	// either does not exist, belongs to a different company, is on a
	// non-posted Receipt (draft or voided), or is not a stock line.
	// Returned from validation BEFORE any write; matching never runs
	// on a bad reference.
	ErrBillLineReceiptRefInvalid = errors.New("bill line: receipt_line_id reference is invalid (not found, cross-tenant, non-posted receipt, or non-stock line)")
)

// billLineMatchingContext carries the per-line matching outcome for
// one bill line that points at a receipt line. qty values in the
// bill line's unit of measure; costs in base currency.
type billLineMatchingContext struct {
	BillLineID uint
	// ReceiptLine is the referenced posted receipt line, preloaded.
	ReceiptLine *models.ReceiptLine
	// MatchedQty is the quantity that clears GR/IR at the Receipt's
	// unit cost. Equals min(billLine.qty, remaining_receipt_qty).
	MatchedQty decimal.Decimal
	// UnmatchedQty is the residual that continues on the H.4 blind
	// path (Dr GR/IR at bill_price). Zero when the match covered the
	// full bill-line qty.
	UnmatchedQty decimal.Decimal
}

// resolveBillLineMatchingContext walks each bill line with a
// non-nil ReceiptLineID, validates scope, and computes matched /
// unmatched quantities against the receipt line's cumulative
// consumption. Returns a map keyed by bill_line.ID. Bill lines
// without a receipt_line_id are absent from the map — the caller
// routes them through the H.4 blind-clearing path unchanged.
func resolveBillLineMatchingContext(tx *gorm.DB, bill models.Bill) (map[uint]billLineMatchingContext, error) {
	result := make(map[uint]billLineMatchingContext)
	for _, line := range bill.Lines {
		if line.ReceiptLineID == nil || *line.ReceiptLineID == 0 {
			continue
		}
		if line.ProductService == nil || !line.ProductService.IsStockItem {
			return nil, fmt.Errorf("%w: bill line %d references a receipt line but is not a stock item",
				ErrBillLineReceiptRefInvalid, line.ID)
		}

		// Load the referenced receipt line with a row-level write lock
		// (SELECT ... FOR UPDATE on PostgreSQL; no-op on SQLite). This
		// is the H-hardening-1 fix: two concurrent PostBill calls that
		// both reference the same receipt line must serialise here so
		// each computes its `available` qty against the other's
		// already-committed bill_line, rather than racing on a shared
		// pre-commit snapshot and both over-matching.
		//
		// Without this lock, the bug manifests as: receipt_line qty=10,
		// Bill A matches 6, Bill B matches 6 concurrently — both
		// succeed because each reads prior_matched=0 before the other
		// commits, producing a cumulative 12 > 10. With the lock, Bill
		// B blocks on this SELECT until Bill A commits, then sees the
		// correct prior_matched=6 and computes available=4.
		var rl models.ReceiptLine
		if err := applyLockForUpdate(tx.Preload("ProductService").
			Where("id = ?", *line.ReceiptLineID)).
			First(&rl).Error; err != nil {
			return nil, fmt.Errorf("%w: receipt line %d: %s",
				ErrBillLineReceiptRefInvalid, *line.ReceiptLineID, err.Error())
		}
		if rl.CompanyID != bill.CompanyID {
			return nil, fmt.Errorf("%w: receipt line %d belongs to company=%d, bill company=%d",
				ErrBillLineReceiptRefInvalid, rl.ID, rl.CompanyID, bill.CompanyID)
		}
		if rl.ProductService == nil || !rl.ProductService.IsStockItem {
			return nil, fmt.Errorf("%w: receipt line %d is not a stock item",
				ErrBillLineReceiptRefInvalid, rl.ID)
		}

		var receipt models.Receipt
		if err := tx.Where("id = ? AND company_id = ?", rl.ReceiptID, bill.CompanyID).
			First(&receipt).Error; err != nil {
			return nil, fmt.Errorf("%w: receipt %d lookup: %s",
				ErrBillLineReceiptRefInvalid, rl.ReceiptID, err.Error())
		}
		if receipt.Status != models.ReceiptStatusPosted {
			return nil, fmt.Errorf("%w: receipt %d has status=%q, must be posted",
				ErrBillLineReceiptRefInvalid, receipt.ID, receipt.Status)
		}

		// Cumulative matched qty from OTHER posted bills (excludes
		// the current bill being posted, so re-posts don't double
		// count). Joins bill_lines → bills to filter by bill.status.
		var priorMatched decimal.Decimal
		row := struct {
			Total decimal.Decimal
		}{}
		if err := tx.Table("bill_lines").
			Select("COALESCE(SUM(bill_lines.qty), 0) AS total").
			Joins("JOIN bills ON bills.id = bill_lines.bill_id").
			Where("bill_lines.receipt_line_id = ? AND bill_lines.company_id = ? AND bills.status = ? AND bills.id <> ?",
				rl.ID, bill.CompanyID, models.BillStatusPosted, bill.ID).
			Scan(&row).Error; err != nil {
			return nil, fmt.Errorf("compute prior matched qty for receipt line %d: %w", rl.ID, err)
		}
		priorMatched = row.Total

		available := rl.Qty.Sub(priorMatched)
		if available.IsNegative() {
			available = decimal.Zero
		}
		matched := line.Qty
		if matched.GreaterThan(available) {
			matched = available
		}
		unmatched := line.Qty.Sub(matched)

		rlCopy := rl
		result[line.ID] = billLineMatchingContext{
			BillLineID:   line.ID,
			ReceiptLine:  &rlCopy,
			MatchedQty:   matched,
			UnmatchedQty: unmatched,
		}
	}
	return result, nil
}

// applyBillLineMatchingToFragments transforms the pre-adjustment
// fragment slice into the H.5 final shape for matched stock lines.
//
// For each bill line with a matching context:
//   - the existing expense-debit fragment (emitted by
//     BuildBillFragments and untouched by the GR/IR aggregate step
//     for this slice's flag=true path) is REPLACED by up to three
//     new fragments:
//       Dr GR/IR  (matched_qty × receipt_unit_cost)          — precise clearing
//       Dr/Cr PPV (matched_qty × (bill_unit − receipt_unit)) — variance, if non-zero
//       Dr GR/IR  (unmatched_qty × bill_unit_price)          — H.4 blind on overflow
//
// Non-matched stock lines stay on the H.4 blind path via
// AdjustBillFragmentsForGRIRClearing (called before this function).
// Non-stock lines are already routed to their expense account and
// are untouched here.
//
// Invariant: for every matched bill line, the sum of replacement
// fragments' debit amounts equals the original line's expense-debit
// amount (LineNet, pre-FX). Preserves overall Dr == Cr balance
// without requiring re-aggregation on the AP credit side.
func applyBillLineMatchingToFragments(
	frags []PostingFragment,
	bill models.Bill,
	matching map[uint]billLineMatchingContext,
	grirAccountID uint,
	ppvAccountID uint,
) ([]PostingFragment, error) {
	if len(matching) == 0 {
		return frags, nil
	}

	// Build lookup from line.ID to the line struct + line-level
	// expense debit amount (net + non-recoverable tax) — matches
	// how BuildBillFragments composes the expense fragment.
	lineByExpenseDebit := make(map[uint]struct {
		Line   models.BillLine
		Amount decimal.Decimal
	})
	for _, l := range bill.Lines {
		if _, ok := matching[l.ID]; !ok {
			continue
		}
		amount := l.LineNet
		if l.TaxCode != nil && l.TaxCode.Scope != models.TaxScopeSales {
			lt := ComputeLineTax(l.LineNet, *l.TaxCode)
			amount = amount.Add(lt.NonRecoverableTaxAmount)
		}
		lineByExpenseDebit[l.ID] = struct {
			Line   models.BillLine
			Amount decimal.Decimal
		}{Line: l, Amount: amount}
	}

	// Phase 1: drop the first matching expense-debit fragment per
	// matched line. We identify by (AccountID == line.ExpenseAccountID
	// AND Debit == expected amount). This reuses the existing pre-
	// aggregation fragment shape; there is one expense fragment per
	// line before aggregation, so the first hit is deterministic.
	//
	// After H.4's AdjustBillFragmentsForGRIRClearing ran, the matched
	// line's expense fragment would have been REDIRECTED to GR/IR
	// already (its AccountID==grirAccountID, same Debit amount). Keep
	// both shapes in the matcher so the ordering of H.4 adjust + H.5
	// match does not matter:
	//   - if still on expense account → matches by (ExpenseAccountID, Amount)
	//   - if already rewritten to GR/IR → matches by (grirAccountID, Amount)
	// The first hit for each line is removed (zeroed out), then the
	// replacement fragments are appended at the end.
	removed := make(map[uint]bool) // bill_line.ID → consumed
	keep := make([]PostingFragment, 0, len(frags)+len(matching)*3)
	for _, f := range frags {
		consumed := false
		for lineID, bucket := range lineByExpenseDebit {
			if removed[lineID] {
				continue
			}
			if !f.Debit.Equal(bucket.Amount) {
				continue
			}
			// Match either pre- or post-H.4-adjust shape.
			if bucket.Line.ExpenseAccountID != nil && f.AccountID == *bucket.Line.ExpenseAccountID {
				removed[lineID] = true
				consumed = true
				break
			}
			if f.AccountID == grirAccountID {
				removed[lineID] = true
				consumed = true
				break
			}
		}
		if !consumed {
			keep = append(keep, f)
		}
	}

	// Phase 2: for each matched line, emit the 2-3 replacement fragments.
	for lineID, ctx := range matching {
		bucket, ok := lineByExpenseDebit[lineID]
		if !ok {
			// line.ExpenseAccountID missing — BuildBillFragments would
			// have errored upstream; defensive skip.
			continue
		}
		bl := bucket.Line
		receiptUnitCost := ctx.ReceiptLine.UnitCost
		billUnitPrice := bl.UnitPrice

		// Matched portion: qty × receipt_unit_cost → Dr GR/IR.
		// Variance:       qty × (billPrice − receiptCost) → Dr (if +) / Cr (if −) PPV.
		matchedGRIR := ctx.MatchedQty.Mul(receiptUnitCost)
		variancePerUnit := billUnitPrice.Sub(receiptUnitCost)
		matchedVariance := ctx.MatchedQty.Mul(variancePerUnit)

		// Unmatched portion (H.4 blind) at bill_price.
		unmatchedGRIR := ctx.UnmatchedQty.Mul(billUnitPrice)

		if matchedGRIR.IsPositive() {
			keep = append(keep, PostingFragment{
				AccountID: grirAccountID,
				Debit:     matchedGRIR,
				Credit:    decimal.Zero,
				Memo:      "GR/IR match: " + bl.Description,
			})
		}
		if matchedVariance.IsPositive() {
			keep = append(keep, PostingFragment{
				AccountID: ppvAccountID,
				Debit:     matchedVariance,
				Credit:    decimal.Zero,
				Memo:      "PPV (unfavorable): " + bl.Description,
			})
		} else if matchedVariance.IsNegative() {
			keep = append(keep, PostingFragment{
				AccountID: ppvAccountID,
				Debit:     decimal.Zero,
				Credit:    matchedVariance.Neg(),
				Memo:      "PPV (favorable): " + bl.Description,
			})
		}
		if unmatchedGRIR.IsPositive() {
			keep = append(keep, PostingFragment{
				AccountID: grirAccountID,
				Debit:     unmatchedGRIR,
				Credit:    decimal.Zero,
				Memo:      "GR/IR blind (overflow): " + bl.Description,
			})
		}
	}

	// All matched lines must have been consumed — if not, the original
	// expense fragment is still in `keep` and would double count.
	for lineID := range matching {
		if !removed[lineID] {
			return nil, fmt.Errorf(
				"matching fragment replacement failed for bill line %d — original expense fragment not identified", lineID)
		}
	}
	return keep, nil
}
