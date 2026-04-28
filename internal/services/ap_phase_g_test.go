// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testPhaseGDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:phg_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Vendor{},
		&models.Bill{},
		&models.BillLine{},
		&models.VendorCreditNote{},
		&models.APCreditApplication{},
	)
	return db
}

type phaseGSetup struct {
	companyID uint
	vendorID  uint
	apAcctID  uint
	vcnID     uint
	billID    uint
}

func setupPhaseG(t *testing.T, db *gorm.DB, vcnAmount, billAmount decimal.Decimal) phaseGSetup {
	t.Helper()

	co := models.Company{Name: "PhaseG Co", IsActive: true}
	db.Create(&co)

	vendor := models.Vendor{CompanyID: co.ID, Name: "ACME Supplies"}
	db.Create(&vendor)

	apAcct := models.Account{
		CompanyID: co.ID, Code: "2000", Name: "Accounts Payable",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailAccountsPayable, IsActive: true,
	}
	db.Create(&apAcct)

	offsetAcct := models.Account{
		CompanyID: co.ID, Code: "5100", Name: "Purchase Returns",
		RootAccountType: models.RootExpense, IsActive: true,
	}
	db.Create(&offsetAcct)

	// Posted VCN with given amount.
	vcn := models.VendorCreditNote{
		CompanyID:        co.ID,
		VendorID:         vendor.ID,
		CreditNoteNumber: "VCN-0001",
		Status:           models.VendorCreditNoteStatusPosted,
		CreditNoteDate:   time.Now(),
		Amount:           vcnAmount,
		AmountBase:       vcnAmount, // 1:1 exchange rate
		RemainingAmount:  vcnAmount,
		APAccountID:      &apAcct.ID,
		OffsetAccountID:  &offsetAcct.ID,
		CurrencyCode:     "CAD",
		ExchangeRate:     decimal.NewFromInt(1),
	}
	db.Create(&vcn)

	// Posted bill with given amount.
	bill := models.Bill{
		CompanyID:  co.ID,
		VendorID:   vendor.ID,
		BillNumber: "BILL-0001",
		Status:     models.BillStatusPosted,
		BillDate:   time.Now(),
		Amount:     billAmount,
		BalanceDue: billAmount,
	}
	db.Create(&bill)

	return phaseGSetup{
		companyID: co.ID,
		vendorID:  vendor.ID,
		apAcctID:  apAcct.ID,
		vcnID:     vcn.ID,
		billID:    bill.ID,
	}
}

// TestApplyVCN_FullApplication applies the full VCN balance to the bill.
func TestApplyVCN_FullApplication(t *testing.T) {
	db := testPhaseGDB(t)
	amount := decimal.NewFromInt(500)
	s := setupPhaseG(t, db, amount, amount)

	err := ApplyVendorCreditNoteToBill(db, s.companyID, s.vcnID, s.billID, amount)
	if err != nil {
		t.Fatalf("ApplyVendorCreditNoteToBill failed: %v", err)
	}

	var vcn models.VendorCreditNote
	db.First(&vcn, s.vcnID)
	if vcn.Status != models.VendorCreditNoteStatusFullyApplied {
		t.Errorf("VCN status expected fully_applied, got %s", vcn.Status)
	}
	if !vcn.RemainingAmount.IsZero() {
		t.Errorf("VCN remaining expected 0, got %s", vcn.RemainingAmount)
	}
	if !vcn.AppliedAmount.Equal(amount) {
		t.Errorf("VCN applied expected %s, got %s", amount, vcn.AppliedAmount)
	}

	var bill models.Bill
	db.First(&bill, s.billID)
	if bill.Status != models.BillStatusPaid {
		t.Errorf("Bill status expected paid, got %s", bill.Status)
	}
	if !bill.BalanceDue.IsZero() {
		t.Errorf("Bill balance expected 0, got %s", bill.BalanceDue)
	}

	// Application record created.
	var appCount int64
	db.Model(&models.APCreditApplication{}).
		Where("company_id = ? AND vendor_credit_note_id = ? AND bill_id = ?", s.companyID, s.vcnID, s.billID).
		Count(&appCount)
	if appCount != 1 {
		t.Errorf("Expected 1 APCreditApplication, got %d", appCount)
	}
}

// TestApplyVCN_PartialApplication applies part of a VCN to a bill.
func TestApplyVCN_PartialApplication(t *testing.T) {
	db := testPhaseGDB(t)
	s := setupPhaseG(t, db, decimal.NewFromInt(1000), decimal.NewFromInt(300))

	partial := decimal.NewFromInt(300)
	err := ApplyVendorCreditNoteToBill(db, s.companyID, s.vcnID, s.billID, partial)
	if err != nil {
		t.Fatalf("ApplyVendorCreditNoteToBill partial failed: %v", err)
	}

	var vcn models.VendorCreditNote
	db.First(&vcn, s.vcnID)
	if vcn.Status != models.VendorCreditNoteStatusPartiallyApplied {
		t.Errorf("VCN status expected partially_applied, got %s", vcn.Status)
	}
	if !vcn.RemainingAmount.Equal(decimal.NewFromInt(700)) {
		t.Errorf("VCN remaining expected 700, got %s", vcn.RemainingAmount)
	}

	var bill models.Bill
	db.First(&bill, s.billID)
	if bill.Status != models.BillStatusPaid {
		t.Errorf("Bill should be paid after full balance applied, got %s", bill.Status)
	}
}

