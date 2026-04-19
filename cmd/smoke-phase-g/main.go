// 遵循project_guide.md
//
// smoke-phase-g — Phase G staging smoke executor.
//
// Runs the three scenarios + two sanity checks from PHASE_G_SMOKE.md
// against a live staging database. Exists because the smoke script
// requires service-layer invocation (ChangeCompanyTrackingCapability,
// ChangeTrackingMode, PostBill, ValidateStockForInvoice), which
// cannot be exercised from SQL alone.
//
// Intended to be run exactly once per staging rollout of Phase G, by
// ops or a release engineer. Not a feature. Not a test harness. This
// binary creates a dedicated isolated company per run (identified by
// a unix-timestamp tag) and cleans up on success by default.
//
// Usage:
//
//   # Uses .env like the main app
//   go run ./cmd/smoke-phase-g
//
//   # Keep entities on success for manual inspection
//   go run ./cmd/smoke-phase-g -keep
//
// Exit codes:
//
//   0  all scenarios passed → Gate #2 green
//   1  any scenario failed  → Gate #2 red; do NOT proceed to Phase H
//   2  infrastructure error (connect / schema / setup) before scenarios ran
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/config"
	"gobooks/internal/db"
	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/services/inventory"
)

type result struct {
	name   string
	passed bool
	detail string
}

type smokeCtx struct {
	db      *gorm.DB
	runTag  string
	actor   string
	actorID *uuid.UUID

	// Entities created during setup. Recorded for verification + cleanup.
	companyAID  uint
	companyS1ID uint

	arID       uint
	apID       uint
	revID      uint
	cogsID     uint
	invAssetID uint
	expenseID  uint

	vendorID      uint
	customerID    uint
	lotItemID     uint
	serialItemID  uint
	s1ItemID      uint

	// Entities created per-scenario. Non-zero only after the scenario runs.
	lotBillID       uint
	serialBillID    uint
	trackedInvID    uint
}

func main() {
	keep := flag.Bool("keep", false, "keep test entities after success (default: cleanup on pass, leave on fail)")
	flag.Parse()

	// Use the same connection path the main app uses so DSN config is
	// single-sourced (no reimplementing env parsing).
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v", err)
		os.Exit(2)
	}
	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Printf("connect: %v", err)
		os.Exit(2)
	}

	ctx := &smokeCtx{
		db:     gormDB,
		runTag: fmt.Sprintf("smoke-phase-g-%d", time.Now().Unix()),
		actor:  "smoke-phase-g",
	}

	fmt.Printf("Run tag: %s\n", ctx.runTag)
	fmt.Printf("Target:  %s:%s db=%s user=%s\n\n", cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser)

	// Schema sanity — fail fast if migrations 061-067 weren't applied.
	if err := ctx.verifySchema(); err != nil {
		log.Printf("schema: %v", err)
		os.Exit(2)
	}

	if err := ctx.setupBaseEntities(); err != nil {
		log.Printf("setup: %v", err)
		log.Printf("run tag %q left in DB for investigation; use cleanup() or drop by company_id", ctx.runTag)
		os.Exit(2)
	}

	results := []result{
		ctx.runScenarioA(),
		ctx.runScenarioB(),
		ctx.runScenarioC(),
		ctx.runSanityS1(),
		ctx.runSanityS2(),
	}

	allPassed := true
	fmt.Println("\n=== RESULTS ===")
	for _, r := range results {
		status := "PASS"
		if !r.passed {
			status = "FAIL"
			allPassed = false
		}
		fmt.Printf("  [%s] %s\n", status, r.name)
		if r.detail != "" {
			fmt.Printf("         %s\n", r.detail)
		}
	}
	fmt.Println()

	if !allPassed {
		fmt.Println("Staging smoke RED — do NOT proceed to Phase H.")
		fmt.Printf("Test entities left in DB under run tag %q. Investigate before cleanup.\n", ctx.runTag)
		fmt.Println("Follow addendum rule 4: any follow-up is a correctness hotfix slice, narrowly scoped.")
		os.Exit(1)
	}

	fmt.Println("Staging smoke GREEN — Gate #2 cleared.")
	if *keep {
		fmt.Printf("Test entities retained under run tag %q (-keep was set).\n", ctx.runTag)
	} else {
		if err := ctx.cleanup(); err != nil {
			// Cleanup failure after green scenarios is not a smoke fail —
			// but should be visible so someone resolves the leftovers.
			fmt.Printf("\nWARNING: cleanup failed: %v\n", err)
			fmt.Printf("Run tag %q left in DB. Manual cleanup needed (see PHASE_G_SMOKE.md §6).\n", ctx.runTag)
		} else {
			fmt.Println("Cleanup complete.")
		}
	}
}

