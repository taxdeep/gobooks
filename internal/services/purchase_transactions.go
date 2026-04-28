// 遵循project_guide.md
package services

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// PurchaseTxFilter is the AP-side mirror of SalesTxFilter. Zero-value
// fields = "no filter". Keeps the two surfaces structurally aligned so
// a future unified filter component can drive either.
type PurchaseTxFilter struct {
	Type     string     // "" or "all" / "bill" / "expense" / "vendor_credit_note" / "vendor_refund"
	DateFrom *time.Time // inclusive; nil = unbounded
	DateTo   *time.Time // inclusive; nil = unbounded
	VendorID uint       // 0 = all vendors
	Status   string     // native status of the type; ignored when Type is ""/all
	Search   string     // LIKE match on document number + memo
}

// PurchaseTxType* are the canonical type discriminators for
// PurchaseTxRow.Type. These values double as filter inputs.
const (
	PurchaseTxTypeBill             = "bill"
	PurchaseTxTypeExpense          = "expense"
	PurchaseTxTypeVendorCreditNote = "vendor_credit_note"
	PurchaseTxTypeVendorRefund     = "vendor_refund"
)

// PurchaseTxRow is one row in the unified AP-side transaction list.
// Mirrors SalesTxRow field-for-field so templ helpers can render
// either with the same shape.
type PurchaseTxRow struct {
	ID         uint
	Type       string    // one of PurchaseTxType* constants
	Date       time.Time // BillDate / ExpenseDate / CreditNoteDate / RefundDate
	Number     string    // BillNumber / ExpenseNumber / CreditNoteNumber / RefundNumber
	VendorID   uint
	VendorName string
	Memo       string
	Amount     decimal.Decimal
	Currency   string
	Status     string // native status string of the document
	DueDate    *time.Time // only populated for Bill (drives Overdue)
	// DetailURL is the canonical deep-link for this row's View action.
	DetailURL string
}

