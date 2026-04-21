// 遵循project_guide.md
package services

// vendor_credit_note_service.go — VendorCreditNote: vendor-issued credit reducing AP.
//
// Accounting rules:
//
//   Post (draft → posted):
//     Dr  APAccountID       AmountBase   (reduces AP liability)
//     Cr  OffsetAccountID   AmountBase   (purchase returns / adjustments)
//
//   Void (draft only):
//     Sets status=voided; no JE.
//
// State machine:
//   draft → posted → partially_applied → fully_applied
//          ↘ voided (from draft only)

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrVendorCreditNoteNotFound      = errors.New("vendor credit note not found")
	ErrVendorCreditNoteInvalidStatus = errors.New("action not allowed in current vendor credit note status")
	ErrVendorCreditNoteNoAPAcct      = errors.New("AP account is required before posting")
	ErrVendorCreditNoteNoOffsetAcct  = errors.New("offset account is required before posting")
	ErrVCNApplyAmountExceedsBalance  = errors.New("amount to apply exceeds vendor credit note remaining balance")
	ErrVCNApplyAmountExceedsBill     = errors.New("amount to apply exceeds bill balance due")
	ErrVCNVendorMismatch             = errors.New("vendor credit note and bill must belong to the same vendor")
	ErrVCNBillNotOpen                = errors.New("bill must be posted or partially paid to apply a credit")

	// IN.6a Rule #4 sentinels — emitted by vendor_credit_note_posting
	// pre-flight when a stock-item line violates the required chain.
	// See RULE4_RUNBOOK.md §10b for operator triage.
	ErrVendorCreditNoteStockItemRequiresReturnReceipt  = errors.New("vendor credit note stock-item line requires Return Receipt (controlled mode)")
	ErrVendorCreditNoteStockItemRequiresBill           = errors.New("vendor credit note stock-item line requires a linked Bill")
	ErrVendorCreditNoteStockItemRequiresOriginalLine   = errors.New("vendor credit note stock-item line requires OriginalBillLineID")
	ErrVendorCreditNoteOriginalLineMismatch            = errors.New("vendor credit note original bill line does not match a known inventory movement")
	ErrVendorCreditNotePartialReturnNotSupported       = errors.New("vendor credit note stock-item partial returns not supported yet; line qty must equal original bill line qty")
)

// ── Input types ───────────────────────────────────────────────────────────────

// VendorCreditNoteInput holds all data needed to create or update a VendorCreditNote.
//
// IN.6a: Lines is the new line-level payload. Empty = legacy header-
// only credit (Amount drives posting, Dr AP / Cr Offset only). Non-
// empty = line-by-line dispatch where stock-item lines route through
// the Rule #4 inventory-reversal path at post time.
//
// When Lines is non-empty, the header Amount is recomputed from the
// sum of line amounts so the AR/AP reconciler sees a single
// authoritative figure.
type VendorCreditNoteInput struct {
	VendorID       uint
	BillID         *uint
	VendorReturnID *uint
	CreditNoteDate time.Time
	CurrencyCode   string
	ExchangeRate   decimal.Decimal
	Amount         decimal.Decimal
	APAccountID    *uint
	OffsetAccountID *uint
	Reason         string
	Memo           string

	Lines []VendorCreditNoteLineInput
}

