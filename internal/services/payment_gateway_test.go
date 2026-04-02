// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testPaymentGatewayDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:pgw_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.Invoice{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

func seedPGCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "PG Co", IsActive: true}
	db.Create(&co)
	return co.ID
}

// ── Gateway Account CRUD ─────────────────────────────────────────────────────

func TestGatewayAccount_CRUD(t *testing.T) {
	db := testPaymentGatewayDB(t)
	companyID := seedPGCompany(t, db)

	acct := models.PaymentGatewayAccount{
		CompanyID: companyID, ProviderType: models.ProviderStripe,
		DisplayName: "Stripe Prod", AuthStatus: "pending", IsActive: true,
	}
	if err := CreateGatewayAccount(db, &acct); err != nil {
		t.Fatal(err)
	}
	if acct.ID == 0 {
		t.Fatal("Account not created")
	}

	accounts, err := ListGatewayAccounts(db, companyID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("Expected 1 account, got %d", len(accounts))
	}
	if accounts[0].DisplayName != "Stripe Prod" {
		t.Error("Wrong name")
	}

	loaded, err := GetGatewayAccount(db, companyID, acct.ID)
	if err != nil || loaded == nil {
		t.Fatal("Get failed")
	}
}

func TestGatewayAccount_CompanyIsolation(t *testing.T) {
	db := testPaymentGatewayDB(t)
	co1 := seedPGCompany(t, db)
	co2 := seedPGCompany(t, db)

	CreateGatewayAccount(db, &models.PaymentGatewayAccount{
		CompanyID: co1, ProviderType: models.ProviderPayPal,
		DisplayName: "PP", AuthStatus: "pending", IsActive: true,
	})

	accounts, _ := ListGatewayAccounts(db, co2)
	if len(accounts) != 0 {
		t.Error("Company 2 should not see company 1's accounts")
	}
}

// ── Payment Accounting Mapping ───────────────────────────────────────────────

func TestPaymentAccountingMapping_SaveAndLoad(t *testing.T) {
	db := testPaymentGatewayDB(t)
	companyID := seedPGCompany(t, db)

	clearing := models.Account{CompanyID: companyID, Code: "1500", Name: "GW Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)

	gw := models.PaymentGatewayAccount{CompanyID: companyID, ProviderType: models.ProviderStripe, DisplayName: "S", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)

	m := models.PaymentAccountingMapping{
		CompanyID: companyID, GatewayAccountID: gw.ID,
		ClearingAccountID: &clearing.ID,
	}
	if err := SavePaymentAccountingMapping(db, &m); err != nil {
		t.Fatal(err)
	}

	loaded, err := GetPaymentAccountingMapping(db, companyID, gw.ID)
	if err != nil || loaded == nil {
		t.Fatal("Mapping not found")
	}
	if loaded.ClearingAccountID == nil || *loaded.ClearingAccountID != clearing.ID {
		t.Error("Clearing account not saved")
	}
}

func TestPaymentAccountingMapping_CompanyIsolation(t *testing.T) {
	db := testPaymentGatewayDB(t)
	co1 := seedPGCompany(t, db)
	co2 := seedPGCompany(t, db)

	gw := models.PaymentGatewayAccount{CompanyID: co1, ProviderType: models.ProviderStripe, DisplayName: "S", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)
	SavePaymentAccountingMapping(db, &models.PaymentAccountingMapping{CompanyID: co1, GatewayAccountID: gw.ID})

	loaded, _ := GetPaymentAccountingMapping(db, co2, gw.ID)
	if loaded != nil {
		t.Error("Company 2 should not see company 1's mapping")
	}
}

// ── Payment Request CRUD ─────────────────────────────────────────────────────

func TestPaymentRequest_CreateAndList(t *testing.T) {
	db := testPaymentGatewayDB(t)
	companyID := seedPGCompany(t, db)

	gw := models.PaymentGatewayAccount{CompanyID: companyID, ProviderType: models.ProviderManual, DisplayName: "M", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)

	req := models.PaymentRequest{
		CompanyID: companyID, GatewayAccountID: gw.ID,
		Amount: decimal.NewFromInt(250), CurrencyCode: "USD",
		Status: models.PaymentRequestDraft, Description: "Test payment",
	}
	if err := CreatePaymentRequest(db, &req); err != nil {
		t.Fatal(err)
	}

	reqs, err := ListPaymentRequests(db, companyID, 50)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("Expected 1 request, got %d", len(reqs))
	}
}