// ── Schema sanity ────────────────────────────────────────────────────────────

func (c *smokeCtx) verifySchema() error {
	// Minimal existence probes on the tables G.1–G.4 added or depend on.
	// A table miss here means migrations 061-067 haven't been applied and
	// every scenario would fail in confusing ways.
	probes := []struct {
		table string
		col   string
	}{
		{"companies", "tracking_enabled"},
		{"product_services", "tracking_mode"},
		{"inventory_lots", "lot_number"},
		{"inventory_serial_units", "serial_number"},
		{"inventory_cost_layers", "is_synthetic"},
		{"inventory_cost_layers", "provenance_type"},
		{"inventory_tracking_consumption", "issue_movement_id"},
		{"inventory_layer_consumption", "layer_id"},
		{"bill_lines", "lot_number"},
		{"bill_lines", "lot_expiry_date"},
	}
	for _, p := range probes {
		var ok bool
		err := c.db.Raw(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = ? AND column_name = ?
		)`, p.table, p.col).Scan(&ok).Error
		if err != nil {
			return fmt.Errorf("probe %s.%s: %w", p.table, p.col, err)
		}
		if !ok {
			return fmt.Errorf("column %s.%s missing — migrations 061-067 not fully applied", p.table, p.col)
		}
	}
	return nil
}

// ── Setup ────────────────────────────────────────────────────────────────────

func (c *smokeCtx) setupBaseEntities() error {
	// Main smoke company (Scenarios A/B/C + Sanity S2).
	coA := models.Company{
		Name:             c.runTag + "-A",
		IsActive:         true,
		BaseCurrencyCode: "CAD",
	}
	if err := c.db.Create(&coA).Error; err != nil {
		return fmt.Errorf("create company A: %w", err)
	}
	c.companyAID = coA.ID

	// Sanity S1 company — tracking_enabled stays default (FALSE).
	coS1 := models.Company{
		Name:             c.runTag + "-S1",
		IsActive:         true,
		BaseCurrencyCode: "CAD",
	}
	if err := c.db.Create(&coS1).Error; err != nil {
		return fmt.Errorf("create company S1: %w", err)
	}
	c.companyS1ID = coS1.ID

	// Accounts for Company A.
	mk := func(code, name string, root models.RootAccountType, detail models.DetailAccountType) (*models.Account, error) {
		a := models.Account{
			CompanyID: c.companyAID, Code: code, Name: name,
			RootAccountType: root, DetailAccountType: detail, IsActive: true,
		}
		if err := c.db.Create(&a).Error; err != nil {
			return nil, err
		}
		return &a, nil
	}
	ar, err := mk("1100", "AR", models.RootAsset, models.DetailAccountsReceivable)
	if err != nil {
		return fmt.Errorf("create AR: %w", err)
	}
	c.arID = ar.ID
	ap, err := mk("2100", "AP", models.RootLiability, models.DetailAccountsPayable)
	if err != nil {
		return fmt.Errorf("create AP: %w", err)
	}
	c.apID = ap.ID
	rev, err := mk("4000", "Revenue", models.RootRevenue, "sales_revenue")
	if err != nil {
		return fmt.Errorf("create revenue: %w", err)
	}
	c.revID = rev.ID
	cogs, err := mk("5000", "COGS", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	if err != nil {
		return fmt.Errorf("create COGS: %w", err)
	}
	c.cogsID = cogs.ID
	invAsset, err := mk("1300", "Inventory", models.RootAsset, models.DetailInventory)
	if err != nil {
		return fmt.Errorf("create inventory asset: %w", err)
	}
	c.invAssetID = invAsset.ID
	expense, err := mk("6000", "Office Supplies", models.RootExpense, "operating_expense")
	if err != nil {
		return fmt.Errorf("create expense: %w", err)
	}
	c.expenseID = expense.ID

	// Accounts for Sanity S1 company (just revenue needed).
	revS1 := models.Account{
		CompanyID: c.companyS1ID, Code: "4000", Name: "Revenue S1",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true,
	}
	if err := c.db.Create(&revS1).Error; err != nil {
		return fmt.Errorf("create S1 revenue: %w", err)
	}

	// Vendor + customer on Company A.
	v := models.Vendor{CompanyID: c.companyAID, Name: c.runTag + "-vendor"}
	if err := c.db.Create(&v).Error; err != nil {
		return fmt.Errorf("create vendor: %w", err)
	}
	c.vendorID = v.ID
	cust := models.Customer{CompanyID: c.companyAID, Name: c.runTag + "-customer", AddrStreet1: "1 Smoke St"}
	if err := c.db.Create(&cust).Error; err != nil {
		return fmt.Errorf("create customer: %w", err)
	}
	c.customerID = cust.ID

	// Stock items on Company A. Tracking flips happen per-scenario.
	mkItem := func(companyID, revAcctID uint, name string) (*models.ProductService, error) {
		item := models.ProductService{
			CompanyID:          companyID,
			Name:               name,
			Type:               models.ProductServiceTypeInventory,
			RevenueAccountID:   revAcctID,
			COGSAccountID:      &c.cogsID,
			InventoryAccountID: &c.invAssetID,
			IsActive:           true,
		}
		// Override COGS/Inventory for non-A items.
		if companyID != c.companyAID {
			item.COGSAccountID = nil
			item.InventoryAccountID = nil
		}
		item.ApplyTypeDefaults()
		if err := c.db.Create(&item).Error; err != nil {
			return nil, err
		}
		return &item, nil
	}
	lotItem, err := mkItem(c.companyAID, c.revID, c.runTag+"-lot-item")
	if err != nil {
		return fmt.Errorf("create lot item: %w", err)
	}
	c.lotItemID = lotItem.ID
	serialItem, err := mkItem(c.companyAID, c.revID, c.runTag+"-serial-item")
	if err != nil {
		return fmt.Errorf("create serial item: %w", err)
	}
	c.serialItemID = serialItem.ID
	s1Item, err := mkItem(c.companyS1ID, revS1.ID, c.runTag+"-s1-item")
	if err != nil {
		return fmt.Errorf("create S1 item: %w", err)
	}
	c.s1ItemID = s1Item.ID

	return nil
}

// ── Scenario A: lot-tracked Bill inbound (POSITIVE) ──────────────────────────

func (c *smokeCtx) runScenarioA() result {
	r := result{name: "Scenario A — lot-tracked Bill inbound"}

	// A.2: enable capability (service, with audit).
	if err := services.ChangeCompanyTrackingCapability(c.db, services.ChangeCompanyTrackingCapabilityInput{
		CompanyID: c.companyAID, Enabled: true, Actor: c.actor,
	}); err != nil {
		r.detail = "A.2 enable capability: " + err.Error()
		return r
	}

	// A.4: flip lot item to 'lot'.
	if err := services.ChangeTrackingMode(c.db, services.ChangeTrackingModeInput{
		CompanyID: c.companyAID, ItemID: c.lotItemID, NewMode: models.TrackingLot, Actor: c.actor,
	}); err != nil {
		r.detail = "A.4 flip lot mode: " + err.Error()
		return r
	}

	// A.6: create bill with lot line.
	expiry := time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)
	bill := models.Bill{
		CompanyID:  c.companyAID,
		VendorID:   c.vendorID,
		BillNumber: c.runTag + "-A",
		BillDate:   time.Now(),
		Status:     models.BillStatusDraft,
		Amount:     decimal.NewFromInt(50),
	}
	if err := c.db.Create(&bill).Error; err != nil {
		r.detail = "A.6 create bill: " + err.Error()
		return r
	}
	c.lotBillID = bill.ID
	line := models.BillLine{
		CompanyID: c.companyAID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &c.lotItemID,
		Description:      "smoke lot item",
		Qty:              decimal.NewFromInt(10),
		UnitPrice:        decimal.NewFromInt(5),
		ExpenseAccountID: &c.expenseID,
		LineNet:          decimal.NewFromInt(50),
		LineTotal:        decimal.NewFromInt(50),
		LotNumber:        c.runTag + "-LOT-A",
		LotExpiryDate:    &expiry,
	}
	if err := c.db.Create(&line).Error; err != nil {
		r.detail = "A.6 create bill line: " + err.Error()
		return r
	}

	// A.7: post bill.
	if err := services.PostBill(c.db, c.companyAID, bill.ID, c.actor, c.actorID); err != nil {
		r.detail = "A.7 PostBill: " + err.Error()
		return r
	}

	// Verification.
	if err := c.expect(
		"A.v1 bill posted",
		func() error {
			var status string
			if err := c.db.Model(&models.Bill{}).Select("status").Where("id = ?", bill.ID).Scan(&status).Error; err != nil {
				return err
			}
			if status != string(models.BillStatusPosted) {
				return fmt.Errorf("bill status=%q, want 'posted'", status)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"A.v2 inventory movement",
		func() error {
			var movs []models.InventoryMovement
			if err := c.db.Where("company_id = ? AND item_id = ?", c.companyAID, c.lotItemID).Find(&movs).Error; err != nil {
				return err
			}
			if len(movs) != 1 {
				return fmt.Errorf("movements=%d, want 1", len(movs))
			}
			m := movs[0]
			if !m.QuantityDelta.Equal(decimal.NewFromInt(10)) {
				return fmt.Errorf("quantity_delta=%s, want 10", m.QuantityDelta)
			}
			if m.UnitCostBase == nil || !m.UnitCostBase.Equal(decimal.NewFromInt(5)) {
				return fmt.Errorf("unit_cost_base=%v, want 5", m.UnitCostBase)
			}
			if m.SourceType != "bill" {
				return fmt.Errorf("source_type=%q, want 'bill'", m.SourceType)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"A.v3 inventory balance",
		func() error {
			var bal models.InventoryBalance
			if err := c.db.Where("company_id = ? AND item_id = ?", c.companyAID, c.lotItemID).First(&bal).Error; err != nil {
				return err
			}
			if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
				return fmt.Errorf("on-hand=%s, want 10", bal.QuantityOnHand)
			}
			if !bal.AverageCost.Equal(decimal.NewFromInt(5)) {
				return fmt.Errorf("avg_cost=%s, want 5", bal.AverageCost)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"A.v4 lot materialised",
		func() error {
			var lots []models.InventoryLot
			if err := c.db.Where("company_id = ? AND item_id = ?", c.companyAID, c.lotItemID).Find(&lots).Error; err != nil {
				return err
			}
			if len(lots) != 1 {
				return fmt.Errorf("lots=%d, want 1", len(lots))
			}
			l := lots[0]
			if l.LotNumber != c.runTag+"-LOT-A" {
				return fmt.Errorf("lot_number=%q", l.LotNumber)
			}
			if !l.OriginalQuantity.Equal(decimal.NewFromInt(10)) || !l.RemainingQuantity.Equal(decimal.NewFromInt(10)) {
				return fmt.Errorf("lot qtys orig=%s rem=%s, want 10/10", l.OriginalQuantity, l.RemainingQuantity)
			}
			if l.ExpiryDate == nil || !l.ExpiryDate.Equal(expiry) {
				return fmt.Errorf("expiry=%v, want %v", l.ExpiryDate, expiry)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"A.v5 FIFO cost layer",
		func() error {
			var layers []models.InventoryCostLayer
			if err := c.db.Where("company_id = ? AND item_id = ?", c.companyAID, c.lotItemID).Find(&layers).Error; err != nil {
				return err
			}
			if len(layers) != 1 {
				return fmt.Errorf("layers=%d, want 1", len(layers))
			}
			l := layers[0]
			if !l.OriginalQuantity.Equal(decimal.NewFromInt(10)) || !l.RemainingQuantity.Equal(decimal.NewFromInt(10)) {
				return fmt.Errorf("layer qtys orig=%s rem=%s, want 10/10", l.OriginalQuantity, l.RemainingQuantity)
			}
			if l.ProvenanceType != models.ProvenanceReceipt {
				return fmt.Errorf("provenance_type=%q, want %q", l.ProvenanceType, models.ProvenanceReceipt)
			}
			if l.IsSynthetic {
				return fmt.Errorf("is_synthetic=true on a real receipt — provenance regression")
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	r.passed = true
	return r
}

// ── Scenario B: serial-via-bill must reject (NEGATIVE GUARD) ─────────────────

func (c *smokeCtx) runScenarioB() result {
	r := result{name: "Scenario B — serial-via-bill rejected"}

	// Flip serial item to 'serial'. Gate was opened in A.
	if err := services.ChangeTrackingMode(c.db, services.ChangeTrackingModeInput{
		CompanyID: c.companyAID, ItemID: c.serialItemID, NewMode: models.TrackingSerial, Actor: c.actor,
	}); err != nil {
		r.detail = "B flip serial mode: " + err.Error()
		return r
	}

	bill := models.Bill{
		CompanyID: c.companyAID, VendorID: c.vendorID,
		BillNumber: c.runTag + "-B", BillDate: time.Now(),
		Status: models.BillStatusDraft, Amount: decimal.NewFromInt(100),
	}
	if err := c.db.Create(&bill).Error; err != nil {
		r.detail = "B create bill: " + err.Error()
		return r
	}
	c.serialBillID = bill.ID
	line := models.BillLine{
		CompanyID: c.companyAID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &c.serialItemID,
		Description:      "smoke serial item (no serials supplied)",
		Qty:              decimal.NewFromInt(1),
		UnitPrice:        decimal.NewFromInt(100),
		ExpenseAccountID: &c.expenseID,
		LineNet:          decimal.NewFromInt(100),
		LineTotal:        decimal.NewFromInt(100),
		// No lot/serial — the point of the guard.
	}
	if err := c.db.Create(&line).Error; err != nil {
		r.detail = "B create bill line: " + err.Error()
		return r
	}

	postErr := services.PostBill(c.db, c.companyAID, bill.ID, c.actor, c.actorID)
	if postErr == nil {
		r.detail = "B PostBill returned nil — guard regressed"
		return r
	}
	if !errors.Is(postErr, inventory.ErrTrackingDataMissing) {
		r.detail = fmt.Sprintf("B PostBill error=%v, want wrap of ErrTrackingDataMissing", postErr)
		return r
	}

	if err := c.expect(
		"B.v1 bill stays draft",
		func() error {
			var status string
			if err := c.db.Model(&models.Bill{}).Select("status").Where("id = ?", bill.ID).Scan(&status).Error; err != nil {
				return err
			}
			if status != string(models.BillStatusDraft) {
				return fmt.Errorf("status=%q, want 'draft'", status)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"B.v2 no movement leaked",
		func() error {
			var n int64
			if err := c.db.Model(&models.InventoryMovement{}).
				Where("company_id = ? AND item_id = ?", c.companyAID, c.serialItemID).
				Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("movements=%d, want 0 (tx rollback failed)", n)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"B.v3 no serial unit leaked",
		func() error {
			var n int64
			if err := c.db.Model(&models.InventorySerialUnit{}).
				Where("company_id = ? AND item_id = ?", c.companyAID, c.serialItemID).
				Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("serial_units=%d, want 0", n)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	r.passed = true
	return r
}

// ── Scenario C: tracked-invoice preview must reject (NEGATIVE GUARD) ─────────

func (c *smokeCtx) runScenarioC() result {
	r := result{name: "Scenario C — tracked-invoice preview rejected"}

	// Reuse lot item from A (still tracked, on-hand = 10).
	inv := models.Invoice{
		CompanyID: c.companyAID, InvoiceNumber: c.runTag + "-C",
		CustomerID: c.customerID, InvoiceDate: time.Now(),
		Status:               models.InvoiceStatusDraft,
		Amount:               decimal.NewFromInt(50),
		BalanceDue:           decimal.NewFromInt(50),
		CustomerNameSnapshot: "smoke customer",
	}
	if err := c.db.Create(&inv).Error; err != nil {
		r.detail = "C create invoice: " + err.Error()
		return r
	}
	c.trackedInvID = inv.ID
	line := models.InvoiceLine{
		CompanyID: c.companyAID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &c.lotItemID,
		Description:      "smoke tracked invoice line",
		Qty:              decimal.NewFromInt(1),
		UnitPrice:        decimal.NewFromInt(50),
		LineNet:          decimal.NewFromInt(50),
		LineTotal:        decimal.NewFromInt(50),
	}
	if err := c.db.Create(&line).Error; err != nil {
		r.detail = "C create invoice line: " + err.Error()
		return r
	}

	// Preview: load item and reuse the line shape the preview expects.
	var item models.ProductService
	if err := c.db.First(&item, c.lotItemID).Error; err != nil {
		r.detail = "C reload item: " + err.Error()
		return r
	}
	lineCopy := line
	lineCopy.ProductService = &item

	_, _, previewErr := services.ValidateStockForInvoice(c.db, c.companyAID, []models.InvoiceLine{lineCopy}, nil)
	if previewErr == nil {
		r.detail = "C ValidateStockForInvoice returned nil — G.2 guard dead"
		return r
	}
	if !errors.Is(previewErr, services.ErrTrackedItemNotSupportedByInvoice) {
		r.detail = fmt.Sprintf("C preview error=%v, want ErrTrackedItemNotSupportedByInvoice", previewErr)
		return r
	}

	// PostInvoice should also fail.
	postErr := services.PostInvoice(c.db, c.companyAID, inv.ID, c.actor, c.actorID)
	if postErr == nil {
		r.detail = "C PostInvoice returned nil — guard regressed"
		return r
	}
	if !errors.Is(postErr, services.ErrTrackedItemNotSupportedByInvoice) {
		r.detail = fmt.Sprintf("C PostInvoice error=%v, want ErrTrackedItemNotSupportedByInvoice", postErr)
		return r
	}

	if err := c.expect(
		"C.v1 invoice not posted",
		func() error {
			var status string
			if err := c.db.Model(&models.Invoice{}).Select("status").Where("id = ?", inv.ID).Scan(&status).Error; err != nil {
				return err
			}
			if status == string(models.InvoiceStatusIssued) {
				return fmt.Errorf("invoice status=issued — post slipped through")
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"C.v2 no sale movement",
		func() error {
			var n int64
			if err := c.db.Model(&models.InventoryMovement{}).
				Where("company_id = ? AND item_id = ? AND source_type = ?", c.companyAID, c.lotItemID, "invoice").
				Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("sale movements=%d, want 0", n)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"C.v3 no JE for invoice",
		func() error {
			var n int64
			if err := c.db.Model(&models.JournalEntry{}).
				Where("company_id = ? AND source_type = ? AND source_id = ?",
					c.companyAID, models.LedgerSourceInvoice, inv.ID).
				Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("JE=%d, want 0", n)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	if err := c.expect(
		"C.v4 lot remaining unchanged",
		func() error {
			var lot models.InventoryLot
			if err := c.db.Where("company_id = ? AND item_id = ?", c.companyAID, c.lotItemID).First(&lot).Error; err != nil {
				return err
			}
			if !lot.RemainingQuantity.Equal(decimal.NewFromInt(10)) {
				return fmt.Errorf("lot remaining=%s, want 10 (partial draw leaked)", lot.RemainingQuantity)
			}
			return nil
		},
	); err != nil {
		r.detail = err.Error()
		return r
	}

	r.passed = true
	return r
}

// ── Sanity S1: tracking_enabled=false blocks mode change ─────────────────────

func (c *smokeCtx) runSanityS1() result {
	r := result{name: "Sanity S1 — gate OFF blocks mode change"}

	err := services.ChangeTrackingMode(c.db, services.ChangeTrackingModeInput{
		CompanyID: c.companyS1ID, ItemID: c.s1ItemID, NewMode: models.TrackingLot, Actor: c.actor,
	})
	if err == nil {
		r.detail = "ChangeTrackingMode returned nil on gate-off company"
		return r
	}
	if !errors.Is(err, services.ErrTrackingCapabilityNotEnabled) {
		r.detail = fmt.Sprintf("S1 got %v, want ErrTrackingCapabilityNotEnabled", err)
		return r
	}

	// Confirm item stayed untouched.
	var mode string
	if err := c.db.Model(&models.ProductService{}).Select("tracking_mode").
		Where("id = ?", c.s1ItemID).Scan(&mode).Error; err != nil {
		r.detail = "S1 reload mode: " + err.Error()
		return r
	}
	if mode != models.TrackingNone {
		r.detail = fmt.Sprintf("S1 tracking_mode=%q, want 'none'", mode)
		return r
	}

	r.passed = true
	return r
}

// ── Sanity S2: disable blocked by tracked items ──────────────────────────────

func (c *smokeCtx) runSanityS2() result {
	r := result{name: "Sanity S2 — disable blocked while tracked items exist"}

	err := services.ChangeCompanyTrackingCapability(c.db, services.ChangeCompanyTrackingCapabilityInput{
		CompanyID: c.companyAID, Enabled: false, Actor: c.actor,
	})
	if err == nil {
		r.detail = "disable returned nil while tracked items exist"
		return r
	}
	if !errors.Is(err, services.ErrTrackingCapabilityHasTrackedItems) {
		r.detail = fmt.Sprintf("S2 got %v, want ErrTrackingCapabilityHasTrackedItems", err)
		return r
	}

	var enabled bool
	if err := c.db.Model(&models.Company{}).Select("tracking_enabled").
		Where("id = ?", c.companyAID).Scan(&enabled).Error; err != nil {
		r.detail = "S2 reload company: " + err.Error()
		return r
	}
	if !enabled {
		r.detail = "S2 company tracking_enabled=false after blocked disable — state mutated"
		return r
	}

	r.passed = true
	return r
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// expect runs a check and wraps the first failure in an annotated error.
// Keeps the per-scenario code linear without one-off if/return noise.
func (c *smokeCtx) expect(label string, check func() error) error {
	if err := check(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// ── Cleanup ──────────────────────────────────────────────────────────────────

func (c *smokeCtx) cleanup() error {
	// Order matters — child tables first, FK-respecting order.
	cids := []uint{c.companyAID, c.companyS1ID}
	for _, cid := range cids {
		if cid == 0 {
			continue
		}
		stmts := []string{
			"DELETE FROM inventory_tracking_consumption WHERE company_id = ?",
			"DELETE FROM inventory_layer_consumption    WHERE company_id = ?",
			"DELETE FROM inventory_cost_layers          WHERE company_id = ?",
			"DELETE FROM inventory_serial_units         WHERE company_id = ?",
			"DELETE FROM inventory_lots                 WHERE company_id = ?",
			"DELETE FROM inventory_movements            WHERE company_id = ?",
			"DELETE FROM inventory_balances             WHERE company_id = ?",
			"DELETE FROM bill_lines                     WHERE company_id = ?",
			"DELETE FROM bills                          WHERE company_id = ?",
			"DELETE FROM invoice_lines                  WHERE company_id = ?",
			"DELETE FROM invoices                       WHERE company_id = ?",
			"DELETE FROM journal_lines                  WHERE company_id = ?",
			"DELETE FROM journal_entries                WHERE company_id = ?",
			"DELETE FROM audit_logs                     WHERE company_id = ?",
			"DELETE FROM product_services               WHERE company_id = ?",
			"DELETE FROM accounts                       WHERE company_id = ?",
			"DELETE FROM vendors                        WHERE company_id = ?",
			"DELETE FROM customers                      WHERE company_id = ?",
			"DELETE FROM companies                      WHERE id = ?",
		}
		for _, s := range stmts {
			if err := c.db.Exec(s, cid).Error; err != nil {
				return fmt.Errorf("cleanup %s (company %d): %w", s, cid, err)
			}
		}
	}
	return nil
}
