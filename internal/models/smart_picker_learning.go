package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	SmartPickerEventSearch    = "search"
	SmartPickerEventImpress   = "impression"
	SmartPickerEventSelect    = "select"
	SmartPickerEventCreateNew = "create_new"
	SmartPickerEventNoMatch   = "no_match"
	SmartPickerEventAbandon   = "abandon"
	SmartPickerEventClear     = "clear"
	SmartPickerEventOverride  = "override"

	SmartPickerScopeCompany = "company"
	SmartPickerScopeUser    = "user"

	SmartPickerSourceSystem = "system"
	SmartPickerSourceAI     = "ai"
	SmartPickerSourceAdmin  = "admin"

	SmartPickerSuggestionPending    = "pending"
	SmartPickerSuggestionActive     = "active"
	SmartPickerSuggestionRejected   = "rejected"
	SmartPickerSuggestionExpired    = "expired"
	SmartPickerSuggestionSuperseded = "superseded"

	SmartPickerValidationUnvalidated = "unvalidated"
	SmartPickerValidationValid       = "valid"
	SmartPickerValidationInvalid     = "invalid"

	AIJobSmartPickerLearning     = "smartpicker_learning"
	AIJobHintValidation          = "ai_hint_validation"
	AIJobAccountingCommandParse  = "accounting_command_parse"
	AIJobReceiptOCR              = "receipt_ocr"
	AIJobStatusQueued            = "queued"
	AIJobStatusRunning           = "running"
	AIJobStatusSucceeded         = "succeeded"
	AIJobStatusFailed            = "failed"
	AIJobStatusCancelled         = "cancelled"
	AIJobStatusPartial           = "partial"
	AIJobTriggerManual           = "manual"
	AIJobTriggerScheduled        = "scheduled"
	AIJobTriggerSystem           = "system"
	AIJobTriggerTest             = "test"
	AIRequestStatusSkipped       = "skipped"
	AIRequestStatusSucceeded     = "succeeded"
	AIRequestStatusFailed        = "failed"
	AIRequestStatusInvalidOutput = "invalid_output"
)

func ensureUUID(id *uuid.UUID) {
	if *id == uuid.Nil {
		*id = uuid.New()
	}
}

type SmartPickerEvent struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey"`
	CompanyID        uint       `gorm:"not null;index:idx_sp_events_company_created,priority:1;index:idx_sp_events_context_created,priority:1;index:idx_sp_events_user_context_created,priority:1;index:idx_sp_events_entity_selected,priority:1;index:idx_sp_events_type_created,priority:1"`
	UserID           *uuid.UUID `gorm:"type:uuid;index:idx_sp_events_user_context_created,priority:2"`
	SessionID        string     `gorm:"type:text"`
	Context          string     `gorm:"type:text;not null;index:idx_sp_events_context_created,priority:2;index:idx_sp_events_user_context_created,priority:3"`
	EntityType       string     `gorm:"type:text;not null;index:idx_sp_events_entity_selected,priority:2"`
	Query            string     `gorm:"type:text"`
	NormalizedQuery  string     `gorm:"type:text"`
	EventType        string     `gorm:"type:text;not null;index:idx_sp_events_type_created,priority:2"`
	SelectedEntityID *uint      `gorm:"index:idx_sp_events_entity_selected,priority:3"`
	RankPosition     *int
	ResultCount      *int
	SourceRoute      string `gorm:"type:text"`
	AnchorContext    string `gorm:"type:text"`
	AnchorEntityType string `gorm:"type:text"`
	AnchorEntityID   *uint
	MetadataJSON     string    `gorm:"column:metadata_json;type:jsonb"`
	CreatedAt        time.Time `gorm:"not null;index:idx_sp_events_company_created,priority:2;index:idx_sp_events_context_created,priority:3;index:idx_sp_events_user_context_created,priority:4;index:idx_sp_events_type_created,priority:3"`
}

func (SmartPickerEvent) TableName() string { return "smart_picker_events" }

