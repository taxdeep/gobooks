// 遵循project_guide.md
package services

// sales_order_service.go — SalesOrder CRUD, status transitions.
//
// Accounting rule: SalesOrders NEVER generate a JE. They are commercial
// commitments only. The Posting Engine is not involved at any point.

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/numbering"

	"github.com/shopspring/decimal"
)

// ── Errors ─────────────────────────────────────────────────────────────────

var (
	ErrSalesOrderNotFound      = errors.New("sales order not found")
	ErrSalesOrderInvalidStatus = errors.New("action not allowed in current sales order status")
)

// ── Input types ──────────────────────────────────────────────────────────────

// SalesOrderLineInput holds user-supplied data for one sales order line.
type SalesOrderLineInput struct {
	ProductServiceID *uint
	RevenueAccountID *uint
	TaxCodeID        *uint
	Description      string
	Quantity         decimal.Decimal
	UnitPrice        decimal.Decimal
}

// SalesOrderInput holds all data needed to create or update a SalesOrder.
type SalesOrderInput struct {
	CustomerID   uint
	CurrencyCode string
	OrderDate    time.Time
	RequiredBy   *time.Time
	Notes        string
	Memo         string
	Lines        []SalesOrderLineInput
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nextSalesOrderNumber derives the next SO number for a company.
// Returns (number, usedSettings) so the caller can decide whether to bump
// the numbering_settings counter. Semantics mirror nextPONumber: settings
// drive the fallback number for a brand-new company; existing sequences
// continue incrementing from data.
func nextSalesOrderNumber(db *gorm.DB, companyID uint) (string, bool) {
	var last models.SalesOrder
	db.Where("company_id = ?", companyID).
		Order("id desc").
		Select("order_number").
		First(&last)

	fallback := "SO-0001"
	usedSettings := false
	if suggestion, err := SuggestNextNumberForModule(db, companyID, numbering.ModuleSalesOrder); err == nil && suggestion != "" {
		fallback = suggestion
		if last.OrderNumber == "" {
			usedSettings = true
		}
	}
	if last.OrderNumber == "" {
		return fallback, usedSettings
	}
	return NextDocumentNumber(last.OrderNumber, fallback), false
}

// calcSalesOrderLine computes derived fields for a SalesOrderLine.
func calcSalesOrderLine(l *models.SalesOrderLine, taxRate decimal.Decimal) {
	l.LineNet = l.Quantity.Mul(l.UnitPrice).Round(4)
	l.TaxAmount = l.LineNet.Mul(taxRate).Round(4)
	l.LineTotal = l.LineNet.Add(l.TaxAmount).Round(4)
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateSalesOrder creates a new draft sales order with its line items.
// No JE is generated.
func CreateSalesOrder(db *gorm.DB, companyID uint, in SalesOrderInput) (*models.SalesOrder, error) {
	if in.CustomerID == 0 {
		return nil, errors.New("customer is required")
	}
	if len(in.Lines) == 0 {
		return nil, errors.New("at least one line item is required")
	}

	soNumber, settingsCounterUsed := nextSalesOrderNumber(db, companyID)
	so := models.SalesOrder{
		CompanyID:    companyID,
		CustomerID:   in.CustomerID,
		OrderNumber:  soNumber,
		Status:       models.SalesOrderStatusDraft,
		OrderDate:    in.OrderDate,
		RequiredBy:   in.RequiredBy,
		CurrencyCode: in.CurrencyCode,
		Notes:        in.Notes,
		Memo:         in.Memo,
	}

	var lines []models.SalesOrderLine
	var subtotal, taxTotal decimal.Decimal
	for i, li := range in.Lines {
		rate := loadTaxRate(db, li.TaxCodeID)
		line := models.SalesOrderLine{
			ProductServiceID: li.ProductServiceID,
			RevenueAccountID: li.RevenueAccountID,
			TaxCodeID:        li.TaxCodeID,
			Description:      li.Description,
			Quantity:         li.Quantity,
			UnitPrice:        li.UnitPrice,
			SortOrder:        i,
		}
		calcSalesOrderLine(&line, rate)
		subtotal = subtotal.Add(line.LineNet)
		taxTotal = taxTotal.Add(line.TaxAmount)
		lines = append(lines, line)
	}
	so.Subtotal = subtotal.Round(4)
	so.TaxTotal = taxTotal.Round(4)
	so.Total = subtotal.Add(taxTotal).Round(4)

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&so).Error; err != nil {
			return fmt.Errorf("create sales order: %w", err)
		}
		for i := range lines {
			lines[i].SalesOrderID = so.ID
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create sales order lines: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	so.Lines = lines
	if settingsCounterUsed {
		_ = BumpModuleNextNumberAfterCreate(db, companyID, numbering.ModuleSalesOrder)
	}
	return &so, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetSalesOrder loads a sales order with its lines for the given company.
func GetSalesOrder(db *gorm.DB, companyID, orderID uint) (*models.SalesOrder, error) {
	var so models.SalesOrder
	err := db.Preload("Lines").Preload("Customer").
		Where("id = ? AND company_id = ?", orderID, companyID).
		First(&so).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSalesOrderNotFound
	}
	return &so, err
}

// ListSalesOrders returns sales orders for a company, newest first.
// statusFilter: empty = all statuses.
// customerID: 0 = all customers.
func ListSalesOrders(db *gorm.DB, companyID uint, statusFilter string, customerID uint) ([]models.SalesOrder, error) {
	q := db.Where("company_id = ?", companyID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	if customerID > 0 {
		q = q.Where("customer_id = ?", customerID)
	}
	var orders []models.SalesOrder
	err := q.Preload("Customer").Order("id desc").Find(&orders).Error
	return orders, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateSalesOrder replaces all editable fields and lines on a draft sales order.
// Only draft orders may be updated.
func UpdateSalesOrder(db *gorm.DB, companyID, orderID uint, in SalesOrderInput) (*models.SalesOrder, error) {
	var so models.SalesOrder
	if err := db.Where("id = ? AND company_id = ?", orderID, companyID).First(&so).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSalesOrderNotFound
		}
		return nil, err
	}
	if so.Status != models.SalesOrderStatusDraft {
		return nil, fmt.Errorf("%w: only draft sales orders may be edited", ErrSalesOrderInvalidStatus)
	}
	if len(in.Lines) == 0 {
		return nil, errors.New("at least one line item is required")
	}

	var subtotal, taxTotal decimal.Decimal
	var newLines []models.SalesOrderLine
	for i, li := range in.Lines {
		rate := loadTaxRate(db, li.TaxCodeID)
		line := models.SalesOrderLine{
			SalesOrderID:     orderID,
			ProductServiceID: li.ProductServiceID,
			RevenueAccountID: li.RevenueAccountID,
			TaxCodeID:        li.TaxCodeID,
			Description:      li.Description,
			Quantity:         li.Quantity,
			UnitPrice:        li.UnitPrice,
			SortOrder:        i,
		}
		calcSalesOrderLine(&line, rate)
		subtotal = subtotal.Add(line.LineNet)
		taxTotal = taxTotal.Add(line.TaxAmount)
		newLines = append(newLines, line)
	}

	return &so, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("sales_order_id = ?", orderID).Delete(&models.SalesOrderLine{}).Error; err != nil {
			return fmt.Errorf("delete old lines: %w", err)
		}
		updates := map[string]any{
			"customer_id":   in.CustomerID,
			"currency_code": in.CurrencyCode,
			"order_date":    in.OrderDate,
			"required_by":   in.RequiredBy,
			"notes":         in.Notes,
			"memo":          in.Memo,
			"subtotal":      subtotal.Round(4),
			"tax_total":     taxTotal.Round(4),
			"total":         subtotal.Add(taxTotal).Round(4),
		}
		if err := tx.Model(&so).Updates(updates).Error; err != nil {
			return fmt.Errorf("update sales order: %w", err)
		}
		if err := tx.Create(&newLines).Error; err != nil {
			return fmt.Errorf("create new lines: %w", err)
		}
		so.Lines = newLines
		return nil
	})
}

