// 遵循project_guide.md
package producers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/searchprojection"
)

// Transaction entity-type discriminators. Values match the SmartPicker
// entity keys used by search_engine so Phase 4 can fan out from a single
// entity=global query to all types without string translation.
const (
	EntityTypeInvoice         = "invoice"
	EntityTypeBill            = "bill"
	EntityTypeQuote           = "quote"
	EntityTypeSalesOrder      = "sales_order"
	EntityTypePurchaseOrder   = "purchase_order"
	EntityTypeCustomerReceipt = "customer_receipt"
	EntityTypeExpense         = "expense"

	// Phase 5.4 / 5.5 — remaining transaction families.
	EntityTypeJournalEntry     = "journal_entry"
	EntityTypeCreditNote       = "credit_note"
	EntityTypeVendorCreditNote = "vendor_credit_note"
	EntityTypeARReturn         = "ar_return"
	EntityTypeVendorReturn     = "vendor_return"
	EntityTypeARRefund         = "ar_refund"
	EntityTypeVendorRefund     = "vendor_refund"
	EntityTypeCustomerDeposit  = "customer_deposit"
	EntityTypeVendorPrepayment = "vendor_prepayment"
)

// Shared Document-building pattern (mirrors contact.go / product.go):
//   - Title     = counterparty name (customer for AR, vendor for AP)
//     so operators searching by "Lighting Geek" find all their docs at once.
//   - DocNumber = transaction number (InvoiceNumber / BillNumber / etc.)
//     so "INV-202604" hits the first-tier exact/prefix match.
//   - Subtitle  = "<type label> <number> · <date> · <currency> <amount>"
//     giving the operator everything needed to pick the right row.
//   - Memo      = native memo / notes field (low-priority substring match).
//   - Status    = native status string; UI layer maps to badge colour.
//   - DocDate   = business date (InvoiceDate / BillDate / etc.) for recency.

// ─────────────────────────────────────────────────────────────────────
// Invoice
// ─────────────────────────────────────────────────────────────────────

// ProjectInvoice loads (id, company_id) from the invoices table, builds
// the Document, and upserts via p. Call after every successful write on
// the invoice (save draft, post, void, status transition). Rejects
// cross-tenant IDs with ErrEntityNotInCompany — caller MUST have
// validated companyID ownership before invoking, per the defence-in-
// depth contract.
func ProjectInvoice(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, invoiceID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectInvoice: companyID is required")
	}
	var inv models.Invoice
	err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		Preload("Customer").
		First(&inv).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectInvoice: load %d for company %d: %w", invoiceID, companyID, err)
	}
	doc := InvoiceDocument(inv)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectInvoice upsert failed",
			"invoice_id", invoiceID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

// DeleteInvoiceProjection removes the search row for a hard-deleted invoice.
// Void transitions run through ProjectInvoice instead so the row stays
// searchable with status=voided.
func DeleteInvoiceProjection(ctx context.Context, p searchprojection.Projector, companyID, invoiceID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeInvoice, invoiceID)
}

// InvoiceDocument is the pure mapping function — exported so the backfill
// CLI can build a Document from a row it already has in memory without
// re-hitting the DB.
func InvoiceDocument(inv models.Invoice) searchprojection.Document {
	number := inv.InvoiceNumber
	title := counterpartyTitle(inv.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Invoice", number, inv.InvoiceDate.Format("2006-01-02"), inv.CurrencyCode, inv.Amount.StringFixed(2))
	docDate := inv.InvoiceDate
	return searchprojection.Document{
		CompanyID:  inv.CompanyID,
		EntityType: EntityTypeInvoice,
		EntityID:   inv.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       inv.Memo,
		DocDate:    &docDate,
		Amount:     inv.Amount.StringFixed(2),
		Currency:   inv.CurrencyCode,
		Status:     string(inv.Status),
		URLPath:    "/invoices/" + strconv.FormatUint(uint64(inv.ID), 10),
	}
}

// ─────────────────────────────────────────────────────────────────────
// Bill
// ─────────────────────────────────────────────────────────────────────

func ProjectBill(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, billID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectBill: companyID is required")
	}
	var bill models.Bill
	err := db.Where("id = ? AND company_id = ?", billID, companyID).
		Preload("Vendor").
		First(&bill).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectBill: load %d for company %d: %w", billID, companyID, err)
	}
	doc := BillDocument(bill)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectBill upsert failed",
			"bill_id", billID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteBillProjection(ctx context.Context, p searchprojection.Projector, companyID, billID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeBill, billID)
}