func (m *SmartPickerEvent) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerUsageStat struct {
	ID              uuid.UUID       `gorm:"type:uuid;primaryKey"`
	CompanyID       uint            `gorm:"not null;index:idx_sp_usage_company_context,priority:1;uniqueIndex:uq_sp_usage_scope_entity,priority:1"`
	ScopeType       string          `gorm:"type:text;not null;default:'company';uniqueIndex:uq_sp_usage_scope_entity,priority:2"`
	UserID          *uuid.UUID      `gorm:"type:uuid;uniqueIndex:uq_sp_usage_scope_entity,priority:3"`
	Context         string          `gorm:"type:text;not null;index:idx_sp_usage_company_context,priority:2;uniqueIndex:uq_sp_usage_scope_entity,priority:4"`
	EntityType      string          `gorm:"type:text;not null;uniqueIndex:uq_sp_usage_scope_entity,priority:5"`
	EntityID        uint            `gorm:"not null;uniqueIndex:uq_sp_usage_scope_entity,priority:6"`
	SelectCount     int             `gorm:"not null;default:0"`
	SelectCount7D   int             `gorm:"column:select_count_7d;not null;default:0"`
	SelectCount30D  int             `gorm:"column:select_count_30d;not null;default:0"`
	SelectCount90D  int             `gorm:"column:select_count_90d;not null;default:0"`
	LastSelectedAt  *time.Time      `gorm:"index"`
	LastQuery       string          `gorm:"type:text"`
	AvgRankPosition decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	UpdatedAt       time.Time       `gorm:"not null"`
}

func (SmartPickerUsageStat) TableName() string { return "smart_picker_usage_stats" }

func (m *SmartPickerUsageStat) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerPairStat struct {
	ID                     uuid.UUID       `gorm:"type:uuid;primaryKey"`
	CompanyID              uint            `gorm:"not null;index:idx_sp_pair_company_source,priority:1;uniqueIndex:uq_sp_pair_scope,priority:1"`
	ScopeType              string          `gorm:"type:text;not null;default:'company';uniqueIndex:uq_sp_pair_scope,priority:2"`
	UserID                 *uuid.UUID      `gorm:"type:uuid;uniqueIndex:uq_sp_pair_scope,priority:3"`
	SourceContext          string          `gorm:"type:text;not null;index:idx_sp_pair_company_source,priority:2;uniqueIndex:uq_sp_pair_scope,priority:4"`
	AnchorEntityType       string          `gorm:"type:text;not null;uniqueIndex:uq_sp_pair_scope,priority:5"`
	AnchorEntityID         uint            `gorm:"not null;uniqueIndex:uq_sp_pair_scope,priority:6"`
	TargetContext          string          `gorm:"type:text;not null;uniqueIndex:uq_sp_pair_scope,priority:7"`
	TargetEntityType       string          `gorm:"type:text;not null;uniqueIndex:uq_sp_pair_scope,priority:8"`
	TargetEntityID         uint            `gorm:"not null;uniqueIndex:uq_sp_pair_scope,priority:9"`
	SelectCount            int             `gorm:"not null;default:0"`
	TotalAnchorSelectCount int             `gorm:"not null;default:0"`
	ConfidenceScore        decimal.Decimal `gorm:"type:numeric(8,4);not null;default:0"`
	LastSelectedAt         *time.Time
	UpdatedAt              time.Time `gorm:"not null"`
}

func (SmartPickerPairStat) TableName() string { return "smart_picker_pair_stats" }

func (m *SmartPickerPairStat) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerRecentQuery struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey"`
	CompanyID         uint       `gorm:"not null;index:idx_sp_recent_user_context_created,priority:1;index:idx_sp_recent_context_query,priority:1;index:idx_sp_recent_clicked_created,priority:1"`
	UserID            *uuid.UUID `gorm:"type:uuid;index:idx_sp_recent_user_context_created,priority:2"`
	Context           string     `gorm:"type:text;not null;index:idx_sp_recent_user_context_created,priority:3;index:idx_sp_recent_context_query,priority:2;index:idx_sp_recent_clicked_created,priority:2"`
	Query             string     `gorm:"type:text;not null"`
	NormalizedQuery   string     `gorm:"type:text;not null;index:idx_sp_recent_context_query,priority:3"`
	ResultClicked     bool       `gorm:"not null;default:false;index:idx_sp_recent_clicked_created,priority:3"`
	ClickedEntityType string     `gorm:"type:text"`
	ClickedEntityID   *uint
	ResultCount       *int
	CreatedAt         time.Time `gorm:"not null;index:idx_sp_recent_user_context_created,priority:4;index:idx_sp_recent_clicked_created,priority:4"`
}

