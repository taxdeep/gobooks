// 遵循project_guide.md
package services

// vendor_prepayment_service.go — VendorPrepayment: advance payment to vendor.
//
// Accounting rules:
//
//   Post (draft → posted):
//     Dr  PrepaymentAccountID   AmountBase   (vendor prepayment asset)
//     Cr  BankAccountID         AmountBase   (cash/bank outflow)
//
//   Void (draft only):
//     Sets status=voided; no JE.
//
// State machine:
//   draft → posted → applied (when fully applied to bills)
//          ↘ voided (from draft only)

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrVendorPrepaymentNotFound      = errors.New("vendor prepayment not found")
	ErrVendorPrepaymentInvalidStatus = errors.New("action not allowed in current vendor prepayment status")
	ErrVendorPrepaymentNoBank        = errors.New("bank account is required before posting")
	ErrVendorPrepaymentNoAcct        = errors.New("prepayment asset account is required before posting")
)

// ── Input types ───────────────────────────────────────────────────────────────

// VendorPrepaymentInput holds all data needed to create or update a VendorPrepayment.
type VendorPrepaymentInput struct {
	VendorID            uint
	PurchaseOrderID     *uint
	PrepaymentDate      time.Time
	CurrencyCode        string
	ExchangeRate        decimal.Decimal
	Amount              decimal.Decimal
	BankAccountID       *uint
	PrepaymentAccountID *uint
	PaymentMethod       models.PaymentMethod
	Reference           string
	Memo                string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextVendorPrepaymentNumber(db *gorm.DB, companyID uint) string {
	var last models.VendorPrepayment
	db.Where("company_id = ?", companyID).Order("id desc").Select("prepayment_number").First(&last)
	return NextDocumentNumber(last.PrepaymentNumber, "PP-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateVendorPrepayment creates a new draft vendor prepayment.
func CreateVendorPrepayment(db *gorm.DB, companyID uint, in VendorPrepaymentInput) (*models.VendorPrepayment, error) {
	if in.VendorID == 0 {
		return nil, errors.New("vendor is required")
	}
	if !in.Amount.IsPositive() {
		return nil, errors.New("prepayment amount must be positive")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	pp := models.VendorPrepayment{
		CompanyID:           companyID,
		VendorID:            in.VendorID,
		PurchaseOrderID:     in.PurchaseOrderID,
		PrepaymentNumber:    nextVendorPrepaymentNumber(db, companyID),
		Status:              models.VendorPrepaymentStatusDraft,
		PrepaymentDate:      in.PrepaymentDate,
		CurrencyCode:        in.CurrencyCode,
		ExchangeRate:        rate,
		Amount:              in.Amount.Round(2),
		RemainingAmount:     in.Amount.Round(2),
		BankAccountID:       in.BankAccountID,
		PrepaymentAccountID: in.PrepaymentAccountID,
		PaymentMethod:       in.PaymentMethod,
		Reference:           in.Reference,
		Memo:                in.Memo,
	}

	if err := db.Create(&pp).Error; err != nil {
		return nil, fmt.Errorf("create vendor prepayment: %w", err)
	}
	return &pp, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetVendorPrepayment loads a prepayment with vendor and accounts for the given company.
func GetVendorPrepayment(db *gorm.DB, companyID, ppID uint) (*models.VendorPrepayment, error) {
	var pp models.VendorPrepayment
	err := db.Preload("Vendor").Preload("BankAccount").Preload("PrepaymentAccount").
		Where("id = ? AND company_id = ?", ppID, companyID).First(&pp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorPrepaymentNotFound
	}
	return &pp, err
}

// VendorPrepaymentListFilter bundles the optional list-page filters.
type VendorPrepaymentListFilter struct {
	Status   string     // empty = all statuses
	VendorID uint       // 0 = all vendors
	DateFrom *time.Time // nil = no lower bound on prepayment_date
	DateTo   *time.Time // nil = no upper bound on prepayment_date
}

// ListVendorPrepayments returns prepayments for a company, newest first.
func ListVendorPrepayments(db *gorm.DB, companyID uint, f VendorPrepaymentListFilter) ([]models.VendorPrepayment, error) {
	q := db.Preload("Vendor").Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.VendorID > 0 {
		q = q.Where("vendor_id = ?", f.VendorID)
	}
	if f.DateFrom != nil {
		q = q.Where("prepayment_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("prepayment_date <= ?", *f.DateTo)
	}
	var pps []models.VendorPrepayment
	err := q.Order("id desc").Find(&pps).Error
	return pps, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateVendorPrepayment updates a draft prepayment.
func UpdateVendorPrepayment(db *gorm.DB, companyID, ppID uint, in VendorPrepaymentInput) (*models.VendorPrepayment, error) {
	var pp models.VendorPrepayment
	if err := db.Where("id = ? AND company_id = ?", ppID, companyID).First(&pp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVendorPrepaymentNotFound
		}
		return nil, err
	}
	if pp.Status != models.VendorPrepaymentStatusDraft {
		return nil, fmt.Errorf("%w: only draft prepayments may be edited", ErrVendorPrepaymentInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	updates := map[string]any{
		"vendor_id":             in.VendorID,
		"purchase_order_id":     in.PurchaseOrderID,
		"prepayment_date":       in.PrepaymentDate,
		"currency_code":         in.CurrencyCode,
		"exchange_rate":         rate,
		"amount":                in.Amount.Round(2),
		"remaining_amount":      in.Amount.Round(2),
		"bank_account_id":       in.BankAccountID,
		"prepayment_account_id": in.PrepaymentAccountID,
		"payment_method":        in.PaymentMethod,
		"reference":             in.Reference,
		"memo":                  in.Memo,
	}
	if err := db.Model(&pp).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update vendor prepayment: %w", err)
	}
	return &pp, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostVendorPrepayment transitions a draft prepayment to posted and generates a JE.
//
// Journal entry:
//
//	Dr  PrepaymentAccountID   AmountBase
//	Cr  BankAccountID         AmountBase
func PostVendorPrepayment(db *gorm.DB, companyID, ppID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var pp models.VendorPrepayment
		if err := tx.Where("id = ? AND company_id = ?", ppID, companyID).First(&pp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorPrepaymentNotFound
			}
			return err
		}
		if pp.Status != models.VendorPrepaymentStatusDraft {
			return fmt.Errorf("%w: only draft prepayments can be posted", ErrVendorPrepaymentInvalidStatus)
		}
		if pp.BankAccountID == nil || *pp.BankAccountID == 0 {
			return ErrVendorPrepaymentNoBank
		}
		if pp.PrepaymentAccountID == nil || *pp.PrepaymentAccountID == 0 {
			return ErrVendorPrepaymentNoAcct
		}

		rate := pp.ExchangeRate
		if rate.IsZero() || rate.IsNegative() {
			rate = decimal.NewFromInt(1)
		}
		amountBase := pp.Amount.Mul(rate).Round(2)
		cash, err := resolveCashPostingCurrency(tx, companyID, *pp.BankAccountID, pp.CurrencyCode, rate)
		if err != nil {
			return err
		}
		txAmount := amountBase
		if cash.BankIsForeign {
			txAmount = pp.Amount.Round(2)
		}

		je := models.JournalEntry{
			CompanyID:               companyID,
			EntryDate:               pp.PrepaymentDate,
			JournalNo:               "PP – " + pp.PrepaymentNumber,
			Status:                  models.JournalEntryStatusPosted,
			TransactionCurrencyCode: cash.TransactionCurrencyCode,
			ExchangeRate:            cash.ExchangeRate,
			ExchangeRateDate:        pp.PrepaymentDate,
			ExchangeRateSource:      cashExchangeRateSource(cash),
			SourceType:              models.LedgerSourceVendorPrepayment,
			SourceID:                pp.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create prepayment JE: %w", err)
		}

		lines := []models.JournalLine{
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *pp.PrepaymentAccountID,
				TxDebit:        txAmount,
				TxCredit:       decimal.Zero,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           pp.PrepaymentNumber + " – vendor prepayment asset",
			},
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *pp.BankAccountID,
				TxDebit:        decimal.Zero,
				TxCredit:       txAmount,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           pp.PrepaymentNumber + " – cash outflow",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create prepayment JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceVendorPrepayment,
			SourceID:     pp.ID,
		}); err != nil {
			return fmt.Errorf("project prepayment to ledger: %w", err)
		}

		now := time.Now()
		updates := map[string]any{
			"status":           string(models.VendorPrepaymentStatusPosted),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"posted_at":        &now,
			"posted_by":        actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		return tx.Model(&pp).Updates(updates).Error
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidVendorPrepayment cancels a draft prepayment. No JE is generated.
func VoidVendorPrepayment(db *gorm.DB, companyID, ppID uint) error {
	var pp models.VendorPrepayment
	if err := db.Where("id = ? AND company_id = ?", ppID, companyID).First(&pp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVendorPrepaymentNotFound
		}
		return err
	}
	if pp.Status != models.VendorPrepaymentStatusDraft {
		return fmt.Errorf("%w: only draft prepayments can be voided", ErrVendorPrepaymentInvalidStatus)
	}
	return db.Model(&pp).Update("status", models.VendorPrepaymentStatusVoided).Error
}
