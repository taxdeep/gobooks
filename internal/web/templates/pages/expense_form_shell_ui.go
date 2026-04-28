// 遵循project_guide.md
package pages

import "balanciz/internal/web/templates/ui"

// expenseFormShellVM maps ExpenseFormVM into the shared DocEditorShell.
// Subtitle adds the auto-assigned reference number on edit pages so the
// operator can always see what they're working on without scrolling.
func expenseFormShellVM(vm ExpenseFormVM) ui.DocEditorShellVM {
	subtitle := "Capture standalone costs and optionally attach them to a customer task."
	if vm.IsEdit && vm.ExpenseNumber != "" {
		subtitle = "Reference: " + vm.ExpenseNumber
	}
	return ui.DocEditorShellVM{
		Title:     expenseFormTitle(vm),
		Subtitle:  subtitle,
		BackURL:   "/expenses",
		BackLabel: "Back to Expenses",
		FormError: vm.FormError,
	}
}

// expenseFormFooterVM is the sticky bottom action bar for the Expense
// editor. Single Save button — Post / Void are status transitions in
// their own POST forms (sibling to the editor form per the nested-form
// prohibition; the Bill / PO editors share the same pattern).
func expenseFormFooterVM(vm ExpenseFormVM) ui.DocEditorFooterVM {
	saveLabel := "Save Expense"
	if vm.IsEdit {
		saveLabel = "Save Changes"
	}
	return ui.DocEditorFooterVM{
		Cancel: &ui.DocEditorFooterLink{
			Label: "Cancel",
			Href:  "/expenses",
		},
		Buttons: []ui.DocEditorFooterButton{
			{Label: saveLabel, Variant: ui.FooterBtnPrimary, Type: "submit"},
		},
	}
}
