// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
)

// testGRIRDB spins a minimal DB with Company, Account, and AuditLog —
// the surface this setter touches. Kept narrow so the tests read
// top-down like the other capability-flip test suites
// (company_receipt_required_test.go, product_service_tracking_test.go).
func testGRIRDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:gr_ir_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.Account{}, &models.AuditLog{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func seedGRIRCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "gr-ir-co", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	return co.ID
}

func seedLiabilityAccount(t *testing.T, db *gorm.DB, companyID uint, code string) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              "Clearing " + code,
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("seed liability account: %v", err)
	}
	return a.ID
}

func seedAssetAccount(t *testing.T, db *gorm.DB, companyID uint, code string) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              "Asset " + code,
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailInventory,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("seed asset account: %v", err)
	}
	return a.ID
}

func TestChangeCompanyGRIRClearingAccount_SetAuditedAndPersisted(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	acctID := seedLiabilityAccount(t, db, cid, "2100")

	if err := ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: &acctID, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}

	var co models.Company
	db.First(&co, cid)
	if co.GRIRClearingAccountID == nil || *co.GRIRClearingAccountID != acctID {
		t.Fatalf("persisted: got %v want %d", co.GRIRClearingAccountID, acctID)
	}
	var logs []models.AuditLog
	db.Where("entity_type = ? AND action = ?",
		"company", "company.gr_ir_clearing_account.set").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("audit: got %d rows want 1", len(logs))
	}
}

func TestChangeCompanyGRIRClearingAccount_RejectsNonLiabilityAccount(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	assetID := seedAssetAccount(t, db, cid, "1300")
	err := ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: &assetID, Actor: "admin@test",
	})
	if !isErr(err, ErrGRIRClearingAccountInvalid) {
		t.Fatalf("got %v want ErrGRIRClearingAccountInvalid", err)
	}
}

func TestChangeCompanyGRIRClearingAccount_RejectsCrossCompanyAccount(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	// Another company + its liability account.
	other := models.Company{Name: "other", IsActive: true}
	db.Create(&other)
	foreignAcct := seedLiabilityAccount(t, db, other.ID, "2100")

	err := ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: &foreignAcct, Actor: "admin@test",
	})
	if !isErr(err, ErrGRIRClearingAccountInvalid) {
		t.Fatalf("got %v want ErrGRIRClearingAccountInvalid", err)
	}
}

func TestChangeCompanyGRIRClearingAccount_ClearAuditedAsCleared(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	acctID := seedLiabilityAccount(t, db, cid, "2100")
	_ = ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: &acctID, Actor: "admin@test",
	})
	// Clear it.
	if err := ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: nil, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var co models.Company
	db.First(&co, cid)
	if co.GRIRClearingAccountID != nil {
		t.Fatalf("expected cleared, got %d", *co.GRIRClearingAccountID)
	}
	var clearedCount int64
	db.Model(&models.AuditLog{}).
		Where("action = ?", "company.gr_ir_clearing_account.cleared").
		Count(&clearedCount)
	if clearedCount != 1 {
		t.Fatalf("cleared audit rows: got %d want 1", clearedCount)
	}
}

func TestChangeCompanyGRIRClearingAccount_NoOpWhenSame(t *testing.T) {
	db := testGRIRDB(t)
	cid := seedGRIRCompany(t, db)
	acctID := seedLiabilityAccount(t, db, cid, "2100")
	_ = ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: &acctID, Actor: "admin@test",
	})
	// Call again with the same target → no-op, no extra audit.
	if err := ChangeCompanyGRIRClearingAccount(db, ChangeCompanyGRIRClearingAccountInput{
		CompanyID: cid, AccountID: &acctID, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("no-op: %v", err)
	}
	var rows int64
	db.Model(&models.AuditLog{}).
		Where("action LIKE ?", "company.gr_ir_clearing_account.%").
		Count(&rows)
	if rows != 1 {
		t.Fatalf("audit rows: got %d want 1 (first set only)", rows)
	}
}
