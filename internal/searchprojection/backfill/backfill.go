// 遵循project_guide.md
//
// Package backfill rebuilds the search_documents projection from the
// canonical business tables. Lives behind a stable in-process library
// API so both the search-backfill CLI and the SysAdmin "Rebuild search
// index" button can drive the same code path — no subprocess spawning,
// no copy-pasted scan loops.
//
// Idempotency: every backfill function uses the projector's Upsert
// (OnConflict UpdateNewValues), so re-running is safe and never
// duplicates rows. Failures on individual entities are logged at WARN
// and the scan continues — one bad row doesn't poison the whole run.
//
// Cross-tenant safety: every projector Upsert validates doc.CompanyID
// against the explicit companyID param. The companyFilter argument here
// is just an "only this company" optimisation for re-runs that target
// a single tenant; the projector still enforces tenant scope per row.
package backfill

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/searchprojection"
	"balanciz/internal/searchprojection/producers"
)

// Family is the canonical key for a single backfill scope. Matches the
// CLI's `-only` flag values + the entity_type column in search_documents
// so callers can pass either.
type Family string

const (
	FamilyCustomer         Family = "customer"
	FamilyVendor           Family = "vendor"
	FamilyProductService   Family = "product_service"
	FamilyInvoice          Family = "invoice"
	FamilyBill             Family = "bill"
	FamilyQuote            Family = "quote"
	FamilySalesOrder       Family = "sales_order"
	FamilyPurchaseOrder    Family = "purchase_order"
	FamilyCustomerReceipt  Family = "customer_receipt"
	FamilyExpense          Family = "expense"
	FamilyJournalEntry     Family = "journal_entry"
	FamilyCreditNote       Family = "credit_note"
	FamilyVendorCreditNote Family = "vendor_credit_note"
	FamilyARReturn         Family = "ar_return"
	FamilyVendorReturn     Family = "vendor_return"
	FamilyARRefund         Family = "ar_refund"
	FamilyVendorRefund     Family = "vendor_refund"
	FamilyCustomerDeposit  Family = "customer_deposit"
	FamilyVendorPrepayment Family = "vendor_prepayment"
)

// AllFamilies is the in-display-order list every "rebuild everything"
// caller iterates. Mirrors the CLI's serial dispatch + the projector
// version registry in cmd/search-reconcile.
func AllFamilies() []Family {
	return []Family{
		FamilyCustomer, FamilyVendor, FamilyProductService,
		FamilyInvoice, FamilyBill, FamilyQuote, FamilySalesOrder,
		FamilyPurchaseOrder, FamilyCustomerReceipt, FamilyExpense,
		FamilyJournalEntry, FamilyCreditNote, FamilyVendorCreditNote,
		FamilyARReturn, FamilyVendorReturn, FamilyARRefund, FamilyVendorRefund,
		FamilyCustomerDeposit, FamilyVendorPrepayment,
	}
}

// Options bundles the knobs every backfill family supports.
type Options struct {
	// CompanyFilter scopes the scan to one company_id. 0 = scan everything.
	CompanyFilter uint
	// Batch is the rows-per-fetch chunk size. Defaults to 500 when zero.
	Batch int
}

// FamilyResult reports per-family outcome for the UI / audit log.
type FamilyResult struct {
	Family   Family
	Rows     int           // rows successfully upserted
	Duration time.Duration // wall time for this family
	Err      error         // non-nil if the scan aborted (rare; per-row failures are logged WARN)
}

// Result is the aggregated outcome of RunAll. Total elapsed wall time
// + per-family breakdown so the operator UI can surface progress.
type Result struct {
	Started   time.Time
	Completed time.Time
	Families  []FamilyResult
}

// TotalRows sums Rows across all families.
func (r Result) TotalRows() int {
	n := 0
	for _, f := range r.Families {
		n += f.Rows
	}
	return n
}

// FirstErr returns the first non-nil per-family error, or nil if every
// family completed cleanly. Lets callers report a single error string.
func (r Result) FirstErr() error {
	for _, f := range r.Families {
		if f.Err != nil {
			return fmt.Errorf("backfill %s: %w", f.Family, f.Err)
		}
	}
	return nil
}

// ParseFamily converts a free-form string (CLI flag, query param, audit
// payload) to a Family. Returns the zero family + false on unknown input
// so callers can fail loudly instead of silently no-op'ing.
func ParseFamily(raw string) (Family, bool) {
	s := Family(strings.TrimSpace(strings.ToLower(raw)))
	for _, f := range AllFamilies() {
		if f == s {
			return f, true
		}
	}
	return "", false
}

