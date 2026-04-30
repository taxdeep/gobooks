// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// receive_payment_deposit_test.go — tests for the Phase-5 unified allocation
// path (2026-04-24 design): invoice apply + deposit consume + new-deposit
// creation in one call.
//
// Cases covered (per design note):
//
//	A. Pure cash payment        — I = bank, no deposit.
//	B. Pure offset              — deposit fully covers invoice, bank = 0.
//	C. Overpayment → new deposit — invoice + extra cash → new CustomerDeposit row.
//	D. Mixed                    — cash + deposit on the same session.
//
// Plus validation guards (overpay on invoice row, cross-customer deposit).

func testRPDepositDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rp_deposit_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.Invoice{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.SettlementAllocation{},
		&models.PaymentReceipt{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.CreditNote{},
		&models.CreditNoteApplication{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedRPCreditNote(t *testing.T, db *gorm.DB, companyID, customerID uint, number, amount string) models.CreditNote {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	cn := models.CreditNote{
		CompanyID:        companyID,
		CreditNoteNumber: number,
		CustomerID:       customerID,
		CreditNoteDate:   time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Status:           models.CreditNoteStatusIssued,
		Reason:           models.CreditNoteReasonOther,
		Subtotal:         amt,
		Amount:           amt,
		BalanceRemaining: amt,
		CurrencyCode:     "",
		ExchangeRate:     decimal.NewFromInt(1),
		AmountBase:       amt,
	}
	if err := db.Create(&cn).Error; err != nil {
		t.Fatal(err)
	}
	return cn
}

func seedRPDepositCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{
		Name:              "RP Deposit Co",
		AccountCodeLength: 4,
		IsActive:          true,
		BaseCurrencyCode:  "CAD",
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedRPDepositAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()
	acc := models.Account{
		CompanyID: companyID, Code: code, Name: code,
		RootAccountType: root, DetailAccountType: detail, IsActive: true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

func seedRPInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, number, amount string) models.Invoice {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	inv := models.Invoice{
		CompanyID:      companyID,
		InvoiceNumber:  number,
		CustomerID:     customerID,
		InvoiceDate:    time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusIssued,
		Amount:         amt,
		AmountBase:     amt,
		Subtotal:       amt,
		BalanceDue:     amt,
		BalanceDueBase: amt,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return inv
}

func seedRPDeposit(t *testing.T, db *gorm.DB, companyID, customerID uint, number, amount string) models.CustomerDeposit {
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

// baseInput returns the common fields. Callers fill in Allocations/Deposits/etc.
func baseInput(cid, custID, bankID, arID uint) ReceivePaymentInput {
	return ReceivePaymentInput{
		CompanyID:     cid,
		CustomerID:    custID,
		EntryDate:     time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankID,
		PaymentMethod: models.PaymentMethodCheck,
		ARAccountID:   arID,
	}
}

// sumBy returns total debit / credit for a JE restricted by accountID (0 = all).
func sumJEByAccount(t *testing.T, db *gorm.DB, companyID, jeID, accountID uint) (debit, credit decimal.Decimal) {
	t.Helper()
	var result struct {
		Debit  decimal.Decimal
		Credit decimal.Decimal
	}
	q := db.Model(&models.JournalLine{}).
		Where("company_id = ? AND journal_entry_id = ?", companyID, jeID)
	if accountID != 0 {
		q = q.Where("account_id = ?", accountID)
	}
	if err := q.Select("COALESCE(SUM(debit),0) AS debit, COALESCE(SUM(credit),0) AS credit").
		Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	return result.Debit, result.Credit
}

// ── Case A: pure cash payment ─────────────────────────────────────────────────

func TestReceivePayment_CaseA_PureCash(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust A"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-A-001", "100.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("100.00")}}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Invoice fully paid.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.Status != models.InvoiceStatusPaid {
		t.Errorf("status = %q, want paid", reloaded.Status)
	}

	// JE: Bank DR 100 / AR CR 100. No deposit lines.
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("bank DR = %s, want 100", bankDR)
	}
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("AR CR = %s, want 100", arCR)
	}
}

// ── Case B: pure offset — deposit fully covers invoice, bank=0 ────────────────

func TestReceivePayment_CaseB_PureOffset(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust B"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv1 := seedRPInvoice(t, db, cid, cust.ID, "INV-B-001", "100.00")
	inv2 := seedRPInvoice(t, db, cid, cust.ID, "INV-B-002", "200.00")
	dep := seedRPDeposit(t, db, cid, cust.ID, "DEP0001", "300.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{
		{InvoiceID: inv1.ID, Amount: decimal.RequireFromString("100.00")},
		{InvoiceID: inv2.ID, Amount: decimal.RequireFromString("200.00")},
	}
	in.Deposits = []DepositApplication{
		{DepositID: dep.ID, Amount: decimal.RequireFromString("300.00")},
	}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank line: should NOT exist (B=0).
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.IsZero() {
		t.Errorf("bank DR = %s, want 0 (pure offset)", bankDR)
	}

	// AR CR total = 300.
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.Equal(decimal.RequireFromString("300.00")) {
		t.Errorf("AR CR = %s, want 300", arCR)
	}

	// Customer Deposits DR = 300 (find the account by system_key).
	var custDepAcc models.Account
	if err := db.Where("company_id = ? AND system_key = ?", cid, "customer_deposits").First(&custDepAcc).Error; err != nil {
		t.Fatalf("customer deposits account not auto-created: %v", err)
	}
	custDepDR, _ := sumJEByAccount(t, db, cid, jeID, custDepAcc.ID)
	if !custDepDR.Equal(decimal.RequireFromString("300.00")) {
		t.Errorf("Customer Deposits DR = %s, want 300", custDepDR)
	}

	// JE balanced.
	totalDR, totalCR := sumJEByAccount(t, db, cid, jeID, 0)
	if !totalDR.Equal(totalCR) {
		t.Errorf("JE unbalanced: DR=%s CR=%s", totalDR, totalCR)
	}

	// Invoices paid, deposit fully applied.
	var rInv1, rInv2 models.Invoice
	db.First(&rInv1, inv1.ID)
	db.First(&rInv2, inv2.ID)
	if rInv1.Status != models.InvoiceStatusPaid || rInv2.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice statuses = %q / %q, want paid / paid", rInv1.Status, rInv2.Status)
	}
	var rDep models.CustomerDeposit
	db.First(&rDep, dep.ID)
	if rDep.Status != models.CustomerDepositStatusFullyApplied {
		t.Errorf("deposit status = %q, want fully_applied", rDep.Status)
	}
	if !rDep.BalanceRemaining.IsZero() {
		t.Errorf("deposit balance = %s, want 0", rDep.BalanceRemaining)
	}

	// CustomerDepositApplication rows: one per invoice.
	var apps []models.CustomerDepositApplication
	db.Where("customer_deposit_id = ?", dep.ID).Order("invoice_id").Find(&apps)
	if len(apps) != 2 {
		t.Fatalf("expected 2 deposit applications, got %d", len(apps))
	}
	// Pro-rata: inv1 100 / 300 × 300 = 100 ; inv2 = 200.
	sumApplied := decimal.Zero
	for _, a := range apps {
		sumApplied = sumApplied.Add(a.AmountApplied)
	}
	if !sumApplied.Equal(decimal.RequireFromString("300.00")) {
		t.Errorf("app amounts sum = %s, want 300", sumApplied)
	}
}

// ── Case C: overpayment creates new deposit ───────────────────────────────────

func TestReceivePayment_CaseC_OverpaymentCreatesNewDeposit(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust C"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-C-001", "100.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("100.00")}}
	in.NewDepositAmount = decimal.RequireFromString("400.00")

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank DR = 500 (100 invoice + 400 new deposit).
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("500.00")) {
		t.Errorf("bank DR = %s, want 500", bankDR)
	}

	// AR CR = 100.
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("AR CR = %s, want 100", arCR)
	}

	// Customer Deposits CR = 400 (new deposit liability).
	var custDepAcc models.Account
	db.Where("company_id = ? AND system_key = ?", cid, "customer_deposits").First(&custDepAcc)
	_, custDepCR := sumJEByAccount(t, db, cid, jeID, custDepAcc.ID)
	if !custDepCR.Equal(decimal.RequireFromString("400.00")) {
		t.Errorf("Customer Deposits CR = %s, want 400", custDepCR)
	}

	// New CustomerDeposit row exists with source=overpayment.
	var deps []models.CustomerDeposit
	db.Where("customer_id = ?", cust.ID).Find(&deps)
	if len(deps) != 1 {
		t.Fatalf("expected 1 deposit, got %d", len(deps))
	}
	dep := deps[0]
	if !dep.Amount.Equal(decimal.RequireFromString("400.00")) {
		t.Errorf("deposit.Amount = %s, want 400", dep.Amount)
	}
	if !dep.BalanceRemaining.Equal(decimal.RequireFromString("400.00")) {
		t.Errorf("deposit.BalanceRemaining = %s, want 400", dep.BalanceRemaining)
	}
	if dep.Source != models.DepositSourceOverpayment {
		t.Errorf("deposit.Source = %q, want overpayment", dep.Source)
	}
	if dep.Status != models.CustomerDepositStatusPosted {
		t.Errorf("deposit.Status = %q, want posted", dep.Status)
	}
	if !strings.HasPrefix(dep.DepositNumber, "DEP") {
		t.Errorf("deposit number %q does not start with DEP", dep.DepositNumber)
	}

	// JE balanced.
	totalDR, totalCR := sumJEByAccount(t, db, cid, jeID, 0)
	if !totalDR.Equal(totalCR) {
		t.Errorf("JE unbalanced: DR=%s CR=%s", totalDR, totalCR)
	}
}

