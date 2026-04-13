// 遵循project_guide.md
package models

import "time"

// UserPlan defines the quota limits for a subscription tier.
// SysAdmin manages these via /admin/plans (full CRUD).
//
// Quota semantics:
//   - MaxOwnedCompanies:    max companies a user may create (role=owner). -1 = unlimited.
//   - MaxMembersPerCompany: max invited team members per company (excl. the owner). -1 = unlimited.
//
// The plan is assigned to a User via User.PlanID. It controls what that user
// can do as an owner. Invitees are governed by the company owner's plan, not
// their own.
//
// SysAdmin users (SysadminUser) are completely separate and are never assigned
// a UserPlan.
type UserPlan struct {
	ID                   int    `gorm:"primaryKey;autoIncrement"`
	Name                 string `gorm:"not null;uniqueIndex"`
	MaxOwnedCompanies    int    `gorm:"not null;default:3"`  // -1 = unlimited
	MaxMembersPerCompany int    `gorm:"not null;default:5"`  // -1 = unlimited; owner not counted
	IsActive             bool   `gorm:"not null;default:true"`
	SortOrder            int    `gorm:"not null;default:0"` // display order in admin

	CreatedAt time.Time
	UpdatedAt time.Time
}
