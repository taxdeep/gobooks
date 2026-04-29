// 遵循project_guide.md
package pages

import "balanciz/internal/services"

type SalesOverviewVM struct {
	HasCompany bool
	FormError  string
	Overview   services.SalesOverview
}