func TestPaymentRequest_CompanyIsolation(t *testing.T) {
	db := testPaymentGatewayDB(t)
	co1 := seedPGCompany(t, db)
	co2 := seedPGCompany(t, db)

	gw := models.PaymentGatewayAccount{CompanyID: co1, ProviderType: models.ProviderManual, DisplayName: "M", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)
	CreatePaymentRequest(db, &models.PaymentRequest{CompanyID: co1, GatewayAccountID: gw.ID, Amount: decimal.NewFromInt(100)})

	reqs, _ := ListPaymentRequests(db, co2, 50)
	if len(reqs) != 0 {
		t.Error("Company 2 should not see company 1's requests")
	}
}

// ── Payment Transaction CRUD ─────────────────────────────────────────────────

func TestPaymentTransaction_CreateAndList(t *testing.T) {
	db := testPaymentGatewayDB(t)
	companyID := seedPGCompany(t, db)

	gw := models.PaymentGatewayAccount{CompanyID: companyID, ProviderType: models.ProviderStripe, DisplayName: "S", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)

	txn := models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: gw.ID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(500),
		CurrencyCode: "USD", Status: "completed", ExternalTxnRef: "ch_xxx",
		RawPayload: datatypes.JSON("{}"),
	}
	if err := CreatePaymentTransaction(db, &txn); err != nil {
		t.Fatal(err)
	}

	txns, err := ListPaymentTransactions(db, companyID, 50)
	if err != nil || len(txns) != 1 {
		t.Fatalf("Expected 1 txn, got %d", len(txns))
	}
	if txns[0].TransactionType != models.TxnTypeCharge {
		t.Errorf("Wrong txn type: %s", txns[0].TransactionType)
	}
}

func TestPaymentTransaction_CompanyIsolation(t *testing.T) {
	db := testPaymentGatewayDB(t)
	co1 := seedPGCompany(t, db)
	co2 := seedPGCompany(t, db)

	gw := models.PaymentGatewayAccount{CompanyID: co1, ProviderType: models.ProviderStripe, DisplayName: "S", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)
	CreatePaymentTransaction(db, &models.PaymentTransaction{
		CompanyID: co1, GatewayAccountID: gw.ID,
		TransactionType: models.TxnTypeFee, Amount: decimal.NewFromInt(10),
		RawPayload: datatypes.JSON("{}"),
	})

	txns, _ := ListPaymentTransactions(db, co2, 50)
	if len(txns) != 0 {
		t.Error("Company 2 should not see company 1's transactions")
	}
}

func TestPaymentTransaction_LinkedRequest(t *testing.T) {
	db := testPaymentGatewayDB(t)
	companyID := seedPGCompany(t, db)

	gw := models.PaymentGatewayAccount{CompanyID: companyID, ProviderType: models.ProviderManual, DisplayName: "M", AuthStatus: "ok", IsActive: true}
	CreateGatewayAccount(db, &gw)

	req := models.PaymentRequest{CompanyID: companyID, GatewayAccountID: gw.ID, Amount: decimal.NewFromInt(100)}
	CreatePaymentRequest(db, &req)

	txn := models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: gw.ID,
		PaymentRequestID: &req.ID, TransactionType: models.TxnTypeCharge,
		Amount: decimal.NewFromInt(100), RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)

	if txn.PaymentRequestID == nil || *txn.PaymentRequestID != req.ID {
		t.Error("Transaction should link to request")
	}
}
