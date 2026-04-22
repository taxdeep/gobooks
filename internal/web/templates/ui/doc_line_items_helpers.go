// 遵循project_guide.md
package ui

// docLineItemsDeleteDisabled returns the Alpine expression for the trash
// button's :disabled binding. Defaults to "lines.length === 1" so the last
// row cannot be deleted (keeping at least one input row visible).
func docLineItemsDeleteDisabled(vm DocLineItemsVM) string {
	if vm.DeleteDisabledWhen != "" {
		return vm.DeleteDisabledWhen
	}
	return "lines.length === 1"
}