func BillDocument(b models.Bill) searchprojection.Document {
	number := b.BillNumber
	title := counterpartyTitle(b.Vendor.Name, "Vendor", number)
	subtitle := formatTxSubtitle("Bill", number, b.BillDate.Format("2006-01-02"), b.CurrencyCode, b.Amount.StringFixed(2))
	docDate := b.BillDate
	return searchprojection.Document{
		CompanyID:  b.CompanyID,
		EntityType: EntityTypeBill,
		EntityID:   b.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       b.Memo,
		DocDate:    &docDate,
		Amount:     b.Amount.StringFixed(2),
		Currency:   b.CurrencyCode,
		Status:     string(b.Status),
		URLPath:    "/bills/" + strconv.FormatUint(uint64(b.ID), 10),
	}
}

// ─────────────────────────────────────────────────────────────────────
// Quote
// ─────────────────────────────────────────────────────────────────────

func ProjectQuote(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, quoteID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectQuote: companyID is required")
	}
	var q models.Quote
	err := db.Where("id = ? AND company_id = ?", quoteID, companyID).
		Preload("Customer").
		First(&q).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectQuote: load %d for company %d: %w", quoteID, companyID, err)
	}
	doc := QuoteDocument(q)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectQuote upsert failed",
			"quote_id", quoteID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteQuoteProjection(ctx context.Context, p searchprojection.Projector, companyID, quoteID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeQuote, quoteID)
}

func QuoteDocument(q models.Quote) searchprojection.Document {
	number := q.QuoteNumber
	title := counterpartyTitle(q.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Quote", number, q.QuoteDate.Format("2006-01-02"), q.CurrencyCode, q.Total.StringFixed(2))
	// Prefer Notes over Memo for search: Notes is customer-visible and
	// usually richer; Memo is a terse internal tag.
	memo := q.Notes
	if memo == "" {
		memo = q.Memo
	}
	docDate := q.QuoteDate
	return searchprojection.Document{
		CompanyID:  q.CompanyID,
		EntityType: EntityTypeQuote,
		EntityID:   q.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       memo,
		DocDate:    &docDate,
		Amount:     q.Total.StringFixed(2),
		Currency:   q.CurrencyCode,
		Status:     string(q.Status),
		URLPath:    "/quotes/" + strconv.FormatUint(uint64(q.ID), 10),
	}
}

// ─────────────────────────────────────────────────────────────────────
// SalesOrder
// ─────────────────────────────────────────────────────────────────────

func ProjectSalesOrder(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, orderID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectSalesOrder: companyID is required")
	}
	var so models.SalesOrder
	err := db.Where("id = ? AND company_id = ?", orderID, companyID).
		Preload("Customer").
		First(&so).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectSalesOrder: load %d for company %d: %w", orderID, companyID, err)
	}
	doc := SalesOrderDocument(so)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectSalesOrder upsert failed",
			"sales_order_id", orderID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteSalesOrderProjection(ctx context.Context, p searchprojection.Projector, companyID, orderID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeSalesOrder, orderID)
}

func SalesOrderDocument(so models.SalesOrder) searchprojection.Document {
	number := so.OrderNumber
	title := counterpartyTitle(so.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Sales Order", number, so.OrderDate.Format("2006-01-02"), so.CurrencyCode, so.Total.StringFixed(2))
	memo := so.Notes
	if memo == "" {
		memo = so.Memo
	}
	docDate := so.OrderDate
	return searchprojection.Document{
		CompanyID:  so.CompanyID,
		EntityType: EntityTypeSalesOrder,
		EntityID:   so.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       memo,
		DocDate:    &docDate,
		Amount:     so.Total.StringFixed(2),
		Currency:   so.CurrencyCode,
		Status:     string(so.Status),
		URLPath:    "/sales-orders/" + strconv.FormatUint(uint64(so.ID), 10),
	}
}

// ─────────────────────────────────────────────────────────────────────
// PurchaseOrder
// ─────────────────────────────────────────────────────────────────────

func ProjectPurchaseOrder(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, poID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectPurchaseOrder: companyID is required")
	}
	var po models.PurchaseOrder
	err := db.Where("id = ? AND company_id = ?", poID, companyID).
		Preload("Vendor").
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectPurchaseOrder: load %d for company %d: %w", poID, companyID, err)
	}
	doc := PurchaseOrderDocument(po)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectPurchaseOrder upsert failed",
			"po_id", poID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeletePurchaseOrderProjection(ctx context.Context, p searchprojection.Projector, companyID, poID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypePurchaseOrder, poID)
}

