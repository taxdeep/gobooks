// 遵循project_guide.md
package ui

// SectionCardChromeClass returns the standard section-card chrome Tailwind
// classes. Shared across SectionCard, DocHeaderGrid, and DocLineItems so
// spacing/border changes only need to happen in one place.
func SectionCardChromeClass() string {
	return "rounded-lg border border-border bg-surface p-5 shadow-sm"
}