func (SmartPickerRecentQuery) TableName() string { return "smart_picker_recent_queries" }

func (m *SmartPickerRecentQuery) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerLearningProfile struct {
	ID                uuid.UUID       `gorm:"type:uuid;primaryKey"`
	CompanyID         uint            `gorm:"not null;index"`
	UserID            *uuid.UUID      `gorm:"type:uuid;index"`
	Context           string          `gorm:"type:text;not null;index"`
	ProfileJSON       string          `gorm:"column:profile_json;type:jsonb;not null"`
	SummaryText       string          `gorm:"type:text"`
	SourceWindowStart time.Time       `gorm:"not null"`
	SourceWindowEnd   time.Time       `gorm:"not null"`
	Source            string          `gorm:"type:text;not null"`
	ModelName         string          `gorm:"type:text"`
	ModelVersion      string          `gorm:"type:text"`
	Confidence        decimal.Decimal `gorm:"type:numeric(8,4);not null;default:0"`
	JobRunID          *uuid.UUID      `gorm:"type:uuid;index"`
	CreatedAt         time.Time       `gorm:"not null"`
	UpdatedAt         time.Time       `gorm:"not null"`
}

func (SmartPickerLearningProfile) TableName() string { return "smart_picker_learning_profiles" }

func (m *SmartPickerLearningProfile) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerRankingHint struct {
	ID                uuid.UUID       `gorm:"type:uuid;primaryKey"`
	CompanyID         uint            `gorm:"not null;index:idx_sp_hint_lookup,priority:1"`
	UserID            *uuid.UUID      `gorm:"type:uuid;index"`
	Context           string          `gorm:"type:text;not null;index:idx_sp_hint_lookup,priority:2"`
	EntityType        string          `gorm:"type:text;not null;index:idx_sp_hint_lookup,priority:3"`
	EntityID          uint            `gorm:"not null;index:idx_sp_hint_lookup,priority:4"`
	BoostScore        decimal.Decimal `gorm:"type:numeric(8,4);not null;default:0"`
	Confidence        decimal.Decimal `gorm:"type:numeric(8,4);not null;default:0"`
	Reason            string          `gorm:"type:text"`
	Source            string          `gorm:"type:text;not null;index"`
	Status            string          `gorm:"type:text;not null;index:idx_sp_hint_lookup,priority:5"`
	ValidationStatus  string          `gorm:"type:text;not null;index:idx_sp_hint_lookup,priority:6"`
	ValidationError   string          `gorm:"type:text"`
	ActivatedByUserID *uuid.UUID      `gorm:"type:uuid"`
	RejectedByUserID  *uuid.UUID      `gorm:"type:uuid"`
	JobRunID          *uuid.UUID      `gorm:"type:uuid;index"`
	ExpiresAt         *time.Time      `gorm:"index"`
	CreatedAt         time.Time       `gorm:"not null"`
	UpdatedAt         time.Time       `gorm:"not null"`
}

func (SmartPickerRankingHint) TableName() string { return "smart_picker_ranking_hints" }