func PurchaseOrderDocument(po models.PurchaseOrder) searchprojection.Document {
	number := po.PONumber
	title := counterpartyTitle(po.Vendor.Name, "Vendor", number)
	subtitle := formatTxSubtitle("Purchase Order", number, po.PODate.Format("2006-01-02"), po.CurrencyCode, po.Amount.StringFixed(2))
	docDate := po.PODate
	return searchprojection.Document{
		CompanyID:  po.CompanyID,
		EntityType: EntityTypePurchaseOrder,
		EntityID:   po.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       po.Notes,
		DocDate:    &docDate,
		Amount:     po.Amount.StringFixed(2),
		Currency:   po.CurrencyCode,
		Status:     string(po.Status),
		URLPath:    "/purchase-orders/" + strconv.FormatUint(uint64(po.ID), 10),
	}
}

// ─────────────────────────────────────────────────────────────────────
// CustomerReceipt (customer-side payment)
// ─────────────────────────────────────────────────────────────────────

func ProjectCustomerReceipt(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, receiptID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectCustomerReceipt: companyID is required")
	}
	var r models.CustomerReceipt
	err := db.Where("id = ? AND company_id = ?", receiptID, companyID).
		Preload("Customer").
		First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectCustomerReceipt: load %d for company %d: %w", receiptID, companyID, err)
	}
	doc := CustomerReceiptDocument(r)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectCustomerReceipt upsert failed",
			"receipt_id", receiptID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteCustomerReceiptProjection(ctx context.Context, p searchprojection.Projector, companyID, receiptID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeCustomerReceipt, receiptID)
}

func CustomerReceiptDocument(r models.CustomerReceipt) searchprojection.Document {
	number := r.ReceiptNumber
	title := counterpartyTitle(r.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Payment", number, r.ReceiptDate.Format("2006-01-02"), r.CurrencyCode, r.Amount.StringFixed(2))
	docDate := r.ReceiptDate
	return searchprojection.Document{
		CompanyID:  r.CompanyID,
		EntityType: EntityTypeCustomerReceipt,
		EntityID:   r.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       r.Memo,
		DocDate:    &docDate,
		Amount:     r.Amount.StringFixed(2),
		Currency:   r.CurrencyCode,
		Status:     string(r.Status),
		URLPath:    "/receipts/" + strconv.FormatUint(uint64(r.ID), 10),
	}
}

// ─────────────────────────────────────────────────────────────────────
// Expense (standalone vendor-or-reimbursable cost)
// ─────────────────────────────────────────────────────────────────────
//
// Expense differs from Bill: Vendor is OPTIONAL (reimbursement expenses
// have no counterparty until they're attached to someone). When no
// vendor is present, Title falls back to the Description or a synthetic
// label so the row stays selectable in search.

func ProjectExpense(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, expenseID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectExpense: companyID is required")
	}
	var e models.Expense
	err := db.Where("id = ? AND company_id = ?", expenseID, companyID).
		Preload("Vendor").
		First(&e).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectExpense: load %d for company %d: %w", expenseID, companyID, err)
	}
	doc := ExpenseDocument(e)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectExpense upsert failed",
			"expense_id", expenseID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteExpenseProjection(ctx context.Context, p searchprojection.Projector, companyID, expenseID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeExpense, expenseID)
}