// ── Status transitions ────────────────────────────────────────────────────────

// ConfirmSalesOrder moves a draft order to confirmed.
func ConfirmSalesOrder(db *gorm.DB, companyID, orderID uint) error {
	return soTransition(db, companyID, orderID, models.SalesOrderStatusDraft, models.SalesOrderStatusConfirmed, func(updates map[string]any) {
		now := time.Now()
		updates["confirmed_at"] = &now
	})
}

// CancelSalesOrder cancels a draft or confirmed order.
func CancelSalesOrder(db *gorm.DB, companyID, orderID uint) error {
	var so models.SalesOrder
	if err := db.Where("id = ? AND company_id = ?", orderID, companyID).First(&so).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSalesOrderNotFound
		}
		return err
	}
	if so.Status != models.SalesOrderStatusDraft && so.Status != models.SalesOrderStatusConfirmed {
		return fmt.Errorf("%w: only draft or confirmed orders may be cancelled", ErrSalesOrderInvalidStatus)
	}
	return db.Model(&so).Update("status", models.SalesOrderStatusCancelled).Error
}

// soTransition is a generic status-change helper for SalesOrders.
func soTransition(db *gorm.DB, companyID, orderID uint, from, to models.SalesOrderStatus, extra func(map[string]any)) error {
	var so models.SalesOrder
	if err := db.Where("id = ? AND company_id = ?", orderID, companyID).First(&so).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSalesOrderNotFound
		}
		return err
	}
	if so.Status != from {
		return fmt.Errorf("%w: expected %s, got %s", ErrSalesOrderInvalidStatus, from, so.Status)
	}
	updates := map[string]any{"status": string(to)}
	if extra != nil {
		extra(updates)
	}
	return db.Model(&so).Updates(updates).Error
}
