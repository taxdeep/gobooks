package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	GlobalSearchEventSelect = "select"

	GlobalSearchQueryKindAmount = "amount"
	GlobalSearchQueryKindText   = "text"
)

type GlobalSearchEvent struct {
	ID                 uuid.UUID  `gorm:"type:uuid;primaryKey"`
	CompanyID          uint       `gorm:"not null;index:idx_gs_events_company_created,priority:1;index:idx_gs_events_kind_created,priority:1;index:idx_gs_events_entity_selected,priority:1"`
	UserID             *uuid.UUID `gorm:"type:uuid;index:idx_gs_events_user_kind_created,priority:2"`
	SessionID          string     `gorm:"type:text"`
	Query              string     `gorm:"type:text"`
	NormalizedQuery    string     `gorm:"type:text"`
	QueryKind          string     `gorm:"type:text;not null;index:idx_gs_events_kind_created,priority:2;index:idx_gs_events_user_kind_created,priority:3"`
	EventType          string     `gorm:"type:text;not null"`
	SelectedEntityType string     `gorm:"type:text;not null;index:idx_gs_events_entity_selected,priority:2"`
	SelectedEntityID   uint       `gorm:"not null;index:idx_gs_events_entity_selected,priority:3"`
	RankPosition       *int
	ResultCount        *int
	SourceRoute        string    `gorm:"type:text"`
	CreatedAt          time.Time `gorm:"not null;index:idx_gs_events_company_created,priority:2;index:idx_gs_events_kind_created,priority:3;index:idx_gs_events_user_kind_created,priority:4"`
}

func (GlobalSearchEvent) TableName() string { return "global_search_events" }

func (m *GlobalSearchEvent) BeforeCreate(_ *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

type GlobalSearchTypeStat struct {
	ID                 uuid.UUID       `gorm:"type:uuid;primaryKey"`
	CompanyID          uint            `gorm:"not null;index:idx_gs_type_stats_lookup,priority:1"`
	ScopeType          string          `gorm:"type:text;not null"`
	UserID             *uuid.UUID      `gorm:"type:uuid"`
	QueryKind          string          `gorm:"type:text;not null;index:idx_gs_type_stats_lookup,priority:2"`
	SelectedEntityType string          `gorm:"type:text;not null;index:idx_gs_type_stats_lookup,priority:3"`
	SelectCount        int             `gorm:"not null;default:0"`
	SelectCount30D     int             `gorm:"column:select_count_30d;not null;default:0"`
	AIWeight           decimal.Decimal `gorm:"column:ai_weight;type:numeric(8,4);not null;default:0"`
	AIConfidence       decimal.Decimal `gorm:"column:ai_confidence;type:numeric(8,4);not null;default:0"`
	WeightSource       string          `gorm:"type:text;not null;default:'behavior'"`
	LastSelectedAt     *time.Time
	LastQuery          string    `gorm:"type:text"`
	UpdatedAt          time.Time `gorm:"not null"`
}

func (GlobalSearchTypeStat) TableName() string { return "global_search_type_stats" }

func (m *GlobalSearchTypeStat) BeforeCreate(_ *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}