func ExpenseDocument(e models.Expense) searchprojection.Document {
	number := e.ExpenseNumber
	vendorName := ""
	if e.Vendor != nil {
		vendorName = e.Vendor.Name
	}
	// Title priority: vendor name → description first line → synthetic
	// (number-only) fallback. Operator searching "Scotia Bank" for a
	// service fee finds the expense by title; searching "rent" finds it
	// via memo/description match.
	title := vendorName
	if title == "" {
		title = firstLine(e.Description)
	}
	if title == "" {
		title = counterpartyTitle("", "Expense", number)
	}
	subtitle := formatTxSubtitle("Expense", number, e.ExpenseDate.Format("2006-01-02"), e.CurrencyCode, e.Amount.StringFixed(2))
	docDate := e.ExpenseDate
	return searchprojection.Document{
		CompanyID:  e.CompanyID,
		EntityType: EntityTypeExpense,
		EntityID:   e.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       e.Description,
		DocDate:    &docDate,
		Amount:     e.Amount.StringFixed(2),
		Currency:   e.CurrencyCode,
		Status:     string(e.Status),
		URLPath:    "/expenses/" + strconv.FormatUint(uint64(e.ID), 10),
	}
}

// firstLine returns s up to the first newline / carriage return, trimmed.
// Used as a title fallback for entities without a counterparty so the
// dropdown row doesn't show a wall of multi-line description text.
func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}

// ─────────────────────────────────────────────────────────────────────
// Shared formatting helpers
// ─────────────────────────────────────────────────────────────────────

// counterpartyTitle returns the display title for a transaction. Uses
// the counterparty name when present; falls back to a synthetic label
// built from the type + doc number so the row is never untitled (which
// would be a UX regression in the dropdown — operator sees blank row).
//
// genericKind is the noun used in the fallback title ("Customer" for
// AR docs, "Vendor" for AP docs) — the whole string reads like
// "(unnamed Customer — INV-202604)" so operators know to fix it.
func counterpartyTitle(name, genericKind, fallbackNumber string) string {
	if name != "" {
		return name
	}
	if fallbackNumber != "" {
		return "(unnamed " + genericKind + " — " + fallbackNumber + ")"
	}
	return "(unnamed " + genericKind + ")"
}

// formatTxSubtitle builds the subtitle line:
//
//	"<type label> <number> · <date> · <currency> <amount>"
//
// Pieces with empty values are skipped so the separator pattern doesn't
// leave stranded " · ·" runs. Example output:
//
//	"Invoice INV-202604 · 2026-04-22 · CAD 3600.00"
//	"Quote QUO-0001 · 2026-04-20"          (amount omitted when zero)
func formatTxSubtitle(label, number, date, currency, amount string) string {
	parts := []string{}
	head := label
	if number != "" {
		head = label + " " + number
	}
	parts = append(parts, head)
	if date != "" {
		parts = append(parts, date)
	}
	if amount != "" && amount != "0.00" {
		amt := amount
		if currency != "" {
			amt = currency + " " + amount
		}
		parts = append(parts, amt)
	}
	out := ""
	for i, p := range parts {
		if i == 0 {
			out = p
			continue
		}
		out = out + " · " + p
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────
// Phase 5.4 / 5.5 — JournalEntry + 4 AR docs + 4 AP docs
// ─────────────────────────────────────────────────────────────────────

// JournalEntry is structurally different from the other transaction
// types: no counterparty (it is a debit/credit ledger row), no Memo /
// Description field, no aggregate Amount on the header row (Amount
// lives on JournalLine.Debit/Credit). For search we surface:
//   Title    "Journal Entry <JournalNo>"
//   Subtitle "Journal Entry <JournalNo> · <date> · source=<SourceType>"
//   Amount   empty (would require summing lines — defer)
//   Memo     empty (no native field)

func ProjectJournalEntry(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, entryID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectJournalEntry: companyID is required")
	}
	var je models.JournalEntry
	err := db.Where("id = ? AND company_id = ?", entryID, companyID).
		Preload("Lines").
		First(&je).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectJournalEntry: load %d for company %d: %w", entryID, companyID, err)
	}
	if !journalEntrySearchVisible(je) {
		return p.Delete(ctx, companyID, EntityTypeJournalEntry, entryID)
	}
	doc := JournalEntrySearchDocument(je)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectJournalEntry upsert failed",
			"entry_id", entryID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func journalEntrySearchVisible(je models.JournalEntry) bool {
	if je.Status == models.JournalEntryStatusVoided || je.Status == models.JournalEntryStatusReversed {
		return false
	}
	return je.SourceType != models.LedgerSourceReversal
}

func DeleteJournalEntryProjection(ctx context.Context, p searchprojection.Projector, companyID, entryID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeJournalEntry, entryID)
}