// VendorCreditNoteLineInput is one line on the VCN create/update
// request (IN.6a). A line pointing at a stock item MUST carry
// OriginalBillLineID; pre-flight in PostVendorCreditNote rejects
// otherwise.
type VendorCreditNoteLineInput struct {
	SortOrder          uint
	ProductServiceID   *uint
	OriginalBillLineID *uint
	Description        string
	Qty                decimal.Decimal
	UnitPrice          decimal.Decimal
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextVendorCreditNoteNumber(db *gorm.DB, companyID uint) string {
	var last models.VendorCreditNote
	db.Where("company_id = ?", companyID).Order("id desc").Select("credit_note_number").First(&last)
	return NextDocumentNumber(last.CreditNoteNumber, "VCN-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateVendorCreditNote creates a new draft vendor credit note.
//
// IN.6a: if Lines is non-empty, the VCN Amount is recomputed from
// the sum of line amounts (Qty × UnitPrice), and each line is
// persisted. If Lines is empty, legacy header-only path — Amount
// from input drives the single-amount record.
func CreateVendorCreditNote(db *gorm.DB, companyID uint, in VendorCreditNoteInput) (*models.VendorCreditNote, error) {
	if in.VendorID == 0 {
		return nil, errors.New("vendor is required")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	// Derive the authoritative amount:
	//   lines present → sum(line.Qty × line.UnitPrice), rounded
	//   lines empty   → trust input header amount
	headerAmount := in.Amount
	if len(in.Lines) > 0 {
		sum := decimal.Zero
		for _, l := range in.Lines {
			sum = sum.Add(l.Qty.Mul(l.UnitPrice))
		}
		headerAmount = sum.Round(2)
	}
	if !headerAmount.IsPositive() {
		return nil, errors.New("credit note amount must be positive")
	}

	vcn := models.VendorCreditNote{
		CompanyID:        companyID,
		VendorID:         in.VendorID,
		BillID:           in.BillID,
		VendorReturnID:   in.VendorReturnID,
		CreditNoteNumber: nextVendorCreditNoteNumber(db, companyID),
		Status:           models.VendorCreditNoteStatusDraft,
		CreditNoteDate:   in.CreditNoteDate,
		CurrencyCode:     in.CurrencyCode,
		ExchangeRate:     rate,
		Amount:           headerAmount,
		RemainingAmount:  headerAmount,
		APAccountID:      in.APAccountID,
		OffsetAccountID:  in.OffsetAccountID,
		Reason:           in.Reason,
		Memo:             in.Memo,
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&vcn).Error; err != nil {
			return fmt.Errorf("create vendor credit note: %w", err)
		}
		for i, l := range in.Lines {
			sortOrder := l.SortOrder
			if sortOrder == 0 {
				sortOrder = uint(i + 1)
			}
			row := models.VendorCreditNoteLine{
				CompanyID:          companyID,
				VendorCreditNoteID: vcn.ID,
				SortOrder:          sortOrder,
				ProductServiceID:   l.ProductServiceID,
				OriginalBillLineID: l.OriginalBillLineID,
				Description:        l.Description,
				Qty:                l.Qty,
				UnitPrice:          l.UnitPrice,
				Amount:             l.Qty.Mul(l.UnitPrice).Round(2),
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create vcn line %d: %w", i+1, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &vcn, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetVendorCreditNote loads a credit note with vendor, accounts, and applications for the given company.
func GetVendorCreditNote(db *gorm.DB, companyID, vcnID uint) (*models.VendorCreditNote, error) {
	var vcn models.VendorCreditNote
	err := db.Preload("Vendor").Preload("APAccount").Preload("OffsetAccount").
		Preload("Bill").Preload("VendorReturn").
		Preload("Applications").Preload("Applications.Bill").
		Preload("Lines").Preload("Lines.ProductService").
		Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorCreditNoteNotFound
	}
	return &vcn, err
}

// ListVendorCreditNotes returns credit notes for a company, newest first.
func ListVendorCreditNotes(db *gorm.DB, companyID uint, statusFilter string, vendorID uint) ([]models.VendorCreditNote, error) {
	q := db.Preload("Vendor").Where("company_id = ?", companyID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	if vendorID > 0 {
		q = q.Where("vendor_id = ?", vendorID)
	}
	var vcns []models.VendorCreditNote
	err := q.Order("id desc").Find(&vcns).Error
	return vcns, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateVendorCreditNote updates a draft vendor credit note.
//
// IN.6a: if Lines is supplied (non-nil, may be empty slice to
// explicitly clear), the existing line set is replaced and the
// header Amount is recomputed from the new line sum. If Lines is
// nil, legacy path — header amount from input is trusted and
// existing lines remain.
func UpdateVendorCreditNote(db *gorm.DB, companyID, vcnID uint, in VendorCreditNoteInput) (*models.VendorCreditNote, error) {
	var vcn models.VendorCreditNote
	if err := db.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVendorCreditNoteNotFound
		}
		return nil, err
	}
	if vcn.Status != models.VendorCreditNoteStatusDraft {
		return nil, fmt.Errorf("%w: only draft credit notes may be edited", ErrVendorCreditNoteInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	// Recompute header amount when lines are supplied.
	headerAmount := in.Amount
	if in.Lines != nil && len(in.Lines) > 0 {
		sum := decimal.Zero
		for _, l := range in.Lines {
			sum = sum.Add(l.Qty.Mul(l.UnitPrice))
		}
		headerAmount = sum.Round(2)
	}
	headerAmount = headerAmount.Round(2)

	err := db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"vendor_id":         in.VendorID,
			"bill_id":           in.BillID,
			"vendor_return_id":  in.VendorReturnID,
			"credit_note_date":  in.CreditNoteDate,
			"currency_code":     in.CurrencyCode,
			"exchange_rate":     rate,
			"amount":            headerAmount,
			"remaining_amount":  headerAmount,
			"ap_account_id":     in.APAccountID,
			"offset_account_id": in.OffsetAccountID,
			"reason":            in.Reason,
			"memo":              in.Memo,
		}
		if err := tx.Model(&vcn).Updates(updates).Error; err != nil {
			return fmt.Errorf("update vendor credit note: %w", err)
		}

		// IN.6a: replace lines when the caller provided a non-nil
		// Lines slice. nil slice = caller is not managing lines this
		// call (legacy-style update).
		if in.Lines != nil {
			if err := tx.Where("company_id = ? AND vendor_credit_note_id = ?", companyID, vcnID).
				Delete(&models.VendorCreditNoteLine{}).Error; err != nil {
				return fmt.Errorf("clear existing vcn lines: %w", err)
			}
			for i, l := range in.Lines {
				sortOrder := l.SortOrder
				if sortOrder == 0 {
					sortOrder = uint(i + 1)
				}
				row := models.VendorCreditNoteLine{
					CompanyID:          companyID,
					VendorCreditNoteID: vcnID,
					SortOrder:          sortOrder,
					ProductServiceID:   l.ProductServiceID,
					OriginalBillLineID: l.OriginalBillLineID,
					Description:        l.Description,
					Qty:                l.Qty,
					UnitPrice:          l.UnitPrice,
					Amount:             l.Qty.Mul(l.UnitPrice).Round(2),
				}
				if err := tx.Create(&row).Error; err != nil {
					return fmt.Errorf("create vcn line %d: %w", i+1, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &vcn, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostVendorCreditNote transitions a draft credit note to posted and generates a JE.
//
// Journal entry (legacy, header-only or non-stock lines):
//
//	Dr  APAccountID       AmountBase   (reduces AP liability)
//	Cr  OffsetAccountID   AmountBase   (purchase returns / adjustments)
//
// IN.6a — Rule #4 on stock-item lines (legacy mode only)
// ----------------------------------------------------
// Each line carrying ProductService.IsStockItem=true triggers the
// stock-return path: the original Bill movement is reversed via
// inventory.ReverseMovement (authoritative snapshot cost), and two
// extra JE fragments are appended:
//
//	Dr  OffsetAccountID   line.InventoryValue   (cancel stock portion of purchase-returns credit)
//	Cr  line.InventoryAccountID   line.InventoryValue   (remove asset)
//
// Net effect for the stock portion: Dr AP / Cr Inventory — the
// correct shape for a physical return. Service and non-stock lines
// continue to land on the purchase-returns / offset account.
//
// Pre-flight rejects stock-item lines that:
//   - post under receipt_required=true (controlled mode) — defer to
//     future Vendor Return Receipt slice,
//   - sit on a VCN with no BillID — can't trace the original cost,
//   - lack OriginalBillLineID — same reason,
//   - have Qty != original Bill movement qty — partial returns not
//     supported in IN.6a (see vendor_credit_note_posting.go).
func PostVendorCreditNote(db *gorm.DB, companyID, vcnID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var vcn models.VendorCreditNote
		err := tx.Preload("Lines").Preload("Lines.ProductService").
			Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorCreditNoteNotFound
			}
			return err
		}
		if vcn.Status != models.VendorCreditNoteStatusDraft {
			return fmt.Errorf("%w: only draft credit notes can be posted", ErrVendorCreditNoteInvalidStatus)
		}
		if vcn.APAccountID == nil || *vcn.APAccountID == 0 {
			return ErrVendorCreditNoteNoAPAcct
		}
		if vcn.OffsetAccountID == nil || *vcn.OffsetAccountID == 0 {
			return ErrVendorCreditNoteNoOffsetAcct
		}

		// Load company capability rail for Rule #4 dispatch.
		var company models.Company
		if err := tx.Select("id", "receipt_required", "shipment_required").
			Where("id = ?", companyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company for VCN post: %w", err)
		}

		// IN.6a pre-flight on stock-item lines.
		stockLineCount := 0
		for i, l := range vcn.Lines {
			if l.ProductService == nil || !l.ProductService.IsStockItem {
				continue
			}
			stockLineCount++
			if company.ReceiptRequired {
				return fmt.Errorf("%w: line[%d]", ErrVendorCreditNoteStockItemRequiresReturnReceipt, i)
			}
			if vcn.BillID == nil || *vcn.BillID == 0 {
				return fmt.Errorf("%w: line[%d] item=%d", ErrVendorCreditNoteStockItemRequiresBill, i, *l.ProductServiceID)
			}
			if l.OriginalBillLineID == nil || *l.OriginalBillLineID == 0 {
				return fmt.Errorf("%w: line[%d] item=%d", ErrVendorCreditNoteStockItemRequiresOriginalLine, i, *l.ProductServiceID)
			}
		}

		rate := vcn.ExchangeRate
		if rate.IsZero() || rate.IsNegative() {
			rate = decimal.NewFromInt(1)
		}
		amountBase := vcn.Amount.Mul(rate).Round(2)

		// Build header (legacy) JE fragments: Dr AP / Cr Offset.
		frags := []PostingFragment{
			{
				AccountID: *vcn.APAccountID,
				Debit:     amountBase,
				Memo:      vcn.CreditNoteNumber + " – AP reduction",
			},
			{
				AccountID: *vcn.OffsetAccountID,
				Credit:    amountBase,
				Memo:      vcn.CreditNoteNumber + " – purchase return",
			},
		}

		// IN.6a inventory path: reverse original bill movements at
		// traced cost for each stock line, append Dr Offset /
		// Cr Inventory fragments.
		returns, err := CreateVendorCreditNoteInventoryReturns(tx, vcn)
		if err != nil {
			return err
		}
		if invFrags := buildVendorCreditNoteInventoryFragments(returns, *vcn.OffsetAccountID, vcn.CreditNoteNumber); len(invFrags) > 0 {
			frags = append(frags, invFrags...)
		}

		// Aggregate so the Offset account nets out correctly.
		aggregated, err := AggregateJournalLines(frags)
		if err != nil {
			return fmt.Errorf("aggregate VCN journal lines: %w", err)
		}

		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  vcn.CreditNoteDate,
			JournalNo:  "VCN – " + vcn.CreditNoteNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceVendorCreditNote,
			SourceID:   vcn.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create credit note JE: %w", err)
		}

		lines := make([]models.JournalLine, 0, len(aggregated))
		for _, f := range aggregated {
			lines = append(lines, models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      f.AccountID,
				Debit:          f.Debit,
				Credit:         f.Credit,
				Memo:           f.Memo,
			})
		}
		if len(lines) == 0 {
			return fmt.Errorf("vendor credit note %d: no journal lines produced", vcn.ID)
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create credit note JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceVendorCreditNote,
			SourceID:     vcn.ID,
		}); err != nil {
			return fmt.Errorf("project credit note to ledger: %w", err)
		}

		now := time.Now()
		updates := map[string]any{
			"status":           string(models.VendorCreditNoteStatusPosted),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"posted_at":        &now,
			"posted_by":        actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		if err := tx.Model(&vcn).Updates(updates).Error; err != nil {
			return err
		}

		// IN.3 invariant assertion — catches the silent-swallow class
		// if a future refactor drops the inventory path.
		return AssertRule4PostTimeInvariant(tx, companyID,
			Rule4DocVendorCreditNote, vcn.ID, stockLineCount,
			Rule4WorkflowState{
				ReceiptRequired:  company.ReceiptRequired,
				ShipmentRequired: company.ShipmentRequired,
			})
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidVendorCreditNote cancels a draft credit note. No JE is generated.
func VoidVendorCreditNote(db *gorm.DB, companyID, vcnID uint) error {
	var vcn models.VendorCreditNote
	if err := db.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVendorCreditNoteNotFound
		}
		return err
	}
	if vcn.Status != models.VendorCreditNoteStatusDraft {
		return fmt.Errorf("%w: only draft credit notes can be voided", ErrVendorCreditNoteInvalidStatus)
	}
	return db.Model(&vcn).Update("status", models.VendorCreditNoteStatusVoided).Error
}

// ── Apply to Bill ─────────────────────────────────────────────────────────────

// ApplyVendorCreditNoteToBill applies a portion of a posted vendor credit note
// against an open bill, reducing the bill's BalanceDue.
//
// No new JE is created — the accounting reduction to AP already occurred when
// the credit note was posted (Dr AP / Cr Purchase Returns). This operation is
// purely an AP open-item allocation.
//
// Rules:
//   - VCN must be posted or partially_applied
//   - Bill must be posted or partially_paid (open)
//   - VCN.VendorID must match Bill.VendorID
//   - amountToApply must be ≤ VCN.RemainingAmount and ≤ Bill.BalanceDue
func ApplyVendorCreditNoteToBill(db *gorm.DB, companyID, vcnID, billID uint, amountToApply decimal.Decimal) error {
	if !amountToApply.IsPositive() {
		return errors.New("amount to apply must be positive")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var vcn models.VendorCreditNote
		if err := tx.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorCreditNoteNotFound
			}
			return err
		}
		if vcn.Status != models.VendorCreditNoteStatusPosted &&
			vcn.Status != models.VendorCreditNoteStatusPartiallyApplied {
			return fmt.Errorf("%w: current status is %s", ErrVendorCreditNoteInvalidStatus, vcn.Status)
		}
		if amountToApply.GreaterThan(vcn.RemainingAmount) {
			return fmt.Errorf("%w: applying %s but only %s remaining",
				ErrVCNApplyAmountExceedsBalance, amountToApply.StringFixed(2), vcn.RemainingAmount.StringFixed(2))
		}

		var bill models.Bill
		if err := tx.Where("id = ? AND company_id = ?", billID, companyID).First(&bill).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("bill not found")
			}
			return err
		}
		if bill.VendorID != vcn.VendorID {
			return ErrVCNVendorMismatch
		}
		if bill.Status != models.BillStatusPosted && bill.Status != models.BillStatusPartiallyPaid {
			return ErrVCNBillNotOpen
		}
		if amountToApply.GreaterThan(bill.BalanceDue) {
			return fmt.Errorf("%w: applying %s but bill balance is %s",
				ErrVCNApplyAmountExceedsBill, amountToApply.StringFixed(2), bill.BalanceDue.StringFixed(2))
		}

		// Compute base amount proportionally (using VCN's exchange rate).
		amountBase := amountToApply
		if vcn.Amount.GreaterThan(decimal.Zero) && vcn.AmountBase.GreaterThan(decimal.Zero) {
			ratio := amountToApply.Div(vcn.Amount)
			amountBase = vcn.AmountBase.Mul(ratio).Round(2)
		}

		now := time.Now()

		// 1. Create application record.
		app := models.APCreditApplication{
			CompanyID:          companyID,
			VendorCreditNoteID: vcnID,
			BillID:             billID,
			AmountApplied:      amountToApply,
			AmountAppliedBase:  amountBase,
			AppliedAt:          now,
		}
		if err := tx.Create(&app).Error; err != nil {
			return fmt.Errorf("create AP credit application: %w", err)
		}

		// 2. Update VCN remaining / applied amounts and status.
		newVCNRemaining := vcn.RemainingAmount.Sub(amountToApply)
		newVCNApplied := vcn.AppliedAmount.Add(amountToApply)
		newVCNStatus := models.VendorCreditNoteStatusPartiallyApplied
		if newVCNRemaining.IsZero() {
			newVCNStatus = models.VendorCreditNoteStatusFullyApplied
		}
		if err := tx.Model(&vcn).Updates(map[string]any{
			"remaining_amount": newVCNRemaining,
			"applied_amount":   newVCNApplied,
			"status":           string(newVCNStatus),
		}).Error; err != nil {
			return fmt.Errorf("update vendor credit note: %w", err)
		}

		// 3. Update Bill balance due and status.
		newBillBalance := bill.BalanceDue.Sub(amountToApply)
		newBillStatus := models.BillStatusPartiallyPaid
		if newBillBalance.IsZero() {
			newBillStatus = models.BillStatusPaid
		}
		if err := tx.Model(&bill).Updates(map[string]any{
			"balance_due": newBillBalance,
			"status":      string(newBillStatus),
		}).Error; err != nil {
			return fmt.Errorf("update bill: %w", err)
		}

		return nil
	})
}

// ListOpenBillsForVendor returns bills that can receive a credit application
// (status posted or partially_paid, balance_due > 0) for a given vendor.
func ListOpenBillsForVendor(db *gorm.DB, companyID, vendorID uint) ([]models.Bill, error) {
	var bills []models.Bill
	err := db.Where("company_id = ? AND vendor_id = ? AND status IN ? AND balance_due > 0",
		companyID, vendorID, []string{
			string(models.BillStatusPosted),
			string(models.BillStatusPartiallyPaid),
		}).
		Order("bill_date asc, id asc").
		Find(&bills).Error
	return bills, err
}

// ListVCNApplicationsForBill returns all AP credit applications for a given bill.
func ListVCNApplicationsForBill(db *gorm.DB, companyID, billID uint) ([]models.APCreditApplication, error) {
	var apps []models.APCreditApplication
	err := db.Where("company_id = ? AND bill_id = ?", companyID, billID).
		Order("applied_at asc").Find(&apps).Error
	return apps, err
}

// ReverseAPCreditApplication removes a single credit application, restoring
// the VCN's remaining balance and the bill's balance due.
// Only valid when neither the VCN nor the bill is voided.
func ReverseAPCreditApplication(db *gorm.DB, companyID, applicationID uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var app models.APCreditApplication
		if err := tx.Where("id = ? AND company_id = ?", applicationID, companyID).
			First(&app).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("credit application not found")
			}
			return fmt.Errorf("load application: %w", err)
		}

		var vcn models.VendorCreditNote
		if err := tx.Where("id = ? AND company_id = ?", app.VendorCreditNoteID, companyID).
			First(&vcn).Error; err != nil {
			return fmt.Errorf("load vendor credit note: %w", err)
		}
		if vcn.Status == models.VendorCreditNoteStatusVoided {
			return errors.New("cannot reverse: vendor credit note is voided")
		}

		var bill models.Bill
		if err := tx.Where("id = ? AND company_id = ?", app.BillID, companyID).
			First(&bill).Error; err != nil {
			return fmt.Errorf("load bill: %w", err)
		}
		if bill.Status == models.BillStatusVoided {
			return errors.New("cannot reverse: bill is voided")
		}

		// Restore VCN balance.
		newRemaining := vcn.RemainingAmount.Add(app.AmountApplied)
		newApplied := vcn.AppliedAmount.Sub(app.AmountApplied)
		if newApplied.IsNegative() {
			newApplied = decimal.Zero
		}
		newVCNStatus := models.VendorCreditNoteStatusPartiallyApplied
		if newApplied.IsZero() {
			newVCNStatus = models.VendorCreditNoteStatusPosted
		}
		if err := tx.Model(&vcn).Updates(map[string]any{
			"remaining_amount": newRemaining,
			"applied_amount":   newApplied,
			"status":           string(newVCNStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore vendor credit note: %w", err)
		}

		// Restore Bill balance.
		newBillBalance := bill.BalanceDue.Add(app.AmountApplied)
		newBillStatus := models.BillStatusPartiallyPaid
		if newBillBalance.Equal(bill.Amount) {
			newBillStatus = models.BillStatusPosted
		}
		if err := tx.Model(&bill).Updates(map[string]any{
			"balance_due": newBillBalance,
			"status":      string(newBillStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore bill: %w", err)
		}

		// Delete application.
		if err := tx.Delete(&app).Error; err != nil {
			return fmt.Errorf("delete application: %w", err)
		}
		return nil
	})
}
