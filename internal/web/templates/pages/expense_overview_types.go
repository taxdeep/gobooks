// 遵循project_guide.md
package pages

import "balanciz/internal/services"

type ExpenseOverviewVM struct {
	HasCompany bool
	FormError  string
	Overview   services.ExpenseOverview
}