func JournalEntryDocument(je models.JournalEntry) searchprojection.Document {
	return JournalEntrySearchDocument(je)
	number := je.JournalNo
	title := "Journal Entry " + number
	if number == "" {
		title = "Journal Entry (no number)"
	}
	src := string(je.SourceType)
	if src == "" {
		src = "manual"
	}
	subtitle := "Journal Entry " + number + " · " + je.EntryDate.Format("2006-01-02") + " · source=" + src
	docDate := je.EntryDate
	return searchprojection.Document{
		CompanyID:  je.CompanyID,
		EntityType: EntityTypeJournalEntry,
		EntityID:   je.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       "",
		DocDate:    &docDate,
		Currency:   je.TransactionCurrencyCode,
		Status:     string(je.Status),
		URLPath:    "/journal-entry/" + strconv.FormatUint(uint64(je.ID), 10),
	}
}

func JournalEntrySearchDocument(je models.JournalEntry) searchprojection.Document {
	number := je.JournalNo
	title := "Journal Entry " + number
	if number == "" {
		title = "Journal Entry (no number)"
	}
	src := string(je.SourceType)
	if src == "" {
		src = "manual"
	}
	txDebitTotal, txCreditTotal, baseDebitTotal, baseCreditTotal := journalEntryTotals(je)
	txTotal := maxDecimal(txDebitTotal, txCreditTotal)
	baseTotal := maxDecimal(baseDebitTotal, baseCreditTotal)
	amount := ""
	if !txTotal.IsZero() {
		amount = txTotal.StringFixed(2)
	}
	subtitleParts := []string{"Journal Entry " + number, je.EntryDate.Format("2006-01-02"), "source=" + src}
	if amountSummary := journalAmountSummary(je.TransactionCurrencyCode, txTotal, baseTotal); amountSummary != "" {
		subtitleParts = append(subtitleParts, amountSummary)
	}
	docDate := je.EntryDate
	return searchprojection.Document{
		CompanyID:  je.CompanyID,
		EntityType: EntityTypeJournalEntry,
		EntityID:   je.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   strings.Join(subtitleParts, " · "),
		Memo:       journalEntrySearchMemo(je, txDebitTotal, txCreditTotal, baseDebitTotal, baseCreditTotal),
		DocDate:    &docDate,
		Amount:     amount,
		Currency:   je.TransactionCurrencyCode,
		Status:     string(je.Status),
		URLPath:    "/journal-entry/" + strconv.FormatUint(uint64(je.ID), 10),
	}
}

func journalEntryTotals(je models.JournalEntry) (decimal.Decimal, decimal.Decimal, decimal.Decimal, decimal.Decimal) {
	txDebitTotal := decimal.Zero
	txCreditTotal := decimal.Zero
	baseDebitTotal := decimal.Zero
	baseCreditTotal := decimal.Zero
	for _, line := range je.Lines {
		txDebit := line.TxDebit
		txCredit := line.TxCredit
		if txDebit.IsZero() && !line.Debit.IsZero() {
			txDebit = line.Debit
		}
		if txCredit.IsZero() && !line.Credit.IsZero() {
			txCredit = line.Credit
		}
		txDebitTotal = txDebitTotal.Add(txDebit)
		txCreditTotal = txCreditTotal.Add(txCredit)
		baseDebitTotal = baseDebitTotal.Add(line.Debit)
		baseCreditTotal = baseCreditTotal.Add(line.Credit)
	}
	return txDebitTotal, txCreditTotal, baseDebitTotal, baseCreditTotal
}

func journalAmountSummary(txCurrency string, txTotal, baseTotal decimal.Decimal) string {
	parts := []string{}
	txCurrency = strings.TrimSpace(txCurrency)
	if !txTotal.IsZero() {
		label := txTotal.StringFixed(2)
		if txCurrency != "" {
			label = txCurrency + " " + label
		}
		parts = append(parts, label)
	}
	if !baseTotal.IsZero() && !baseTotal.Equal(txTotal) {
		parts = append(parts, "base "+baseTotal.StringFixed(2))
	}
	return strings.Join(parts, " / ")
}

