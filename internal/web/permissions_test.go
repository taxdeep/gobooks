package web

import "testing"

func TestCanPerformActionPermissionMatrix(t *testing.T) {
	tests := []struct {
		name   string
		role   string
		action string
		want   bool
	}{
		{name: "owner can update settings", role: "owner", action: ActionSettingsUpdate, want: true},
		{name: "admin can manage members", role: "admin", action: ActionMemberManage, want: true},
		{name: "accountant can approve invoices", role: "accountant", action: ActionInvoiceApprove, want: true},
		{name: "accountant cannot update settings", role: "accountant", action: ActionSettingsUpdate, want: false},
		{name: "bookkeeper can create invoices", role: "bookkeeper", action: ActionInvoiceCreate, want: true},
		{name: "bookkeeper can access reconciliation writes", role: "bookkeeper", action: ActionJournalCreate, want: true},
		{name: "bookkeeper cannot approve invoices", role: "bookkeeper", action: ActionInvoiceApprove, want: false},
		{name: "ap can pay bills", role: "ap", action: ActionBillPay, want: true},
		{name: "ap cannot access ar write flows", role: "ap", action: ActionInvoiceCreate, want: false},
		{name: "ap cannot access reconciliation writes", role: "ap", action: ActionJournalCreate, want: false},
		{name: "ap cannot view reports", role: "ap", action: ActionReportView, want: false},
		{name: "viewer can view reports", role: "viewer", action: ActionReportView, want: true},
		{name: "viewer cannot create accounts", role: "viewer", action: ActionAccountCreate, want: false},
		{name: "unknown action fails closed", role: "owner", action: "missing:action", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CanPerformAction(tc.role, tc.action)
			if got != tc.want {
				t.Fatalf("CanPerformAction(%q, %q) = %v, want %v", tc.role, tc.action, got, tc.want)
			}
		})
	}
}