// ── Case D: mixed — cash + partial deposit ────────────────────────────────────

func TestReceivePayment_UnallocatedCashCreatesCustomerDeposit(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust Deposit Only"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-OPEN-001", "100.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.NewDepositAmount = decimal.RequireFromString("100.00")

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("bank DR = %s, want 100", bankDR)
	}
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.IsZero() {
		t.Errorf("AR CR = %s, want 0 for unapplied customer deposit", arCR)
	}
	var custDepAcc models.Account
	if err := db.Where("company_id = ? AND system_key = ?", cid, "customer_deposits").First(&custDepAcc).Error; err != nil {
		t.Fatalf("customer deposits account not auto-created: %v", err)
	}
	_, custDepCR := sumJEByAccount(t, db, cid, jeID, custDepAcc.ID)
	if !custDepCR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("Customer Deposits CR = %s, want 100", custDepCR)
	}
	var dep models.CustomerDeposit
	if err := db.Where("customer_id = ?", cust.ID).First(&dep).Error; err != nil {
		t.Fatalf("expected customer deposit row: %v", err)
	}
	if !dep.BalanceRemaining.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("deposit balance = %s, want 100", dep.BalanceRemaining)
	}
	var rInv models.Invoice
	db.First(&rInv, inv.ID)
	if !rInv.BalanceDue.Equal(decimal.RequireFromString("100.00")) || rInv.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice changed unexpectedly: status=%s balance=%s", rInv.Status, rInv.BalanceDue)
	}
}

