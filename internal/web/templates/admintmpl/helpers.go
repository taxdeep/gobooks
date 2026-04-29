// 遵循project_guide.md
package admintmpl

import (
	"fmt"
	"strconv"
	"time"
)

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

// adminFmtRowCount formats a row count with thousands separators for the
// search-rebuild summary table — easier to read large totals at a glance
// (e.g. "1,205" vs "1205").
func adminFmtRowCount(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.Itoa(n)
	// Walk right-to-left inserting commas every 3 digits.
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// adminFmtDuration renders a Duration in the most compact human-readable
// form for the rebuild card. Sub-second uses ms; longer uses seconds with
// one decimal. Avoids time.Duration's default "1.234567s" precision.
func adminFmtDuration(d time.Duration) string {
	if d <= 0 {
		return "0ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// adminFmtDurationMs is the int64-millisecond variant — used for per-family
// rows where we already have ms from FamilyResult.Duration.Milliseconds().
func adminFmtDurationMs(ms int64) string {
	return adminFmtDuration(time.Duration(ms) * time.Millisecond)
}

func adminFmtBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	if n < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
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

// Primary / secondary / danger — align with main Balanciz CTA patterns.
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
