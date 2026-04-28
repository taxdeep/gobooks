package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"balanciz/internal/models"
)

func TestVendorQuickCreateCreatesVendorForActiveCompany(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db, SPAcceleration: NewSmartPickerAcceleration()}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Vendor Quick Create Co")
	if err := db.Create(&models.PaymentTerm{
		CompanyID: companyID, Code: "N30", Description: "Net 30",
		NetDays: 30, IsActive: true, SortOrder: 1,
	}).Error; err != nil {
		t.Fatalf("seed payment term: %v", err)
	}

	app := editorFlowApp(server, user, companyID)
	app.Post("/api/vendors/quick-create", server.handleVendorQuickCreate)

	body := map[string]string{
		"name":          "Inline Vendor",
		"email":         "ap@example.com",
		"phone":         "604-555-0199",
		"address":       "123 Main St",
		"payment_term":  "N30",
		"currency_code": "USD",
		"notes":         "Created from bill",
	}
	resp := performJSONRequest(t, app, http.MethodPost, "/api/vendors/quick-create", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quick create: status %d", resp.StatusCode)
	}

	var out struct {
		ID          uint   `json:"id"`
		Name        string `json:"name"`
		PaymentTerm string `json:"payment_term"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.ID == 0 || out.Name != "Inline Vendor" || out.PaymentTerm != "N30" {
		t.Fatalf("unexpected response: %+v", out)
	}

	var vendor models.Vendor
	if err := db.First(&vendor, out.ID).Error; err != nil {
		t.Fatalf("load vendor: %v", err)
	}
	if vendor.CompanyID != companyID || !vendor.IsActive || vendor.Email != "ap@example.com" || vendor.DefaultPaymentTermCode != "N30" {
		t.Fatalf("unexpected vendor: %+v", vendor)
	}
}

func TestVendorQuickCreateRejectsDuplicateName(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db, SPAcceleration: NewSmartPickerAcceleration()}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Vendor Duplicate Co")
	seedEditorFlowVendor(t, db, companyID, "Existing Vendor")

	app := editorFlowApp(server, user, companyID)
	app.Post("/api/vendors/quick-create", server.handleVendorQuickCreate)

	resp := performJSONRequest(t, app, http.MethodPost, "/api/vendors/quick-create", map[string]string{
		"name": "Existing Vendor",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate quick create: status %d", resp.StatusCode)
	}
}

func TestVendorProviderBillPickerExcludesInactiveVendors(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Vendor Picker Active Co")
	activeID := seedSPVendor(t, db, companyID, "Active Supplies", "active@example.com", "")
	inactiveID := seedSPVendor(t, db, companyID, "Inactive Supplies", "inactive@example.com", "")
	if err := db.Model(&models.Vendor{}).Where("id = ?", inactiveID).Update("is_active", false).Error; err != nil {
		t.Fatalf("mark inactive: %v", err)
	}

	var p VendorProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "bill.vendor_picker", Limit: 20}
	result, err := p.Search(db, ctx, "supplies")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != strconv.FormatUint(uint64(activeID), 10) {
		t.Fatalf("expected only active vendor %d, got %+v", activeID, result.Candidates)
	}
	item, err := p.GetByID(db, ctx, strconv.FormatUint(uint64(inactiveID), 10))
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Fatalf("expected inactive vendor to be hidden from bill picker, got %+v", item)
	}
}

func performJSONRequest(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, method, path string, payload any) *http.Response {
	t.Helper()

	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
