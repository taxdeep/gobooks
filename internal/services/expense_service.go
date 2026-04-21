package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/numbering"
)

var (
	ErrExpenseNotFound              = errors.New("expense not found")
	ErrExpenseDateRequired          = errors.New("expense date is required")
	ErrExpenseDescriptionRequired   = errors.New("description is required")
	ErrExpenseAmountInvalid         = errors.New("amount must be greater than zero")
	ErrExpenseCurrencyRequired      = errors.New("currency is required")
	ErrExpenseAccountRequired       = errors.New("expense account is required")
	ErrExpenseAccountInvalid        = errors.New("expense account is not valid for this company")
	ErrExpenseVendorInvalid         = errors.New("vendor is not valid for this company")
	ErrExpensePaymentAccountInvalid = errors.New("payment account is not valid for this company")
	ErrExpensePaymentMethodRequired = errors.New("payment method is required when a payment account is selected")
	ErrExpensePaymentMethodInvalid  = errors.New("payment method is not valid")
	ErrExpenseLinesRequired         = errors.New("at least one expense line with a positive amount is required")
	ErrExpenseLineAccountRequired   = errors.New("each expense line must have an expense category")
	ErrExpenseLineAccountInvalid    = errors.New("one or more expense line categories are not valid for this company")
)

// ExpenseLineInput represents a single cost-category row within an expense.
// When Lines is non-empty on ExpenseInput, the service derives the expense's
// total Amount and primary ExpenseAccountID from the lines.
type ExpenseLineInput struct {
	Description      string
	Amount           decimal.Decimal // pre-tax net (pure-expense line) or qty*unit_price (stock line)
	ExpenseAccountID *uint
	// ProductServiceID is the optional catalog linkage. Lets an
	// expense line reference a product or service in the catalog
	// alongside its GL ExpenseAccountID; nil means the line is a
	// pure cost-category entry with no catalog attachment.
	//
	// IN.2 / Rule #4: when ProductServiceID points at a stock item
	// (ProductService.IsStockItem=true), the line becomes a stock
	// line on post — legacy mode forms inventory_movements; controlled
	// mode rejects the post.
	ProductServiceID *uint
	// Qty and UnitPrice are authoritative when ProductServiceID is
	// set. The service layer records them on ExpenseLine and uses
	// them to drive inventory.ReceiveStock at post time for stock
	// lines. For pure-expense (ProductServiceID=nil) lines these
	// default to Qty=1, UnitPrice=Amount (legacy fallback).
	Qty              decimal.Decimal
	UnitPrice        decimal.Decimal
	TaxCodeID        *uint
	LineTax          decimal.Decimal
	LineTotal        decimal.Decimal // Amount + LineTax
	TaskID           *uint
	IsBillable       bool
}

type ExpenseInput struct {
	CompanyID          uint
	TaskID             *uint
	BillableCustomerID *uint
	IsBillable         bool

	ExpenseDate      time.Time
	Description      string
	Amount           decimal.Decimal
	CurrencyCode     string
	VendorID         *uint
	ExpenseAccountID *uint
	Notes            string

	// WarehouseID is the header-level warehouse used when stock-line
	// expenses form inventory movements on post (IN.2 / Q3). Nil is
	// legal for pure-expense submissions; post-time validation will
	// fail loud if a stock line is present but WarehouseID (and the
	// company default) are both absent.
	WarehouseID *uint

	// Lines replaces the single Amount/ExpenseAccountID when non-empty.
	// The service sums line amounts → Expense.Amount and uses lines[0].ExpenseAccountID
	// as Expense.ExpenseAccountID for backward-compat reporting joins.
	Lines []ExpenseLineInput

	// Payment settlement (all optional for draft save; POST requires
	// a PaymentAccountID — the credit side of the JE needs a target).
	PaymentAccountID *uint
	PaymentMethod    models.PaymentMethod
	PaymentReference string
}

type ExpenseListFilter struct {
	CompanyID uint
	TaskID    *uint
}

func CreateExpense(db *gorm.DB, in ExpenseInput) (*models.Expense, error) {
	expense, err := upsertExpense(db, 0, in)
	if err != nil {
		return nil, err
	}
	return expense, nil
}

