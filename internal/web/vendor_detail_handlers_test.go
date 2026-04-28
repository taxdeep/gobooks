// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// TestVendorDetailPageTabs locks the tab-based layout introduced in
// the QB-style rewrite. Asserts:
//
//   - Default view (no ?tab= query) = Transactions tab. Compact
//     header renders (avatar, name, contact grid, financial summary).
//     Transactions table lists the vendor's bills and expenses.
//   - ?tab=purchase-orders shows the PO pipeline.
//   - Cross-tenant bills stay hidden.
//
// This is the AP mirror of TestCustomerDetailPageHappyPath.
func TestVendorDetailPageTabs(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Vendor Detail Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	vendorID := seedVendor(t, db, companyID, "Test Vendor")
	otherVendorID := seedVendor(t, db, companyID, "Other Vendor")

	// Vendor profile tweaks so the contact grid has something to show.
	if err := db.Model(&models.Vendor{}).
		Where("id = ?", vendorID).
		Updates(map[string]any{
			"email":                     "vendor@example.com",
			"phone":                     "+1-555-0100",
			"address":                   "100 Supply Ln, Seattle, WA",
			"default_payment_term_code": "N30",
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.PaymentTerm{
		CompanyID:   companyID,
		Code:        "N30",
		Description: "Net 30",
		IsDefault:   true,
		IsActive:    true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// Seed bills — two for this vendor, one for a different vendor.
	bills := []models.Bill{
		{
			CompanyID:    companyID,
			VendorID:     vendorID,
			BillNumber:   "BILL-VEND-001",
			BillDate:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			Amount:       decimal.RequireFromString("250.00"),
			BalanceDue:   decimal.RequireFromString("250.00"),
			Status:       models.BillStatusPosted,
			CurrencyCode: "CAD",
		},
		{
			CompanyID:    companyID,
			VendorID:     vendorID,
			BillNumber:   "BILL-VEND-002",
			BillDate:     time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
			Amount:       decimal.RequireFromString("120.00"),
			BalanceDue:   decimal.Zero,
			Status:       models.BillStatusPaid,
			CurrencyCode: "CAD",
		},
		{
			CompanyID:    companyID,
			VendorID:     otherVendorID,
			BillNumber:   "BILL-OTHER-001",
			BillDate:     time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC),
			Amount:       decimal.RequireFromString("999.00"),
			BalanceDue:   decimal.RequireFromString("999.00"),
			Status:       models.BillStatusPosted,
			CurrencyCode: "CAD",
		},
	}
	for _, b := range bills {
		bill := b
		if err := db.Create(&bill).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Seed a purchase order to verify the PO tab renders content.
	poDate := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.PurchaseOrder{
		CompanyID:    companyID,
		VendorID:     vendorID,
		PONumber:     "PO-VEND-001",
		PODate:       poDate,
		Amount:       decimal.RequireFromString("500.00"),
		Status:       models.POStatusConfirmed,
		CurrencyCode: "CAD",
	}).Error; err != nil {
		t.Fatal(err)
	}

	app := testRouteApp(t, db)

	// Default tab = transactions. Check header + transactions rows +
	// tab strip + isolation from other vendor.
	resp := performRequest(t, app, fmt.Sprintf("/vendors/%d", vendorID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default tab status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		// Header
		"Test Vendor",
		"vendor@example.com",
		"+1-555-0100",
		"Net 30",
		// Financial summary + tabs
		"Financial summary",
		"Open balance",
		"tab=transactions",
		"tab=purchase-orders",
		"tab=details",
		// Transactions table
		"BILL-VEND-001",
		"BILL-VEND-002",
		// CTA in header
		fmt.Sprintf("/bills/new?vendor_id=%d", vendorID),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("default vendor detail tab missing %q", want)
		}
	}
	if strings.Contains(body, "BILL-OTHER-001") {
		t.Fatalf("other-vendor bill leaked onto vendor detail page")
	}

	// Purchase Orders tab still surfaces the PO.
	respPO := performRequest(t, app, fmt.Sprintf("/vendors/%d?tab=purchase-orders", vendorID), rawToken)
	if respPO.StatusCode != http.StatusOK {
		t.Fatalf("PO tab status = %d", respPO.StatusCode)
	}
	poBody := readResponseBody(t, respPO)
	for _, want := range []string{
		"Recent Purchase Orders",
		"PO-VEND-001",
	} {
		if !strings.Contains(poBody, want) {
			t.Fatalf("PO tab missing %q", want)
		}
	}
}

func TestVendorDetailsTabRequiresEditMode(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Vendor Details Edit Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	vendorID := seedVendor(t, db, companyID, "Editable Vendor")
	if err := db.Model(&models.Vendor{}).
		Where("id = ?", vendorID).
		Updates(map[string]any{
			"email":   "editable-vendor@example.com",
			"phone":   "+1-555-0200",
			"address": "789 Vendor Way",
			"notes":   "Preferred shipping window: morning.",
		}).Error; err != nil {
		t.Fatal(err)
	}

	app := testRouteApp(t, db)
	readResp := performRequest(t, app, fmt.Sprintf("/vendors/%d?tab=details", vendorID), rawToken)
	if readResp.StatusCode != http.StatusOK {
		t.Fatalf("details read mode status = %d, want %d", readResp.StatusCode, http.StatusOK)
	}
	readBody := readResponseBody(t, readResp)
	for _, want := range []string{
		"Vendor Details",
		"Editable Vendor",
		"editable-vendor@example.com",
		"+1-555-0200",
		"789 Vendor Way",
		"edit=1",
	} {
		if !strings.Contains(readBody, want) {
			t.Fatalf("read-only vendor details missing %q", want)
		}
	}
	for _, notWant := range []string{
		`name="name"`,
		"Deactivate vendor",
		fmt.Sprintf("/vendors/%d/deactivate", vendorID),
	} {
		if strings.Contains(readBody, notWant) {
			t.Fatalf("read-only vendor details should not contain %q", notWant)
		}
	}

	editResp := performRequest(t, app, fmt.Sprintf("/vendors/%d?tab=details&edit=1", vendorID), rawToken)
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("details edit mode status = %d, want %d", editResp.StatusCode, http.StatusOK)
	}
	editBody := readResponseBody(t, editResp)
	for _, want := range []string{
		`name="name"`,
		"Save",
		"Cancel",
		"Deactivate vendor",
		fmt.Sprintf("/vendors/%d/deactivate", vendorID),
	} {
		if !strings.Contains(editBody, want) {
			t.Fatalf("edit vendor details missing %q", want)
		}
	}
}
