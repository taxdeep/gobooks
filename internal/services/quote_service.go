// 遵循project_guide.md
package services

// quote_service.go — Quote CRUD, status transitions, Quote→SalesOrder conversion.
//
// Accounting rule: Quotes NEVER generate a JE. They are commercial offers only.
// The Posting Engine is not involved at any point in the Quote lifecycle.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/numbering"
)

// ── Errors ─────────────────────────────────────────────────────────────────

var (
	ErrQuoteNotFound      = errors.New("quote not found")
	ErrQuoteWrongCompany  = errors.New("quote belongs to a different company")
	ErrQuoteInvalidStatus = errors.New("action not allowed in current quote status")
)

// ── Input types ──────────────────────────────────────────────────────────────

// QuoteLineInput holds the user-supplied data for one quote line.
type QuoteLineInput struct {
	ProductServiceID *uint
	RevenueAccountID *uint
	TaxCodeID        *uint
	Description      string
	Quantity         decimal.Decimal
	UnitPrice        decimal.Decimal
}

// QuoteInput holds all data needed to create or update a Quote.
type QuoteInput struct {
	CustomerID   uint
	CurrencyCode string
	QuoteDate    time.Time
	ExpiryDate   *time.Time
	Notes        string
	Memo         string
	Lines        []QuoteLineInput
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nextQuoteNumber derives the next quote number for a company.
// Returns (number, usedSettings). Settings-driven for first-ever quote in
// a company; scan-last-and-increment after that (data owns the sequence).
func nextQuoteNumber(db *gorm.DB, companyID uint) (string, bool) {
	var last models.Quote
	db.Where("company_id = ?", companyID).
		Order("id desc").
		Select("quote_number").
		First(&last)

	fallback := "QUO-0001"
	usedSettings := false
	if suggestion, err := SuggestNextNumberForModule(db, companyID, numbering.ModuleQuote); err == nil && suggestion != "" {
		fallback = suggestion
		if last.QuoteNumber == "" {
			usedSettings = true
		}
	}
	if last.QuoteNumber == "" {
		return fallback, usedSettings
	}
	return NextDocumentNumber(last.QuoteNumber, fallback), false
}

// calcQuoteLine computes derived fields for a QuoteLine.
func calcQuoteLine(l *models.QuoteLine, taxRate decimal.Decimal) {
	l.LineNet = l.Quantity.Mul(l.UnitPrice).Round(4)
	l.TaxAmount = l.LineNet.Mul(taxRate).Round(4)
	l.LineTotal = l.LineNet.Add(l.TaxAmount).Round(4)
}

// loadTaxRate fetches a TaxCode's Rate, returning zero if nil or not found.
func loadTaxRate(db *gorm.DB, taxCodeID *uint) decimal.Decimal {
	if taxCodeID == nil {
		return decimal.Zero
	}
	var tc models.TaxCode
	if err := db.Select("rate").First(&tc, *taxCodeID).Error; err != nil {
		return decimal.Zero
	}
	return tc.Rate
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateQuote creates a new draft quote with its line items.
// No JE is generated.
func CreateQuote(db *gorm.DB, companyID uint, in QuoteInput) (*models.Quote, error) {
	if in.CustomerID == 0 {
		return nil, errors.New("customer is required")
	}
	if len(in.Lines) == 0 {
		return nil, errors.New("at least one line item is required")
	}

	quoteNumber, settingsCounterUsed := nextQuoteNumber(db, companyID)
	quote := models.Quote{
		CompanyID:    companyID,
		CustomerID:   in.CustomerID,
		QuoteNumber:  quoteNumber,
		Status:       models.QuoteStatusDraft,
		QuoteDate:    in.QuoteDate,
		ExpiryDate:   in.ExpiryDate,
		CurrencyCode: in.CurrencyCode,
		Notes:        in.Notes,
		Memo:         in.Memo,
	}

	var lines []models.QuoteLine
	var subtotal, taxTotal decimal.Decimal
	for i, li := range in.Lines {
		if err := validateStockItemQty(db, companyID, li.ProductServiceID, li.Quantity, i+1); err != nil {
			return nil, err
		}
		rate := loadTaxRate(db, li.TaxCodeID)
		line := models.QuoteLine{
			ProductServiceID: li.ProductServiceID,
			RevenueAccountID: li.RevenueAccountID,
			TaxCodeID:        li.TaxCodeID,
			Description:      li.Description,
			Quantity:         li.Quantity,
			UnitPrice:        li.UnitPrice,
			SortOrder:        i,
		}
		calcQuoteLine(&line, rate)
		subtotal = subtotal.Add(line.LineNet)
		taxTotal = taxTotal.Add(line.TaxAmount)
		lines = append(lines, line)
	}
	quote.Subtotal = subtotal.Round(4)
	quote.TaxTotal = taxTotal.Round(4)
	quote.Total = subtotal.Add(taxTotal).Round(4)

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&quote).Error; err != nil {
			return fmt.Errorf("create quote: %w", err)
		}
		for i := range lines {
			lines[i].QuoteID = quote.ID
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create quote lines: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	quote.Lines = lines
	if settingsCounterUsed {
		_ = BumpModuleNextNumberAfterCreate(db, companyID, numbering.ModuleQuote)
	}
	return &quote, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetQuote loads a quote with its lines for the given company.
func GetQuote(db *gorm.DB, companyID, quoteID uint) (*models.Quote, error) {
	var q models.Quote
	err := db.Preload("Lines").Preload("Customer").
		Where("id = ? AND company_id = ?", quoteID, companyID).
		First(&q).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrQuoteNotFound
	}
	return &q, err
}

// QuoteListFilter bundles the optional list-page filters. Mirrors
// SalesOrderListFilter so the AR list pages stay structurally aligned.
type QuoteListFilter struct {
	Status     string     // empty = all statuses
	CustomerID uint       // 0 = all customers
	DateFrom   *time.Time // nil = no lower bound on quote_date
	DateTo     *time.Time // nil = no upper bound on quote_date
}

// ListQuotes returns quotes for a company, newest first. All filters
// are optional — see QuoteListFilter for the contract.
func ListQuotes(db *gorm.DB, companyID uint, f QuoteListFilter) ([]models.Quote, error) {
	q := db.Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.CustomerID > 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.DateFrom != nil {
		q = q.Where("quote_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("quote_date <= ?", *f.DateTo)
	}
	var quotes []models.Quote
	err := q.Preload("Customer").Order("id desc").Find(&quotes).Error
	return quotes, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateQuote replaces all editable fields and lines on a draft quote.
// Only draft quotes may be updated.
func UpdateQuote(db *gorm.DB, companyID, quoteID uint, in QuoteInput) (*models.Quote, error) {
	var q models.Quote
	if err := db.Where("id = ? AND company_id = ?", quoteID, companyID).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuoteNotFound
		}
		return nil, err
	}
	if q.Status != models.QuoteStatusDraft {
		return nil, fmt.Errorf("%w: only draft quotes may be edited", ErrQuoteInvalidStatus)
	}
	if len(in.Lines) == 0 {
		return nil, errors.New("at least one line item is required")
	}

	var subtotal, taxTotal decimal.Decimal
	var newLines []models.QuoteLine
	for i, li := range in.Lines {
		if err := validateStockItemQty(db, companyID, li.ProductServiceID, li.Quantity, i+1); err != nil {
			return nil, err
		}
		rate := loadTaxRate(db, li.TaxCodeID)
		line := models.QuoteLine{
			QuoteID:          quoteID,
			ProductServiceID: li.ProductServiceID,
			RevenueAccountID: li.RevenueAccountID,
			TaxCodeID:        li.TaxCodeID,
			Description:      li.Description,
			Quantity:         li.Quantity,
			UnitPrice:        li.UnitPrice,
			SortOrder:        i,
		}
		calcQuoteLine(&line, rate)
		subtotal = subtotal.Add(line.LineNet)
		taxTotal = taxTotal.Add(line.TaxAmount)
		newLines = append(newLines, line)
	}

	return &q, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("quote_id = ?", quoteID).Delete(&models.QuoteLine{}).Error; err != nil {
			return fmt.Errorf("delete old lines: %w", err)
		}
		updates := map[string]any{
			"customer_id":   in.CustomerID,
			"currency_code": in.CurrencyCode,
			"quote_date":    in.QuoteDate,
			"expiry_date":   in.ExpiryDate,
			"notes":         in.Notes,
			"memo":          in.Memo,
			"subtotal":      subtotal.Round(4),
			"tax_total":     taxTotal.Round(4),
			"total":         subtotal.Add(taxTotal).Round(4),
		}
		if err := tx.Model(&q).Updates(updates).Error; err != nil {
			return fmt.Errorf("update quote: %w", err)
		}
		if err := tx.Create(&newLines).Error; err != nil {
			return fmt.Errorf("create new lines: %w", err)
		}
		q.Lines = newLines
		return nil
	})
}

