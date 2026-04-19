package web

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

func reportCacheLifecycleApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	membership := &models.CompanyMembership{UserID: user.ID, CompanyID: companyID, Role: "owner", IsActive: true}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/invoices/:id/post", server.handleInvoicePost)
	app.Post("/invoices/:id/void", server.handleInvoiceVoid)
	app.Post("/invoices/:id/receive-payment", server.handleInvoiceReceivePaymentSubmit)
	app.Post("/bills/:id/post", server.handleBillPost)
	app.Post("/bills/:id/void", server.handleBillVoid)
	app.Post("/banking/receive-payment", server.handleReceivePaymentSubmit)
	app.Post("/banking/pay-bills", server.handlePayBillsSubmit)
	app.Post("/journal-entry", server.handleJournalEntryPost)
	return app
}

func primeReportCacheForCompany(server *Server, companyID uint) {
	server.ReportCache.plCache.Set(
		fmt.Sprintf("rpt:c%d|pl|2026-04-01|2026-04-30", companyID),
		services.IncomeStatement{},
	)
	server.ReportCache.arCache.Set(
		fmt.Sprintf("rpt:c%d|ar|2026-04-30", companyID),
		services.ARAgingReport{},
	)
}

func seedReportCacheDraftInvoice(t *testing.T, db *gorm.DB, companyID uint, invoiceNumber string) uint {
	t.Helper()

	customerID := seedValidationCustomer(t, db, companyID, "Report Invoice Customer")
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	productID := seedValidationProduct(t, db, companyID, revenueID, "Report Service")

	invoice := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         invoiceNumber,
		CustomerID:            customerID,
		InvoiceDate:           time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Amount:                decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "Report Invoice Customer",
		CustomerEmailSnapshot: "invoice@example.com",
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}

	productIDCopy := productID
	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        invoice.ID,
		ProductServiceID: &productIDCopy,
		Description:      "Lifecycle service",
		Qty:              decimal.NewFromInt(1),
		UnitPrice:        decimal.RequireFromString("100.00"),
		LineNet:          decimal.RequireFromString("100.00"),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.RequireFromString("100.00"),
		SortOrder:        1,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	return invoice.ID
}

