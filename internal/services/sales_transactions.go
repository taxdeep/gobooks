// 遵循project_guide.md
package services

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// SalesTxFilter captures query-string filters for the Sales Transactions
// list page. Zero-value = "no filter".
type SalesTxFilter struct {
	// Type narrows the result set to a single document kind. "" or "all" = all
	// types; other valid values match SalesTxTypeConst below.
	Type string
	// DateFrom / DateTo are inclusive. nil = unbounded.
	DateFrom *time.Time
	DateTo   *time.Time
	// CustomerID filters to transactions for one customer. 0 = all.
	CustomerID uint
	// Status narrows within a single type; ignored when Type is empty/all.
	// Value space is the type's native status string.
	Status string
	// Search does a LIKE match on document number and memo.
	Search string
	// SortBy / SortDir control the merged feed order before pagination.
	// Empty or invalid values fall back to date desc.
	SortBy  string
	SortDir string
}

// SalesTxType constants — the canonical "type" discriminator for rows
// returned by ListSalesTransactions. These double as filter values.
const (
	SalesTxTypeInvoice    = "invoice"
	SalesTxTypeQuote      = "quote"
	SalesTxTypeSalesOrder = "sales_order"
	SalesTxTypePayment    = "payment" // CustomerReceipt
	SalesTxTypeCreditNote = "credit_note"
	SalesTxTypeReturn     = "return"

	// Pseudo-types: map to a base type + pre-applied filter.
	SalesTxPseudoUnbilled     = "unbilled"      // confirmed/partial SOs
	SalesTxPseudoRecentlyPaid = "recently_paid" // confirmed customer receipts, last 7d
)

const (
	SalesTxSortDate     = "date"
	SalesTxSortType     = "type"
	SalesTxSortNumber   = "number"
	SalesTxSortCustomer = "customer"
	SalesTxSortAmount   = "amount"
	SalesTxSortStatus   = "status"

	SalesTxSortAsc  = "asc"
	SalesTxSortDesc = "desc"
)

// SalesTxRow is one row in the unified Sales Transactions list.
//
// All amounts are in document currency. Status is the native status
// string of the underlying document (invoice.status, quote.status,
// etc.) — the templ maps it to display label + badge colour.
type SalesTxRow struct {
	ID           uint
	Type         string    // one of SalesTxType* constants
	Date         time.Time // InvoiceDate / QuoteDate / OrderDate / ReceiptDate / CreditNoteDate / ReturnDate
	Number       string    // InvoiceNumber / QuoteNumber / OrderNumber / ReceiptNumber / CreditNoteNumber / ReturnNumber
	CustomerID   uint
	CustomerName string
	Memo         string
	Amount       decimal.Decimal
	Currency     string
	Status       string     // native status string for the document
	DueDate      *time.Time // only populated for Invoice (drives Overdue display)
	// DetailURL is the canonical deep-link for this row's View/Edit action.
	DetailURL string
}

// SalesTxKPI holds the five aggregates shown in the top KPI strip.
// Each aggregate is computed independently — they may overlap (an
// overdue invoice is also counted in "open invoices").
type SalesTxKPI struct {
	// QuotesOpen — draft + sent quotes. Label: "N quotes".
	QuotesOpenCount int
	QuotesOpenTotal decimal.Decimal

	// Unbilled — Σ(SO.Total − SO.InvoicedAmount) for confirmed /
	// partially-invoiced SOs. Label: "Unbilled income".
	UnbilledTotal decimal.Decimal

	// Overdue — invoices with Status=overdue OR (issued/sent/partially_paid
	// AND DueDate < today). Amount = sum of BalanceDue. Label: "N overdue invoices".
	OverdueCount int
	OverdueTotal decimal.Decimal

	// Open — invoices with BalanceDue > 0 (any non-paid, non-voided state)
	// PLUS credit notes with BalanceRemaining > 0. Label: "N open invoices + credits".
	OpenCount int
	OpenTotal decimal.Decimal

	// RecentlyPaid — confirmed customer receipts in the last 7 days.
	// Label: "N recently paid".
	RecentlyPaidCount int
	RecentlyPaidTotal decimal.Decimal
}