// RunAll runs every family backfill in display order. Per-family errors
// are captured into the Result rather than aborting the whole sweep —
// one broken family shouldn't block the others from refreshing.
func RunAll(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) Result {
	res := Result{Started: time.Now()}
	for _, fam := range AllFamilies() {
		res.Families = append(res.Families, RunFamily(ctx, db, p, fam, opts))
	}
	res.Completed = time.Now()
	return res
}

// RunFamily runs a single family's backfill. Exposed so callers (e.g.
// the CLI's `-only customer` flag) can target one entity without paying
// for the full sweep.
func RunFamily(ctx context.Context, db *gorm.DB, p searchprojection.Projector, fam Family, opts Options) FamilyResult {
	if opts.Batch <= 0 {
		opts.Batch = 500
	}
	start := time.Now()
	var (
		rows int
		err  error
	)
	switch fam {
	case FamilyCustomer:
		rows, err = backfillCustomers(ctx, db, p, opts)
	case FamilyVendor:
		rows, err = backfillVendors(ctx, db, p, opts)
	case FamilyProductService:
		rows, err = backfillProductServices(ctx, db, p, opts)
	case FamilyInvoice:
		rows, err = backfillInvoices(ctx, db, p, opts)
	case FamilyBill:
		rows, err = backfillBills(ctx, db, p, opts)
	case FamilyQuote:
		rows, err = backfillQuotes(ctx, db, p, opts)
	case FamilySalesOrder:
		rows, err = backfillSalesOrders(ctx, db, p, opts)
	case FamilyPurchaseOrder:
		rows, err = backfillPurchaseOrders(ctx, db, p, opts)
	case FamilyCustomerReceipt:
		rows, err = backfillCustomerReceipts(ctx, db, p, opts)
	case FamilyExpense:
		rows, err = backfillExpenses(ctx, db, p, opts)
	case FamilyJournalEntry:
		rows, err = backfillJournalEntries(ctx, db, p, opts)
	case FamilyCreditNote:
		rows, err = backfillCreditNotes(ctx, db, p, opts)
	case FamilyVendorCreditNote:
		rows, err = backfillVendorCreditNotes(ctx, db, p, opts)
	case FamilyARReturn:
		rows, err = backfillARReturns(ctx, db, p, opts)
	case FamilyVendorReturn:
		rows, err = backfillVendorReturns(ctx, db, p, opts)
	case FamilyARRefund:
		rows, err = backfillARRefunds(ctx, db, p, opts)
	case FamilyVendorRefund:
		rows, err = backfillVendorRefunds(ctx, db, p, opts)
	case FamilyCustomerDeposit:
		rows, err = backfillCustomerDeposits(ctx, db, p, opts)
	case FamilyVendorPrepayment:
		rows, err = backfillVendorPrepayments(ctx, db, p, opts)
	default:
		err = fmt.Errorf("backfill: unknown family %q", fam)
	}
	return FamilyResult{
		Family:   fam,
		Rows:     rows,
		Duration: time.Since(start),
		Err:      err,
	}
}

// scanLoop runs a generic keyset-cursor scan. Centralised so adding a
// new family is one switch case + a tiny adapter call rather than 30
// lines of boilerplate per type.
//
//   - load(cursor, batch) returns the next page of rows ordered by ID ASC.
//     Empty slice signals end-of-table.
//   - upsert(row) projects one row and returns its ID + any per-row error.
//     Per-row errors are logged WARN and skipped (matches the original
//     CLI behaviour); the scan continues to the next row.
//
// Returns (rowsUpserted, fatalErr). fatalErr is non-nil only on a load
// failure (DB connection lost, etc.) — per-row errors never abort.
func scanLoop(
	ctx context.Context,
	famName string,
	load func(cursor uint) (count int, lastID uint, perRowErr int, fatalErr error),
) (int, error) {
	logging.L().Info("backfill " + famName + " start")
	var cursor uint
	total := 0
	for {
		count, lastID, _, fatalErr := load(cursor)
		if fatalErr != nil {
			return total, fatalErr
		}
		if count == 0 {
			break
		}
		total += count
		cursor = lastID
		logging.L().Info("backfill "+famName+" progress", "scanned_total", total)
	}
	logging.L().Info("backfill "+famName+" done", "total", total)
	return total, nil
}

// applyCompanyFilter is a small ergonomics helper that mutates a query
// only when companyFilter != 0. Saves one if-block per backfill func.
func applyCompanyFilter(q *gorm.DB, companyFilter uint) *gorm.DB {
	if companyFilter != 0 {
		return q.Where("company_id = ?", companyFilter)
	}
	return q
}

