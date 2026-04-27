// 遵循project_guide.md
package pages

// CompaniesVM is the view model for the /companies page.
type CompaniesVM struct {
	Rows            []CompanyRowVM
	ActiveCompanyID uint   // currently active company in the session (0 if none)
	PlanName        string // user's plan name, shown in the page subtitle
	SearchQuery     string // optional company-name filter scoped to the current user
}

// CompanyRowVM is one row in the company list.
type CompanyRowVM struct {
	CompanyID    uint
	CompanyIDStr string
	Name         string
	RoleLabel    string
	IsActive     bool // true when this is the session's active company
}