// ── Status transitions ────────────────────────────────────────────────────────

// SendQuote marks a draft quote as sent.
func SendQuote(db *gorm.DB, companyID, quoteID uint, actor string) error {
	return quoteTransition(db, companyID, quoteID, models.QuoteStatusDraft, models.QuoteStatusSent, func(updates map[string]any) {
		now := time.Now()
		updates["sent_at"] = &now
		_ = actor
	})
}

// AcceptQuote marks a sent quote as accepted.
func AcceptQuote(db *gorm.DB, companyID, quoteID uint) error {
	return quoteTransition(db, companyID, quoteID, models.QuoteStatusSent, models.QuoteStatusAccepted, nil)
}

// RejectQuote marks a sent quote as rejected.
func RejectQuote(db *gorm.DB, companyID, quoteID uint) error {
	return quoteTransition(db, companyID, quoteID, models.QuoteStatusSent, models.QuoteStatusRejected, nil)
}

// CancelQuote cancels a draft or sent quote.
func CancelQuote(db *gorm.DB, companyID, quoteID uint) error {
	var q models.Quote
	if err := db.Where("id = ? AND company_id = ?", quoteID, companyID).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrQuoteNotFound
		}
		return err
	}
	if q.Status != models.QuoteStatusDraft && q.Status != models.QuoteStatusSent {
		return fmt.Errorf("%w: only draft or sent quotes may be cancelled", ErrQuoteInvalidStatus)
	}
	return db.Model(&q).Update("status", models.QuoteStatusCancelled).Error
}

