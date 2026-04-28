// 遵循project_guide.md
package services

// bill_post.go — PostBill: posting pipeline for purchase bills.
//
// Posting pipeline (Phase 4 + Phase 6 concurrency controls):
//
//   1. Load bill + lines  (TaxCode + ProductService preloaded)
//   2. Pre-flight validation
//   3. Resolve Accounts Payable account
//   4. BuildBillFragments   → raw []PostingFragment per line (fragment_builder.go)
//   5. AggregateJournalLines → collapse by account + side (journal_aggregate.go)
//   6. Validate double-entry balance (ΣDebit == ΣCredit)
//   7. Transaction:
//        a. INSERT journal_entries header
//        b. INSERT journal_lines (one per aggregated fragment)
//        c. ProjectToLedger   → INSERT ledger_entries (ledger.go)
//        d. UPDATE bills      → status='posted', amount=total, journal_entry_id
//        e. WriteAuditLog
//
// Before vs after journal shape — example bill $1 000 net, 13% HST (full recovery):
//
//   Line 1: Office rent   $600.00  net, HST $78.00  → expense account 6100
//   Line 2: Office supply $400.00  net, HST $52.00  → expense account 6100  (same acct)
//
//   Raw fragments (pre-aggregation):
//     DR  6100 Office Expense   600.00   (rent net — ITC fully recoverable)
//     DR  1320 ITC Receivable    78.00   (rent HST)
//     DR  6100 Office Expense   400.00   (supplies net)
//     DR  1320 ITC Receivable    52.00   (supplies HST)
//     CR  2000 AP              1130.00
//
//   After AggregateJournalLines (merged by account + side):
//     DR  6100 Office Expense  1 000.00  ← two expense lines merged
//     DR  1320 ITC Receivable    130.00  ← two ITC lines merged
//     CR  2000 AP              1 130.00
//
//   Non-recoverable tax variant — same bill, TaxCode.RecoveryMode = none:
//     Raw fragments:
//       DR  6100 Office Expense   678.00  (600 + 78 embedded non-recoverable)
//       DR  6100 Office Expense   452.00  (400 + 52 embedded)
//       CR  2000 AP              1130.00
//     After aggregation:
//       DR  6100 Office Expense  1 130.00  ← net + full tax merged into expense
//       CR  2000 AP              1 130.00
//
//   Ledger entries (one per journal line, status=active):
//     company 1, account 6100, debit  1 000.00, credit      0
//     company 1, account 1320, debit    130.00, credit      0
//     company 1, account 2000, debit        0,  credit  1 130.00

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrBillNotDraft is returned when posting is attempted on a non-draft bill.
var ErrBillNotDraft = errors.New("only draft bills can be posted")

// ErrNoAPAccount is returned when no active Accounts Payable account exists for the company.
var ErrNoAPAccount = errors.New("no active Accounts Payable account found — create one in your Chart of Accounts first")

