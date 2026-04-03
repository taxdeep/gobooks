// 遵循project_guide.md
package pages

import "gobooks/internal/services"

type ClearingReportVM struct {
	HasCompany        bool
	Summaries         []services.ClearingSummary
	SelectedChannelID uint
	Movements         []services.ClearingMovement
	Warnings          []string
}
