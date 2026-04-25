// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// receive_payment_deposit_handler_test.go — HTTP-level coverage for the
// deposit-aware Receive Payment POST.
//
// Service-layer rules (JE balance, status transitions, pro-rata deposit
// applications) are covered exhaustively in
// internal/services/receive_payment_deposit_test.go — these tests just
// verify the handler parses the new form fields (`deposit_id[]`,
// `deposit_amount[]`, `new_deposit_amount`) and passes them through.

func seedDepositForCustomer(t *testing.T, db *gorm.DB, companyID, customerID uint, number, amount string) models.CustomerDeposit {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	dep := models.CustomerDeposit{
		CompanyID:        companyID,
		CustomerID:       customerID,
		DepositNumber:    number,
		Status:           models.CustomerDepositStatusPosted,
		Source:           models.DepositSourceManual,
		DepositDate:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		CurrencyCode:     "",
		ExchangeRate:     decimal.NewFromInt(1),
		Amount:           amt,
		AmountBase:       amt,
		BalanceRemaining: amt,
		PaymentMethod:    models.PaymentMethodCheck,
	}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatal(err)
	}
	return dep
}

// TestReceivePayment_FormDeepLinkPreselectsInvoice locks the entry point
// from the invoice-detail "Apply Credits / Deposits" button: hitting
// `/banking/receive-payment?invoice_id=X` pre-selects the invoice and its
// customer so the unified Apply table renders the customer's open CNs +
// Deposits ready to tick.
func TestReceivePayment_FormDeepLinkPreselectsInvoice(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(
		&models.CustomerDeposit{},
		&models.CreditNote{},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "DeepLink Co")
	_ = seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	customerID := seedValidationCustomer(t, db, companyID, "DeepLink Customer")
	inv := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-DL-001")

	app := reportCacheLifecycleApp(server, user, companyID)
	app.Get("/banking/receive-payment", server.handleReceivePaymentForm)

	resp := performRequest(t, app, fmt.Sprintf("/banking/receive-payment?invoice_id=%d", inv.ID), "")
	body := readResponseBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		// Hidden input set from the deep-link
		fmt.Sprintf("data-initial-invoice=\"%d\"", inv.ID),
		// Customer pre-selected so the unified Apply table renders
		fmt.Sprintf("data-initial-customer=\"%d\"", customerID),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("deep-link page missing %q", want)
		}
	}
}

// TestReceivePayment_FormRendersDeposits confirms the GET form surface
// includes the unified document table with the Deposit row visible when
// the customer has any. Catches regressions in the VM / JSON wiring
// (OpenDepositsJSON) without driving the page via a browser.
func TestReceivePayment_FormRendersDeposits(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(&models.CustomerDeposit{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Render Deposits Co")
	_ = seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	customerID := seedValidationCustomer(t, db, companyID, "Dep Render Customer")
	_ = seedDepositForCustomer(t, db, companyID, customerID, "DEP0042", "150.00")

	app := reportCacheLifecycleApp(server, user, companyID)
	// reportCacheLifecycleApp only wires POST routes; register GET locally
	// so this render smoke test can reach the form handler.
	app.Get("/banking/receive-payment", server.handleReceivePaymentForm)

	resp := performRequest(t, app, "/banking/receive-payment", "")
	body := readResponseBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		"data-deposits",     // Alpine attribute for the JSON payload
		"DEP0042",           // deposit number in the JSON
		"new_deposit_amount", // the overpayment input field
		"Extra \u2192 New Deposit",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("receive-payment form missing %q", want)
		}
	}
}