func seedReportCacheDraftBill(t *testing.T, db *gorm.DB, companyID uint, billNumber string) uint {
	t.Helper()

	vendorID := seedEditorFlowVendor(t, db, companyID, "Report Vendor")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	_ = seedValidationAccount(t, db, companyID, "2000", models.RootLiability, models.DetailAccountsPayable)

	bill := models.Bill{
		CompanyID:      companyID,
		BillNumber:     billNumber,
		VendorID:       vendorID,
		BillDate:       time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Status:         models.BillStatusDraft,
		Amount:         decimal.RequireFromString("80.00"),
		Subtotal:       decimal.RequireFromString("80.00"),
		TaxTotal:       decimal.Zero,
		BalanceDue:     decimal.RequireFromString("80.00"),
		BalanceDueBase: decimal.RequireFromString("80.00"),
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	expenseAccountIDCopy := expenseAccountID
	line := models.BillLine{
		CompanyID:        companyID,
		BillID:           bill.ID,
		ExpenseAccountID: &expenseAccountIDCopy,
		Description:      "Lifecycle bill line",
		Qty:              decimal.NewFromInt(1),
		UnitPrice:        decimal.RequireFromString("80.00"),
		LineNet:          decimal.RequireFromString("80.00"),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.RequireFromString("80.00"),
		SortOrder:        1,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	return bill.ID
}

func seedReportCacheOpenInvoice(t *testing.T, db *gorm.DB, companyID uint, invoiceNumber string) *models.Invoice {
	t.Helper()

	customerID := seedValidationCustomer(t, db, companyID, "Report Payment Customer")
	invoice := &models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         invoiceNumber,
		CustomerID:            customerID,
		InvoiceDate:           time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		DueDate:               ptrTime(time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("100.00"),
		AmountBase:            decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "Report Payment Customer",
		CustomerEmailSnapshot: "payment@example.com",
	}
	if err := db.Create(invoice).Error; err != nil {
		t.Fatal(err)
	}
	return invoice
}

func seedReportCacheOpenBill(t *testing.T, db *gorm.DB, companyID uint, billNumber string) *models.Bill {
	t.Helper()

	vendorID := seedEditorFlowVendor(t, db, companyID, "Report Payment Vendor")
	bill := &models.Bill{
		CompanyID:      companyID,
		BillNumber:     billNumber,
		VendorID:       vendorID,
		BillDate:       time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		DueDate:        ptrTime(time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)),
		Status:         models.BillStatusPosted,
		Amount:         decimal.RequireFromString("80.00"),
		AmountBase:     decimal.RequireFromString("80.00"),
		Subtotal:       decimal.RequireFromString("80.00"),
		TaxTotal:       decimal.Zero,
		BalanceDue:     decimal.RequireFromString("80.00"),
		BalanceDueBase: decimal.RequireFromString("80.00"),
	}
	if err := db.Create(bill).Error; err != nil {
		t.Fatal(err)
	}
	return bill
}

func assertARAgingRecomputed(t *testing.T, server *Server, companyID uint) {
	t.Helper()

	computeCalls := 0
	_, source, err := server.ReportCache.GetARAgingReport(
		companyID,
		time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		func() (services.ARAgingReport, error) {
			computeCalls++
			return services.ARAgingReport{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if source != "recomputed" {
		t.Fatalf("expected recomputed AR Aging after invalidation, got %q", source)
	}
	if computeCalls != 1 {
		t.Fatalf("expected AR Aging compute to run once after invalidation, got %d", computeCalls)
	}
}

func assertIncomeStatementRecomputed(t *testing.T, server *Server, companyID uint) {
	t.Helper()

	computeCalls := 0
	_, source, err := server.ReportCache.GetIncomeStatement(
		companyID,
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		func() (services.IncomeStatement, error) {
			computeCalls++
			return services.IncomeStatement{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if source != "recomputed" {
		t.Fatalf("expected recomputed income statement after invalidation, got %q", source)
	}
	if computeCalls != 1 {
		t.Fatalf("expected income statement compute to run once after invalidation, got %d", computeCalls)
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func TestReportCacheInvalidatedAfterInvoicePostAndVoid(t *testing.T) {
	db := testEditorFlowDB(t)
	// VoidInvoice walks payment transactions, settlement allocations,
	// inventory ledger, and credit note applications — all of which must
	// exist or the void returns an error and the handler redirects to
	// ?voiderror=1 without invalidating the report cache.
	if err := db.AutoMigrate(
		&models.PaymentTransaction{},
		&models.SettlementAllocation{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.CreditNoteApplication{},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Report Invoice Cache Co")
	invoiceID := seedReportCacheDraftInvoice(t, db, companyID, "INV-RPT-001")
	app := reportCacheLifecycleApp(server, user, companyID)

	primeReportCacheForCompany(server, companyID)
	postResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/invoices/%d/post", invoiceID), url.Values{}, "")
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for invoice post, got %d", postResp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after invoice post, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}

	primeReportCacheForCompany(server, companyID)
	voidResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/invoices/%d/void", invoiceID), url.Values{}, "")
	if voidResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for invoice void, got %d", voidResp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after invoice void, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}
}

func TestReportCacheInvalidatedAfterBillPostAndVoid(t *testing.T) {
	db := testEditorFlowDB(t)
	// VoidBill walks settlement allocations, inventory ledger, and AP
	// credit applications — missing any of those tables fails the void
	// and the handler redirects to ?voiderror=1 without invalidating the
	// report cache.
	if err := db.AutoMigrate(
		&models.SettlementAllocation{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.APCreditApplication{},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Report Bill Cache Co")
	billID := seedReportCacheDraftBill(t, db, companyID, "BILL-RPT-001")
	app := reportCacheLifecycleApp(server, user, companyID)

	primeReportCacheForCompany(server, companyID)
	postResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/bills/%d/post", billID), url.Values{}, "")
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for bill post, got %d", postResp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after bill post, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}

	primeReportCacheForCompany(server, companyID)
	voidResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/bills/%d/void", billID), url.Values{}, "")
	if voidResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for bill void, got %d", voidResp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after bill void, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}
}

func TestReportCacheInvalidatedAfterJournalEntryPost(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Report Journal Cache Co")
	debitAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	creditAccountID := seedValidationAccount(t, db, companyID, "3100", models.RootEquity, models.DetailShareCapital)
	app := reportCacheLifecycleApp(server, user, companyID)

	primeReportCacheForCompany(server, companyID)
	form := url.Values{
		"entry_date":           {"2026-04-10"},
		"journal_no":           {"JE-RPT-001"},
		"lines[0][account_id]": {fmt.Sprintf("%d", debitAccountID)},
		"lines[0][debit]":      {"25.00"},
		"lines[0][credit]":     {"0.00"},
		"lines[0][memo]":       {"Lifecycle debit"},
		"lines[0][party]":      {""},
		"lines[1][account_id]": {fmt.Sprintf("%d", creditAccountID)},
		"lines[1][debit]":      {"0.00"},
		"lines[1][credit]":     {"25.00"},
		"lines[1][memo]":       {"Lifecycle credit"},
		"lines[1][party]":      {""},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/journal-entry", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for journal entry post, got %d", resp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after journal entry post, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}
}

func TestReportCacheInvalidatedAfterReceivePayment(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(&models.PaymentReceipt{}, &models.SettlementAllocation{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Report Receive Payment Cache Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	invoice := seedReportCacheOpenInvoice(t, db, companyID, "INV-RPT-PAY-001")
	app := reportCacheLifecycleApp(server, user, companyID)

	primeReportCacheForCompany(server, companyID)
	form := url.Values{
		"customer_id":     {fmt.Sprintf("%d", invoice.CustomerID)},
		"payment_method":  {string(models.PaymentMethodCheck)},
		"entry_date":      {"2026-04-30"},
		"bank_account_id": {fmt.Sprintf("%d", bankAccountID)},
		"invoice_id":      {fmt.Sprintf("%d", invoice.ID)},
		"amount":          {"100.00"},
		"memo":            {"Lifecycle payment"},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/banking/receive-payment", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for receive payment, got %d", resp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after receive payment, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}
	assertARAgingRecomputed(t, server, companyID)
}

func TestReportCacheInvalidatedAfterInvoiceReceivePayment(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(&models.PaymentReceipt{}, &models.SettlementAllocation{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Report Invoice Receive Cache Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	invoice := seedReportCacheOpenInvoice(t, db, companyID, "INV-RPT-PAY-002")
	app := reportCacheLifecycleApp(server, user, companyID)

	primeReportCacheForCompany(server, companyID)
	form := url.Values{
		"payment_method":  {string(models.PaymentMethodCheck)},
		"entry_date":      {"2026-04-30"},
		"bank_account_id": {fmt.Sprintf("%d", bankAccountID)},
		"amount":          {"100.00"},
		"memo":            {"Lifecycle invoice payment"},
	}
	resp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/invoices/%d/receive-payment", invoice.ID), form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for invoice receive payment, got %d", resp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after invoice receive payment, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}
	assertARAgingRecomputed(t, server, companyID)
}

func TestReportCacheInvalidatedAfterPayBills(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(&models.SettlementAllocation{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Report Pay Bills Cache Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	apAccountID := seedValidationAccount(t, db, companyID, "2000", models.RootLiability, models.DetailAccountsPayable)
	bill := seedReportCacheOpenBill(t, db, companyID, "BILL-RPT-PAY-001")
	app := reportCacheLifecycleApp(server, user, companyID)

	primeReportCacheForCompany(server, companyID)
	form := url.Values{
		"entry_date":      {"2026-04-30"},
		"bank_account_id": {fmt.Sprintf("%d", bankAccountID)},
		"ap_account_id":   {fmt.Sprintf("%d", apAccountID)},
		"bill_selected":   {fmt.Sprintf("%d", bill.ID)},
		"pay_amount_" + fmt.Sprintf("%d", bill.ID): {"80.00"},
		"memo": {"Lifecycle bill payment"},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/banking/pay-bills", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for pay bills, got %d", resp.StatusCode)
	}
	if server.ReportCache.plCache.Len() != 0 || server.ReportCache.arCache.Len() != 0 {
		t.Fatalf("expected report cache flush after pay bills, got pl=%d ar=%d", server.ReportCache.plCache.Len(), server.ReportCache.arCache.Len())
	}
	assertIncomeStatementRecomputed(t, server, companyID)
}
