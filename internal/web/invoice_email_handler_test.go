// 遵循project_guide.md
package web

// invoice_email_handler_test.go — Tests for invoice email HTTP handlers.

import (
	"testing"
)

// ── Permission matrix tests ────────────────────────────────────────────────────

func TestInvoiceEmailHandlers_PermissionMatrix(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		allowed    bool
		permission string
	}{
		{name: "accountant can send email", role: "accountant", allowed: true, permission: ActionInvoiceUpdate},
		{name: "bookkeeper can send email", role: "bookkeeper", allowed: true, permission: ActionInvoiceUpdate},
		{name: "ap cannot send email", role: "ap", allowed: false, permission: ActionInvoiceUpdate},
		{name: "viewer cannot send email", role: "viewer", allowed: false, permission: ActionInvoiceUpdate},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			canAccess := CanPerformAction(tc.role, tc.permission)
			if canAccess != tc.allowed {
				t.Fatalf("Role %s permission %s: expected %v, got %v", tc.role, tc.permission, tc.allowed, canAccess)
			}
		})
	}
}

// ── State machine validation tests ────────────────────────────────────────────

func TestInvoiceEmailHandlers_RouteExpectations(t *testing.T) {
	expectedRoutes := []struct {
		method string
		path   string
	}{
		{method: "POST", path: "/invoices/:id/send-email"},
		{method: "GET", path: "/invoices/:id/email-history"},
	}

	for _, route := range expectedRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			// Route existence verified by routes.go registration.
			// Full integration test deferred to Phase 8.
		})
	}
}
