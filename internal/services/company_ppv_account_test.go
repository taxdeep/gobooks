// 遵循project_guide.md
package services

import (
	"testing"

	"gobooks/internal/models"
)

// The PPV setter mirrors ChangeCompanyGRIRClearingAccount's shape and
// validation strategy; these tests lock the differences that matter
// for Phase H.5: Expense OR CostOfSales root type only, company
// scope, idempotent no-op.

func TestChangeCompanyPPVAccount_SetAuditedAndPersisted(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	// Expense account is an acceptable PPV target.
	expense := models.Account{
		CompanyID:         cid,
		Code:              "5900",
		Name:              "PPV",
		RootAccountType:   models.RootExpense,
		DetailAccountType: "operating_expense",
		IsActive:          true,
	}
	if err := db.Create(&expense).Error; err != nil {
		t.Fatalf("seed expense: %v", err)
	}
	if err := ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &expense.ID, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	var co models.Company
	db.First(&co, cid)
	if co.PurchasePriceVarianceAccountID == nil || *co.PurchasePriceVarianceAccountID != expense.ID {
		t.Fatalf("persisted: got %v want %d", co.PurchasePriceVarianceAccountID, expense.ID)
	}
	var logs []models.AuditLog
	db.Where("entity_type = ? AND action = ?",
		"company", "company.ppv_account.set").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("audit rows: got %d want 1", len(logs))
	}
}

func TestChangeCompanyPPVAccount_AcceptsCostOfSales(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	cogs := models.Account{
		CompanyID:         cid,
		Code:              "5000",
		Name:              "COGS PPV",
		RootAccountType:   models.RootCostOfSales,
		DetailAccountType: models.DetailCostOfGoodsSold,
		IsActive:          true,
	}
	db.Create(&cogs)
	if err := ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &cogs.ID, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	var co models.Company
	db.First(&co, cid)
	if co.PurchasePriceVarianceAccountID == nil || *co.PurchasePriceVarianceAccountID != cogs.ID {
		t.Fatalf("persisted: got %v want %d", co.PurchasePriceVarianceAccountID, cogs.ID)
	}
}

func TestChangeCompanyPPVAccount_RejectsLiability(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	liab := seedLiabilityAccount(t, db, cid, "2200")
	err := ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &liab, Actor: "admin@test",
	})
	if !isErr(err, ErrPPVAccountInvalid) {
		t.Fatalf("got %v want ErrPPVAccountInvalid (liability rejected)", err)
	}
}

func TestChangeCompanyPPVAccount_RejectsAsset(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	asset := seedAssetAccount(t, db, cid, "1500")
	err := ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &asset, Actor: "admin@test",
	})
	if !isErr(err, ErrPPVAccountInvalid) {
		t.Fatalf("got %v want ErrPPVAccountInvalid (asset rejected)", err)
	}
}

func TestChangeCompanyPPVAccount_RejectsCrossCompany(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	other := models.Company{Name: "other", IsActive: true}
	db.Create(&other)
	foreignExpense := models.Account{
		CompanyID: other.ID, Code: "5900", Name: "PPV",
		RootAccountType: models.RootExpense, DetailAccountType: "operating_expense",
		IsActive: true,
	}
	db.Create(&foreignExpense)
	err := ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &foreignExpense.ID, Actor: "admin@test",
	})
	if !isErr(err, ErrPPVAccountInvalid) {
		t.Fatalf("got %v want ErrPPVAccountInvalid (cross-company rejected)", err)
	}
}

func TestChangeCompanyPPVAccount_NoOpWhenSame(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	expense := models.Account{
		CompanyID: cid, Code: "5900", Name: "PPV",
		RootAccountType: models.RootExpense, DetailAccountType: "operating_expense",
		IsActive: true,
	}
	db.Create(&expense)
	_ = ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &expense.ID, Actor: "admin@test",
	})
	if err := ChangeCompanyPPVAccount(db, ChangeCompanyPPVAccountInput{
		CompanyID: cid, AccountID: &expense.ID, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("no-op: %v", err)
	}
	var rows int64
	db.Model(&models.AuditLog{}).
		Where("action LIKE ?", "company.ppv_account.%").
		Count(&rows)
	if rows != 1 {
		t.Fatalf("audit rows: got %d want 1 (first set only)", rows)
	}
}
