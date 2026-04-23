// 遵循project_guide.md
//
// search-backfill — rebuilds the search_documents projection from the
// canonical business tables. Run this:
//
//   - Once after deploying Phase 1 to populate the projection for all
//     existing customers + vendors (handlers only cover rows touched
//     after deploy).
//   - Any time projection logic (producers/*) changes in a way that
//     rewrites existing rows — bumping searchprojection.CurrentProjectorVersion
//     makes this explicit.
//   - To recover from a projection drift incident (rare — projector
//     failures are logged by the handlers).
//
// The tool is idempotent and safe to re-run. It upserts one row per
// entity without interleaving reads/writes that could race with live
// traffic; running against a production DB is supported but expect
// table scans on customers + vendors.
//
// Usage:
//
//	go run ./cmd/search-backfill                        # all entity families
//	go run ./cmd/search-backfill -only customer         # only customers
//	go run ./cmd/search-backfill -only vendor           # only vendors
//	go run ./cmd/search-backfill -only product_service  # only products
//	go run ./cmd/search-backfill -only invoice          # only invoices
//	go run ./cmd/search-backfill -only bill             # only bills
//	go run ./cmd/search-backfill -only quote            # only quotes
//	go run ./cmd/search-backfill -only sales_order      # only sales orders
//	go run ./cmd/search-backfill -only purchase_order   # only purchase orders
//	go run ./cmd/search-backfill -only customer_receipt # only payments
//	go run ./cmd/search-backfill -only expense          # only expenses
//	go run ./cmd/search-backfill -dry                   # log progress, skip upserts
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"gorm.io/gorm"

	"gobooks/internal/config"
	"gobooks/internal/db"
	"gobooks/internal/logging"
	"gobooks/internal/models"
	"gobooks/internal/searchprojection"
	"gobooks/internal/searchprojection/producers"
)