func UpdateExpense(db *gorm.DB, companyID, expenseID uint, in ExpenseInput) (*models.Expense, error) {
	in.CompanyID = companyID
	return upsertExpense(db, expenseID, in)
}

func GetExpenseByID(db *gorm.DB, companyID, expenseID uint) (*models.Expense, error) {
	var expense models.Expense
	err := db.
		Preload("Task.Customer").
		Preload("BillableCustomer").
		Preload("Vendor").
		Preload("ExpenseAccount").
		Preload("Warehouse").
		Preload("Lines.ProductService").
		Preload("Lines", func(db *gorm.DB) *gorm.DB {
			return db.Order("line_order ASC, id ASC")
		}).
		Where("id = ? AND company_id = ?", expenseID, companyID).
		First(&expense).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrExpenseNotFound
	}
	if err != nil {
		return nil, err
	}
	return &expense, nil
}

func ListExpenses(db *gorm.DB, filter ExpenseListFilter) ([]models.Expense, error) {
	var expenses []models.Expense
	q := db.
		Preload("Task.Customer").
		Preload("BillableCustomer").
		Preload("Vendor").
		Preload("ExpenseAccount").
		Where("company_id = ?", filter.CompanyID)
	if filter.TaskID != nil && *filter.TaskID > 0 {
		q = q.Where("task_id = ?", *filter.TaskID)
	}
	if err := q.Order("expense_date desc, id desc").Find(&expenses).Error; err != nil {
		return nil, err
	}
	return expenses, nil
}

