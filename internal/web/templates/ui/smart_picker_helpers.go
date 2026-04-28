// 遵循project_guide.md
package ui

import "strconv"

// SmartPickerVM is the view model for ui.SmartPicker.
//
// entity/context semantics:
//
//	entity="account" in Phase 1 maps to ExpenseAccountProvider, which returns
//	only expense-root active accounts for the authenticated company.
//	It does NOT mean "all GL accounts". The actual result scope is always
//	determined by the backend provider (entity + context together).
//	The frontend component passes these values verbatim to the backend.
type SmartPickerVM struct {
	// Label is the visible field label above the picker. Empty string skips label rendering.
	Label string
	// FieldName is the name attribute of the hidden input submitted with the form
	// (e.g. "expense_account_id"). Must match what the backend handler expects.
	FieldName string
	// Entity identifies the backend provider (e.g. "account").
	Entity string
	// Context narrows the provider's search scope (e.g. "expense_form_category").
	Context string
	// Value is the selected entity ID for edit-page rehydration. Empty string for new forms.
	// The hidden input always carries this value on server render.
	Value string
	// SelectedLabel is the human-readable display text corresponding to Value.
	// Used to pre-populate the visible input on edit pages without a round-trip fetch.
	//
	// IMPORTANT: if Value is non-empty but SelectedLabel is empty, the visible input
	// shows blank (placeholder) — it never falls back to displaying Value as text.
	// Displaying a raw database ID as user-visible text is not allowed.
	SelectedLabel string
	// Placeholder is the visible input placeholder text. Defaults to "Search…".
	Placeholder string
	// Required=true means: no clear button is rendered, and the field uses
	// required-field styling. Backend validation remains the sole authority;
	// no HTML required attribute is set (hidden inputs are excluded from
	// browser constraint validation anyway).
	Required bool
	// AllowCreate, when true, shows a persistent "+ Add new [query]" row at the
	// top of every dropdown (not just the empty state). Clicking it dispatches
	// balanciz-picker-create so the host page can open an inline creation panel
	// without navigating away. Leave false (default) for read-only pickers.
	AllowCreate bool
	// CreateURL, if non-empty, shows an "Add new" link in the empty-results state.
	// Intended for Batch C inline-create flows; leave empty in Phase 1.
	CreateURL string
	// CreateLabel overrides the default "Add new" link text.
	CreateLabel string
	// Error is the server-side validation error message shown below the picker.
	Error string
	// Limit controls the max results per search request (default 10).
	// The handler caps this at 20 regardless of what is sent.
	Limit int
	// Disabled renders a read-only display div instead of the interactive picker.
	// The hidden input is still rendered to preserve the submitted value.
	Disabled bool
	// HelpText is an optional hint rendered below the picker and error message.
	HelpText string
}

func smartPickerPlaceholder(vm SmartPickerVM) string {
	if vm.Placeholder != "" {
		return vm.Placeholder
	}
	return "Search\u2026"
}

func smartPickerLimit(vm SmartPickerVM) string {
	n := vm.Limit
	if n <= 0 {
		n = 10
	}
	return strconv.Itoa(n)
}

func smartPickerBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
