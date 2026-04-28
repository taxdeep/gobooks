// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// testFeaturesDB spins a minimum DB footprint for company-feature
// tests. Only Company, CompanyFeature, and AuditLog are required
// — the service does not touch inventory / bills / receipts.
func testFeaturesDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:features_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.CompanyFeature{}, &models.AuditLog{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func seedFeatureCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "feat-co", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	return co.ID
}

// validEnableInput returns a well-formed EnableCompanyFeatureInput
// for Inventory Alpha. Tests that exercise specific failure modes
// mutate exactly one field and leave the rest valid.
func validEnableInput(companyID uint, actorID uuid.UUID) EnableCompanyFeatureInput {
	return EnableCompanyFeatureInput{
		CompanyID:         companyID,
		FeatureKey:        models.FeatureKeyInventory,
		Actor:             "owner@test",
		ActorUserID:       &actorID,
		ActorRole:         models.CompanyRoleOwner,
		ReasonCode:        models.ReasonCodeTrialPilot,
		ReasonNote:        "exploring receipt-first",
		AckVersion:        models.AckVersionInventoryAlphaV1,
		TypedConfirmation: "ENABLE INVENTORY",
		ConfirmAcknowledgements: []bool{true, true, true},
	}
}

// ── Happy paths ──────────────────────────────────────────────────────────────

func TestEnableCompanyFeature_InventoryByOwner_Succeeds(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()

	if err := EnableCompanyFeature(db, validEnableInput(cid, uid)); err != nil {
		t.Fatalf("enable: %v", err)
	}
	// Row should be enabled with audit fields populated.
	var row models.CompanyFeature
	if err := db.Where("company_id = ? AND feature_key = ?",
		cid, models.FeatureKeyInventory).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Status != models.FeatureStatusEnabled {
		t.Fatalf("status: got %q want enabled", row.Status)
	}
	if row.AckVersion != models.AckVersionInventoryAlphaV1 {
		t.Fatalf("ack_version: got %q want %q", row.AckVersion, models.AckVersionInventoryAlphaV1)
	}
	if row.ReasonCode != models.ReasonCodeTrialPilot {
		t.Fatalf("reason_code: got %q", row.ReasonCode)
	}
	if row.EnabledByUserID == nil || *row.EnabledByUserID != uid {
		t.Fatalf("enabled_by_user_id: got %v", row.EnabledByUserID)
	}
	if row.EnabledAt == nil {
		t.Fatalf("enabled_at not set")
	}
	if row.AcknowledgedAt == nil {
		t.Fatalf("acknowledged_at not set")
	}
	// Two audit rows: ack + enable.
	var logs []models.AuditLog
	db.Where("entity_type = ?", "company_feature").
		Order("id").Find(&logs)
	if len(logs) != 2 {
		t.Fatalf("audit rows: got %d want 2 (ack + enable)", len(logs))
	}
	if logs[0].Action != "company.feature.inventory.warning_acknowledged" {
		t.Fatalf("first audit action: got %q", logs[0].Action)
	}
	if logs[1].Action != "company.feature.inventory.enabled" {
		t.Fatalf("second audit action: got %q", logs[1].Action)
	}
}

func TestDisableCompanyFeature_ByOwner_Succeeds(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	_ = EnableCompanyFeature(db, validEnableInput(cid, uid))

	if err := DisableCompanyFeature(db, DisableCompanyFeatureInput{
		CompanyID:   cid,
		FeatureKey:  models.FeatureKeyInventory,
		Actor:       "owner@test",
		ActorUserID: &uid,
		ActorRole:   models.CompanyRoleOwner,
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	var row models.CompanyFeature
	db.Where("company_id = ? AND feature_key = ?",
		cid, models.FeatureKeyInventory).First(&row)
	if row.Status != models.FeatureStatusOff {
		t.Fatalf("status: got %q want off", row.Status)
	}
	// Historical fields preserved.
	if row.EnabledAt == nil {
		t.Fatalf("enabled_at cleared — history must be preserved")
	}
	var disableLogs []models.AuditLog
	db.Where("action = ?", "company.feature.inventory.disabled").Find(&disableLogs)
	if len(disableLogs) != 1 {
		t.Fatalf("disable audit rows: got %d want 1", len(disableLogs))
	}
}

// ── Owner-required gate ──────────────────────────────────────────────────────

func TestEnableCompanyFeature_NonOwner_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.ActorRole = models.CompanyRoleAdmin

	err := EnableCompanyFeature(db, in)
	if err != ErrFeatureOwnerRequired {
		t.Fatalf("got %v want ErrFeatureOwnerRequired", err)
	}
	// No row created, no audit written.
	var n int64
	db.Model(&models.CompanyFeature{}).Count(&n)
	if n != 0 {
		t.Fatalf("rows: got %d want 0 (enable rejected before write)", n)
	}
}

func TestDisableCompanyFeature_NonOwner_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	_ = EnableCompanyFeature(db, validEnableInput(cid, uid))

	err := DisableCompanyFeature(db, DisableCompanyFeatureInput{
		CompanyID:  cid,
		FeatureKey: models.FeatureKeyInventory,
		ActorRole:  models.CompanyRoleBookkeeper,
	})
	if err != ErrFeatureOwnerRequired {
		t.Fatalf("got %v want ErrFeatureOwnerRequired", err)
	}
	// Feature still enabled.
	enabled, _ := IsCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory)
	if !enabled {
		t.Fatalf("feature flipped off despite owner rejection")
	}
}