func journalEntrySearchMemo(je models.JournalEntry, txDebitTotal, txCreditTotal, baseDebitTotal, baseCreditTotal decimal.Decimal) string {
	seen := map[string]struct{}{}
	parts := []string{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		parts = append(parts, v)
	}

	add(je.JournalNo)
	add(string(je.Status))
	add(string(je.SourceType))
	add(je.TransactionCurrencyCode)
	for _, v := range []decimal.Decimal{txDebitTotal, txCreditTotal, baseDebitTotal, baseCreditTotal} {
		if !v.IsZero() {
			add(v.StringFixed(2))
		}
	}
	for _, line := range je.Lines {
		add(line.Memo)
		for _, v := range []decimal.Decimal{line.TxDebit, line.TxCredit, line.Debit, line.Credit} {
			if !v.IsZero() {
				add(v.StringFixed(2))
			}
		}
	}
	return strings.Join(parts, " ")
}

func maxDecimal(a, b decimal.Decimal) decimal.Decimal {
	if b.GreaterThan(a) {
		return b
	}
	return a
}

// CreditNote (customer-side)
func ProjectCreditNote(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, cnID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectCreditNote: companyID is required")
	}
	var cn models.CreditNote
	err := db.Where("id = ? AND company_id = ?", cnID, companyID).Preload("Customer").First(&cn).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectCreditNote: load %d for company %d: %w", cnID, companyID, err)
	}
	doc := CreditNoteDocument(cn)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectCreditNote upsert failed",
			"credit_note_id", cnID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteCreditNoteProjection(ctx context.Context, p searchprojection.Projector, companyID, cnID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeCreditNote, cnID)
}

func CreditNoteDocument(cn models.CreditNote) searchprojection.Document {
	number := cn.CreditNoteNumber
	title := counterpartyTitle(cn.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Credit Memo", number, cn.CreditNoteDate.Format("2006-01-02"), cn.CurrencyCode, cn.Amount.StringFixed(2))
	docDate := cn.CreditNoteDate
	return searchprojection.Document{
		CompanyID:  cn.CompanyID,
		EntityType: EntityTypeCreditNote,
		EntityID:   cn.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       cn.Memo,
		DocDate:    &docDate,
		Amount:     cn.Amount.StringFixed(2),
		Currency:   cn.CurrencyCode,
		Status:     string(cn.Status),
		URLPath:    "/credit-notes/" + strconv.FormatUint(uint64(cn.ID), 10),
	}
}

// VendorCreditNote (AP)
func ProjectVendorCreditNote(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, vcnID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectVendorCreditNote: companyID is required")
	}
	var vcn models.VendorCreditNote
	err := db.Where("id = ? AND company_id = ?", vcnID, companyID).Preload("Vendor").First(&vcn).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectVendorCreditNote: load %d for company %d: %w", vcnID, companyID, err)
	}
	doc := VendorCreditNoteDocument(vcn)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectVendorCreditNote upsert failed",
			"vcn_id", vcnID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteVendorCreditNoteProjection(ctx context.Context, p searchprojection.Projector, companyID, vcnID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeVendorCreditNote, vcnID)
}

func VendorCreditNoteDocument(vcn models.VendorCreditNote) searchprojection.Document {
	number := vcn.CreditNoteNumber
	title := counterpartyTitle(vcn.Vendor.Name, "Vendor", number)
	subtitle := formatTxSubtitle("Vendor Credit", number, vcn.CreditNoteDate.Format("2006-01-02"), vcn.CurrencyCode, vcn.Amount.StringFixed(2))
	docDate := vcn.CreditNoteDate
	return searchprojection.Document{
		CompanyID:  vcn.CompanyID,
		EntityType: EntityTypeVendorCreditNote,
		EntityID:   vcn.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       vcn.Memo,
		DocDate:    &docDate,
		Amount:     vcn.Amount.StringFixed(2),
		Currency:   vcn.CurrencyCode,
		Status:     string(vcn.Status),
		URLPath:    "/vendor-credit-notes/" + strconv.FormatUint(uint64(vcn.ID), 10),
	}
}

// ARReturn (customer return)
func ProjectARReturn(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, retID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectARReturn: companyID is required")
	}
	var r models.ARReturn
	err := db.Where("id = ? AND company_id = ?", retID, companyID).Preload("Customer").First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectARReturn: load %d for company %d: %w", retID, companyID, err)
	}
	doc := ARReturnDocument(r)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectARReturn upsert failed",
			"return_id", retID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteARReturnProjection(ctx context.Context, p searchprojection.Projector, companyID, retID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeARReturn, retID)
}