// TestReceivePayment_Handler_CaseB_PureOffset locks the user's reference
// scenario: one POST with two invoices (100 + 200) and one deposit (300)
// produces a bank-zero JE (DR Customer Deposits / CR AR) and marks both
// invoices paid + the deposit fully-applied.
func TestReceivePayment_Handler_CaseB_PureOffset(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Offset Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	customerID := seedValidationCustomer(t, db, companyID, "Offset Customer")
	inv1 := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-B-H-001")
	inv2 := models.Invoice{
		CompanyID:            companyID,
		InvoiceNumber:        "INV-B-H-002",
		CustomerID:           customerID,
		InvoiceDate:          time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Status:               models.InvoiceStatusIssued,
		Amount:               decimal.RequireFromString("200.00"),
		AmountBase:           decimal.RequireFromString("200.00"),
		Subtotal:             decimal.RequireFromString("200.00"),
		BalanceDue:           decimal.RequireFromString("200.00"),
		BalanceDueBase:       decimal.RequireFromString("200.00"),
		CustomerNameSnapshot: "Offset Customer",
	}
	if err := db.Create(&inv2).Error; err != nil {
		t.Fatal(err)
	}
	dep := seedDepositForCustomer(t, db, companyID, customerID, "DEP0001", "300.00")

	app := reportCacheLifecycleApp(server, user, companyID)

	form := url.Values{
		"customer_id":     {fmt.Sprintf("%d", customerID)},
		"payment_method":  {string(models.PaymentMethodCheck)},
		"entry_date":      {"2026-04-30"},
		"bank_account_id": {fmt.Sprintf("%d", bankAccountID)},
		"allocation_invoice_id": {
			fmt.Sprintf("%d", inv1.ID),
			fmt.Sprintf("%d", inv2.ID),
		},
		"allocation_amount": {"100.00", "200.00"},
		"deposit_id":        {fmt.Sprintf("%d", dep.ID)},
		"deposit_amount":    {"300.00"},
		"memo":              {"Pure offset — user screenshot case"},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/banking/receive-payment", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303, got %d — body: %s", resp.StatusCode, body)
	}

	// Invoices paid.
	var r1, r2 models.Invoice
	db.First(&r1, inv1.ID)
	db.First(&r2, inv2.ID)
	if r1.Status != models.InvoiceStatusPaid || r2.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice statuses = %q / %q, want paid / paid", r1.Status, r2.Status)
	}

	// Deposit fully applied, balance zero.
	var rDep models.CustomerDeposit
	db.First(&rDep, dep.ID)
	if rDep.Status != models.CustomerDepositStatusFullyApplied {
		t.Errorf("deposit.Status = %q, want fully_applied", rDep.Status)
	}
	if !rDep.BalanceRemaining.IsZero() {
		t.Errorf("deposit.BalanceRemaining = %s, want 0", rDep.BalanceRemaining)
	}

	// Two CustomerDepositApplication rows (one per invoice).
	var apps []models.CustomerDepositApplication
	db.Where("customer_deposit_id = ?", dep.ID).Find(&apps)
	if len(apps) != 2 {
		t.Fatalf("expected 2 deposit applications, got %d", len(apps))
	}
	sum := decimal.Zero
	for _, a := range apps {
		sum = sum.Add(a.AmountApplied)
	}
	if !sum.Equal(decimal.RequireFromString("300.00")) {
		t.Errorf("deposit apps sum = %s, want 300", sum)
	}

	// Bank line should NOT exist (pure offset) — no DR against bank account.
	var bankDebit decimal.Decimal
	db.Model(&models.JournalLine{}).
		Where("company_id = ? AND account_id = ?", companyID, bankAccountID).
		Select("COALESCE(SUM(debit),0)").
		Scan(&bankDebit)
	if !bankDebit.IsZero() {
		t.Errorf("bank DR total = %s, want 0 for pure offset", bankDebit)
	}
}

