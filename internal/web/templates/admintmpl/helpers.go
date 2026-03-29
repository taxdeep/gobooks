// 遵循产品需求 v1.0
package admintmpl

import "strconv"

// adminInt converts an int to string for use in templ expressions.
func adminInt(n int) string {
	return strconv.Itoa(n)
}

// adminUint converts a uint to string for use in templ expressions.
func adminUint(n uint) string {
	return strconv.FormatUint(uint64(n), 10)
}

// Shared table row action buttons (backoffice-style: bordered, obvious, not link-only).
const adminRowActionBase = "inline-flex items-center justify-center rounded-md border px-3 py-1.5 text-small font-semibold shadow-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-focus "

func adminClassActionDefault() string {
	return adminRowActionBase + "border-border-input bg-surface text-text hover:bg-background"
}

func adminClassActionCaution() string {
	return adminRowActionBase + "border-border-input bg-surface text-text hover:border-warning-border hover:bg-warning-soft hover:text-warning"
}

func adminClassActionPositive() string {
	return adminRowActionBase + "border-border-input bg-surface text-text hover:border-success-border hover:bg-success-soft hover:text-success"
}

func adminClassActionLink() string {
	return adminRowActionBase + "border-transparent bg-transparent text-primary shadow-none hover:bg-primary/5 hover:text-primary-hover"
}

// Toolbar / header secondary actions (e.g. View all).
func adminClassToolbarAction() string {
	return adminClassActionDefault() + " shrink-0"
}

func adminClassBadgeActive() string {
	return "inline-flex rounded-md border border-success-border bg-success-soft px-2 py-0.5 text-small font-medium text-success"
}

func adminClassBadgeInactive() string {
	return "inline-flex rounded-md border border-border bg-background px-2 py-0.5 text-small font-medium text-text-muted2"
}

// Primary / secondary / danger — align with main GoBooks CTA patterns.
func adminClassPrimaryButton() string {
	return "inline-flex items-center justify-center rounded-md bg-primary px-4 py-2 text-body font-semibold text-onPrimary shadow-sm hover:bg-primary-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-focus"
}

func adminClassPrimaryButtonSmall() string {
	return "inline-flex items-center justify-center rounded-md bg-primary px-3 py-1.5 text-small font-semibold text-onPrimary shadow-sm hover:bg-primary-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-focus"
}

func adminClassSecondaryButton() string {
	return "inline-flex items-center justify-center rounded-md border border-border-input bg-surface px-4 py-2 text-body font-semibold text-text shadow-sm hover:bg-background focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-focus"
}

// Data tables: zebra + hover for scanability.
func adminClassTableRow() string {
	return "border-b border-border-subtle transition-colors last:border-0 even:bg-background/40 hover:bg-primary/[0.06]"
}

func adminClassTableRowInactive() string {
	return adminClassTableRow() + " bg-background/60 opacity-90"
}