// ListSalesTransactions returns a page of unified sales transaction rows
// for the given company + filter combination. Rows are sorted after the
// per-type slices are merged, then paginated.
//
// Implementation: loads each document type separately (respecting the
// filter's Type selector to skip irrelevant types), merges into a single
// slice, sorts, and slices by page/size. Fine for company volumes in
// the low thousands per type; a future revision may switch to a SQL
// UNION when that ceases to scale.
//
// Returns (rows, total unfiltered row count before pagination, error).
func ListSalesTransactions(db *gorm.DB, companyID uint, f SalesTxFilter, page, size int) ([]SalesTxRow, int, error) {
	if companyID == 0 {
		return nil, 0, nil
	}
	if size <= 0 {
		size = 50
	}
	if page <= 0 {
		page = 1
	}

	// Normalise Type: blank → all. Pseudo-types translate into (base type
	// + pre-applied filter) before the loader scans.
	typ := NormalizeSalesTxType(f.Type)
	switch typ {
	case "all":
		typ = "all"
	case SalesTxPseudoUnbilled:
		// Unbilled = confirmed/partially-invoiced SalesOrders.
		typ = SalesTxTypeSalesOrder
		if f.Status == "" {
			f.Status = "unbilled" // special marker, handled in loadSalesOrders
		}
	case SalesTxPseudoRecentlyPaid:
		// Recently paid = confirmed customer receipts, last 7 days.
		typ = SalesTxTypePayment
		if f.DateFrom == nil {
			d := time.Now().AddDate(0, 0, -7)
			f.DateFrom = &d
		}
		if f.Status == "" {
			f.Status = "confirmed"
		}
	}

	var all []SalesTxRow

	if typ == "all" || typ == SalesTxTypeInvoice {
		rows, err := loadInvoicesForSalesTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "all" || typ == SalesTxTypeQuote {
		rows, err := loadQuotesForSalesTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "all" || typ == SalesTxTypeSalesOrder {
		rows, err := loadSalesOrdersForSalesTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "all" || typ == SalesTxTypePayment {
		rows, err := loadCustomerReceiptsForSalesTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "all" || typ == SalesTxTypeCreditNote {
		rows, err := loadCreditNotesForSalesTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "all" || typ == SalesTxTypeReturn {
		rows, err := loadReturnsForSalesTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}

	sortSalesTransactions(all, f.SortBy, f.SortDir)

	total := len(all)
	start := (page - 1) * size
	if start >= total {
		return []SalesTxRow{}, total, nil
	}
	end := start + size
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

// ──────────────────────────────────────────────────────────────────────
// Per-type loaders. Each applies the shared filter's date range + customer
// + search + status (when applicable) and emits a slice of SalesTxRow.
// ──────────────────────────────────────────────────────────────────────

// NormalizeSalesTxType maps UI/query aliases to the canonical service
// discriminator. It preserves unknown values so callers get an empty result
// set rather than silently widening scope.
func NormalizeSalesTxType(raw string) string {
	typ := strings.ToLower(strings.TrimSpace(raw))
	switch typ {
	case "", "all":
		return "all"
	case "invoices":
		return SalesTxTypeInvoice
	case "quotes":
		return SalesTxTypeQuote
	case "sales_orders", "sales-orders":
		return SalesTxTypeSalesOrder
	case "payments", "receipts", "customer_receipts":
		return SalesTxTypePayment
	case "credit_memos", "credit-notes", "credit_notes":
		return SalesTxTypeCreditNote
	case "returns":
		return SalesTxTypeReturn
	default:
		return typ
	}
}

func NormalizeSalesTxSort(sortBy, sortDir string) (string, string) {
	field := strings.ToLower(strings.TrimSpace(sortBy))
	switch field {
	case "", SalesTxSortDate:
		field = SalesTxSortDate
	case SalesTxSortType, SalesTxSortNumber, SalesTxSortCustomer, SalesTxSortAmount, SalesTxSortStatus:
	default:
		return SalesTxSortDate, SalesTxSortDesc
	}

	dir := strings.ToLower(strings.TrimSpace(sortDir))
	switch dir {
	case SalesTxSortAsc, SalesTxSortDesc:
	default:
		dir = salesTxDefaultSortDir(field)
	}
	return field, dir
}

func salesTxDefaultSortDir(field string) string {
	switch field {
	case SalesTxSortType, SalesTxSortNumber, SalesTxSortCustomer, SalesTxSortStatus:
		return SalesTxSortAsc
	default:
		return SalesTxSortDesc
	}
}

func sortSalesTransactions(rows []SalesTxRow, sortBy, sortDir string) {
	field, dir := NormalizeSalesTxSort(sortBy, sortDir)
	sort.SliceStable(rows, func(i, j int) bool {
		cmp := compareSalesTxRows(rows[i], rows[j], field)
		if cmp == 0 {
			return compareSalesTxRowsFallback(rows[i], rows[j]) < 0
		}
		if dir == SalesTxSortAsc {
			return cmp < 0
		}
		return cmp > 0
	})
}

func compareSalesTxRows(a, b SalesTxRow, field string) int {
	switch field {
	case SalesTxSortDate:
		return compareSalesTxTime(a.Date, b.Date)
	case SalesTxSortType:
		return strings.Compare(strings.ToLower(a.Type), strings.ToLower(b.Type))
	case SalesTxSortNumber:
		return strings.Compare(strings.ToLower(a.Number), strings.ToLower(b.Number))
	case SalesTxSortCustomer:
		return strings.Compare(strings.ToLower(a.CustomerName), strings.ToLower(b.CustomerName))
	case SalesTxSortAmount:
		return a.Amount.Cmp(b.Amount)
	case SalesTxSortStatus:
		return strings.Compare(strings.ToLower(a.Status), strings.ToLower(b.Status))
	default:
		return compareSalesTxTime(a.Date, b.Date)
	}
}

func compareSalesTxRowsFallback(a, b SalesTxRow) int {
	if cmp := compareSalesTxTime(a.Date, b.Date); cmp != 0 {
		return -cmp // fallback date desc
	}
	if a.Type != b.Type {
		return strings.Compare(a.Type, b.Type)
	}
	if a.ID < b.ID {
		return 1 // fallback ID desc
	}
	if a.ID > b.ID {
		return -1
	}
	return 0
}

func compareSalesTxTime(a, b time.Time) int {
	if a.Before(b) {
		return -1
	}
	if a.After(b) {
		return 1
	}
	return 0
}

func loadInvoicesForSalesTx(db *gorm.DB, companyID uint, f SalesTxFilter) ([]SalesTxRow, error) {
	q := db.Model(&models.Invoice{}).
		Where("company_id = ?", companyID).
		Preload("Customer")
	if f.DateFrom != nil {
		q = q.Where("invoice_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("invoice_date <= ?", *f.DateTo)
	}
	if f.CustomerID != 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("invoice_number ILIKE ? OR memo ILIKE ?", like, like)
	}
	var invs []models.Invoice
	if err := q.Order("invoice_date desc, id desc").Find(&invs).Error; err != nil {
		return nil, err
	}
	today := time.Now().Truncate(24 * time.Hour)
	out := make([]SalesTxRow, 0, len(invs))
	for _, iv := range invs {
		status := string(iv.Status)
		// Synthesize Overdue from DueDate when backend hasn't flipped status yet.
		if iv.DueDate != nil && iv.DueDate.Before(today) && iv.BalanceDue.IsPositive() &&
			status != string(models.InvoiceStatusPaid) && status != string(models.InvoiceStatusVoided) {
			status = string(models.InvoiceStatusOverdue)
		}
		out = append(out, SalesTxRow{
			ID:           iv.ID,
			Type:         SalesTxTypeInvoice,
			Date:         iv.InvoiceDate,
			Number:       iv.InvoiceNumber,
			CustomerID:   iv.CustomerID,
			CustomerName: iv.Customer.Name,
			Memo:         iv.Memo,
			Amount:       iv.Amount,
			Currency:     iv.CurrencyCode,
			Status:       status,
			DueDate:      iv.DueDate,
			DetailURL:    "/invoices/" + uitoa(iv.ID),
		})
	}
	return out, nil
}

func loadQuotesForSalesTx(db *gorm.DB, companyID uint, f SalesTxFilter) ([]SalesTxRow, error) {
	q := db.Model(&models.Quote{}).
		Where("company_id = ?", companyID).
		Preload("Customer")
	if f.DateFrom != nil {
		q = q.Where("quote_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("quote_date <= ?", *f.DateTo)
	}
	if f.CustomerID != 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("quote_number ILIKE ? OR memo ILIKE ? OR notes ILIKE ?", like, like, like)
	}
	var qs []models.Quote
	if err := q.Order("quote_date desc, id desc").Find(&qs).Error; err != nil {
		return nil, err
	}
	out := make([]SalesTxRow, 0, len(qs))
	for _, r := range qs {
		out = append(out, SalesTxRow{
			ID:           r.ID,
			Type:         SalesTxTypeQuote,
			Date:         r.QuoteDate,
			Number:       r.QuoteNumber,
			CustomerID:   r.CustomerID,
			CustomerName: r.Customer.Name,
			Memo:         r.Memo,
			Amount:       r.Total,
			Currency:     r.CurrencyCode,
			Status:       string(r.Status),
			DetailURL:    "/quotes/" + uitoa(r.ID),
		})
	}
	return out, nil
}

func loadSalesOrdersForSalesTx(db *gorm.DB, companyID uint, f SalesTxFilter) ([]SalesTxRow, error) {
	q := db.Model(&models.SalesOrder{}).
		Where("company_id = ?", companyID).
		Preload("Customer")
	if f.DateFrom != nil {
		q = q.Where("order_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("order_date <= ?", *f.DateTo)
	}
	if f.CustomerID != 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	// Special status marker from the "unbilled" pseudo-type.
	if f.Status == "unbilled" {
		q = q.Where("status IN ?", []string{
			string(models.SalesOrderStatusConfirmed),
			string(models.SalesOrderStatusPartiallyInvoiced),
		}).Where("total > invoiced_amount")
	} else if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("order_number ILIKE ? OR memo ILIKE ? OR notes ILIKE ?", like, like, like)
	}
	var sos []models.SalesOrder
	if err := q.Order("order_date desc, id desc").Find(&sos).Error; err != nil {
		return nil, err
	}
	out := make([]SalesTxRow, 0, len(sos))
	for _, so := range sos {
		out = append(out, SalesTxRow{
			ID:           so.ID,
			Type:         SalesTxTypeSalesOrder,
			Date:         so.OrderDate,
			Number:       so.OrderNumber,
			CustomerID:   so.CustomerID,
			CustomerName: so.Customer.Name,
			Memo:         so.Memo,
			Amount:       so.Total,
			Currency:     so.CurrencyCode,
			Status:       string(so.Status),
			DetailURL:    "/sales-orders/" + uitoa(so.ID),
		})
	}
	return out, nil
}

func loadCustomerReceiptsForSalesTx(db *gorm.DB, companyID uint, f SalesTxFilter) ([]SalesTxRow, error) {
	q := db.Model(&models.CustomerReceipt{}).
		Where("company_id = ?", companyID).
		Preload("Customer")
	if f.DateFrom != nil {
		q = q.Where("receipt_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("receipt_date <= ?", *f.DateTo)
	}
	if f.CustomerID != 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("receipt_number ILIKE ? OR memo ILIKE ? OR reference ILIKE ?", like, like, like)
	}
	var rs []models.CustomerReceipt
	if err := q.Order("receipt_date desc, id desc").Find(&rs).Error; err != nil {
		return nil, err
	}
	out := make([]SalesTxRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, SalesTxRow{
			ID:           r.ID,
			Type:         SalesTxTypePayment,
			Date:         r.ReceiptDate,
			Number:       r.ReceiptNumber,
			CustomerID:   r.CustomerID,
			CustomerName: r.Customer.Name,
			Memo:         r.Memo,
			Amount:       r.Amount,
			Currency:     r.CurrencyCode,
			Status:       string(r.Status),
			DetailURL:    "/receipts/" + uitoa(r.ID),
		})
	}
	return out, nil
}

func loadCreditNotesForSalesTx(db *gorm.DB, companyID uint, f SalesTxFilter) ([]SalesTxRow, error) {
	q := db.Model(&models.CreditNote{}).
		Where("company_id = ?", companyID).
		Preload("Customer")
	if f.DateFrom != nil {
		q = q.Where("credit_note_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("credit_note_date <= ?", *f.DateTo)
	}
	if f.CustomerID != 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("credit_note_number ILIKE ? OR memo ILIKE ?", like, like)
	}
	var cns []models.CreditNote
	if err := q.Order("credit_note_date desc, id desc").Find(&cns).Error; err != nil {
		return nil, err
	}
	out := make([]SalesTxRow, 0, len(cns))
	for _, cn := range cns {
		out = append(out, SalesTxRow{
			ID:           cn.ID,
			Type:         SalesTxTypeCreditNote,
			Date:         cn.CreditNoteDate,
			Number:       cn.CreditNoteNumber,
			CustomerID:   cn.CustomerID,
			CustomerName: cn.Customer.Name,
			Memo:         cn.Memo,
			Amount:       cn.Amount.Neg(), // credits reduce AR; show as negative
			Currency:     cn.CurrencyCode,
			Status:       string(cn.Status),
			DetailURL:    "/credit-notes/" + uitoa(cn.ID),
		})
	}
	return out, nil
}

func loadReturnsForSalesTx(db *gorm.DB, companyID uint, f SalesTxFilter) ([]SalesTxRow, error) {
	q := db.Model(&models.ARReturn{}).
		Where("company_id = ?", companyID).
		Preload("Customer")
	if f.DateFrom != nil {
		q = q.Where("return_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("return_date <= ?", *f.DateTo)
	}
	if f.CustomerID != 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("return_number ILIKE ? OR description ILIKE ?", like, like)
	}
	var rs []models.ARReturn
	if err := q.Order("return_date desc, id desc").Find(&rs).Error; err != nil {
		return nil, err
	}
	out := make([]SalesTxRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, SalesTxRow{
			ID:           r.ID,
			Type:         SalesTxTypeReturn,
			Date:         r.ReturnDate,
			Number:       r.ReturnNumber,
			CustomerID:   r.CustomerID,
			CustomerName: r.Customer.Name,
			Memo:         r.Description,
			Amount:       r.ReturnAmount.Neg(),
			Currency:     r.CurrencyCode,
			Status:       string(r.Status),
			DetailURL:    "/returns/" + uitoa(r.ID),
		})
	}
	return out, nil
}

// ComputeSalesTxKPI returns the five aggregates shown in the KPI strip.
// Each aggregate is a standalone query — no overlap suppression — so an
// overdue invoice contributes to both OverdueTotal and OpenTotal.
func ComputeSalesTxKPI(db *gorm.DB, companyID uint) (SalesTxKPI, error) {
	var kpi SalesTxKPI
	if companyID == 0 {
		return kpi, nil
	}
	today := time.Now().Truncate(24 * time.Hour)

	// Quotes open (draft + sent).
	var quotes []models.Quote
	if err := db.Select("id, total").
		Where("company_id = ? AND status IN ?", companyID,
			[]string{string(models.QuoteStatusDraft), string(models.QuoteStatusSent)}).
		Find(&quotes).Error; err != nil {
		return kpi, err
	}
	kpi.QuotesOpenCount = len(quotes)
	for _, q := range quotes {
		kpi.QuotesOpenTotal = kpi.QuotesOpenTotal.Add(q.Total)
	}

	// Unbilled income — confirmed/partial SOs with total > invoiced.
	var sos []models.SalesOrder
	if err := db.Select("id, total, invoiced_amount").
		Where("company_id = ? AND status IN ?", companyID,
			[]string{
				string(models.SalesOrderStatusConfirmed),
				string(models.SalesOrderStatusPartiallyInvoiced),
			}).
		Find(&sos).Error; err != nil {
		return kpi, err
	}
	for _, so := range sos {
		rem := so.Total.Sub(so.InvoicedAmount)
		if rem.IsPositive() {
			kpi.UnbilledTotal = kpi.UnbilledTotal.Add(rem)
		}
	}

	// Overdue — invoices past due date with balance > 0.
	var overdue []models.Invoice
	if err := db.Select("id, balance_due").
		Where("company_id = ? AND balance_due > 0 AND due_date IS NOT NULL AND due_date < ? AND status NOT IN ?",
			companyID, today,
			[]string{string(models.InvoiceStatusPaid), string(models.InvoiceStatusVoided), string(models.InvoiceStatusDraft)}).
		Find(&overdue).Error; err != nil {
		return kpi, err
	}
	kpi.OverdueCount = len(overdue)
	for _, iv := range overdue {
		kpi.OverdueTotal = kpi.OverdueTotal.Add(iv.BalanceDue)
	}

	// Open — invoices with balance > 0 (includes overdue) + credit notes with remaining balance.
	var openInvs []models.Invoice
	if err := db.Select("id, balance_due").
		Where("company_id = ? AND balance_due > 0 AND status NOT IN ?",
			companyID,
			[]string{string(models.InvoiceStatusPaid), string(models.InvoiceStatusVoided), string(models.InvoiceStatusDraft)}).
		Find(&openInvs).Error; err != nil {
		return kpi, err
	}
	kpi.OpenCount = len(openInvs)
	for _, iv := range openInvs {
		kpi.OpenTotal = kpi.OpenTotal.Add(iv.BalanceDue)
	}
	var openCNs []models.CreditNote
	if err := db.Select("id, balance_remaining").
		Where("company_id = ? AND balance_remaining > 0 AND status = ?", companyID,
			string(models.CreditNoteStatusIssued)).
		Find(&openCNs).Error; err != nil {
		return kpi, err
	}
	kpi.OpenCount += len(openCNs)
	for _, cn := range openCNs {
		kpi.OpenTotal = kpi.OpenTotal.Add(cn.BalanceRemaining)
	}

	// Recently paid — confirmed customer receipts in the last 7 days.
	sevenDaysAgo := today.AddDate(0, 0, -7)
	var recent []models.CustomerReceipt
	if err := db.Select("id, amount").
		Where("company_id = ? AND status = ? AND receipt_date >= ?",
			companyID, string(models.CustomerReceiptStatusConfirmed), sevenDaysAgo).
		Find(&recent).Error; err != nil {
		return kpi, err
	}
	kpi.RecentlyPaidCount = len(recent)
	for _, r := range recent {
		kpi.RecentlyPaidTotal = kpi.RecentlyPaidTotal.Add(r.Amount)
	}

	return kpi, nil
}

// uitoa is a tiny local helper to keep the per-loader URL-assembly lines
// short. strconv.FormatUint is the canonical path.
func uitoa(u uint) string {
	return strconv.FormatUint(uint64(u), 10)
}
