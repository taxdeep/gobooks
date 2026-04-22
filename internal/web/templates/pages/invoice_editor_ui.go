// 遵循project_guide.md
package pages

import (
	"encoding/json"
	"fmt"

	"gobooks/internal/web/templates/ui"
)

// invoiceSalesOrderLink returns the SO detail-page URL the "SO Number" cell
// links to in the invoice editor header. Centralised so SO routing changes
// only need updating in one place.
func invoiceSalesOrderLink(salesOrderID uint) string {
	return fmt.Sprintf("/sales-orders/%d", salesOrderID)
}

// shippingAddressesJSON serialises a customer's named shipping addresses for
// the editor's data-initial-shipping-addresses attribute. Empty list yields
// "[]" so JSON.parse never fails on the Alpine side.
func shippingAddressesJSON(opts []ShippingAddressOption) string {
	if len(opts) == 0 {
		return "[]"
	}
	type entry struct {
		Label   string `json:"label"`
		Address string `json:"address"`
		Default bool   `json:"is_default"`
	}
	out := make([]entry, 0, len(opts))
	for _, o := range opts {
		out = append(out, entry{Label: o.Label, Address: o.Address, Default: o.IsDefault})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// invoiceEditorShellVM maps InvoiceEditorVM → the shell's DocEditorShellVM,
// assembling title / subtitle / info-banner copy for all three editor modes
// (new draft, edit draft, task-generated read-only).
func invoiceEditorShellVM(vm InvoiceEditorVM) ui.DocEditorShellVM {
	return ui.DocEditorShellVM{
		Title:      InvoiceEditorTitle(vm),
		Subtitle:   invoiceEditorSubtitle(vm),
		BackURL:    "/invoices",
		BackLabel:  "Back to Invoices",
		FormError:  vm.FormError,
		LinesError: vm.LinesError,
		InfoBanner: invoiceEditorInfoBanner(vm),
	}
}

func invoiceEditorSubtitle(vm InvoiceEditorVM) string {
	switch {
	case vm.TaskGeneratedReadOnly:
		return "This draft was generated from Billable Work. Review it here, or delete the draft and regenerate from Billable Work to change the included work."
	case vm.IsEdit:
		return "Edit draft invoice. Changes are saved as a draft until posted."
	default:
		return "Create a new invoice. Save as draft to review before posting."
	}
}

func invoiceEditorInfoBanner(vm InvoiceEditorVM) string {
	if !vm.TaskGeneratedReadOnly {
		return ""
	}
	prefix := ""
	if vm.Saved {
		prefix = "Changes saved. "
	}
	return prefix + "Task-generated draft: memo, tax codes, GST, and extra lines are editable. All other fields are locked. To change the core line items, delete the draft and regenerate from Billable Work."
}

// invoiceEditorFooterVM assembles the sticky bottom action bar for all three
// Invoice editor modes:
//
//   - Task-generated read-only: [Back] … [Delete Draft] [Save Changes] [Submit]
//   - Normal edit (draft):     [Cancel] … [Save Draft] (Submit hidden until review)
//   - Review mode:             [← Edit] … [Submit]    (Save Draft hidden)
//
// Review-mode toggling is done client-side by ieExitReview(): Save Draft and
// Submit are both rendered, each with Style="display:none" on the side that
// should be initially hidden. The JS flips display styles on Edit click.
func invoiceEditorFooterVM(vm InvoiceEditorVM) ui.DocEditorFooterVM {
	if vm.TaskGeneratedReadOnly {
		return invoiceFooterTaskReadOnly(vm)
	}
	return invoiceFooterEditable(vm)
}

func invoiceFooterTaskReadOnly(vm InvoiceEditorVM) ui.DocEditorFooterVM {
	footer := ui.DocEditorFooterVM{
		Cancel: &ui.DocEditorFooterLink{
			Label: "Back to Invoices",
			Href:  "/invoices",
		},
	}
	if vm.DeletePath != "" {
		footer.Buttons = append(footer.Buttons, ui.DocEditorFooterButton{
			Label:      "Delete Draft",
			Variant:    ui.FooterBtnDanger,
			Type:       "submit",
			FormAction: vm.DeletePath,
			// Alpine's @click is an x-on expression; pair a false confirm result
			// with $event.preventDefault() to cancel the submit.
			OnClick: "if (!confirm('Delete this draft invoice? This will release the linked Billable Work so you can regenerate it.')) $event.preventDefault()",
		})
	}
	if vm.SaveTaskDraftPath != "" {
		footer.Buttons = append(footer.Buttons, ui.DocEditorFooterButton{
			Label:      "Save Changes",
			Variant:    ui.FooterBtnSecondary,
			Type:       "submit",
			FormAction: vm.SaveTaskDraftPath,
		})
	}
	if vm.SubmitPath != "" {
		footer.Buttons = append(footer.Buttons, invoiceSubmitButton(vm.SubmitPath, ""))
	}
	return footer
}

func invoiceFooterEditable(vm InvoiceEditorVM) ui.DocEditorFooterVM {
	footer := ui.DocEditorFooterVM{
		Cancel: &ui.DocEditorFooterLink{
			ID:    "ie-cancel-link",
			Label: "Cancel",
			Href:  "/invoices",
			Style: editorDisplay(!vm.ReviewLocked),
		},
	}
	footer.Buttons = append(footer.Buttons,
		ui.DocEditorFooterButton{
			ID:      "ie-edit-btn",
			Label:   "← Edit",
			Variant: ui.FooterBtnSecondary,
			Type:    "button",
			OnClick: "ieExitReview()",
			Style:   editorDisplay(vm.ReviewLocked),
		},
		ui.DocEditorFooterButton{
			ID:      "ie-save-btn",
			Label:   "Save Draft",
			Variant: ui.FooterBtnPrimary,
			Type:    "submit",
			Style:   editorDisplay(!vm.ReviewLocked),
		},
	)
	if vm.SubmitPath != "" {
		footer.Buttons = append(footer.Buttons, invoiceSubmitButton(vm.SubmitPath, editorDisplay(vm.ReviewLocked)))
	}
	return footer
}

func invoiceSubmitButton(submitPath, style string) ui.DocEditorFooterButton {
	return ui.DocEditorFooterButton{
		ID:         "ie-submit-btn",
		Label:      "Submit",
		Variant:    ui.FooterBtnPrimary,
		Type:       "submit",
		FormAction: submitPath,
		Style:      style,
	}
}