// PostBill transitions a draft bill to "posted" and generates a double-entry
// journal entry in a single database transaction.
//
// Recovery mode behaviour (from TaxCode.RecoveryMode):
//   - full:    entire tax → ITC Receivable debit; expense = lineNet only.
//   - partial: TaxCode.RecoveryRate % → ITC Receivable; remainder embedded in expense.
//   - none:    no ITC line; full tax embedded in expense debit.
//
// Returns ErrBillNotDraft, ErrNoAPAccount, or a descriptive error on failure.
func PostBill(db *gorm.DB, companyID, billID uint, actor string, userID *uuid.UUID) error {
	// ── 1. Load bill with full line detail ────────────────────────────────────
	var bill models.Bill
	if err := db.
		Preload("Lines.TaxCode").
		Preload("Lines.ProductService").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&bill).Error; err != nil {
		return fmt.Errorf("load bill: %w", err)
	}

	// ── 2. Pre-flight checks ──────────────────────────────────────────────────
	if bill.Status != models.BillStatusDraft {
		return ErrBillNotDraft
	}
	if len(bill.Lines) == 0 {
		return errors.New("bill has no line items")
	}
	for i, l := range bill.Lines {
		if l.ExpenseAccountID == nil {
			return fmt.Errorf("line %d (%q): expense account is required before posting", i+1, l.Description)
		}
		// Bundle items cannot be used on bills — bundles are sales-only combinations.
		if l.ProductService != nil && l.ProductService.ItemStructureType == models.ItemStructureBundle {
			return fmt.Errorf("line %d (%q): bundle items cannot be used on purchase bills", i+1, l.Description)
		}
	}

	// ── 2b. Load company (base currency + Phase H capability rails) ──────────
	// Full row so receipt_required and gr_ir_clearing_account_id are
	// available for the H.4 flag branch.
	var company models.Company
	if err := db.First(&company, companyID).Error; err != nil {
		return fmt.Errorf("load company: %w", err)
	}

	// ── 2b1. Phase H.4 capability gate (receipt_required) ────────────────────
	// When receipt_required=true and the bill carries any stock-backed line,
	// Bill post must NOT form inventory (Receipt owns that) and must route
	// the stock-line debit to the company's GR/IR clearing account. If GR/IR
	// is not configured, fail loud with the same sentinel Receipt uses —
	// so the configuration requirement is symmetric across the two post
	// paths (no half-configured half-bridged state).
	stockOnBill := billHasStockLine(bill)
	billUsesGRIRClearing := company.ReceiptRequired && stockOnBill
	if billUsesGRIRClearing && company.GRIRClearingAccountID == nil {
		return ErrGRIRAccountNotConfigured
	}

	// ── 2c. Determine exchange rate ───────────────────────────────────────────
	exchangeRate := decimal.NewFromInt(1)
	transactionCurrencyCode := company.BaseCurrencyCode
	if normalizeCurrencyCode(bill.CurrencyCode) != "" {
		transactionCurrencyCode = normalizeCurrencyCode(bill.CurrencyCode)
	}
	isForeignCurrency := transactionCurrencyCode != company.BaseCurrencyCode
	jeSnapshot := IdentityExchangeRateSnapshot(company.BaseCurrencyCode, bill.BillDate)
	if isForeignCurrency {
		if bill.ExchangeRate.GreaterThan(decimal.Zero) && !bill.ExchangeRate.Equal(decimal.NewFromInt(1)) {
			exchangeRate = bill.ExchangeRate
			jeSnapshot = ExchangeRateSnapshot{
				TransactionCurrencyCode: transactionCurrencyCode,
				BaseCurrencyCode:        company.BaseCurrencyCode,
				ExchangeRate:            exchangeRate.RoundBank(8),
				ExchangeRateDate:        normalizeDate(bill.BillDate),
				ExchangeRateSource:      JournalEntryExchangeRateSourceManual,
				SourceLabel:             ExchangeRateSourceLabel(JournalEntryExchangeRateSourceManual),
			}
		} else {
			row, found, err := lookupExchangeRateRow(db, companyID, transactionCurrencyCode, company.BaseCurrencyCode, bill.BillDate)
			if err != nil {
				return fmt.Errorf("exchange rate %s→%s not found for %s: %w",
					transactionCurrencyCode, company.BaseCurrencyCode, bill.BillDate.Format("2006-01-02"), err)
			}
			if !found {
				return fmt.Errorf("exchange rate %s→%s not found for %s: %w",
					transactionCurrencyCode, company.BaseCurrencyCode, bill.BillDate.Format("2006-01-02"), ErrNoRate)
			}
			jeSnapshot = snapshotFromExchangeRateRow(row, companyID)
			exchangeRate = jeSnapshot.ExchangeRate
		}
	}

	// ── 2b. Validate vendor currency policy (Phase 12) ───────────────────────
	if err := ValidateDocumentCurrency(db, companyID, bill.VendorID,
		models.PartyTypeVendor, transactionCurrencyCode, company.BaseCurrencyCode); err != nil {
		return err
	}

	// ── 3. Resolve AP account (Phase 11: ARAPControlMapping) ─────────────────
	// Consults the control-account mapping table first, then falls back through
	// legacy system_key ("ap_{code}") → first active AP account.
	apAccount, err := ResolveControlAccount(db, companyID, 0,
		models.ARAPDocTypeBill, transactionCurrencyCode, isForeignCurrency,
		models.DetailAccountsPayable, ErrNoAPAccount)
	if err != nil {
		return err
	}

	// ── 3b. Resolve warehouse for inventory posting ───────────────────────────
	// Resolve: bill.WarehouseID → company default warehouse → nil (legacy path).
	billWarehouseID := ResolveInventoryWarehouse(db, companyID, bill.WarehouseID)

	// ── 4. Build posting fragments ────────────────────────────────────────────
	// Pure function: one DR per line (expense ± embedded tax), one DR per
	// recoverable-tax line (ITC), and one CR (AP) for the gross total.
	frags, err := BuildBillFragments(bill, apAccount.ID)
	if err != nil {
		return fmt.Errorf("build bill fragments: %w", err)
	}

	// ── 4b. Redirect stock-line expense debits.
	// Legacy path (receipt_required=false): Expense → Inventory Asset
	// (inventory-forming). Phase H.4 (receipt_required=true with stock
	// lines): Expense → GR/IR Clearing (Bill is financial-only; Receipt
	// owns inventory formation). Non-stock lines untouched on both paths.
	// Phase H.5 (receipt_required=true with receipt-line-matched stock
	// lines) runs INSIDE the tx below to read cumulative matching state
	// with current row locks; outside-tx reads could let a concurrent
	// post double-match the same receipt line.
	if billUsesGRIRClearing {
		frags = AdjustBillFragmentsForGRIRClearing(frags, bill, *company.GRIRClearingAccountID)
	} else {
		frags = AdjustBillFragmentsForInventory(frags, bill)
	}

	// ── 4c. Phase H.5 PPV pre-guard (fail before tx if config missing) ──────
	// Configuration errors surface before any DB write. Actual matching
	// computation runs inside the tx (needs stable reads).
	hasReceiptRef := false
	for _, l := range bill.Lines {
		if l.ReceiptLineID != nil && *l.ReceiptLineID != 0 {
			hasReceiptRef = true
			break
		}
	}
	if billUsesGRIRClearing && hasReceiptRef && company.PurchasePriceVarianceAccountID == nil {
		return ErrPPVAccountNotConfigured
	}

	// ── 5-6. Aggregate + FX scaling + balance check happen INSIDE the tx so
	// that the H.5 matching transform (which must read cumulative state
	// from other posted bills) can be stitched in before aggregation,
	// avoiding a two-pass outside-then-inside-tx pattern. Legacy bills
	// (flag=false) and flag=true bills without matching pass through the
	// same tx-local finalization with zero matching work performed.
	var (
		jeLines         []PostingFragment
		txJournalLines  []PostingFragment
		docCreditSum    decimal.Decimal
		creditSum       decimal.Decimal
	)

	// ── 7. Transaction ────────────────────────────────────────────────────────
	return db.Transaction(func(tx *gorm.DB) error {
		// a. Lock bill row and re-validate status inside the lock.
		var locked models.Bill
		if err := applyLockForUpdate(
			tx.Select("id", "company_id", "status").
				Where("id = ? AND company_id = ?", billID, companyID),
		).First(&locked).Error; err != nil {
			return fmt.Errorf("lock bill: %w", err)
		}
		if locked.Status != models.BillStatusDraft {
			return ErrAlreadyPosted
		}

		// a2. H.5 matching transform (when engaged): resolve per-line
		// match context and split the relevant fragments into precise
		// GR/IR + PPV + blind-GR/IR shapes. resolveBillLineMatchingContext
		// takes the necessary row-level write lock on each referenced
		// receipt_line (H-hardening-1) so concurrent PostBills targeting
		// the same receipt line serialise through this path.
		//
		// (The previous pre-matching preload of ReceiptLine onto each
		// bill.Lines[i] has been removed: it loaded without a lock and
		// was not actually consumed downstream. The matching function
		// re-loads under lock and owns the authoritative copy.)
		if billUsesGRIRClearing && hasReceiptRef {
			matchingCtx, err := resolveBillLineMatchingContext(tx, bill)
			if err != nil {
				return err
			}
			if len(matchingCtx) > 0 {
				frags, err = applyBillLineMatchingToFragments(
					frags, bill, matchingCtx,
					*company.GRIRClearingAccountID,
					*company.PurchasePriceVarianceAccountID,
				)
				if err != nil {
					return err
				}
			}
		}

		// a3. Aggregate + FX scaling + balance check (moved inside tx to
		// share the tx-local frags with the matching transform above).
		aggLines, err := AggregateJournalLines(frags)
		if err != nil {
			return fmt.Errorf("aggregate journal lines: %w", err)
		}
		jeLines = aggLines
		txJournalLines = make([]PostingFragment, len(jeLines))
		copy(txJournalLines, jeLines)
		docCreditSum = sumPostingCredits(jeLines)
		if isForeignCurrency {
			jeLines = applyFXScaling(jeLines, exchangeRate, apAccount.ID, false)
		}
		debitSum := sumPostingDebits(jeLines)
		creditSum = sumPostingCredits(jeLines)
		if !debitSum.Equal(creditSum) {
			return fmt.Errorf(
				"journal entry imbalance: debit sum %s, credit sum %s — check line totals",
				debitSum.StringFixed(2), creditSum.StringFixed(2),
			)
		}

		// b. Journal entry header.
		je := models.JournalEntry{
			CompanyID:               companyID,
			EntryDate:               bill.BillDate,
			JournalNo:               bill.BillNumber,
			Status:                  models.JournalEntryStatusPosted,
			SourceType:              models.LedgerSourceBill,
			SourceID:                bill.ID,
			TransactionCurrencyCode: transactionCurrencyCode,
			ExchangeRate:            jeSnapshot.ExchangeRate,
			ExchangeRateDate:        jeSnapshot.ExchangeRateDate,
			ExchangeRateSource:      jeSnapshot.ExchangeRateSource,
		}
		if err := wrapUniqueViolation(tx.Create(&je).Error, "create journal entry"); err != nil {
			return fmt.Errorf("create journal entry: %w", err)
		}

		// c. Journal lines — one per aggregated fragment.
		//    Collect created rows for the ledger projection step.
		createdLines := make([]models.JournalLine, 0, len(jeLines))
		for i, jl := range jeLines {
			txLine := txJournalLines[i]
			line := models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      jl.AccountID,
				TxDebit:        txLine.Debit,
				TxCredit:       txLine.Credit,
				Debit:          jl.Debit,
				Credit:         jl.Credit,
				Memo:           jl.Memo,
				PartyType:      models.PartyTypeVendor,
				PartyID:        bill.VendorID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create journal line: %w", err)
			}
			createdLines = append(createdLines, line)
		}

		// c2. Secondary book amounts — no-op when no secondary books are configured.
		if err := WriteSecondaryBookAmounts(tx, companyID, createdLines,
			transactionCurrencyCode, bill.BillDate,
			models.FXPostingReasonTransaction); err != nil {
			return fmt.Errorf("write secondary book amounts: %w", err)
		}

		// d. Ledger projection — one ledger_entry per journal_line, status=active.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourceBill,
			SourceID:     bill.ID,
		}); err != nil {
			return fmt.Errorf("project to ledger: %w", err)
		}

		// e. Record inventory purchase movements for stock items.
		// Phase H.4 gate: under receipt_required=true, Bill does NOT form
		// inventory — Receipt (H.3) owns that side. Skipping the call
		// entirely keeps source_type='bill' movements absent from
		// inventory_movements so queries, valuation reports, and
		// reversals see a clean separation between pre-H and post-H
		// receiving histories within the same company.
		if !company.ReceiptRequired {
			if err := CreatePurchaseMovements(tx, companyID, bill, billWarehouseID); err != nil {
				return fmt.Errorf("inventory purchase movements: %w", err)
			}
		}

		// f. Update bill: mark posted, cache grand total (document-currency), link journal entry,
		//    and snapshot base-currency equivalents.
		// Phase 4: also set balance_due = amount (doc currency) and balance_due_base = amountBase
		// so FX settlement can pro-rate the AP carrying value across partial payments.
		amountBase := creditSum
		billUpdates := map[string]any{
			"status":           string(models.BillStatusPosted),
			"amount":           docCreditSum,
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"subtotal_base":    bill.Subtotal.Mul(exchangeRate).Round(2),
			"tax_total_base":   bill.TaxTotal.Mul(exchangeRate).Round(2),
			"balance_due":      docCreditSum,
			"balance_due_base": amountBase,
		}
		if isForeignCurrency {
			billUpdates["exchange_rate"] = exchangeRate
		}
		if err := tx.Model(&bill).Updates(billUpdates).Error; err != nil {
			return fmt.Errorf("update bill status: %w", err)
		}

		// IN.3: Rule #4 post-time invariant. Count stock-item lines
		// and assert the movement-owner dispatch actually produced
		// (or correctly suppressed) inventory_movements rows. Any
		// violation aborts the tx — no JE survives a Rule #4 break.
		billStockLines := 0
		for _, l := range bill.Lines {
			if l.ProductService != nil && l.ProductService.IsStockItem {
				billStockLines++
			}
		}
		if err := AssertRule4PostTimeInvariant(tx, companyID,
			Rule4DocBill, bill.ID, billStockLines,
			Rule4WorkflowState{
				ReceiptRequired:  company.ReceiptRequired,
				ShipmentRequired: company.ShipmentRequired,
			},
		); err != nil {
			return err
		}

		// f. Audit log.
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "bill.posted", "bill", bill.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"bill_number":      bill.BillNumber,
				"journal_entry_id": je.ID,
				"total":            creditSum.StringFixed(2),
			},
		)
	})
}
