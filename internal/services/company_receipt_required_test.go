// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// testReceiptRequiredDB spins a minimum in-memory DB with the subset
// of models H.1 touches: Company (the rail target) and AuditLog (the
// audit trail sink). Kept narrow so the four tests below can be read
// top-down without chasing shared fixtures.
func testReceiptRequiredDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rcpt_req_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.AuditLog{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func seedReceiptRequiredCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "rcpt-req-co", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	return co.ID
}

// Default on fresh company must be FALSE — migration-safe guarantee
// so existing customers keep Phase G byte-identical behavior when
// the column lands.
func TestChangeCompanyReceiptRequired_DefaultFalseOnNewCompany(t *testing.T) {
	db := testReceiptRequiredDB(t)
	cid := seedReceiptRequiredCompany(t, db)

	var company models.Company
	if err := db.First(&company, cid).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if company.ReceiptRequired {
		t.Fatalf("expected ReceiptRequired=false on fresh company, got true")
	}
}

// Enabling writes exactly one audit row with the enabled action and
// persists the flip. Actor and company identity are captured.
func TestChangeCompanyReceiptRequired_EnableWritesAudit(t *testing.T) {
	db := testReceiptRequiredDB(t)
	cid := seedReceiptRequiredCompany(t, db)

	if err := ChangeCompanyReceiptRequired(db, ChangeCompanyReceiptRequiredInput{
		CompanyID: cid, Required: true, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	var company models.Company
	if err := db.First(&company, cid).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if !company.ReceiptRequired {
		t.Fatalf("expected ReceiptRequired=true after enable")
	}

	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"company", cid, "company.receipt_required.enabled").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("audit: got %d rows want 1 (%+v)", len(logs), logs)
	}
	if logs[0].Actor != "admin@test" {
		t.Fatalf("audit actor: got %q want admin@test", logs[0].Actor)
	}
}

// Disabling writes exactly one audit row with the disabled action and
// persists the flip back to FALSE. Exercises the symmetric path so a
// company that was flipped on during engineering verification can be
// wound back down cleanly.
func TestChangeCompanyReceiptRequired_DisableWritesAudit(t *testing.T) {
	db := testReceiptRequiredDB(t)
	cid := seedReceiptRequiredCompany(t, db)

	// Seed state: rail is ON.
	if err := ChangeCompanyReceiptRequired(db, ChangeCompanyReceiptRequiredInput{
		CompanyID: cid, Required: true, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("seed enable: %v", err)
	}

	// Flip OFF.
	if err := ChangeCompanyReceiptRequired(db, ChangeCompanyReceiptRequiredInput{
		CompanyID: cid, Required: false, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	var company models.Company
	if err := db.First(&company, cid).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if company.ReceiptRequired {
		t.Fatalf("expected ReceiptRequired=false after disable")
	}

	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"company", cid, "company.receipt_required.disabled").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("disable audit: got %d rows want 1 (%+v)", len(logs), logs)
	}
}

// No-op flip (already in target state) must produce neither a column
// update nor an audit row. Protects against audit-noise on idempotent
// admin calls.
func TestChangeCompanyReceiptRequired_NoOpWhenAlreadyInState(t *testing.T) {
	db := testReceiptRequiredDB(t)
	cid := seedReceiptRequiredCompany(t, db) // default FALSE

	// Flip to current state (FALSE → FALSE).
	if err := ChangeCompanyReceiptRequired(db, ChangeCompanyReceiptRequiredInput{
		CompanyID: cid, Required: false, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("no-op disable: %v", err)
	}

	var logs int64
	db.Model(&models.AuditLog{}).
		Where("entity_type = ? AND action LIKE ?",
			"company", "company.receipt_required.%").Count(&logs)
	if logs != 0 {
		t.Fatalf("no-op flip must not write audit: got %d rows", logs)
	}

	// Same for TRUE → TRUE: first flip on, then call again with TRUE.
	if err := ChangeCompanyReceiptRequired(db, ChangeCompanyReceiptRequiredInput{
		CompanyID: cid, Required: true, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	// After enable: expect exactly one audit row (the enable).
	db.Model(&models.AuditLog{}).
		Where("entity_type = ? AND action LIKE ?",
			"company", "company.receipt_required.%").Count(&logs)
	if logs != 1 {
		t.Fatalf("post-enable rows: got %d want 1", logs)
	}
	// No-op TRUE → TRUE.
	if err := ChangeCompanyReceiptRequired(db, ChangeCompanyReceiptRequiredInput{
		CompanyID: cid, Required: true, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("no-op enable: %v", err)
	}
	db.Model(&models.AuditLog{}).
		Where("entity_type = ? AND action LIKE ?",
			"company", "company.receipt_required.%").Count(&logs)
	if logs != 1 {
		t.Fatalf("no-op enable must not write audit: got %d rows want 1", logs)
	}
}