// ── Typed confirmation + acks ────────────────────────────────────────────────

func TestEnableCompanyFeature_TypedConfirmationWrong_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.TypedConfirmation = "enable inventory" // wrong case

	if err := EnableCompanyFeature(db, in); err != ErrFeatureTypedConfirmationMismatch {
		t.Fatalf("got %v want ErrFeatureTypedConfirmationMismatch", err)
	}
}

func TestEnableCompanyFeature_MissingAck_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.ConfirmAcknowledgements = []bool{true, false, true} // one false

	if err := EnableCompanyFeature(db, in); err != ErrFeatureAcknowledgementsIncomplete {
		t.Fatalf("got %v want ErrFeatureAcknowledgementsIncomplete", err)
	}
}

func TestEnableCompanyFeature_ShortAckSlice_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.ConfirmAcknowledgements = []bool{true, true} // missing one entry

	if err := EnableCompanyFeature(db, in); err != ErrFeatureAcknowledgementsIncomplete {
		t.Fatalf("got %v want ErrFeatureAcknowledgementsIncomplete", err)
	}
}

func TestEnableCompanyFeature_WrongAckVersion_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.AckVersion = "inventory-alpha-v99" // stale / forged

	if err := EnableCompanyFeature(db, in); err != ErrFeatureAcknowledgementsIncomplete {
		t.Fatalf("got %v want ErrFeatureAcknowledgementsIncomplete (wrong ack_version)", err)
	}
}

// ── Feature registry gates ───────────────────────────────────────────────────

func TestEnableCompanyFeature_TaskFeature_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.FeatureKey = models.FeatureKeyTask
	in.TypedConfirmation = "" // task has no typed text anyway

	if err := EnableCompanyFeature(db, in); err != ErrFeatureNotSelfServe {
		t.Fatalf("got %v want ErrFeatureNotSelfServe (Task is coming_soon)", err)
	}
}

func TestEnableCompanyFeature_UnknownKey_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.FeatureKey = models.FeatureKey("made_up")

	if err := EnableCompanyFeature(db, in); err != ErrFeatureUnknown {
		t.Fatalf("got %v want ErrFeatureUnknown", err)
	}
}

func TestEnableCompanyFeature_BadReasonCode_Rejected(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	in := validEnableInput(cid, uid)
	in.ReasonCode = models.ReasonCode("")

	if err := EnableCompanyFeature(db, in); err != ErrFeatureReasonCodeInvalid {
		t.Fatalf("got %v want ErrFeatureReasonCodeInvalid", err)
	}
}

// ── Idempotency ──────────────────────────────────────────────────────────────

func TestEnableCompanyFeature_Idempotent_WhenAlreadyEnabled(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	_ = EnableCompanyFeature(db, validEnableInput(cid, uid))

	// Second enable: should no-op. No new audit rows written beyond
	// the first call's two (ack + enable).
	if err := EnableCompanyFeature(db, validEnableInput(cid, uid)); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	var auditCount int64
	db.Model(&models.AuditLog{}).
		Where("entity_type = ?", "company_feature").
		Count(&auditCount)
	if auditCount != 2 {
		t.Fatalf("audit rows: got %d want 2 (second enable must be a no-op)", auditCount)
	}
}