// quoteTransition is a generic status-change helper.
func quoteTransition(db *gorm.DB, companyID, quoteID uint, from, to models.QuoteStatus, extra func(map[string]any)) error {
	var q models.Quote
	if err := db.Where("id = ? AND company_id = ?", quoteID, companyID).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrQuoteNotFound
		}
		return err
	}
	if q.Status != from {
		return fmt.Errorf("%w: expected %s, got %s", ErrQuoteInvalidStatus, from, q.Status)
	}
	updates := map[string]any{"status": string(to)}
	if extra != nil {
		extra(updates)
	}
	return db.Model(&q).Updates(updates).Error
}

// ── Quote → SalesOrder conversion ────────────────────────────────────────────

// ConvertQuoteToSalesOrder converts an accepted (or sent) Quote into a new
// draft SalesOrder, atomically. The Quote status is set to "converted".
//
// No JE is generated. This is a pure business-layer operation.
func ConvertQuoteToSalesOrder(db *gorm.DB, companyID, quoteID uint, actor string, actorID *uuid.UUID) (*models.SalesOrder, error) {
	_ = actor
	_ = actorID

	var q models.Quote
	if err := db.Preload("Lines").
		Where("id = ? AND company_id = ?", quoteID, companyID).
		First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuoteNotFound
		}
		return nil, err
	}
	if q.Status != models.QuoteStatusAccepted && q.Status != models.QuoteStatusSent {
		return nil, fmt.Errorf("%w: only accepted or sent quotes may be converted", ErrQuoteInvalidStatus)
	}

	// Derive next SalesOrder number (same settings-aware helper as
	// direct SO creation — quote→SO conversion is just another SO
	// creation path and should share numbering semantics).
	orderNumber, soSettingsCounterUsed := nextSalesOrderNumber(db, companyID)

	so := models.SalesOrder{
		CompanyID:    companyID,
		CustomerID:   q.CustomerID,
		QuoteID:      &q.ID,
		OrderNumber:  orderNumber,
		Status:       models.SalesOrderStatusDraft,
		OrderDate:    time.Now(),
		CurrencyCode: q.CurrencyCode,
		Subtotal:     q.Subtotal,
		TaxTotal:     q.TaxTotal,
		Total:        q.Total,
		Notes:        q.Notes,
		Memo:         q.Memo,
	}

	var soLines []models.SalesOrderLine
	for i, ql := range q.Lines {
		soLines = append(soLines, models.SalesOrderLine{
			ProductServiceID: ql.ProductServiceID,
			RevenueAccountID: ql.RevenueAccountID,
			TaxCodeID:        ql.TaxCodeID,
			Description:      ql.Description,
			Quantity:         ql.Quantity,
			UnitPrice:        ql.UnitPrice,
			LineNet:          ql.LineNet,
			TaxAmount:        ql.TaxAmount,
			LineTotal:        ql.LineTotal,
			SortOrder:        i,
		})
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&so).Error; err != nil {
			return fmt.Errorf("create sales order: %w", err)
		}
		for i := range soLines {
			soLines[i].SalesOrderID = so.ID
		}
		if len(soLines) > 0 {
			if err := tx.Create(&soLines).Error; err != nil {
				return fmt.Errorf("create sales order lines: %w", err)
			}
		}
		// Mark quote as converted.
		if err := tx.Model(&q).Updates(map[string]any{
			"status":          string(models.QuoteStatusConverted),
			"sales_order_id":  so.ID,
		}).Error; err != nil {
			return fmt.Errorf("update quote status: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	so.Lines = soLines
	if soSettingsCounterUsed {
		_ = BumpModuleNextNumberAfterCreate(db, companyID, numbering.ModuleSalesOrder)
	}
	return &so, nil
}
