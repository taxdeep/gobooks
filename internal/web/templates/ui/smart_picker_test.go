// 遵循project_guide.md
package ui_test

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/web/templates/ui"
)

// renderSP renders a SmartPicker to a string for assertion.
func renderSP(t *testing.T, vm ui.SmartPickerVM) string {
	t.Helper()
	var sb strings.Builder
	if err := ui.SmartPicker(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render error: %v", err)
	}
	return sb.String()
}

// TestSmartPicker_NewForm verifies a blank new-form render:
// - no pre-selected value in hidden input
// - placeholder shown in data-placeholder
// - no error div rendered
func TestSmartPicker_NewForm(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		Label:     "Category",
		FieldName: "expense_account_id",
		Entity:    "account",
		Context:   "expense_form_category",
	})

	// Field routing: data-field-name carries the field name for Alpine to read.
	if !strings.Contains(html, `data-field-name="expense_account_id"`) {
		t.Error("missing data-field-name on Alpine root")
	}
	// Interactive hidden input must NOT have a static name attribute.
	// Alpine init() assigns name dynamically to prevent double-submit with no-JS fallback.
	if strings.Contains(html, `<input type="hidden" name=`) {
		t.Error("interactive hidden input must not have a static name attribute")
	}
	// Hidden input must have Alpine :value binding for dynamic sync.
	if !strings.Contains(html, `:value="selectedId"`) {
		t.Error("hidden input must have Alpine :value binding")
	}
	if !strings.Contains(html, `value=""`) {
		t.Error("expected empty value on new form")
	}
	if !strings.Contains(html, "Category") {
		t.Error("missing label")
	}
	if !strings.Contains(html, "Search\u2026") {
		t.Error("missing default placeholder")
	}
	// No wrapper error div (the inline error message below the picker)
	if strings.Contains(html, `mt-1 text-body text-danger`) {
		t.Error("unexpected wrapper error div on clean form")
	}
}

// TestSmartPicker_EditRehydration verifies that Value + SelectedLabel are reflected
// in data-value and data-selected-label; hidden input carries the ID.
func TestSmartPicker_EditRehydration(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName:     "expense_account_id",
		Entity:        "account",
		Value:         "42",
		SelectedLabel: "Office Supplies",
	})

	if !strings.Contains(html, `data-value="42"`) {
		t.Error("missing data-value")
	}
	if !strings.Contains(html, `data-selected-label="Office Supplies"`) {
		t.Error("missing data-selected-label")
	}
	// Hidden input must carry the server value for no-JS fallback
	if !strings.Contains(html, `value="42"`) {
		t.Error("hidden input must carry the server value")
	}
	// Raw ID must NOT appear as visible text (it is in data-value attribute, which is fine,
	// but must not be a text node — SelectedLabel is the human-readable text)
	if strings.Contains(html, ">42<") {
		t.Error("raw ID must not appear as visible text content")
	}
}

// TestSmartPicker_RequiredOmitsClearButton verifies that Required=true omits the clear button.
func TestSmartPicker_RequiredOmitsClearButton(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Required:  true,
	})

	if strings.Contains(html, `aria-label="Clear selection"`) {
		t.Error("clear button must not be rendered for required fields")
	}
	if !strings.Contains(html, `data-required="true"`) {
		t.Error("missing data-required=true")
	}
}

// TestSmartPicker_OptionalShowsClearButton verifies that Required=false renders the clear button.
func TestSmartPicker_OptionalShowsClearButton(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Required:  false,
	})

	if !strings.Contains(html, `aria-label="Clear selection"`) {
		t.Error("clear button must be rendered for optional fields")
	}
	if !strings.Contains(html, `x-show="selectedId !== ''"`) {
		t.Error("clear button must carry correct x-show expression")
	}
	if !strings.Contains(html, `data-required="false"`) {
		t.Error("missing data-required=false")
	}
}

// TestSmartPicker_ErrorState verifies error message and data-has-error attribute.
func TestSmartPicker_ErrorState(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Error:     "Account is required",
	})

	if !strings.Contains(html, "Account is required") {
		t.Error("missing error message text")
	}
	if !strings.Contains(html, `data-has-error="true"`) {
		t.Error("missing data-has-error=true")
	}
}

