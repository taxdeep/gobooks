// 遵循project_guide.md
package ui

// docLineItemsAddLabel returns the "+ Add Lines" button label with a default.
func docLineItemsAddLabel(vm DocLineItemsVM) string {
	if vm.AddLabel != "" {
		return vm.AddLabel
	}
	return "+ Add Line"
}

// docLineItemsClearLabel returns the "Clear all lines" button label with a default.
func docLineItemsClearLabel(vm DocLineItemsVM) string {
	if vm.ClearLabel != "" {
		return vm.ClearLabel
	}
	return "Clear all lines"
}

// docLineItemsDeleteDisabled returns the Alpine expression for the trash
// button's :disabled binding. Defaults to "lines.length === 1" so the last
// row cannot be deleted (keeping at least one input row visible).
func docLineItemsDeleteDisabled(vm DocLineItemsVM) string {
	if vm.DeleteDisabledWhen != "" {
		return vm.DeleteDisabledWhen
	}
	return "lines.length === 1"
}
