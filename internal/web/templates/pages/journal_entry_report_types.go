// 遵循project_guide.md
package pages

import "gobooks/internal/services"

// JournalEntryReportVM is the view-model for Reports → Journal Entries.
type JournalEntryReportVM struct {
	HasCompany bool

	From string
	To   string

	Entries []services.JournalEntryReportEntry

	FormError string

	Toolbar ReportToolbarVM
}