func TestReceivePayment_PartialInvoiceApplyCreatesDepositForRemainder(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust Partial Plus Deposit"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-PART-001", "100.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("50.00")}}
	in.NewDepositAmount = decimal.RequireFromString("50.00")

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("bank DR = %s, want 100", bankDR)
	}
	var rInv models.Invoice
	db.First(&rInv, inv.ID)
	if rInv.Status != models.InvoiceStatusPartiallyPaid || !rInv.BalanceDue.Equal(decimal.RequireFromString("50.00")) {
		t.Errorf("invoice status/balance = %s/%s, want partially_paid/50", rInv.Status, rInv.BalanceDue)
	}
	var dep models.CustomerDeposit
	if err := db.Where("customer_id = ?", cust.ID).First(&dep).Error; err != nil {
		t.Fatalf("expected customer deposit row: %v", err)
	}
	if !dep.BalanceRemaining.Equal(decimal.RequireFromString("50.00")) {
		t.Errorf("deposit balance = %s, want 50", dep.BalanceRemaining)
	}
}

func TestReceivePayment_MultiplePartialInvoiceAllocations(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust Multi Partial"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv1 := seedRPInvoice(t, db, cid, cust.ID, "INV-MP-001", "100.00")
	inv2 := seedRPInvoice(t, db, cid, cust.ID, "INV-MP-002", "50.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{
		{InvoiceID: inv1.ID, Amount: decimal.RequireFromString("40.00")},
		{InvoiceID: inv2.ID, Amount: decimal.RequireFromString("20.00")},
	}

	_, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	var rInv1, rInv2 models.Invoice
	db.First(&rInv1, inv1.ID)
	db.First(&rInv2, inv2.ID)
	if !rInv1.BalanceDue.Equal(decimal.RequireFromString("60.00")) {
		t.Errorf("A1 balance = %s, want 60", rInv1.BalanceDue)
	}
	if !rInv2.BalanceDue.Equal(decimal.RequireFromString("30.00")) {
		t.Errorf("A2 balance = %s, want 30", rInv2.BalanceDue)
	}
}