func (m *SmartPickerRankingHint) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerAliasSuggestion struct {
	ID               uuid.UUID       `gorm:"type:uuid;primaryKey"`
	CompanyID        uint            `gorm:"not null;index:idx_sp_alias_lookup,priority:1"`
	Context          string          `gorm:"type:text;not null;index:idx_sp_alias_lookup,priority:2"`
	EntityType       string          `gorm:"type:text;not null;index:idx_sp_alias_lookup,priority:3"`
	EntityID         uint            `gorm:"not null;index:idx_sp_alias_lookup,priority:4"`
	Alias            string          `gorm:"type:text;not null"`
	NormalizedAlias  string          `gorm:"type:text;not null;index:idx_sp_alias_lookup,priority:5"`
	Confidence       decimal.Decimal `gorm:"type:numeric(8,4);not null;default:0"`
	Reason           string          `gorm:"type:text"`
	Source           string          `gorm:"type:text;not null"`
	Status           string          `gorm:"type:text;not null;index:idx_sp_alias_lookup,priority:6"`
	ValidationStatus string          `gorm:"type:text;not null;index:idx_sp_alias_lookup,priority:7"`
	ValidationError  string          `gorm:"type:text"`
	ApprovedByUserID *uuid.UUID      `gorm:"type:uuid"`
	RejectedByUserID *uuid.UUID      `gorm:"type:uuid"`
	JobRunID         *uuid.UUID      `gorm:"type:uuid;index"`
	CreatedAt        time.Time       `gorm:"not null"`
	UpdatedAt        time.Time       `gorm:"not null"`
}

func (SmartPickerAliasSuggestion) TableName() string { return "smart_picker_alias_suggestions" }

func (m *SmartPickerAliasSuggestion) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type AIJobRun struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey"`
	CompanyID         *uint      `gorm:"index"`
	JobType           string     `gorm:"type:text;not null;index"`
	Status            string     `gorm:"type:text;not null;index"`
	TriggerType       string     `gorm:"type:text;not null"`
	TriggeredByUserID *uuid.UUID `gorm:"type:uuid;index"`
	StartedAt         *time.Time
	FinishedAt        *time.Time
	SourceWindowStart *time.Time
	SourceWindowEnd   *time.Time
	InputSummaryJSON  string `gorm:"column:input_summary_json;type:jsonb"`
	OutputSummaryJSON string `gorm:"column:output_summary_json;type:jsonb"`
	ErrorMessage      string `gorm:"type:text"`
	WarningsJSON      string `gorm:"column:warnings_json;type:jsonb"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (AIJobRun) TableName() string { return "ai_job_runs" }

func (m *AIJobRun) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type AIRequestLog struct {
	ID                    uuid.UUID  `gorm:"type:uuid;primaryKey"`
	CompanyID             *uint      `gorm:"index"`
	JobRunID              *uuid.UUID `gorm:"type:uuid;index"`
	TaskType              string     `gorm:"type:text;not null;index"`
	Provider              string     `gorm:"type:text"`
	Model                 string     `gorm:"type:text"`
	RequestSchemaVersion  string     `gorm:"type:text"`
	ResponseSchemaVersion string     `gorm:"type:text"`
	InputHash             string     `gorm:"type:text"`
	InputRedactedJSON     string     `gorm:"column:input_redacted_json;type:jsonb"`
	OutputRedactedJSON    string     `gorm:"column:output_redacted_json;type:jsonb"`
	Status                string     `gorm:"type:text;not null;index"`
	ErrorMessage          string     `gorm:"type:text"`
	PromptVersion         string     `gorm:"type:text"`
	TokenInputCount       *int
	TokenOutputCount      *int
	EstimatedCost         decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`
	LatencyMS             *int
	CreatedAt             time.Time `gorm:"not null"`
}

func (AIRequestLog) TableName() string { return "ai_request_logs" }

func (m *AIRequestLog) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}

type SmartPickerDecisionTrace struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey"`
	CompanyID        uint       `gorm:"not null;index"`
	UserID           *uuid.UUID `gorm:"type:uuid;index"`
	Context          string     `gorm:"type:text;not null;index"`
	EntityType       string     `gorm:"type:text;not null;index"`
	Query            string     `gorm:"type:text"`
	NormalizedQuery  string     `gorm:"type:text"`
	SelectedEntityID *uint
	ReturnedCount    *int
	TraceJSON        string    `gorm:"column:trace_json;type:jsonb;not null"`
	CreatedAt        time.Time `gorm:"not null"`
}

func (SmartPickerDecisionTrace) TableName() string { return "smart_picker_decision_traces" }

func (m *SmartPickerDecisionTrace) BeforeCreate(_ *gorm.DB) error {
	ensureUUID(&m.ID)
	return nil
}