func ARReturnDocument(r models.ARReturn) searchprojection.Document {
	number := r.ReturnNumber
	title := counterpartyTitle(r.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Return", number, r.ReturnDate.Format("2006-01-02"), r.CurrencyCode, r.ReturnAmount.StringFixed(2))
	docDate := r.ReturnDate
	return searchprojection.Document{
		CompanyID:  r.CompanyID,
		EntityType: EntityTypeARReturn,
		EntityID:   r.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       r.Description,
		DocDate:    &docDate,
		Amount:     r.ReturnAmount.StringFixed(2),
		Currency:   r.CurrencyCode,
		Status:     string(r.Status),
		URLPath:    "/returns/" + strconv.FormatUint(uint64(r.ID), 10),
	}
}

// VendorReturn
func ProjectVendorReturn(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, retID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectVendorReturn: companyID is required")
	}
	var r models.VendorReturn
	err := db.Where("id = ? AND company_id = ?", retID, companyID).Preload("Vendor").First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectVendorReturn: load %d for company %d: %w", retID, companyID, err)
	}
	doc := VendorReturnDocument(r)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectVendorReturn upsert failed",
			"return_id", retID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteVendorReturnProjection(ctx context.Context, p searchprojection.Projector, companyID, retID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeVendorReturn, retID)
}

func VendorReturnDocument(r models.VendorReturn) searchprojection.Document {
	number := r.ReturnNumber
	title := counterpartyTitle(r.Vendor.Name, "Vendor", number)
	subtitle := formatTxSubtitle("Vendor Return", number, r.ReturnDate.Format("2006-01-02"), r.CurrencyCode, r.Amount.StringFixed(2))
	docDate := r.ReturnDate
	return searchprojection.Document{
		CompanyID:  r.CompanyID,
		EntityType: EntityTypeVendorReturn,
		EntityID:   r.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       r.Memo,
		DocDate:    &docDate,
		Amount:     r.Amount.StringFixed(2),
		Currency:   r.CurrencyCode,
		Status:     string(r.Status),
		URLPath:    "/vendor-returns/" + strconv.FormatUint(uint64(r.ID), 10),
	}
}

// ARRefund
func ProjectARRefund(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, refID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectARRefund: companyID is required")
	}
	var r models.ARRefund
	err := db.Where("id = ? AND company_id = ?", refID, companyID).Preload("Customer").First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectARRefund: load %d for company %d: %w", refID, companyID, err)
	}
	doc := ARRefundDocument(r)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectARRefund upsert failed",
			"refund_id", refID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteARRefundProjection(ctx context.Context, p searchprojection.Projector, companyID, refID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeARRefund, refID)
}

func ARRefundDocument(r models.ARRefund) searchprojection.Document {
	number := r.RefundNumber
	title := counterpartyTitle(r.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Refund", number, r.RefundDate.Format("2006-01-02"), r.CurrencyCode, r.Amount.StringFixed(2))
	docDate := r.RefundDate
	return searchprojection.Document{
		CompanyID:  r.CompanyID,
		EntityType: EntityTypeARRefund,
		EntityID:   r.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       r.Memo,
		DocDate:    &docDate,
		Amount:     r.Amount.StringFixed(2),
		Currency:   r.CurrencyCode,
		Status:     string(r.Status),
		URLPath:    "/refunds/" + strconv.FormatUint(uint64(r.ID), 10),
	}
}

// VendorRefund
func ProjectVendorRefund(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, refID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectVendorRefund: companyID is required")
	}
	var r models.VendorRefund
	err := db.Where("id = ? AND company_id = ?", refID, companyID).Preload("Vendor").First(&r).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectVendorRefund: load %d for company %d: %w", refID, companyID, err)
	}
	doc := VendorRefundDocument(r)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectVendorRefund upsert failed",
			"refund_id", refID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteVendorRefundProjection(ctx context.Context, p searchprojection.Projector, companyID, refID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeVendorRefund, refID)
}