func TestReceivePayment_PartialDepositUseLeavesRemainingBalance(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust Deposit Remainder"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv1 := seedRPInvoice(t, db, cid, cust.ID, "INV-DR-001", "30.00")
	inv2 := seedRPInvoice(t, db, cid, cust.ID, "INV-DR-002", "100.00")
	dep := seedRPDeposit(t, db, cid, cust.ID, "DEP-D4", "50.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{
		{InvoiceID: inv1.ID, Amount: decimal.RequireFromString("30.00")},
		{InvoiceID: inv2.ID, Amount: decimal.RequireFromString("100.00")},
	}
	in.Deposits = []DepositApplication{{DepositID: dep.ID, Amount: decimal.RequireFromString("30.00")}}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("bank DR = %s, want 100", bankDR)
	}
	var rDep models.CustomerDeposit
	db.First(&rDep, dep.ID)
	if rDep.Status != models.CustomerDepositStatusPartiallyApplied {
		t.Errorf("deposit status = %q, want partially_applied", rDep.Status)
	}
	if !rDep.BalanceRemaining.Equal(decimal.RequireFromString("20.00")) {
		t.Errorf("deposit balance = %s, want 20", rDep.BalanceRemaining)
	}
}

func TestReceivePayment_CaseD_Mixed(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust D"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-D-001", "500.00")
	dep := seedRPDeposit(t, db, cid, cust.ID, "DEP0001", "500.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("500.00")}}
	in.Deposits = []DepositApplication{{DepositID: dep.ID, Amount: decimal.RequireFromString("200.00")}}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank = 500 - 200 = 300.
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("300.00")) {
		t.Errorf("bank DR = %s, want 300", bankDR)
	}

	// AR CR = 500.
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.Equal(decimal.RequireFromString("500.00")) {
		t.Errorf("AR CR = %s, want 500", arCR)
	}

	// Customer Deposits DR = 200.
	var custDepAcc models.Account
	db.Where("company_id = ? AND system_key = ?", cid, "customer_deposits").First(&custDepAcc)
	custDepDR, _ := sumJEByAccount(t, db, cid, jeID, custDepAcc.ID)
	if !custDepDR.Equal(decimal.RequireFromString("200.00")) {
		t.Errorf("Customer Deposits DR = %s, want 200", custDepDR)
	}

	// Deposit partially applied.
	var rDep models.CustomerDeposit
	db.First(&rDep, dep.ID)
	if rDep.Status != models.CustomerDepositStatusPartiallyApplied {
		t.Errorf("deposit.Status = %q, want partially_applied", rDep.Status)
	}
	if !rDep.BalanceRemaining.Equal(decimal.RequireFromString("300.00")) {
		t.Errorf("deposit.BalanceRemaining = %s, want 300", rDep.BalanceRemaining)
	}

	// JE balanced.
	totalDR, totalCR := sumJEByAccount(t, db, cid, jeID, 0)
	if !totalDR.Equal(totalCR) {
		t.Errorf("JE unbalanced: DR=%s CR=%s", totalDR, totalCR)
	}
}

