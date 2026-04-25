// Package numbering holds business display numbering configuration (prefix, sequence, padding).
// It does not manage internal immutable entity numbers (e.g. EN…); those are backend-only.
package numbering

import (
	"fmt"
	"strings"
)

// Module keys are stable identifiers for routing, storage, and future APIs.
const (
	ModuleJournalEntry  = "journal_entry"
	ModuleInvoice       = "invoice"
	ModulePayment       = "payment"
	ModuleCustomer      = "customer"
	ModuleVendor        = "vendor"
	ModulePurchaseOrder = "purchase_order"
	ModuleSalesOrder    = "sales_order"
	ModuleQuote         = "quote"
	ModuleExpense       = "expense"
	// ModuleCustomerDeposit drives the human-readable number for
	// customer deposits (money held on behalf of a customer — see
	// models.CustomerDeposit). Default format: DEP0001, DEP0002, …
	ModuleCustomerDeposit = "customer_deposit"
)

// DisplayRule describes how user-visible document/reference numbers are formatted for one module.
// This is separate from internal database identifiers and entity_number values.
type DisplayRule struct {
	ModuleKey       string `json:"module_key"`
	ModuleName      string `json:"module_name"`
	Prefix          string `json:"prefix"`
	NextNumber      int    `json:"next_number"`
	PaddingLength   int    `json:"padding_length"`
	Enabled         bool   `json:"enabled"`
}

// DefaultDisplayRules returns the built-in defaults when no file exists yet.
func DefaultDisplayRules() []DisplayRule {
	return []DisplayRule{
		{ModuleKey: ModuleJournalEntry, ModuleName: "Journal Entry", Prefix: "JE-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModuleInvoice, ModuleName: "Invoice", Prefix: "INV-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModulePayment, ModuleName: "Payment", Prefix: "PMT-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModuleCustomer, ModuleName: "Customer", Prefix: "CUST-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModuleVendor, ModuleName: "Vendor", Prefix: "VEN-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		// Phase: document-reference numbering for procurement + sales +
		// expense. PO / SO / Quote already have their *Number columns
		// populated by scan-last-and-increment; settings here drive the
		// FALLBACK used when no prior document exists (first-ever number
		// per company) plus offer a future override path. Expense has
		// no prior number and is driven entirely by these settings.
		{ModuleKey: ModulePurchaseOrder, ModuleName: "Purchase Order", Prefix: "PO-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModuleSalesOrder, ModuleName: "Sales Order", Prefix: "SO-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModuleQuote, ModuleName: "Quote", Prefix: "QUO-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		{ModuleKey: ModuleExpense, ModuleName: "Expense", Prefix: "EXP-", NextNumber: 1, PaddingLength: 4, Enabled: true},
		// Customer Deposit default: prefix "DEP" (no hyphen per
		// 2026-04-24 design), padding 4 → DEP0001 first. Bookkeepers
		// can customise prefix/padding per company under Settings.
		{ModuleKey: ModuleCustomerDeposit, ModuleName: "Customer Deposit", Prefix: "DEP", NextNumber: 1, PaddingLength: 4, Enabled: true},
	}
}

// FormatPreview builds a sample display number from prefix + padded next value.
func FormatPreview(prefix string, next int, padding int) string {
	prefix = strings.TrimSpace(prefix)
	if padding <= 0 {
		return prefix + fmt.Sprintf("%d", next)
	}
	if padding > 32 {
		padding = 32
	}
	return prefix + fmt.Sprintf("%0*d", padding, next)
}

// MergeSavedOntoDefaults applies saved rules onto defaults by module_key (same semantics as file-based numbering).
func MergeSavedOntoDefaults(defaults []DisplayRule, saved []DisplayRule) []DisplayRule {
	byKey := map[string]DisplayRule{}
	for _, r := range defaults {
		byKey[r.ModuleKey] = r
	}
	for _, r := range saved {
		r = NormalizeRule(r)
		if _, ok := byKey[r.ModuleKey]; !ok {
			continue
		}
		base := byKey[r.ModuleKey]
		if r.ModuleName == "" {
			r.ModuleName = base.ModuleName
		}
		byKey[r.ModuleKey] = r
	}
	out := make([]DisplayRule, 0, len(defaults))
	for _, d := range defaults {
		out = append(out, byKey[d.ModuleKey])
	}
	return out
}

// NormalizeRule clamps values to safe ranges for storage and UI.
func NormalizeRule(r DisplayRule) DisplayRule {
	if r.NextNumber < 0 {
		r.NextNumber = 0
	}
	if r.PaddingLength < 0 {
		r.PaddingLength = 0
	}
	if r.PaddingLength > 20 {
		r.PaddingLength = 20
	}
	if len(r.Prefix) > 64 {
		r.Prefix = r.Prefix[:64]
	}
	return r
}