func VendorRefundDocument(r models.VendorRefund) searchprojection.Document {
	number := r.RefundNumber
	title := counterpartyTitle(r.Vendor.Name, "Vendor", number)
	subtitle := formatTxSubtitle("Vendor Refund", number, r.RefundDate.Format("2006-01-02"), r.CurrencyCode, r.Amount.StringFixed(2))
	docDate := r.RefundDate
	return searchprojection.Document{
		CompanyID:  r.CompanyID,
		EntityType: EntityTypeVendorRefund,
		EntityID:   r.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       r.Memo,
		DocDate:    &docDate,
		Amount:     r.Amount.StringFixed(2),
		Currency:   r.CurrencyCode,
		Status:     string(r.Status),
		URLPath:    "/vendor-refunds/" + strconv.FormatUint(uint64(r.ID), 10),
	}
}

// CustomerDeposit
func ProjectCustomerDeposit(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, depID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectCustomerDeposit: companyID is required")
	}
	var d models.CustomerDeposit
	err := db.Where("id = ? AND company_id = ?", depID, companyID).Preload("Customer").First(&d).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectCustomerDeposit: load %d for company %d: %w", depID, companyID, err)
	}
	doc := CustomerDepositDocument(d)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectCustomerDeposit upsert failed",
			"deposit_id", depID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteCustomerDepositProjection(ctx context.Context, p searchprojection.Projector, companyID, depID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeCustomerDeposit, depID)
}

func CustomerDepositDocument(d models.CustomerDeposit) searchprojection.Document {
	number := d.DepositNumber
	title := counterpartyTitle(d.Customer.Name, "Customer", number)
	subtitle := formatTxSubtitle("Deposit", number, d.DepositDate.Format("2006-01-02"), d.CurrencyCode, d.Amount.StringFixed(2))
	docDate := d.DepositDate
	return searchprojection.Document{
		CompanyID:  d.CompanyID,
		EntityType: EntityTypeCustomerDeposit,
		EntityID:   d.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       d.Memo,
		DocDate:    &docDate,
		Amount:     d.Amount.StringFixed(2),
		Currency:   d.CurrencyCode,
		Status:     string(d.Status),
		URLPath:    "/deposits/" + strconv.FormatUint(uint64(d.ID), 10),
	}
}

// VendorPrepayment
func ProjectVendorPrepayment(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyID, prepID uint) error {
	if p == nil {
		return nil
	}
	if companyID == 0 {
		return errors.New("producers.ProjectVendorPrepayment: companyID is required")
	}
	var pr models.VendorPrepayment
	err := db.Where("id = ? AND company_id = ?", prepID, companyID).Preload("Vendor").First(&pr).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEntityNotInCompany
		}
		return fmt.Errorf("producers.ProjectVendorPrepayment: load %d for company %d: %w", prepID, companyID, err)
	}
	doc := VendorPrepaymentDocument(pr)
	if err := p.Upsert(ctx, companyID, doc); err != nil {
		logging.L().Warn("searchprojection.ProjectVendorPrepayment upsert failed",
			"prepayment_id", prepID, "company_id", companyID, "err", err)
		return err
	}
	return nil
}

func DeleteVendorPrepaymentProjection(ctx context.Context, p searchprojection.Projector, companyID, prepID uint) error {
	if p == nil {
		return nil
	}
	return p.Delete(ctx, companyID, EntityTypeVendorPrepayment, prepID)
}

func VendorPrepaymentDocument(pr models.VendorPrepayment) searchprojection.Document {
	number := pr.PrepaymentNumber
	title := counterpartyTitle(pr.Vendor.Name, "Vendor", number)
	subtitle := formatTxSubtitle("Prepayment", number, pr.PrepaymentDate.Format("2006-01-02"), pr.CurrencyCode, pr.Amount.StringFixed(2))
	docDate := pr.PrepaymentDate
	return searchprojection.Document{
		CompanyID:  pr.CompanyID,
		EntityType: EntityTypeVendorPrepayment,
		EntityID:   pr.ID,
		DocNumber:  number,
		Title:      title,
		Subtitle:   subtitle,
		Memo:       pr.Memo,
		DocDate:    &docDate,
		Amount:     pr.Amount.StringFixed(2),
		Currency:   pr.CurrencyCode,
		Status:     string(pr.Status),
		URLPath:    "/vendor-prepayments/" + strconv.FormatUint(uint64(pr.ID), 10),
	}
}
