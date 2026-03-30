// 遵循project_guide.md
package models

import "time"

// NormalBalance indicates whether an account's natural balance is debit or credit.
type NormalBalance string

const (
	NormalBalanceDebit  NormalBalance = "debit"
	NormalBalanceCredit NormalBalance = "credit"
)

// COATemplate is a named, versioned Chart of Accounts template.
// is_default = true marks the single active default used when creating new companies.
type COATemplate struct {
	ID        uint      `gorm:"primaryKey"`
	Name      string    `gorm:"not null"`
	IsDefault bool      `gorm:"not null;default:false;index"`
	CreatedAt time.Time
}

// COATemplateAccount is one account row within a COATemplate.
// account_code is a 4-digit base code (expanded by ExpandAccountCodeToLength at import time).
type COATemplateAccount struct {
	ID                uint              `gorm:"primaryKey"`
	TemplateID        uint              `gorm:"not null;index"`
	AccountCode       string            `gorm:"not null;size:4"`
	Name              string            `gorm:"not null"`
	RootAccountType   RootAccountType   `gorm:"column:root_account_type;type:text;not null"`
	DetailAccountType DetailAccountType `gorm:"column:detail_account_type;type:text;not null"`
	NormalBalance     NormalBalance     `gorm:"column:normal_balance;type:text;not null"`
	SortOrder         int               `gorm:"not null;default:0"`
	// Metadata is optional JSON for future extensibility (e.g. GIFI codes, flags).
	Metadata  *string   `gorm:"column:metadata;type:text"`
	CreatedAt time.Time
}