// TestSmartPicker_HelpText verifies help text is rendered below the picker.
func TestSmartPicker_HelpText(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		HelpText:  "Select the expense category for this transaction.",
	})

	if !strings.Contains(html, "Select the expense category") {
		t.Error("missing help text")
	}
}

// TestSmartPicker_Disabled verifies that Disabled=true renders the static display,
// not the interactive Alpine component.
func TestSmartPicker_Disabled(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName:     "expense_account_id",
		Entity:        "account",
		Value:         "42",
		SelectedLabel: "Office Supplies",
		Disabled:      true,
	})

	// No Alpine component
	if strings.Contains(html, `x-data="balancizSmartPicker()"`) {
		t.Error("disabled picker must not render Alpine component")
	}
	// Static label shown
	if !strings.Contains(html, "Office Supplies") {
		t.Error("missing selected label in disabled view")
	}
	// Hidden input still present
	if !strings.Contains(html, `value="42"`) {
		t.Error("disabled picker must still carry value in hidden input")
	}
}

// TestSmartPicker_DisabledNoLabel verifies that Disabled with no SelectedLabel shows the em-dash.
func TestSmartPicker_DisabledNoLabel(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Value:     "",
		Disabled:  true,
	})

	if !strings.Contains(html, "\u2014") {
		t.Error("disabled picker with no label must show em-dash")
	}
}

// TestSmartPicker_CreateURL verifies that CreateURL + CreateLabel are passed via data-* attrs.
func TestSmartPicker_CreateURL(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName:   "vendor_id",
		Entity:      "vendor",
		CreateURL:   "/vendors/new",
		CreateLabel: "Add vendor",
	})

	if !strings.Contains(html, `data-create-url="/vendors/new"`) {
		t.Error("missing data-create-url")
	}
	if !strings.Contains(html, `data-create-label="Add vendor"`) {
		t.Error("missing data-create-label")
	}
}

// TestSmartPicker_XSSEscaping verifies that special characters in SelectedLabel are
// HTML-escaped in the data-selected-label attribute and never appear as raw characters.
func TestSmartPicker_XSSEscaping(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName:     "expense_account_id",
		Entity:        "account",
		Value:         "42",
		SelectedLabel: `O"Supplies<script>alert(1)</script>`,
	})

	// Raw characters must not appear verbatim in the attribute value.
	if strings.Contains(html, `<script>alert`) {
		t.Error("raw <script> must not appear in HTML output")
	}
	if strings.Contains(html, `data-selected-label="O"Supplies`) {
		t.Error("unescaped double-quote must not appear inside attribute")
	}
	// Escaped forms must be present.
	if !strings.Contains(html, `&lt;script&gt;`) {
		t.Error("< and > must be HTML-escaped in attribute value")
	}
}

// TestSmartPicker_DataEntityContextRendered verifies that data-entity and data-context
// are rendered in the interactive component HTML, as they are required by Alpine to
// route requests to the correct backend provider.
func TestSmartPicker_DataEntityContextRendered(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Context:   "expense_form_category",
	})

	if !strings.Contains(html, `data-entity="account"`) {
		t.Error("missing data-entity attribute")
	}
	if !strings.Contains(html, `data-context="expense_form_category"`) {
		t.Error("missing data-context attribute")
	}
}

// TestSmartPicker_DisabledHasStaticName verifies that the disabled branch renders a
// hidden input WITH a static name attribute, since disabled mode has no Alpine to
// assign it dynamically.
func TestSmartPicker_DisabledHasStaticName(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Value:     "42",
		Disabled:  true,
	})

	if !strings.Contains(html, `<input type="hidden" name="expense_account_id"`) {
		t.Error("disabled hidden input must have a static name attribute")
	}
}

// TestSmartPicker_NoLabel verifies that an empty Label omits the <label> element entirely.
func TestSmartPicker_NoLabel(t *testing.T) {
	html := renderSP(t, ui.SmartPickerVM{
		FieldName: "expense_account_id",
		Entity:    "account",
		Label:     "",
	})

	if strings.Contains(html, "<label") {
		t.Error("empty Label must not render a <label> element")
	}
}
