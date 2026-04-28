// 遵循project_guide.md
package pages

import "balanciz/internal/web/templates/ui"

// billEditorShellVM maps BillEditorVM → DocEditorShellVM, providing the
// Bill editor's title / subtitle / banner stack to the shared shell.
func billEditorShellVM(vm BillEditorVM) ui.DocEditorShellVM {
	subtitle := "Create a new bill. Save as draft to review before posting."
	if vm.IsEdit {
		subtitle = "Edit draft bill. Changes are saved as a draft until posted."
	}
	return ui.DocEditorShellVM{
		Title:      BillEditorTitle(vm),
		Subtitle:   subtitle,
		BackURL:    "/bills",
		BackLabel:  "Back to Bills",
		FormError:  vm.FormError,
		LinesError: vm.LinesError,
	}
}

// billEditorFooterVM assembles the sticky bottom action bar for the Bill
// editor. Mirrors the Invoice editor's review-mode toggling: Save Draft +
// Submit are both rendered, each Style="display:none" on the hidden side
// so beExitReview() can flip display styles client-side without re-rendering.
func billEditorFooterVM(vm BillEditorVM) ui.DocEditorFooterVM {
	footer := ui.DocEditorFooterVM{
		Cancel: &ui.DocEditorFooterLink{
			ID:    "be-cancel-link",
			Label: "Cancel",
			Href:  "/bills",
			Style: editorDisplay(!vm.ReviewLocked),
		},
	}
	footer.Buttons = append(footer.Buttons,
		ui.DocEditorFooterButton{
			ID:      "be-edit-btn",
			Label:   "← Edit",
			Variant: ui.FooterBtnSecondary,
			Type:    "button",
			OnClick: "beExitReview()",
			Style:   editorDisplay(vm.ReviewLocked),
		},
		ui.DocEditorFooterButton{
			ID:      "be-save-btn",
			Label:   "Save Draft",
			Variant: ui.FooterBtnPrimary,
			Type:    "submit",
			Style:   editorDisplay(!vm.ReviewLocked),
		},
	)
	if vm.SubmitPath != "" {
		footer.Buttons = append(footer.Buttons, ui.DocEditorFooterButton{
			ID:         "be-submit-btn",
			Label:      "Submit",
			Variant:    ui.FooterBtnPrimary,
			Type:       "submit",
			FormAction: vm.SubmitPath,
			FormMethod: "post",
			Style:      editorDisplay(vm.ReviewLocked),
		})
	}
	return footer
}
