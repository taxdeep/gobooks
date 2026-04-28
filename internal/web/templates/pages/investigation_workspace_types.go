// 遵循project_guide.md
package pages

import "balanciz/internal/services"

// WorkspaceTypeOption is a single item in the workspace type dropdown.
type WorkspaceTypeOption struct {
	Value string
	Label string
}

// InvestigationWorkspaceVM is the view model for the investigation workspace page.
//
// It carries the unified list of workspace rows, summary bucket counts, the
// currently-active filter, type dropdown options, and pagination metadata.
//
// This VM is intentionally thin: all business logic lives in the service layer.
// The template consumes it read-only.
type InvestigationWorkspaceVM struct {
	HasCompany bool

	// Rows is the current page of filtered, sorted workspace rows.
	// May be empty if no exceptions match the current filter.
	Rows []services.WorkspaceRow

	// Buckets holds summary counts across all exception domains.
	// Counts are unfiltered (always reflect the full company state).
	Buckets services.OperationalBuckets

	// Filter is the currently-active filter, echoed back from query params.
	// Used by the template to mark active filter controls.
	Filter services.WorkspaceFilter

	// TypeOptions are the type dropdown items for the currently-selected domain.
	// The handler pre-filters these based on Filter.Domain so the dropdown
	// only shows types that make sense for the current domain selection.
	TypeOptions []WorkspaceTypeOption

	// Pagination metadata.
	TotalCount  int // total rows matching the filter (before pagination)
	CurrentPage int // 1-based current page number
	PageSize    int // rows per page
	TotalPages  int // total number of pages
	HasMore     bool
	NextCursor  string
}
