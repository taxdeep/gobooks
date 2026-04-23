// 遵循project_guide.md
package pages

// listFilterInputClass is the shared compact dark-mode-safe styling for
// list-page filter bars (<select> + <input type="date">). Single source
// of truth so the Sales Orders / Purchase Orders / Quotes / Invoices /
// Bills / Receipts pages stay visually aligned.
//
// Density matches the Sales Transactions filter bar (py-1) — half the
// height of the standard form field, which is right for a filter strip
// that lives above the data table.
func listFilterInputClass() string {
	return "mt-2 block w-full rounded-md border border-border-input bg-surface px-2.5 py-1 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
}
