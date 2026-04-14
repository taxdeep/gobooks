// 遵循project_guide.md
package services

// purchase_order_service.go — PurchaseOrder: AP commercial commitment.
//
// A PurchaseOrder is a pre-accounting document. It records the company's
// intent to purchase goods or services from a vendor.
//
// State machine:
//   draft → confirmed → partially_received → received → closed
//          ↘ cancelled (from draft or confirmed)
//
// No journal entry is created at any stage.
// Accounting truth begins when the vendor's Bill is posted.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrPONotFound      = errors.New("purchase order not found")
	ErrPOInvalidStatus = errors.New("action not allowed in current purchase order status")
)

// ── Input types ───────────────────────────────────────────────────────────────

// POLineInput holds one line of a purchase order.
type POLineInput struct {
	SortOrder        uint
	ProductServiceID *uint
	Description      string
	Qty              decimal.Decimal
	UnitPrice        decimal.Decimal
	TaxCodeID        *uint
	ExpenseAccountID *uint
}

// POInput holds all data needed to create or update a PurchaseOrder.
type POInput struct {
	VendorID     uint
	PODate       time.Time
	ExpectedDate *time.Time
	CurrencyCode string
	ExchangeRate decimal.Decimal
	Notes        string
	Memo         string
	Lines        []POLineInput
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextPONumber(db *gorm.DB, companyID uint) string {
	var last models.PurchaseOrder
	db.Where("company_id = ?", companyID).Order("id desc").Select("po_number").First(&last)
	return NextDocumentNumber(last.PONumber, "PO-0001")
}

// computePOLine computes cached totals for a PurchaseOrderLine.
// Tax computation is simplified (rate × net); for full tax logic use TaxCode service.
func computePOLine(db *gorm.DB, companyID uint, in POLineInput) (models.PurchaseOrderLine, error) {
	line := models.PurchaseOrderLine{
		CompanyID:        companyID,
		SortOrder:        in.SortOrder,
		ProductServiceID: in.ProductServiceID,
		Description:      in.Description,
		Qty:              in.Qty,
		UnitPrice:        in.UnitPrice,
		TaxCodeID:        in.TaxCodeID,
		ExpenseAccountID: in.ExpenseAccountID,
	}

	lineNet := in.Qty.Mul(in.UnitPrice).Round(2)
	line.LineNet = lineNet

	// Apply tax if code provided
	var lineTax decimal.Decimal
	if in.TaxCodeID != nil {
		var tc models.TaxCode
		if err := db.First(&tc, *in.TaxCodeID).Error; err == nil {
			lineTax = lineNet.Mul(tc.Rate).Div(decimal.NewFromInt(100)).Round(2)
		}
	}
	line.LineTax = lineTax
	line.LineTotal = lineNet.Add(lineTax)
	return line, nil
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreatePurchaseOrder creates a new draft purchase order with lines.
func CreatePurchaseOrder(db *gorm.DB, companyID uint, in POInput) (*models.PurchaseOrder, error) {
	if in.VendorID == 0 {
		return nil, errors.New("vendor is required")
	}
	if len(in.Lines) == 0 {
		return nil, errors.New("at least one line is required")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	po := models.PurchaseOrder{
		CompanyID:    companyID,
		VendorID:     in.VendorID,
		PONumber:     nextPONumber(db, companyID),
		Status:       models.POStatusDraft,
		PODate:       in.PODate,
		ExpectedDate: in.ExpectedDate,
		CurrencyCode: in.CurrencyCode,
		ExchangeRate: rate,
		Notes:        in.Notes,
		Memo:         in.Memo,
	}

	var subtotal, taxTotal decimal.Decimal
	for i, lin := range in.Lines {
		if lin.SortOrder == 0 {
			lin.SortOrder = uint(i + 1)
		}
		computed, err := computePOLine(db, companyID, lin)
		if err != nil {
			return nil, err
		}
		computed.PurchaseOrderID = po.ID // set after create below
		subtotal = subtotal.Add(computed.LineNet)
		taxTotal = taxTotal.Add(computed.LineTax)
		po.Lines = append(po.Lines, computed)
	}
	po.Subtotal = subtotal
	po.TaxTotal = taxTotal
	po.Amount = subtotal.Add(taxTotal)

	if err := db.Create(&po).Error; err != nil {
		return nil, fmt.Errorf("create purchase order: %w", err)
	}
	return &po, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetPurchaseOrder loads a PO with vendor and lines for the given company.
func GetPurchaseOrder(db *gorm.DB, companyID, poID uint) (*models.PurchaseOrder, error) {
	var po models.PurchaseOrder
	err := db.Preload("Vendor").Preload("Lines").Preload("Lines.ProductService").
		Preload("Lines.TaxCode").Preload("Lines.ExpenseAccount").
		Where("id = ? AND company_id = ?", poID, companyID).First(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPONotFound
	}
	return &po, err
}

// ListPurchaseOrders returns POs for a company, newest first.
func ListPurchaseOrders(db *gorm.DB, companyID uint, statusFilter string, vendorID uint) ([]models.PurchaseOrder, error) {
	q := db.Preload("Vendor").Where("company_id = ?", companyID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	if vendorID > 0 {
		q = q.Where("vendor_id = ?", vendorID)
	}
	var pos []models.PurchaseOrder
	err := q.Order("id desc").Find(&pos).Error
	return pos, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdatePurchaseOrder replaces lines and header fields on a draft PO.
func UpdatePurchaseOrder(db *gorm.DB, companyID, poID uint, in POInput) (*models.PurchaseOrder, error) {
	var po models.PurchaseOrder
	if err := db.Where("id = ? AND company_id = ?", poID, companyID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPONotFound
		}
		return nil, err
	}
	if po.Status != models.POStatusDraft {
		return nil, fmt.Errorf("%w: only draft purchase orders may be edited", ErrPOInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	return &po, db.Transaction(func(tx *gorm.DB) error {
		// Delete existing lines
		if err := tx.Where("purchase_order_id = ?", poID).Delete(&models.PurchaseOrderLine{}).Error; err != nil {
			return fmt.Errorf("delete po lines: %w", err)
		}

		var subtotal, taxTotal decimal.Decimal
		var newLines []models.PurchaseOrderLine
		for i, lin := range in.Lines {
			if lin.SortOrder == 0 {
				lin.SortOrder = uint(i + 1)
			}
			computed, err := computePOLine(tx, companyID, lin)
			if err != nil {
				return err
			}
			computed.PurchaseOrderID = poID
			subtotal = subtotal.Add(computed.LineNet)
			taxTotal = taxTotal.Add(computed.LineTax)
			newLines = append(newLines, computed)
		}

		if len(newLines) > 0 {
			if err := tx.Create(&newLines).Error; err != nil {
				return fmt.Errorf("create po lines: %w", err)
			}
		}

		updates := map[string]any{
			"vendor_id":     in.VendorID,
			"po_date":       in.PODate,
			"expected_date": in.ExpectedDate,
			"currency_code": in.CurrencyCode,
			"exchange_rate": rate,
			"subtotal":      subtotal,
			"tax_total":     taxTotal,
			"amount":        subtotal.Add(taxTotal),
			"notes":         in.Notes,
			"memo":          in.Memo,
		}
		return tx.Model(&po).Updates(updates).Error
	})
}

// ── Status transitions ────────────────────────────────────────────────────────

// ConfirmPurchaseOrder transitions a draft PO to confirmed.
func ConfirmPurchaseOrder(db *gorm.DB, companyID, poID uint) error {
	return poTransition(db, companyID, poID, models.POStatusDraft, models.POStatusConfirmed)
}

// CancelPurchaseOrder cancels a draft or confirmed PO.
func CancelPurchaseOrder(db *gorm.DB, companyID, poID uint) error {
	var po models.PurchaseOrder
	if err := db.Where("id = ? AND company_id = ?", poID, companyID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPONotFound
		}
		return err
	}
	if po.Status != models.POStatusDraft && po.Status != models.POStatusConfirmed {
		return fmt.Errorf("%w: only draft or confirmed purchase orders can be cancelled", ErrPOInvalidStatus)
	}
	return db.Model(&po).Update("status", models.POStatusCancelled).Error
}

// MarkPOReceived marks a confirmed PO as fully received.
func MarkPOReceived(db *gorm.DB, companyID, poID uint) error {
	return poTransition(db, companyID, poID, models.POStatusConfirmed, models.POStatusReceived)
}

// ClosePurchaseOrder closes a received PO.
func ClosePurchaseOrder(db *gorm.DB, companyID, poID uint) error {
	return poTransition(db, companyID, poID, models.POStatusReceived, models.POStatusClosed)
}

func poTransition(db *gorm.DB, companyID, poID uint, from, to models.POStatus) error {
	var po models.PurchaseOrder
	if err := db.Where("id = ? AND company_id = ?", poID, companyID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPONotFound
		}
		return err
	}
	if po.Status != from {
		return fmt.Errorf("%w: expected %s, got %s", ErrPOInvalidStatus, from, po.Status)
	}
	return db.Model(&po).Update("status", to).Error
}