func upsertExpense(db *gorm.DB, expenseID uint, in ExpenseInput) (*models.Expense, error) {
	// When lines are present, derive header fields from them.
	if len(in.Lines) > 0 {
		grandTotal := decimal.Zero
		for _, l := range in.Lines {
			grandTotal = grandTotal.Add(l.LineTotal) // grand total = net + tax per line
		}
		in.Amount = grandTotal
		in.ExpenseAccountID = in.Lines[0].ExpenseAccountID
		// Use first non-empty line description as header description if blank.
		if strings.TrimSpace(in.Description) == "" {
			for _, l := range in.Lines {
				if d := strings.TrimSpace(l.Description); d != "" {
					in.Description = d
					break
				}
			}
		}
		// Derive header task linkage from first line that has a task.
		for _, l := range in.Lines {
			if l.TaskID != nil && *l.TaskID > 0 {
				in.TaskID = l.TaskID
				in.IsBillable = l.IsBillable
				break
			}
		}
	}

	if err := validateExpenseInput(db, in); err != nil {
		return nil, err
	}

	// Validate per-line accounts when lines are present.
	if len(in.Lines) > 0 {
		if err := validateExpenseLines(db, in); err != nil {
			return nil, err
		}
	}

	linkage, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:          in.CompanyID,
		TaskID:             in.TaskID,
		BillableCustomerID: in.BillableCustomerID,
		IsBillable:         in.IsBillable,
	})
	if err != nil {
		return nil, err
	}

	var savedID uint
	var assignedExpenseNumber string
	err = db.Transaction(func(tx *gorm.DB) error {
		var expense models.Expense
		if expenseID > 0 {
			if err := tx.Where("id = ? AND company_id = ?", expenseID, in.CompanyID).First(&expense).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrExpenseNotFound
				}
				return err
			}
		} else {
			expense = models.Expense{CompanyID: in.CompanyID}
			// Auto-assign the reference number from numbering settings
			// on create only. Edits never rewrite the number — a
			// posted expense's reference is part of its paper-trail
			// identity once saved.
			if expense.ExpenseNumber == "" {
				if suggestion, sErr := SuggestNextNumberForModule(tx, in.CompanyID, numbering.ModuleExpense); sErr == nil && suggestion != "" {
					expense.ExpenseNumber = suggestion
					assignedExpenseNumber = suggestion
				}
			}
		}

		expense.TaskID = linkage.TaskID
		expense.BillableCustomerID = linkage.BillableCustomerID
		expense.IsBillable = linkage.IsBillable
		expense.ReinvoiceStatus = linkage.ReinvoiceStatus
		expense.ExpenseDate = in.ExpenseDate
		expense.Description = strings.TrimSpace(in.Description)
		expense.Amount = in.Amount.RoundBank(2)
		expense.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
		expense.VendorID = in.VendorID
		expense.ExpenseAccountID = in.ExpenseAccountID
		expense.Notes = strings.TrimSpace(in.Notes)
		expense.PaymentAccountID = in.PaymentAccountID
		expense.PaymentMethod = in.PaymentMethod
		expense.PaymentReference = strings.TrimSpace(in.PaymentReference)

		if expenseID > 0 {
			if err := tx.Save(&expense).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(&expense).Error; err != nil {
				return err
			}
		}
		savedID = expense.ID

		expense.WarehouseID = in.WarehouseID

		// Replace expense lines when the submission includes them.
		if len(in.Lines) > 0 {
			if err := tx.Where("expense_id = ?", savedID).Delete(&models.ExpenseLine{}).Error; err != nil {
				return err
			}
			for i, l := range in.Lines {
				// IN.2 Qty/UnitPrice normalisation:
				//   - ProductServiceID set → form values authoritative
				//     (with fallback Qty=1 if blank). Amount stays as
				//     submitted (editor computes qty*unit_price).
				//   - ProductServiceID nil → legacy fallback: Qty=1,
				//     UnitPrice=Amount. Operator never needs to know
				//     Qty/UnitPrice existed for a pure-expense line.
				qty := l.Qty
				unitPrice := l.UnitPrice
				if l.ProductServiceID == nil || *l.ProductServiceID == 0 {
					qty = decimal.NewFromInt(1)
					unitPrice = l.Amount
				} else {
					if !qty.IsPositive() {
						qty = decimal.NewFromInt(1)
					}
					if unitPrice.IsNegative() {
						unitPrice = decimal.Zero
					}
				}
				line := models.ExpenseLine{
					ExpenseID:        savedID,
					LineOrder:        i,
					Description:      strings.TrimSpace(l.Description),
					Amount:           l.Amount.RoundBank(2),
					Qty:              qty,
					UnitPrice:        unitPrice,
					ExpenseAccountID: l.ExpenseAccountID,
					ProductServiceID: l.ProductServiceID,
					TaxCodeID:        l.TaxCodeID,
					LineTax:          l.LineTax.RoundBank(2),
					LineTotal:        l.LineTotal.RoundBank(2),
					TaskID:           l.TaskID,
					IsBillable:       l.IsBillable,
				}
				if err := tx.Create(&line).Error; err != nil {
					return err
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	// Bump the numbering settings counter outside the transaction
	// when this expense consumed the settings-derived suggestion. A
	// failed bump is non-fatal: the expense itself is already
	// committed; a subsequent expense would just reuse the same
	// counter value and get the same visible number. Rare enough to
	// not warrant a complex dual-commit strategy here.
	if assignedExpenseNumber != "" {
		_ = BumpModuleNextNumberAfterCreate(db, in.CompanyID, numbering.ModuleExpense)
	}
	return GetExpenseByID(db, in.CompanyID, savedID)
}

// validateExpenseLines checks per-line accounts exist and belong to
// the company. IN.2 relaxation: accept both Expense and Asset root
// types to support stock-item lines whose Category resolves to the
// product's Inventory Asset account. At post time, the JE builder
// (buildExpensePostingFragments) uses InventoryAccountID for stock
// lines regardless of what the line's own ExpenseAccountID is —
// mirrors the Bill pattern. Liability / Revenue / Equity accounts
// are still rejected: they would never be a legitimate category
// for an expense line regardless of stock/non-stock.
func validateExpenseLines(db *gorm.DB, in ExpenseInput) error {
	for _, l := range in.Lines {
		if l.ExpenseAccountID == nil || *l.ExpenseAccountID == 0 {
			return ErrExpenseLineAccountRequired
		}
	}
	// Batch-check all distinct account IDs.
	seen := map[uint]bool{}
	ids := make([]uint, 0, len(in.Lines))
	for _, l := range in.Lines {
		if l.ExpenseAccountID != nil && *l.ExpenseAccountID > 0 && !seen[*l.ExpenseAccountID] {
			seen[*l.ExpenseAccountID] = true
			ids = append(ids, *l.ExpenseAccountID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var count int64
	if err := db.Model(&models.Account{}).
		Where("id IN ? AND company_id = ? AND root_account_type IN ? AND is_active = true",
			ids, in.CompanyID,
			[]models.RootAccountType{models.RootExpense, models.RootAsset, models.RootCostOfSales}).
		Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(ids) {
		return ErrExpenseLineAccountInvalid
	}
	return nil
}

func validateExpenseInput(db *gorm.DB, in ExpenseInput) error {
	if in.CompanyID == 0 {
		return ErrExpenseNotFound
	}
	if in.ExpenseDate.IsZero() {
		return ErrExpenseDateRequired
	}
	if strings.TrimSpace(in.Description) == "" {
		return ErrExpenseDescriptionRequired
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return ErrExpenseAmountInvalid
	}
	if strings.TrimSpace(in.CurrencyCode) == "" {
		return ErrExpenseCurrencyRequired
	}
	if in.ExpenseAccountID == nil || *in.ExpenseAccountID == 0 {
		return ErrExpenseAccountRequired
	}

	// IN.2: header ExpenseAccountID (backward-compat reporting join;
	// derived from lines[0]) accepts Expense / Asset / CostOfSales
	// root types to accommodate stock-line Categories that resolve
	// to the product's Inventory Asset account.
	var count int64
	if err := db.Model(&models.Account{}).
		Where("id = ? AND company_id = ? AND root_account_type IN ? AND is_active = true",
			*in.ExpenseAccountID, in.CompanyID,
			[]models.RootAccountType{models.RootExpense, models.RootAsset, models.RootCostOfSales}).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrExpenseAccountInvalid
	}

	if in.VendorID != nil && *in.VendorID > 0 {
		count = 0
		if err := db.Model(&models.Vendor{}).
			Where("id = ? AND company_id = ?", *in.VendorID, in.CompanyID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrExpenseVendorInvalid
		}
	}

	if in.PaymentAccountID != nil && *in.PaymentAccountID > 0 {
		count = 0
		if err := db.Model(&models.Account{}).
			Where("id = ? AND company_id = ? AND detail_account_type IN ? AND is_active = true",
				*in.PaymentAccountID, in.CompanyID,
				models.PaymentSourceDetailTypes()).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrExpensePaymentAccountInvalid
		}
		if in.PaymentMethod == "" {
			return ErrExpensePaymentMethodRequired
		}
	}

	if in.PaymentMethod != "" {
		if _, err := models.ParsePaymentMethod(string(in.PaymentMethod)); err != nil {
			return ErrExpensePaymentMethodInvalid
		}
	}

	return nil
}

// ── IN.2 lifecycle — PostExpense / VoidExpense ───────────────────────────────

// PostExpense flips a draft Expense to posted, books the JE, and
// (under legacy receipt_required=false) forms inventory movements
// for every stock-item line. Under receipt_required=true (controlled
// mode), any stock-item line causes a hard rejection per Rule #4 Q2.
//
// Precondition checks:
//   - Expense exists and status=draft
//   - PaymentAccountID is set (credit target for the JE)
//   - No stock-item line when controlled mode is on
//
// Writes exactly one audit row (expense.posted) on success.
func PostExpense(db *gorm.DB, companyID, expenseID uint, actor string, actorUserID *uuid.UUID) (*models.Expense, error) {
	var out models.Expense
	err := db.Transaction(func(tx *gorm.DB) error {
		var expense models.Expense
		if err := applyLockForUpdate(
			tx.Preload("Lines.ProductService").
				Where("id = ? AND company_id = ?", expenseID, companyID),
		).First(&expense).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrExpenseNotFound
			}
			return err
		}
		if expense.Status != models.ExpenseStatusDraft {
			return fmt.Errorf("%w: current=%s", ErrExpenseNotDraft, expense.Status)
		}
		if expense.PaymentAccountID == nil || *expense.PaymentAccountID == 0 {
			return ErrExpensePaymentAccountRequiredForPost
		}

		// Re-read company for rail state.
		var company models.Company
		if err := tx.Select("id", "receipt_required").
			First(&company, companyID).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		// Controlled-mode rejection: no stock-item line allowed.
		hasStock := false
		for _, l := range expense.Lines {
			if l.ProductService != nil && l.ProductService.IsStockItem {
				hasStock = true
				break
			}
		}
		if hasStock && company.ReceiptRequired {
			return ErrExpenseStockItemRequiresReceipt
		}

		// Resolve warehouse for stock lines (legacy mode only).
		var resolvedWarehouseID uint
		if hasStock {
			if expense.WarehouseID != nil && *expense.WarehouseID != 0 {
				resolvedWarehouseID = *expense.WarehouseID
			} else {
				// Fall back to the company's default warehouse
				// (is_default=true). Fail loud if even that doesn't
				// exist — matches the PO / Bill stock-routing
				// convention rather than silently picking a warehouse.
				if dwID, err := DefaultWarehouseID(tx, companyID); err == nil && dwID != 0 {
					resolvedWarehouseID = dwID
				} else {
					return ErrExpenseWarehouseRequiredForStockLine
				}
			}
		}

		// Form inventory movements (legacy stock lines only).
		var stockResults []expenseMovementResult
		if hasStock {
			results, err := CreateExpenseMovements(tx, expense, resolvedWarehouseID)
			if err != nil {
				return fmt.Errorf("create expense movements: %w", err)
			}
			stockResults = results
		}

		// Build + aggregate JE fragments.
		frags, err := buildExpensePostingFragments(expense, stockResults, *expense.PaymentAccountID)
		if err != nil {
			return err
		}
		if len(frags) == 0 {
			return fmt.Errorf("expense post: no journal fragments produced (all lines zero?)")
		}
		jeLines, err := AggregateJournalLines(frags)
		if err != nil {
			return fmt.Errorf("aggregate journal lines: %w", err)
		}
		debitSum := sumPostingDebits(jeLines)
		creditSum := sumPostingCredits(jeLines)
		if !debitSum.Equal(creditSum) {
			return fmt.Errorf(
				"expense JE imbalance: debit %s, credit %s",
				debitSum.StringFixed(2), creditSum.StringFixed(2),
			)
		}

		// JE header + lines.
		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  expense.ExpenseDate,
			JournalNo:  expense.ExpenseNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceExpense,
			SourceID:   expense.ID,
		}
		if err := wrapUniqueViolation(tx.Create(&je).Error, "create expense journal entry"); err != nil {
			return fmt.Errorf("create journal entry: %w", err)
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
			// Vendor attribution carried on JE lines so vendor-scoped
			// drilldown reports can see expense spend without joining
			// back through the Expense header.
			if expense.VendorID != nil && *expense.VendorID != 0 {
				line.PartyType = models.PartyTypeVendor
				line.PartyID = *expense.VendorID
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create expense journal line: %w", err)
			}
			createdLines = append(createdLines, line)
		}
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourceExpense,
			SourceID:     expense.ID,
		}); err != nil {
			return fmt.Errorf("project expense to ledger: %w", err)
		}

		// Lifecycle flip.
		now := time.Now().UTC()
		expense.Status = models.ExpenseStatusPosted
		expense.PostedAt = &now
		expense.JournalEntryID = &je.ID
		if err := tx.Save(&expense).Error; err != nil {
			return fmt.Errorf("save expense: %w", err)
		}

		// IN.3: Rule #4 post-time invariant. Expense is movement
		// owner under legacy mode only; controlled mode rejected
		// stock lines pre-post (IN.2 Q2), so if we're here with
		// stock lines, we're legacy and movements must exist.
		expenseStockLines := 0
		for _, l := range expense.Lines {
			if l.ProductService != nil && l.ProductService.IsStockItem {
				expenseStockLines++
			}
		}
		if err := AssertRule4PostTimeInvariant(tx, companyID,
			Rule4DocExpense, expense.ID, expenseStockLines,
			Rule4WorkflowState{
				ReceiptRequired: company.ReceiptRequired,
			},
		); err != nil {
			return err
		}

		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"expense.posted",
			"expense",
			expense.ID,
			actorOrSystem(actor),
			map[string]any{
				"expense_number":   expense.ExpenseNumber,
				"journal_entry_id": je.ID,
				"stock_lines":      len(stockResults),
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ExpenseStatusDraft)},
			map[string]any{"status": string(models.ExpenseStatusPosted)},
		)
		out = expense
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetExpenseByID(db, companyID, out.ID)
}