// TestReceivePayment_Handler_CaseE_CreditNoteOffset locks the CN-side of
// the unified Apply table — POST `credit_note_id[]` + `credit_note_amount[]`
// retires the invoice via CN consumption (no new JE lines, only sub-ledger
// reshuffle). Mirrors the deposit-offset test above.
func TestReceivePayment_Handler_CaseE_CreditNoteOffset(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.CreditNote{},
		&models.CreditNoteApplication{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "CN Offset Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	customerID := seedValidationCustomer(t, db, companyID, "CN Offset Customer")
	inv := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-CN-001")

	// Seed a $100 issued credit note for the same customer (matches invoice balance).
	cn := models.CreditNote{
		CompanyID:        companyID,
		CreditNoteNumber: "CN-FORM-001",
		CustomerID:       customerID,
		CreditNoteDate:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Status:           models.CreditNoteStatusIssued,
		Reason:           models.CreditNoteReasonOther,
		Subtotal:         decimal.RequireFromString("100.00"),
		Amount:           decimal.RequireFromString("100.00"),
		BalanceRemaining: decimal.RequireFromString("100.00"),
		ExchangeRate:     decimal.NewFromInt(1),
		AmountBase:       decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&cn).Error; err != nil {
		t.Fatal(err)
	}

	app := reportCacheLifecycleApp(server, user, companyID)

	form := url.Values{
		"customer_id":           {fmt.Sprintf("%d", customerID)},
		"payment_method":        {string(models.PaymentMethodCheck)},
		"entry_date":            {"2026-04-30"},
		"bank_account_id":       {fmt.Sprintf("%d", bankAccountID)},
		"allocation_invoice_id": {fmt.Sprintf("%d", inv.ID)},
		"allocation_amount":     {"100.00"},
		"credit_note_id":        {fmt.Sprintf("%d", cn.ID)},
		"credit_note_amount":    {"100.00"},
		"memo":                  {"CN fully offsets invoice"},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/banking/receive-payment", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303, got %d — body: %s", resp.StatusCode, body)
	}

	var rInv models.Invoice
	db.First(&rInv, inv.ID)
	if rInv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status = %q, want paid", rInv.Status)
	}
	var rCN models.CreditNote
	db.First(&rCN, cn.ID)
	if rCN.Status != models.CreditNoteStatusFullyApplied {
		t.Errorf("CN status = %q, want fully_applied", rCN.Status)
	}

	var apps []models.CreditNoteApplication
	db.Where("credit_note_id = ?", cn.ID).Find(&apps)
	if len(apps) != 1 || !apps[0].AmountApplied.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("expected 1 CN application of 100.00, got %+v", apps)
	}

	// Bank line should NOT exist (pure offset).
	var bankDebit decimal.Decimal
	db.Model(&models.JournalLine{}).
		Where("company_id = ? AND account_id = ?", companyID, bankAccountID).
		Select("COALESCE(SUM(debit),0)").
		Scan(&bankDebit)
	if !bankDebit.IsZero() {
		t.Errorf("bank DR = %s, want 0 for pure CN offset", bankDebit)
	}
}

// TestReceivePayment_Handler_CaseC_NewDepositFromOverpayment locks the
// "customer paid more than the invoice balance" → auto new CustomerDeposit
// path. Invoice gets paid; a fresh DEP-numbered deposit row appears.
func TestReceivePayment_Handler_CaseC_NewDepositFromOverpayment(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Overpay Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	customerID := seedValidationCustomer(t, db, companyID, "Overpay Customer")
	inv := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-C-H-001")

	app := reportCacheLifecycleApp(server, user, companyID)

	// Invoice balance 100. Customer pays 100 for the invoice + 400 extra.
	// Result: invoice paid, 400 parked as new Customer Deposit (source=overpayment).
	form := url.Values{
		"customer_id":           {fmt.Sprintf("%d", customerID)},
		"payment_method":        {string(models.PaymentMethodCheck)},
		"entry_date":            {"2026-04-30"},
		"bank_account_id":       {fmt.Sprintf("%d", bankAccountID)},
		"allocation_invoice_id": {fmt.Sprintf("%d", inv.ID)},
		"allocation_amount":     {"100.00"},
		"new_deposit_amount":    {"400.00"},
		"memo":                  {"Invoice 100 + prepayment 400"},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/banking/receive-payment", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303, got %d — body: %s", resp.StatusCode, body)
	}

	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status = %q, want paid", reloaded.Status)
	}

	var deposits []models.CustomerDeposit
	db.Where("company_id = ? AND customer_id = ?", companyID, customerID).Find(&deposits)
	if len(deposits) != 1 {
		t.Fatalf("expected 1 deposit, got %d", len(deposits))
	}
	d := deposits[0]
	if !d.Amount.Equal(decimal.RequireFromString("400.00")) {
		t.Errorf("deposit.Amount = %s, want 400", d.Amount)
	}
	if d.Source != models.DepositSourceOverpayment {
		t.Errorf("deposit.Source = %q, want overpayment", d.Source)
	}
	if d.DepositNumber == "" {
		t.Error("deposit has no number")
	}

	// Bank DR = 500 (100 invoice + 400 new deposit).
	var bankDebit decimal.Decimal
	db.Model(&models.JournalLine{}).
		Where("company_id = ? AND account_id = ?", companyID, bankAccountID).
		Select("COALESCE(SUM(debit),0)").
		Scan(&bankDebit)
	if !bankDebit.Equal(decimal.RequireFromString("500.00")) {
		t.Errorf("bank DR = %s, want 500", bankDebit)
	}
}