// TestApplyVCN_ExceedsVCNBalance should fail.
func TestApplyVCN_ExceedsVCNBalance(t *testing.T) {
	db := testPhaseGDB(t)
	s := setupPhaseG(t, db, decimal.NewFromInt(100), decimal.NewFromInt(500))

	err := ApplyVendorCreditNoteToBill(db, s.companyID, s.vcnID, s.billID, decimal.NewFromInt(200))
	if err == nil {
		t.Error("Expected error when applying more than VCN remaining balance")
	}
}

// TestApplyVCN_ExceedsBillBalance should fail.
func TestApplyVCN_ExceedsBillBalance(t *testing.T) {
	db := testPhaseGDB(t)
	s := setupPhaseG(t, db, decimal.NewFromInt(1000), decimal.NewFromInt(100))

	err := ApplyVendorCreditNoteToBill(db, s.companyID, s.vcnID, s.billID, decimal.NewFromInt(200))
	if err == nil {
		t.Error("Expected error when applying more than bill balance due")
	}
}

// TestApplyVCN_VendorMismatch should fail when VCN and Bill have different vendors.
func TestApplyVCN_VendorMismatch(t *testing.T) {
	db := testPhaseGDB(t)
	s := setupPhaseG(t, db, decimal.NewFromInt(500), decimal.NewFromInt(500))

	// Create a second vendor and a bill for them.
	vendor2 := models.Vendor{CompanyID: s.companyID, Name: "Other Vendor"}
	db.Create(&vendor2)
	bill2 := models.Bill{
		CompanyID:  s.companyID,
		VendorID:   vendor2.ID,
		BillNumber: "BILL-0002",
		Status:     models.BillStatusPosted,
		BillDate:   time.Now(),
		Amount:     decimal.NewFromInt(500),
		BalanceDue: decimal.NewFromInt(500),
	}
	db.Create(&bill2)

	err := ApplyVendorCreditNoteToBill(db, s.companyID, s.vcnID, bill2.ID, decimal.NewFromInt(100))
	if err == nil {
		t.Error("Expected vendor mismatch error")
	}
}

// TestApplyVCN_DraftVCNBlocked should fail for draft credit notes.
func TestApplyVCN_DraftVCNBlocked(t *testing.T) {
	db := testPhaseGDB(t)
	s := setupPhaseG(t, db, decimal.NewFromInt(500), decimal.NewFromInt(500))

	// Change VCN back to draft.
	db.Model(&models.VendorCreditNote{}).Where("id = ?", s.vcnID).Update("status", "draft")

	err := ApplyVendorCreditNoteToBill(db, s.companyID, s.vcnID, s.billID, decimal.NewFromInt(100))
	if err == nil {
		t.Error("Expected error applying a draft VCN")
	}
}

// TestListOpenBillsForVendor only returns posted/partially_paid bills with balance > 0.
func TestListOpenBillsForVendor(t *testing.T) {
	db := testPhaseGDB(t)
	s := setupPhaseG(t, db, decimal.NewFromInt(100), decimal.NewFromInt(200))

	// Add a paid bill (should not appear).
	db.Create(&models.Bill{
		CompanyID:  s.companyID,
		VendorID:   s.vendorID,
		BillNumber: "BILL-PAID",
		Status:     models.BillStatusPaid,
		BillDate:   time.Now(),
		Amount:     decimal.NewFromInt(100),
		BalanceDue: decimal.Zero,
	})
	// Add a draft bill (should not appear).
	db.Create(&models.Bill{
		CompanyID:  s.companyID,
		VendorID:   s.vendorID,
		BillNumber: "BILL-DRAFT",
		Status:     models.BillStatusDraft,
		BillDate:   time.Now(),
		Amount:     decimal.NewFromInt(100),
		BalanceDue: decimal.NewFromInt(100),
	})

	bills, err := ListOpenBillsForVendor(db, s.companyID, s.vendorID)
	if err != nil {
		t.Fatalf("ListOpenBillsForVendor: %v", err)
	}
	if len(bills) != 1 {
		t.Errorf("Expected 1 open bill, got %d", len(bills))
	}
	if bills[0].BillNumber != "BILL-0001" {
		t.Errorf("Expected BILL-0001, got %s", bills[0].BillNumber)
	}
}