// ── Per-family scan funcs ──────────────────────────────────────────────
//
// Each follows the same shape: keyset-cursor over the table ordered by
// ID ASC, project one row at a time, log WARN on per-row failures,
// continue. The shared scanLoop above owns the iteration; these closures
// only own "how to read this entity" + "how to project it".

func backfillCustomers(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "customers", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.Customer{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch), opts.CompanyFilter)
		var rows []models.Customer
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok := 0
		errs := 0
		for _, c := range rows {
			doc := producers.CustomerDocument(c)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("customer upsert failed (continuing)", "id", c.ID, "company_id", c.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = c.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillVendors(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "vendors", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.Vendor{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch), opts.CompanyFilter)
		var rows []models.Vendor
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, v := range rows {
			doc := producers.VendorDocument(v)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("vendor upsert failed (continuing)", "id", v.ID, "company_id", v.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = v.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillProductServices(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "product_services", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.ProductService{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch), opts.CompanyFilter)
		var rows []models.ProductService
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, item := range rows {
			doc := producers.ProductServiceDocument(item)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("product_service upsert failed (continuing)", "id", item.ID, "company_id", item.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = item.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillInvoices(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "invoices", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.Invoice{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.Invoice
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, inv := range rows {
			doc := producers.InvoiceDocument(inv)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("invoice upsert failed (continuing)", "id", inv.ID, "company_id", inv.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = inv.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillBills(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "bills", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.Bill{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.Bill
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, b := range rows {
			doc := producers.BillDocument(b)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("bill upsert failed (continuing)", "id", b.ID, "company_id", b.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = b.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillQuotes(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "quotes", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.Quote{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.Quote
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, qt := range rows {
			doc := producers.QuoteDocument(qt)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("quote upsert failed (continuing)", "id", qt.ID, "company_id", qt.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = qt.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillSalesOrders(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "sales_orders", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.SalesOrder{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.SalesOrder
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, so := range rows {
			doc := producers.SalesOrderDocument(so)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("sales_order upsert failed (continuing)", "id", so.ID, "company_id", so.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = so.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillPurchaseOrders(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "purchase_orders", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.PurchaseOrder{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.PurchaseOrder
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, po := range rows {
			doc := producers.PurchaseOrderDocument(po)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("purchase_order upsert failed (continuing)", "id", po.ID, "company_id", po.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = po.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillCustomerReceipts(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "customer_receipts", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.CustomerReceipt{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.CustomerReceipt
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.CustomerReceiptDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("customer_receipt upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillExpenses(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "expenses", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.Expense{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.Expense
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, e := range rows {
			doc := producers.ExpenseDocument(e)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("expense upsert failed (continuing)", "id", e.ID, "company_id", e.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = e.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillJournalEntries(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "journal_entries", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.JournalEntry{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch), opts.CompanyFilter)
		var rows []models.JournalEntry
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.JournalEntryDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("journal_entries upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillCreditNotes(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "credit_notes", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.CreditNote{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.CreditNote
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.CreditNoteDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("credit_notes upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillVendorCreditNotes(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "vendor_credit_notes", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.VendorCreditNote{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.VendorCreditNote
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.VendorCreditNoteDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("vendor_credit_notes upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillARReturns(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "ar_returns", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.ARReturn{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.ARReturn
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.ARReturnDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("ar_returns upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillVendorReturns(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "vendor_returns", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.VendorReturn{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.VendorReturn
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.VendorReturnDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("vendor_returns upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillARRefunds(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "ar_refunds", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.ARRefund{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.ARRefund
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.ARRefundDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("ar_refunds upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillVendorRefunds(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "vendor_refunds", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.VendorRefund{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.VendorRefund
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.VendorRefundDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("vendor_refunds upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillCustomerDeposits(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "customer_deposits", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.CustomerDeposit{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Customer"), opts.CompanyFilter)
		var rows []models.CustomerDeposit
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.CustomerDepositDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("customer_deposits upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}

func backfillVendorPrepayments(ctx context.Context, db *gorm.DB, p searchprojection.Projector, opts Options) (int, error) {
	return scanLoop(ctx, "vendor_prepayments", func(cursor uint) (int, uint, int, error) {
		q := applyCompanyFilter(db.Model(&models.VendorPrepayment{}).Where("id > ?", cursor).Order("id ASC").Limit(opts.Batch).Preload("Vendor"), opts.CompanyFilter)
		var rows []models.VendorPrepayment
		if err := q.Find(&rows).Error; err != nil {
			return 0, cursor, 0, err
		}
		var lastID uint
		ok, errs := 0, 0
		for _, r := range rows {
			doc := producers.VendorPrepaymentDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("vendor_prepayments upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				errs++
				continue
			}
			lastID = r.ID
			ok++
		}
		return ok, lastID, errs, nil
	})
}
