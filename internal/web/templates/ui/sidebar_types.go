// 遵循project_guide.md
package ui

import "context"

// SidebarVM is a small view-model for consistent sidebar rendering.
// Keeping it explicit helps keep UI behavior predictable.
type SidebarVM struct {
	Active     string
	HasCompany bool
	UserEmail  string // optional; shown in top-bar user menu when set
}

// SidebarData carries company-switcher data injected into the Go context by
// LoadSidebarData() middleware. layout.templ reads this via SidebarDataFromCtx(ctx)
// so that no individual page template or ViewModel needs to change.
type SidebarData struct {
	// CompanyName is the display name of the currently active company.
	// Empty string means no active company (new user, setup not complete).
	CompanyName string
	// PlanName is the user's subscription plan label (e.g. "Starter").
	// Empty string when no plan is resolved.
	PlanName string
	// SwitcherRows lists all companies the user can access.
	// Used to render the company-switcher dropdown.
	SwitcherRows []SwitcherRow
}

// SwitcherRow is one entry in the company-switcher dropdown.
type SwitcherRow struct {
	CompanyIDStr string
	Name         string
	IsActive     bool // true = currently active company (shows checkmark)
}

type sidebarDataKey struct{}

// WithSidebarData stores sd into ctx and returns the new context.
func WithSidebarData(ctx context.Context, sd SidebarData) context.Context {
	return context.WithValue(ctx, sidebarDataKey{}, sd)
}

// SidebarDataFromCtx retrieves the SidebarData from ctx.
// Returns zero-value SidebarData if not set (safe: empty string fields render nothing).
func SidebarDataFromCtx(ctx context.Context) SidebarData {
	sd, _ := ctx.Value(sidebarDataKey{}).(SidebarData)
	return sd
}