// VoidExpense flips a posted Expense to voided. Reverses the JE
// (standard pattern) and, when stock lines exist, reverses their
// inventory movements via ReverseExpenseMovements.
//
// Writes exactly one audit row (expense.voided).
func VoidExpense(db *gorm.DB, companyID, expenseID uint, actor string, actorUserID *uuid.UUID) (*models.Expense, error) {
	var out models.Expense
	err := db.Transaction(func(tx *gorm.DB) error {
		var expense models.Expense
		if err := applyLockForUpdate(
			tx.Preload("Lines.ProductService").
				Where("id = ? AND company_id = ?", expenseID, companyID),
		).First(&expense).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrExpenseNotFound
			}
			return err
		}
		if expense.Status != models.ExpenseStatusPosted {
			return fmt.Errorf("%w: current=%s", ErrExpenseNotPosted, expense.Status)
		}
		if expense.JournalEntryID == nil {
			return fmt.Errorf("expense %d posted but has no journal_entry_id — data integrity issue", expense.ID)
		}

		var origJE models.JournalEntry
		if err := tx.Preload("Lines").
			Where("id = ? AND company_id = ?", *expense.JournalEntryID, companyID).
			First(&origJE).Error; err != nil {
			return fmt.Errorf("load original expense JE: %w", err)
		}
		if len(origJE.Lines) == 0 {
			return fmt.Errorf("original expense JE %d has no lines", origJE.ID)
		}
		reversalJE := models.JournalEntry{
			CompanyID:      companyID,
			EntryDate:      origJE.EntryDate,
			JournalNo:      "VOID-" + expense.ExpenseNumber,
			ReversedFromID: &origJE.ID,
			Status:         models.JournalEntryStatusPosted,
			SourceType:     models.LedgerSourceReversal,
			SourceID:       expense.ID,
		}
		if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal expense JE"); err != nil {
			return fmt.Errorf("create reversal JE: %w", err)
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
				return fmt.Errorf("create reversal line: %w", err)
			}
			createdRevLines = append(createdRevLines, line)
		}
		if err := tx.Model(&models.JournalEntry{}).
			Where("id = ? AND company_id = ?", origJE.ID, companyID).
			Update("status", models.JournalEntryStatusReversed).Error; err != nil {
			return fmt.Errorf("mark original JE reversed: %w", err)
		}
		if err := MarkLedgerEntriesReversed(tx, companyID, origJE.ID); err != nil {
			return fmt.Errorf("mark ledger entries reversed: %w", err)
		}
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: reversalJE,
			Lines:        createdRevLines,
			SourceType:   models.LedgerSourceReversal,
			SourceID:     expense.ID,
		}); err != nil {
			return fmt.Errorf("project reversal to ledger: %w", err)
		}

		// Reverse inventory movements (no-op if expense had no stock lines).
		if err := ReverseExpenseMovements(tx, companyID, expense); err != nil {
			return fmt.Errorf("reverse expense movements: %w", err)
		}

		now := time.Now().UTC()
		expense.Status = models.ExpenseStatusVoided
		expense.VoidedAt = &now
		if err := tx.Save(&expense).Error; err != nil {
			return fmt.Errorf("save expense: %w", err)
		}

		cid := companyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"expense.voided",
			"expense",
			expense.ID,
			actorOrSystem(actor),
			map[string]any{
				"expense_number": expense.ExpenseNumber,
				"original_je_id": *expense.JournalEntryID,
				"reversal_je_id": reversalJE.ID,
			},
			&cid,
			actorUserID,
			map[string]any{"status": string(models.ExpenseStatusPosted)},
			map[string]any{"status": string(models.ExpenseStatusVoided)},
		)
		out = expense
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetExpenseByID(db, companyID, out.ID)
}