// ListPurchaseTransactions returns a page of unified AP transaction
// rows for the given company + filter. Rows are sorted by date DESC
// (then Type + ID DESC for stable ordering within the same date).
//
// Implementation strategy matches ListSalesTransactions — load each
// doc type in its own query, skipping when the Type filter asks for
// something else, then merge + sort + slice. A SQL UNION refactor can
// land when per-type row counts grow past a few thousand per company.
func ListPurchaseTransactions(db *gorm.DB, companyID uint, f PurchaseTxFilter, page, size int) ([]PurchaseTxRow, int, error) {
	if companyID == 0 {
		return nil, 0, nil
	}
	if size <= 0 {
		size = 50
	}
	if page <= 0 {
		page = 1
	}

	typ := strings.TrimSpace(strings.ToLower(f.Type))
	if typ == "all" {
		typ = ""
	}

	var all []PurchaseTxRow

	if typ == "" || typ == PurchaseTxTypeBill {
		rows, err := loadBillsForPurchaseTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "" || typ == PurchaseTxTypeExpense {
		rows, err := loadExpensesForPurchaseTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "" || typ == PurchaseTxTypeVendorCreditNote {
		rows, err := loadVendorCreditNotesForPurchaseTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}
	if typ == "" || typ == PurchaseTxTypeVendorRefund {
		rows, err := loadVendorRefundsForPurchaseTx(db, companyID, f)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
	}

	// Sort DESC by Date, then Type+ID for stability.
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Date.Equal(all[j].Date) {
			return all[i].Date.After(all[j].Date)
		}
		if all[i].Type != all[j].Type {
			return all[i].Type < all[j].Type
		}
		return all[i].ID > all[j].ID
	})

	total := len(all)
	start := (page - 1) * size
	if start >= len(all) {
		return []PurchaseTxRow{}, total, nil
	}
	end := start + size
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

// ── Per-type loaders ────────────────────────────────────────────────────────

func loadBillsForPurchaseTx(db *gorm.DB, companyID uint, f PurchaseTxFilter) ([]PurchaseTxRow, error) {
	q := db.Model(&models.Bill{}).
		Where("company_id = ?", companyID).
		Preload("Vendor")
	if f.DateFrom != nil {
		q = q.Where("bill_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("bill_date <= ?", *f.DateTo)
	}
	if f.VendorID != 0 {
		q = q.Where("vendor_id = ?", f.VendorID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		q = applyPurchaseTxSearch(q, db.Dialector.Name(), f.Search, "bill_number", "memo")
	}
	var bills []models.Bill
	if err := q.Order("bill_date desc, id desc").Find(&bills).Error; err != nil {
		return nil, err
	}
	out := make([]PurchaseTxRow, 0, len(bills))
	for _, b := range bills {
		row := PurchaseTxRow{
			ID:         b.ID,
			Type:       PurchaseTxTypeBill,
			Date:       b.BillDate,
			Number:     b.BillNumber,
			VendorID:   b.VendorID,
			VendorName: b.Vendor.Name,
			Memo:       b.Memo,
			Amount:     b.Amount,
			Currency:   b.CurrencyCode,
			Status:     string(b.Status),
			DetailURL:  billDetailURL(b.ID),
		}
		if b.DueDate != nil {
			row.DueDate = b.DueDate
		}
		out = append(out, row)
	}
	return out, nil
}

func loadExpensesForPurchaseTx(db *gorm.DB, companyID uint, f PurchaseTxFilter) ([]PurchaseTxRow, error) {
	q := db.Model(&models.Expense{}).
		Where("company_id = ?", companyID).
		Preload("Vendor")
	if f.DateFrom != nil {
		q = q.Where("expense_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("expense_date <= ?", *f.DateTo)
	}
	if f.VendorID != 0 {
		q = q.Where("vendor_id = ?", f.VendorID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		q = applyPurchaseTxSearch(q, db.Dialector.Name(), f.Search, "expense_number", "description")
	}
	var rs []models.Expense
	if err := q.Order("expense_date desc, id desc").Find(&rs).Error; err != nil {
		return nil, err
	}
	out := make([]PurchaseTxRow, 0, len(rs))
	for _, e := range rs {
		vname := ""
		var vid uint
		if e.Vendor != nil {
			vname = e.Vendor.Name
			vid = e.Vendor.ID
		}
		out = append(out, PurchaseTxRow{
			ID:         e.ID,
			Type:       PurchaseTxTypeExpense,
			Date:       e.ExpenseDate,
			Number:     e.ExpenseNumber,
			VendorID:   vid,
			VendorName: vname,
			Memo:       e.Description,
			Amount:     e.Amount,
			Currency:   e.CurrencyCode,
			Status:     string(e.Status),
			DetailURL:  "/expenses/" + uitoaPTx(e.ID) + "/edit",
		})
	}
	return out, nil
}

func loadVendorCreditNotesForPurchaseTx(db *gorm.DB, companyID uint, f PurchaseTxFilter) ([]PurchaseTxRow, error) {
	q := db.Model(&models.VendorCreditNote{}).
		Where("company_id = ?", companyID).
		Preload("Vendor")
	if f.DateFrom != nil {
		q = q.Where("credit_note_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("credit_note_date <= ?", *f.DateTo)
	}
	if f.VendorID != 0 {
		q = q.Where("vendor_id = ?", f.VendorID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		q = applyPurchaseTxSearch(q, db.Dialector.Name(), f.Search, "credit_note_number", "memo")
	}
	var rs []models.VendorCreditNote
	if err := q.Order("credit_note_date desc, id desc").Find(&rs).Error; err != nil {
		return nil, err
	}
	out := make([]PurchaseTxRow, 0, len(rs))
	for _, cn := range rs {
		out = append(out, PurchaseTxRow{
			ID:         cn.ID,
			Type:       PurchaseTxTypeVendorCreditNote,
			Date:       cn.CreditNoteDate,
			Number:     cn.CreditNoteNumber,
			VendorID:   cn.VendorID,
			VendorName: cn.Vendor.Name,
			Memo:       cn.Memo,
			Amount:     cn.Amount,
			Currency:   cn.CurrencyCode,
			Status:     string(cn.Status),
			DetailURL:  "/vendor-credit-notes/" + uitoaPTx(cn.ID),
		})
	}
	return out, nil
}

func loadVendorRefundsForPurchaseTx(db *gorm.DB, companyID uint, f PurchaseTxFilter) ([]PurchaseTxRow, error) {
	q := db.Model(&models.VendorRefund{}).
		Where("company_id = ?", companyID).
		Preload("Vendor")
	if f.DateFrom != nil {
		q = q.Where("refund_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("refund_date <= ?", *f.DateTo)
	}
	if f.VendorID != 0 {
		q = q.Where("vendor_id = ?", f.VendorID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		q = applyPurchaseTxSearch(q, db.Dialector.Name(), f.Search, "refund_number", "memo")
	}
	var rs []models.VendorRefund
	if err := q.Order("refund_date desc, id desc").Find(&rs).Error; err != nil {
		return nil, err
	}
	out := make([]PurchaseTxRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, PurchaseTxRow{
			ID:         r.ID,
			Type:       PurchaseTxTypeVendorRefund,
			Date:       r.RefundDate,
			Number:     r.RefundNumber,
			VendorID:   r.VendorID,
			VendorName: r.Vendor.Name,
			Memo:       r.Memo,
			Amount:     r.Amount,
			Currency:   r.CurrencyCode,
			Status:     string(r.Status),
			DetailURL:  "/vendor-refunds/" + uitoaPTx(r.ID),
		})
	}
	return out, nil
}

// ── Tiny helpers ────────────────────────────────────────────────────────────

// billDetailURL hides the route choice (detail page vs edit page)
// behind one lookup so callers don't have to remember which bills
// have detail routes — kept in sync with the Bills list link.
func billDetailURL(id uint) string {
	return "/bills/" + uitoaPTx(id)
}

// uitoaPTx is an inlined strconv-free uint-to-string — same trick as
// search_engine.uintKey. Purchase-tx-scoped name avoids colliding with
// any pages-package helper that might share the shorter name.
func uitoaPTx(id uint) string {
	if id == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	return string(buf[i:])
}

// applyPurchaseTxSearch builds a case-insensitive LIKE across the given
// columns. Matches the dialect handling in applySmartPickerTextSearch
// (LIKE on sqlite, ILIKE on postgres) so the behaviour is identical
// across dev + prod.
func applyPurchaseTxSearch(db *gorm.DB, dialect, query string, fields ...string) *gorm.DB {
	query = strings.TrimSpace(query)
	if query == "" || len(fields) == 0 {
		return db
	}
	op := "LIKE"
	if dialect == "postgres" {
		op = "ILIKE"
	}
	clauses := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields))
	for _, field := range fields {
		clauses = append(clauses, field+" "+op+" ?")
		args = append(args, "%"+query+"%")
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}