// ── Case E: pure CN offset — credit note covers invoice, bank=0, no DR-AR for CN ─

func TestReceivePayment_CaseE_PureCreditNoteOffset(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust E"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-E-001", "100.00")
	cn := seedRPCreditNote(t, db, cid, cust.ID, "CN-E-001", "100.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("100.00")}}
	in.CreditNotes = []CreditNoteConsumption{{CreditNoteID: cn.ID, Amount: decimal.RequireFromString("100.00")}}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank line should not exist (B = 100 - 100 = 0).
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.IsZero() {
		t.Errorf("bank DR = %s, want 0 (pure CN offset)", bankDR)
	}

	// AR CR per invoice should be 0 too (the invoice's $100 retirement is
	// fully absorbed by CN release — no GL movement). With both Bank and
	// AR at zero, the JE has no lines at all and is technically empty,
	// which is fine: SettlementAllocation + CreditNoteApplication still
	// record the sub-ledger reshuffle.
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.IsZero() {
		t.Errorf("AR CR = %s, want 0 (CN absorbs the retirement)", arCR)
	}

	// Invoice paid + CN fully applied.
	var rInv models.Invoice
	db.First(&rInv, inv.ID)
	if rInv.Status != models.InvoiceStatusPaid || !rInv.BalanceDue.IsZero() {
		t.Errorf("invoice %q balance=%s, want paid/0", rInv.Status, rInv.BalanceDue)
	}
	var rCN models.CreditNote
	db.First(&rCN, cn.ID)
	if rCN.Status != models.CreditNoteStatusFullyApplied || !rCN.BalanceRemaining.IsZero() {
		t.Errorf("CN status=%q balance=%s, want fully_applied/0", rCN.Status, rCN.BalanceRemaining)
	}

	// One CreditNoteApplication row for the (cn, invoice) pair.
	var apps []models.CreditNoteApplication
	db.Where("credit_note_id = ?", cn.ID).Find(&apps)
	if len(apps) != 1 {
		t.Fatalf("expected 1 CN application, got %d", len(apps))
	}
	if !apps[0].AmountApplied.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("CN app amount = %s, want 100", apps[0].AmountApplied)
	}
}

// ── Case F: mixed — invoice + CN + deposit + cash ────────────────────────────

func TestReceivePayment_CaseF_Mixed(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust F"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-F-001", "1000.00")
	cn := seedRPCreditNote(t, db, cid, cust.ID, "CN-F-001", "150.00")
	dep := seedRPDeposit(t, db, cid, cust.ID, "DEP0001", "200.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("1000.00")}}
	in.CreditNotes = []CreditNoteConsumption{{CreditNoteID: cn.ID, Amount: decimal.RequireFromString("150.00")}}
	in.Deposits = []DepositApplication{{DepositID: dep.ID, Amount: decimal.RequireFromString("200.00")}}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank = 1000 - 150 - 200 = 650.
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("650.00")) {
		t.Errorf("bank DR = %s, want 650", bankDR)
	}
	// AR CR = 1000 - 150 = 850 (invoice retirement net of CN absorption).
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.Equal(decimal.RequireFromString("850.00")) {
		t.Errorf("AR CR = %s, want 850", arCR)
	}
	// Customer Deposits DR = 200.
	var custDepAcc models.Account
	db.Where("company_id = ? AND system_key = ?", cid, "customer_deposits").First(&custDepAcc)
	custDepDR, _ := sumJEByAccount(t, db, cid, jeID, custDepAcc.ID)
	if !custDepDR.Equal(decimal.RequireFromString("200.00")) {
		t.Errorf("Customer Deposits DR = %s, want 200", custDepDR)
	}
	// JE balanced.
	totalDR, totalCR := sumJEByAccount(t, db, cid, jeID, 0)
	if !totalDR.Equal(totalCR) {
		t.Errorf("JE unbalanced: DR=%s CR=%s", totalDR, totalCR)
	}

	// CN + deposit fully applied.
	var rCN models.CreditNote
	db.First(&rCN, cn.ID)
	if rCN.Status != models.CreditNoteStatusFullyApplied {
		t.Errorf("CN status = %q, want fully_applied", rCN.Status)
	}
	var rDep models.CustomerDeposit
	db.First(&rDep, dep.ID)
	if rDep.Status != models.CustomerDepositStatusFullyApplied {
		t.Errorf("deposit status = %q, want fully_applied", rDep.Status)
	}
}