func TestDisableCompanyFeature_Idempotent_WhenAlreadyOff(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)

	// Disable against a company that never enabled: must no-op, no
	// audit, no error.
	if err := DisableCompanyFeature(db, DisableCompanyFeatureInput{
		CompanyID:  cid,
		FeatureKey: models.FeatureKeyInventory,
		ActorRole:  models.CompanyRoleOwner,
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	var n int64
	db.Model(&models.AuditLog{}).Count(&n)
	if n != 0 {
		t.Fatalf("audit rows: got %d want 0 (disable on absent row must no-op)", n)
	}
}

// ── Re-enable after disable ──────────────────────────────────────────────────

func TestEnableCompanyFeature_ReEnableAfterDisable_Succeeds(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()
	_ = EnableCompanyFeature(db, validEnableInput(cid, uid))
	_ = DisableCompanyFeature(db, DisableCompanyFeatureInput{
		CompanyID: cid, FeatureKey: models.FeatureKeyInventory, ActorRole: models.CompanyRoleOwner,
	})

	if err := EnableCompanyFeature(db, validEnableInput(cid, uid)); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	enabled, _ := IsCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory)
	if !enabled {
		t.Fatalf("expected re-enabled")
	}
}

// ── GetCompanyFeatures (read surface) ───────────────────────────────────────

func TestGetCompanyFeatures_ReturnsAllRegisteredWithCurrentState(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)

	// Before enable: both features returned; both off.
	views, err := GetCompanyFeatures(db, cid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(views) != len(models.AllCompanyFeatureDefinitions()) {
		t.Fatalf("views: got %d want %d", len(views), len(models.AllCompanyFeatureDefinitions()))
	}
	for _, v := range views {
		if v.IsEnabled() {
			t.Fatalf("feature %q enabled on fresh company", v.Key)
		}
	}

	// After enabling Inventory: view reflects it; Task still off.
	uid := uuid.New()
	_ = EnableCompanyFeature(db, validEnableInput(cid, uid))
	views, _ = GetCompanyFeatures(db, cid)
	for _, v := range views {
		switch v.Key {
		case models.FeatureKeyInventory:
			if !v.IsEnabled() {
				t.Fatalf("Inventory should be enabled")
			}
			if v.AckVersionStored != models.AckVersionInventoryAlphaV1 {
				t.Fatalf("AckVersionStored: got %q", v.AckVersionStored)
			}
		case models.FeatureKeyTask:
			if v.IsEnabled() || v.SelfServeEnable {
				t.Fatalf("Task must remain off and non-self-serve")
			}
		}
	}
}

// ── Backend guard proofs ─────────────────────────────────────────────────────

// IsCompanyFeatureEnabled is the low-level bool helper.
func TestIsCompanyFeatureEnabled_DefaultFalseThenTrueAfterEnable(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)

	enabled, err := IsCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory)
	if err != nil {
		t.Fatalf("is enabled default: %v", err)
	}
	if enabled {
		t.Fatalf("default must be false (row absent)")
	}

	uid := uuid.New()
	_ = EnableCompanyFeature(db, validEnableInput(cid, uid))
	enabled, _ = IsCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory)
	if !enabled {
		t.Fatalf("post-enable must be true")
	}
}

// RequireCompanyFeatureEnabled is the backend guard that composable
// downstream entry points call. This is the "backend guard is not a
// shell" proof required by the spec: a service call that should be
// gated rejects with a dedicated sentinel when the feature is off,
// and passes when on — regardless of what the UI did or did not
// hide.
func TestRequireCompanyFeatureEnabled_InventoryGuardProof(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	uid := uuid.New()

	// Off (absent row) → rejected with the dedicated sentinel.
	if err := RequireCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory); err != ErrInventoryAlphaNotEnabled {
		t.Fatalf("off state: got %v want ErrInventoryAlphaNotEnabled", err)
	}

	// Enable the feature.
	if err := EnableCompanyFeature(db, validEnableInput(cid, uid)); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// On → guard opens.
	if err := RequireCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory); err != nil {
		t.Fatalf("enabled state: got %v want nil", err)
	}

	// Disable again.
	_ = DisableCompanyFeature(db, DisableCompanyFeatureInput{
		CompanyID: cid, FeatureKey: models.FeatureKeyInventory, ActorRole: models.CompanyRoleOwner,
	})

	// Back off → rejected again. History (row present with prior
	// enabled_at) is retained but current state gates the guard.
	if err := RequireCompanyFeatureEnabled(db, cid, models.FeatureKeyInventory); err != ErrInventoryAlphaNotEnabled {
		t.Fatalf("post-disable: got %v want ErrInventoryAlphaNotEnabled", err)
	}
}

func TestRequireCompanyFeatureEnabled_UnknownKey_ReturnsErrUnknown(t *testing.T) {
	db := testFeaturesDB(t)
	cid := seedFeatureCompany(t, db)
	if err := RequireCompanyFeatureEnabled(db, cid, models.FeatureKey("nonsense")); err != ErrFeatureUnknown {
		t.Fatalf("got %v want ErrFeatureUnknown", err)
	}
}