func main() {
	only := flag.String("only", "all", "restrict to one family: all | customer | vendor | product_service | invoice | bill | quote | sales_order | purchase_order | customer_receipt | expense")
	dry := flag.Bool("dry", false, "scan + log counts but skip projection upserts")
	companyFilter := flag.Uint("company", 0, "limit to a single company_id (0 = all companies)")
	batchSize := flag.Int("batch", 500, "rows per batch; lower = gentler on the pool")
	flag.Parse()

	logging.Init()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}
	logging.SetLevel(cfg.LogLevel)

	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}

	var projector searchprojection.Projector
	if *dry {
		projector = searchprojection.NoopProjector{}
	} else {
		client, err := searchprojection.OpenEntFromGorm(gormDB)
		if err != nil {
			log.Fatalf("ent client init failed: %v", err)
		}
		p, err := searchprojection.NewEntProjector(client, searchprojection.AsciiNormalizer{})
		if err != nil {
			log.Fatalf("projector init failed: %v", err)
		}
		projector = p
	}

	ctx := context.Background()
	start := time.Now()

	if *only == "all" || *only == "customer" {
		if err := backfillCustomers(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("customer backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "vendor" {
		if err := backfillVendors(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("vendor backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "product_service" {
		if err := backfillProductServices(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("product_service backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "invoice" {
		if err := backfillInvoices(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("invoice backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "bill" {
		if err := backfillBills(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("bill backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "quote" {
		if err := backfillQuotes(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("quote backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "sales_order" {
		if err := backfillSalesOrders(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("sales_order backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "purchase_order" {
		if err := backfillPurchaseOrders(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("purchase_order backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "customer_receipt" {
		if err := backfillCustomerReceipts(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("customer_receipt backfill failed: %v", err)
		}
	}
	if *only == "all" || *only == "expense" {
		if err := backfillExpenses(ctx, gormDB, projector, *companyFilter, *batchSize); err != nil {
			log.Fatalf("expense backfill failed: %v", err)
		}
	}

	logging.L().Info("search-backfill complete", "elapsed_ms", time.Since(start).Milliseconds(), "dry", *dry)
}

// backfillCustomers scans the customers table in batches and upserts
// each row's projection. Uses a keyset-style ID cursor so the scan is
// order-stable even if rows are written concurrently by handler traffic.
func backfillCustomers(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill customers start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.Customer{}).Where("id > ?", cursor).Order("id ASC").Limit(batch)
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.Customer
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, c := range rows {
			doc := producers.CustomerDocument(c)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("customer upsert failed (continuing)", "id", c.ID, "company_id", c.CompanyID, "err", err)
				continue
			}
			cursor = c.ID
			total++
		}
		logging.L().Info("backfill customers progress", "scanned_total", total)
	}
	logging.L().Info("backfill customers done", "total", total)
	return nil
}

func backfillVendors(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill vendors start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.Vendor{}).Where("id > ?", cursor).Order("id ASC").Limit(batch)
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.Vendor
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, v := range rows {
			doc := producers.VendorDocument(v)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("vendor upsert failed (continuing)", "id", v.ID, "company_id", v.CompanyID, "err", err)
				continue
			}
			cursor = v.ID
			total++
		}
		logging.L().Info("backfill vendors progress", "scanned_total", total)
	}
	logging.L().Info("backfill vendors done", "total", total)
	return nil
}

func backfillProductServices(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill product_services start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.ProductService{}).Where("id > ?", cursor).Order("id ASC").Limit(batch)
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.ProductService
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, item := range rows {
			doc := producers.ProductServiceDocument(item)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("product_service upsert failed (continuing)", "id", item.ID, "company_id", item.CompanyID, "err", err)
				continue
			}
			cursor = item.ID
			total++
		}
		logging.L().Info("backfill product_services progress", "scanned_total", total)
	}
	logging.L().Info("backfill product_services done", "total", total)
	return nil
}

// ── Transaction backfills (Phase 3) ────────────────────────────────────
//
// All six follow the identical keyset-cursor pattern as customers / vendors
// above. Preload the counterparty (Customer or Vendor) so the producer's
// Document mapper finds the name in-place.

func backfillInvoices(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill invoices start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.Invoice{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Customer")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.Invoice
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, inv := range rows {
			doc := producers.InvoiceDocument(inv)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("invoice upsert failed (continuing)", "id", inv.ID, "company_id", inv.CompanyID, "err", err)
				continue
			}
			cursor = inv.ID
			total++
		}
		logging.L().Info("backfill invoices progress", "scanned_total", total)
	}
	logging.L().Info("backfill invoices done", "total", total)
	return nil
}

func backfillBills(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill bills start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.Bill{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Vendor")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.Bill
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, b := range rows {
			doc := producers.BillDocument(b)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("bill upsert failed (continuing)", "id", b.ID, "company_id", b.CompanyID, "err", err)
				continue
			}
			cursor = b.ID
			total++
		}
		logging.L().Info("backfill bills progress", "scanned_total", total)
	}
	logging.L().Info("backfill bills done", "total", total)
	return nil
}

func backfillQuotes(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill quotes start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.Quote{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Customer")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.Quote
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, qt := range rows {
			doc := producers.QuoteDocument(qt)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("quote upsert failed (continuing)", "id", qt.ID, "company_id", qt.CompanyID, "err", err)
				continue
			}
			cursor = qt.ID
			total++
		}
		logging.L().Info("backfill quotes progress", "scanned_total", total)
	}
	logging.L().Info("backfill quotes done", "total", total)
	return nil
}

func backfillSalesOrders(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill sales_orders start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.SalesOrder{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Customer")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.SalesOrder
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, so := range rows {
			doc := producers.SalesOrderDocument(so)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("sales_order upsert failed (continuing)", "id", so.ID, "company_id", so.CompanyID, "err", err)
				continue
			}
			cursor = so.ID
			total++
		}
		logging.L().Info("backfill sales_orders progress", "scanned_total", total)
	}
	logging.L().Info("backfill sales_orders done", "total", total)
	return nil
}

func backfillPurchaseOrders(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill purchase_orders start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.PurchaseOrder{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Vendor")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.PurchaseOrder
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, po := range rows {
			doc := producers.PurchaseOrderDocument(po)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("purchase_order upsert failed (continuing)", "id", po.ID, "company_id", po.CompanyID, "err", err)
				continue
			}
			cursor = po.ID
			total++
		}
		logging.L().Info("backfill purchase_orders progress", "scanned_total", total)
	}
	logging.L().Info("backfill purchase_orders done", "total", total)
	return nil
}

func backfillCustomerReceipts(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill customer_receipts start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.CustomerReceipt{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Customer")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.CustomerReceipt
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			doc := producers.CustomerReceiptDocument(r)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("customer_receipt upsert failed (continuing)", "id", r.ID, "company_id", r.CompanyID, "err", err)
				continue
			}
			cursor = r.ID
			total++
		}
		logging.L().Info("backfill customer_receipts progress", "scanned_total", total)
	}
	logging.L().Info("backfill customer_receipts done", "total", total)
	return nil
}

func backfillExpenses(ctx context.Context, db *gorm.DB, p searchprojection.Projector, companyFilter uint, batch int) error {
	logging.L().Info("backfill expenses start")
	var cursor uint
	total := 0
	for {
		q := db.Model(&models.Expense{}).Where("id > ?", cursor).Order("id ASC").Limit(batch).Preload("Vendor")
		if companyFilter != 0 {
			q = q.Where("company_id = ?", companyFilter)
		}
		var rows []models.Expense
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, e := range rows {
			doc := producers.ExpenseDocument(e)
			if err := p.Upsert(ctx, doc.CompanyID, doc); err != nil {
				logging.L().Warn("expense upsert failed (continuing)", "id", e.ID, "company_id", e.CompanyID, "err", err)
				continue
			}
			cursor = e.ID
			total++
		}
		logging.L().Info("backfill expenses progress", "scanned_total", total)
	}
	logging.L().Info("backfill expenses done", "total", total)
	return nil
}