// ── Case G: row-level overage auto-rolls into a new Customer Deposit ─────────
//
// Mirror of the user's 2026-04-25 bug report: typing $5000 in an invoice
// row whose balance is $3600 should silently retire the invoice ($3600)
// and park the $1400 excess as a new Customer Deposit. No rejection — the
// user shouldn't have to round-trip to a separate "Extra" field for the
// common case.
func TestReceivePayment_CaseG_RowOverageAutoFoldsIntoDeposit(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust G"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-G-001", "3600.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{
		InvoiceID: inv.ID,
		Amount:    decimal.RequireFromString("5000.00"),
	}}

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank DR = full 5000 (what the customer actually paid).
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("5000.00")) {
		t.Errorf("bank DR = %s, want 5000", bankDR)
	}
	// AR CR = 3600 (capped at invoice balance).
	_, arCR := sumJEByAccount(t, db, cid, jeID, arID)
	if !arCR.Equal(decimal.RequireFromString("3600.00")) {
		t.Errorf("AR CR = %s, want 3600", arCR)
	}
	// Customer Deposits CR = 1400 (auto-overage).
	var custDepAcc models.Account
	db.Where("company_id = ? AND system_key = ?", cid, "customer_deposits").First(&custDepAcc)
	_, custDepCR := sumJEByAccount(t, db, cid, jeID, custDepAcc.ID)
	if !custDepCR.Equal(decimal.RequireFromString("1400.00")) {
		t.Errorf("Customer Deposits CR = %s, want 1400", custDepCR)
	}
	// JE balanced.
	totalDR, totalCR := sumJEByAccount(t, db, cid, jeID, 0)
	if !totalDR.Equal(totalCR) {
		t.Errorf("JE unbalanced: DR=%s CR=%s", totalDR, totalCR)
	}

	// Invoice fully paid (capped, not overpaid).
	var rInv models.Invoice
	db.First(&rInv, inv.ID)
	if rInv.Status != models.InvoiceStatusPaid || !rInv.BalanceDue.IsZero() {
		t.Errorf("invoice %q balance=%s, want paid/0", rInv.Status, rInv.BalanceDue)
	}

	// CustomerDeposit row created with Source=overpayment, amount=1400.
	var deps []models.CustomerDeposit
	db.Where("customer_id = ?", cust.ID).Find(&deps)
	if len(deps) != 1 {
		t.Fatalf("expected 1 deposit, got %d", len(deps))
	}
	d := deps[0]
	if !d.Amount.Equal(decimal.RequireFromString("1400.00")) {
		t.Errorf("deposit.Amount = %s, want 1400", d.Amount)
	}
	if d.Source != models.DepositSourceOverpayment {
		t.Errorf("deposit.Source = %q, want overpayment", d.Source)
	}
}

// TestReceivePayment_CaseG_AutoOverage_PlusExplicitExtra confirms row
// overage and the explicit Extra → New Deposit field stack into one
// deposit row (operator types 5000 in invoice $3600 row + 200 in Extra
// → one $1600 deposit).
func TestReceivePayment_CaseG_AutoOverage_PlusExplicitExtra(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust G2"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-G2-001", "3600.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("5000.00")}}
	in.NewDepositAmount = decimal.RequireFromString("200.00")

	jeID, err := RecordReceivePayment(db, in)
	if err != nil {
		t.Fatalf("RecordReceivePayment: %v", err)
	}

	// Bank DR = 5000 (raw invoice payment) + 200 (extra) = 5200.
	bankDR, _ := sumJEByAccount(t, db, cid, jeID, bankID)
	if !bankDR.Equal(decimal.RequireFromString("5200.00")) {
		t.Errorf("bank DR = %s, want 5200", bankDR)
	}
	// Single deposit row aggregates 1400 (auto) + 200 (explicit) = 1600.
	var deps []models.CustomerDeposit
	db.Where("customer_id = ?", cust.ID).Find(&deps)
	if len(deps) != 1 {
		t.Fatalf("expected 1 deposit, got %d", len(deps))
	}
	if !deps[0].Amount.Equal(decimal.RequireFromString("1600.00")) {
		t.Errorf("deposit.Amount = %s, want 1600 (auto 1400 + explicit 200)", deps[0].Amount)
	}
}

