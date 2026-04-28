// 遵循project_guide.md
package pages

import "balanciz/internal/models"

type AuditLogVM struct {
	HasCompany bool
	Items      []models.AuditLog

	FilterQ      string
	FilterAction string
	FilterEntity string
	FilterFrom   string
	FilterTo     string

	Actions  []string
	Entities []string

	Page       int
	PrevPage   int
	NextPage   int
	HasPrev    bool
	HasNext    bool
	TotalCount int64
}

