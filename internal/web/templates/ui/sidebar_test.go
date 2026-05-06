package ui_test

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/web/templates/ui"
)

func TestSidebarSettingsIncludesTemplatesEntry(t *testing.T) {
	var sb strings.Builder
	if err := ui.Sidebar(ui.SidebarVM{Active: "Templates", HasCompany: true}).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render sidebar: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`href="/settings/templates"`,
		"Templates",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected sidebar HTML to contain %q", want)
		}
	}
}

func TestSidebarInventoryHidesWorkflowEntries(t *testing.T) {
	var sb strings.Builder
	if err := ui.Sidebar(ui.SidebarVM{Active: "Warehouses", HasCompany: true}).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render sidebar: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`href="/products-services"`,
		`href="/warehouses"`,
		"Products &amp; Services",
		"Warehouses",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected sidebar HTML to contain %q", want)
		}
	}
	for _, notWant := range []string{
		`href="/inventory/transfers"`,
		`href="/inventory/stock"`,
		`href="/ar-return-receipts"`,
		`href="/vendor-return-shipments"`,
		"Warehouse Transfers",
		"Stock Report",
		"Return Receipts",
		"Returns to Vendor",
	} {
		if strings.Contains(html, notWant) {
			t.Fatalf("expected sidebar HTML not to contain %q", notWant)
		}
	}
}