// TestReceivePayment_RejectsForeignRowOverpayment locks the v1 limitation:
// FX overpayment is still rejected (rate semantics on the over-portion are
// out of scope for this slice).
func TestReceivePayment_RejectsForeignRowOverpayment(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust FX"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	// Foreign-currency invoice — base is CAD, this is USD.
	usdInv := models.Invoice{
		CompanyID:      cid,
		InvoiceNumber:  "INV-FX-001",
		CustomerID:     cust.ID,
		InvoiceDate:    time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusIssued,
		Amount:         decimal.RequireFromString("100.00"),
		AmountBase:     decimal.RequireFromString("130.00"),
		Subtotal:       decimal.RequireFromString("100.00"),
		BalanceDue:     decimal.RequireFromString("100.00"),
		BalanceDueBase: decimal.RequireFromString("130.00"),
		CurrencyCode:   "USD",
	}
	if err := db.Create(&usdInv).Error; err != nil {
		t.Fatal(err)
	}

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: usdInv.ID, Amount: decimal.RequireFromString("150.00")}}

	_, err := RecordReceivePayment(db, in)
	if err == nil {
		t.Fatal("expected error for FX overpayment, got nil")
	}
	if !strings.Contains(err.Error(), "overpayment on foreign-currency") {
		t.Errorf("error = %v, want FX-overpayment guidance", err)
	}
}

// ── Validation: deposit consumption > invoice total rejected ─────────────────

func TestReceivePayment_RejectsDepositExceedsInvoices(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	cust := models.Customer{CompanyID: cid, Name: "Cust V2"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	inv := seedRPInvoice(t, db, cid, cust.ID, "INV-V2-001", "100.00")
	dep := seedRPDeposit(t, db, cid, cust.ID, "DEP0001", "500.00")

	in := baseInput(cid, cust.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("100.00")}}
	in.Deposits = []DepositApplication{{DepositID: dep.ID, Amount: decimal.RequireFromString("200.00")}}

	_, err := RecordReceivePayment(db, in)
	if err == nil {
		t.Fatal("expected error for deposit > invoices, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds invoice allocations") {
		t.Errorf("error = %v, want ‘exceeds invoice allocations’", err)
	}
}

// ── Validation: deposit belongs to different customer rejected ───────────────

func TestReceivePayment_RejectsCrossCustomerDeposit(t *testing.T) {
	db := testRPDepositDB(t)
	cid := seedRPDepositCompany(t, db)
	bankID := seedRPDepositAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)
	arID := seedRPDepositAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	custA := models.Customer{CompanyID: cid, Name: "Cust A"}
	custB := models.Customer{CompanyID: cid, Name: "Cust B"}
	db.Create(&custA)
	db.Create(&custB)
	inv := seedRPInvoice(t, db, cid, custA.ID, "INV-X-001", "100.00")
	depForB := seedRPDeposit(t, db, cid, custB.ID, "DEP0001", "100.00")

	in := baseInput(cid, custA.ID, bankID, arID)
	in.Allocations = []InvoiceAllocation{{InvoiceID: inv.ID, Amount: decimal.RequireFromString("100.00")}}
	in.Deposits = []DepositApplication{{DepositID: depForB.ID, Amount: decimal.RequireFromString("100.00")}}

	_, err := RecordReceivePayment(db, in)
	if err == nil {
		t.Fatal("expected error for cross-customer deposit, got nil")
	}
	if !strings.Contains(err.Error(), "different customer") {
		t.Errorf("error = %v, want ‘different customer’", err)
	}
}
