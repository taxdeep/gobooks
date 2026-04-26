package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"gobooks/internal/models"
)

func TestSmartPickerUsage_PersistsSelectionEvent(t *testing.T) {
	db := testRouteDB(t)
	if err := db.AutoMigrate(&models.SmartPickerUsage{}); err != nil {
		t.Fatal(err)
	}

	companyID := seedCompany(t, db, "Picker Usage Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	accountID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)

	app := testRouteApp(t, db)
	payload, err := json.Marshal(map[string]any{
		"entity":        "account",
		"context":       "expense_form_category",
		"item_id":       fmt.Sprintf("%d", accountID),
		"event_type":    "select",
		"rank_position": 2,
		"result_count":  5,
		"query":         "office",
		"request_id":    "req-usage-001",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "/api/smart-picker/usage", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CSRFHeaderName, "csrf-usage-001")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-usage-001", Path: "/"})

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var usage models.SmartPickerUsage
	if err := db.Where("company_id = ? AND entity = ? AND context = ? AND item_id = ?",
		companyID, "account", "expense_form_category", accountID).
		First(&usage).Error; err != nil {
		t.Fatalf("expected persisted usage row, got %v", err)
	}
	if usage.RequestID != "req-usage-001" {
		t.Fatalf("expected request_id to persist, got %q", usage.RequestID)
	}
	var event models.SmartPickerEvent
	if err := db.Where("company_id = ? AND context = ? AND entity_type = ? AND selected_entity_id = ?",
		companyID, "expense_form_category", "account", accountID).
		First(&event).Error; err != nil {
		t.Fatalf("expected smart picker event, got %v", err)
	}
	var companyStat models.SmartPickerUsageStat
	if err := db.Where("company_id = ? AND scope_type = ? AND context = ? AND entity_type = ? AND entity_id = ?",
		companyID, models.SmartPickerScopeCompany, "expense_form_category", "account", accountID).
		First(&companyStat).Error; err != nil {
		t.Fatalf("expected company usage stat, got %v", err)
	}
	if companyStat.SelectCount != 1 || companyStat.AvgRankPosition.InexactFloat64() != 2 {
		t.Fatalf("unexpected company stat: %+v", companyStat)
	}
}

func TestSmartPickerUsage_PersistsVendorContext(t *testing.T) {
	db := testRouteDB(t)
	if err := db.AutoMigrate(&models.SmartPickerUsage{}); err != nil {
		t.Fatal(err)
	}

	companyID := seedCompany(t, db, "Picker Vendor Usage Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	vendorID := seedSPVendor(t, db, companyID, "North Supplies", "north@example.com", "")

	app := testRouteApp(t, db)
	payload, err := json.Marshal(map[string]any{
		"entity":     "vendor",
		"context":    "expense_form_vendor",
		"item_id":    fmt.Sprintf("%d", vendorID),
		"request_id": "req-vendor-usage-001",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "/api/smart-picker/usage", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CSRFHeaderName, "csrf-vendor-usage-001")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-vendor-usage-001", Path: "/"})

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var usage models.SmartPickerUsage
	if err := db.Where("company_id = ? AND entity = ? AND context = ? AND item_id = ?",
		companyID, "vendor", "expense_form_vendor", vendorID).
		First(&usage).Error; err != nil {
		t.Fatalf("expected persisted vendor usage row, got %v", err)
	}
	if usage.RequestID != "req-vendor-usage-001" {
		t.Fatalf("expected vendor request_id to persist, got %q", usage.RequestID)
	}
}

func TestSmartPickerAcceleration_SearchRanksByUsage(t *testing.T) {
	db := testRouteDB(t)
	if err := db.AutoMigrate(&models.SmartPickerUsage{}); err != nil {
		t.Fatal(err)
	}

	companyID := seedCompany(t, db, "Picker Ranking Co")
	officeID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	travelID := seedSPAccount(t, db, companyID, "6200", "Travel", models.RootExpense, true)

	now := time.Now().UTC()
	if err := db.Create(&models.SmartPickerUsageStat{
		CompanyID:      companyID,
		ScopeType:      models.SmartPickerScopeCompany,
		Context:        "expense_form_category",
		EntityType:     "account",
		EntityID:       travelID,
		SelectCount:    3,
		LastSelectedAt: &now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SmartPickerUsageStat{
		CompanyID:      companyID,
		ScopeType:      models.SmartPickerScopeCompany,
		Context:        "expense_form_category",
		EntityType:     "account",
		EntityID:       officeID,
		SelectCount:    1,
		LastSelectedAt: &now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	acceleration := NewSmartPickerAcceleration()
	t.Cleanup(acceleration.cache.Close)

	result, source, err := acceleration.Search(db, &ExpenseAccountProvider{}, SmartPickerContext{
		CompanyID: companyID,
		Context:   "expense_form_category",
		Limit:     20,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if source != "ranked" {
		t.Fatalf("expected ranked source, got %q", source)
	}
	if len(result.Candidates) < 2 {
		t.Fatalf("expected ranked results, got %+v", result.Candidates)
	}
	if result.Candidates[0].ID != fmt.Sprintf("%d", travelID) {
		t.Fatalf("expected most-used account %d first, got %+v", travelID, result.Candidates)
	}

	_, cachedSource, err := acceleration.Search(db, &ExpenseAccountProvider{}, SmartPickerContext{
		CompanyID: companyID,
		Context:   "expense_form_category",
		Limit:     20,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cachedSource != "cache" {
		t.Fatalf("expected second lookup to hit cache, got %q", cachedSource)
	}
}

func TestSmartPickerAcceleration_InvalidateCompanyFlushesOnlyMatchingCompany(t *testing.T) {
	acceleration := NewSmartPickerAcceleration()
	t.Cleanup(acceleration.cache.Close)

	acceleration.cache.Set(spCacheKey("product_service", "task_form_service_item", 1, "", 20), &SmartPickerResult{
		Candidates: []SmartPickerItem{{ID: "10", Payload: map[string]string{"default_price": "50.00"}}},
	})
	acceleration.cache.Set(spCacheKey("product_service", "task_form_service_item", 2, "", 20), &SmartPickerResult{
		Candidates: []SmartPickerItem{{ID: "20", Payload: map[string]string{"default_price": "75.00"}}},
	})

	acceleration.InvalidateCompany(1)

	if _, ok := acceleration.cache.Get(spCacheKey("product_service", "task_form_service_item", 1, "", 20)); ok {
		t.Fatal("expected company 1 cache entry to be invalidated")
	}
	if _, ok := acceleration.cache.Get(spCacheKey("product_service", "task_form_service_item", 2, "", 20)); !ok {
		t.Fatal("expected company 2 cache entry to remain")
	}
}
