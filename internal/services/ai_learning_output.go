package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"balanciz/internal/models"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type AIJobRunInput struct {
	CompanyID         *uint
	JobType           string
	TriggerType       string
	TriggeredByUserID *uuid.UUID
	SourceWindowStart *time.Time
	SourceWindowEnd   *time.Time
	InputSummaryJSON  string
}

func StartAIJobRun(db *gorm.DB, in AIJobRunInput) (*models.AIJobRun, error) {
	now := time.Now().UTC()
	trigger := strings.TrimSpace(in.TriggerType)
	if trigger == "" {
		trigger = models.AIJobTriggerSystem
	}
	run := models.AIJobRun{
		CompanyID:         in.CompanyID,
		JobType:           strings.TrimSpace(in.JobType),
		Status:            models.AIJobStatusRunning,
		TriggerType:       trigger,
		TriggeredByUserID: in.TriggeredByUserID,
		StartedAt:         &now,
		SourceWindowStart: in.SourceWindowStart,
		SourceWindowEnd:   in.SourceWindowEnd,
		InputSummaryJSON:  in.InputSummaryJSON,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if run.JobType == "" {
		return nil, fmt.Errorf("ai job_type required")
	}
	if err := db.Create(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func FinishAIJobRun(db *gorm.DB, run *models.AIJobRun, status string, outputSummary any, warnings []string, errMsg string) error {
	if run == nil || run.ID == uuid.Nil {
		return fmt.Errorf("ai job run required")
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"status":      status,
		"finished_at": now,
		"updated_at":  now,
	}
	if outputSummary != nil {
		updates["output_summary_json"] = jsonString(outputSummary)
	}
	if len(warnings) > 0 {
		updates["warnings_json"] = jsonString(warnings)
	}
	if strings.TrimSpace(errMsg) != "" {
		updates["error_message"] = strings.TrimSpace(errMsg)
	}
	if err := db.Model(run).Updates(updates).Error; err != nil {
		return err
	}
	run.Status = status
	run.FinishedAt = &now
	run.UpdatedAt = now
	if outputSummary != nil {
		run.OutputSummaryJSON = jsonString(outputSummary)
	}
	if len(warnings) > 0 {
		run.WarningsJSON = jsonString(warnings)
	}
	run.ErrorMessage = strings.TrimSpace(errMsg)
	return nil
}

type AIRequestLogInput struct {
	CompanyID             *uint
	JobRunID              *uuid.UUID
	TaskType              string
	Provider              string
	Model                 string
	RequestSchemaVersion  string
	ResponseSchemaVersion string
	InputRedactedJSON     string
	OutputRedactedJSON    string
	Status                string
	ErrorMessage          string
	PromptVersion         string
	TokenInputCount       *int
	TokenOutputCount      *int
	EstimatedCost         decimal.Decimal
	LatencyMS             *int
}

func CreateAIRequestLog(db *gorm.DB, in AIRequestLogInput) (*models.AIRequestLog, error) {
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = models.AIRequestStatusSkipped
	}
	log := models.AIRequestLog{
		CompanyID:             in.CompanyID,
		JobRunID:              in.JobRunID,
		TaskType:              strings.TrimSpace(in.TaskType),
		Provider:              strings.TrimSpace(in.Provider),
		Model:                 strings.TrimSpace(in.Model),
		RequestSchemaVersion:  strings.TrimSpace(in.RequestSchemaVersion),
		ResponseSchemaVersion: strings.TrimSpace(in.ResponseSchemaVersion),
		InputHash:             hashJSONLike(in.InputRedactedJSON),
		InputRedactedJSON:     emptyJSONIfBlank(in.InputRedactedJSON),
		OutputRedactedJSON:    emptyJSONIfBlank(in.OutputRedactedJSON),
		Status:                status,
		ErrorMessage:          strings.TrimSpace(in.ErrorMessage),
		PromptVersion:         strings.TrimSpace(in.PromptVersion),
		TokenInputCount:       in.TokenInputCount,
		TokenOutputCount:      in.TokenOutputCount,
		EstimatedCost:         in.EstimatedCost,
		LatencyMS:             in.LatencyMS,
		CreatedAt:             time.Now().UTC(),
	}
	if log.Provider == "" {
		log.Provider = "noop"
	}
	if log.TaskType == "" {
		return nil, fmt.Errorf("ai request task_type required")
	}
	if err := db.Create(&log).Error; err != nil {
		return nil, err
	}
	return &log, nil
}

type ReportUsageInput struct {
	CompanyID    uint
	UserID       *uuid.UUID
	ReportKey    string
	EventType    string
	DateRangeKey string
	Filters      map[string]any
	SourceRoute  string
	Metadata     map[string]any
	CreatedAt    time.Time
}

func RecordReportUsage(db *gorm.DB, in ReportUsageInput) error {
	reportKey := strings.TrimSpace(in.ReportKey)
	if ReportByKey(reportKey) == nil {
		return fmt.Errorf("unknown report_key: %s", reportKey)
	}
	eventType := strings.TrimSpace(in.EventType)
	if !validReportUsageEvent(eventType) {
		return fmt.Errorf("invalid report usage event_type: %s", eventType)
	}
	now := in.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return db.Transaction(func(tx *gorm.DB) error {
		event := models.ReportUsageEvent{
			CompanyID:    in.CompanyID,
			UserID:       in.UserID,
			ReportKey:    reportKey,
			EventType:    eventType,
			DateRangeKey: strings.TrimSpace(in.DateRangeKey),
			FiltersJSON:  jsonString(in.Filters),
			SourceRoute:  strings.TrimSpace(in.SourceRoute),
			MetadataJSON: jsonString(in.Metadata),
			CreatedAt:    now,
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		if err := upsertReportUsageStat(tx, in.CompanyID, nil, models.SmartPickerScopeCompany, reportKey, eventType, in.DateRangeKey, now); err != nil {
			return err
		}
		if in.UserID != nil {
			if err := upsertReportUsageStat(tx, in.CompanyID, in.UserID, models.SmartPickerScopeUser, reportKey, eventType, in.DateRangeKey, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertReportUsageStat(tx *gorm.DB, companyID uint, userID *uuid.UUID, scopeType, reportKey, eventType, dateRangeKey string, now time.Time) error {
	var stat models.ReportUsageStat
	q := tx.Where("company_id = ? AND scope_type = ? AND report_key = ?", companyID, scopeType, reportKey)
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	err := q.First(&stat).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		stat = models.ReportUsageStat{
			CompanyID: companyID,
			ScopeType: scopeType,
			UserID:    userID,
			ReportKey: reportKey,
			UpdatedAt: now,
		}
	}
	switch eventType {
	case models.ReportUsageOpened:
		stat.OpenCount++
		stat.LastOpenedAt = &now
	case models.ReportUsageExported:
		stat.ExportCount++
	case models.ReportUsagePrinted:
		stat.PrintCount++
	case models.ReportUsageDrilldownClicked:
		stat.DrilldownCount++
	case models.ReportUsageFiltered:
		stat.FilterCount++
	}
	stat.LastUsedAt = &now
	if strings.TrimSpace(dateRangeKey) != "" {
		stat.CommonDateRangeKey = strings.TrimSpace(dateRangeKey)
	}
	stat.UpdatedAt = now
	if stat.ID == uuid.Nil {
		return tx.Create(&stat).Error
	}
	return tx.Save(&stat).Error
}

type DashboardSuggestionOptions struct {
	WindowStart       time.Time
	WindowEnd         time.Time
	JobRunID          *uuid.UUID
	TriggeredByUserID *uuid.UUID
}

type dashboardReportEvidence struct {
	ReportKey      string     `json:"report_key"`
	OpenCount30D   int        `json:"open_count_30d"`
	ExportCount30D int        `json:"export_count_30d"`
	DrillCount30D  int        `json:"drilldown_count_30d"`
	LastOpenedAt   *time.Time `json:"last_opened_at,omitempty"`
}

func GenerateDashboardWidgetSuggestions(db *gorm.DB, companyID uint, userID *uuid.UUID, opts DashboardSuggestionOptions) ([]models.DashboardWidgetSuggestion, error) {
	end := opts.WindowEnd.UTC()
	if end.IsZero() {
		end = time.Now().UTC()
	}
	start := opts.WindowStart.UTC()
	if start.IsZero() {
		start = end.AddDate(0, 0, -30)
	}

	evidenceRows, err := dashboardSuggestionEvidence(db, companyID, userID, start, end)
	if err != nil {
		return nil, err
	}
	out := []models.DashboardWidgetSuggestion{}
	for _, evidence := range evidenceRows {
		widgetKey, title := dashboardWidgetForReport(evidence.ReportKey)
		if widgetKey == "" {
			continue
		}
		if evidence.OpenCount30D < 3 && evidence.ExportCount30D < 1 && evidence.DrillCount30D < 2 {
			continue
		}
		if dashboardWidgetActive(db, companyID, userID, widgetKey) || dashboardSuggestionExists(db, companyID, userID, widgetKey) {
			continue
		}
		reason := dashboardSuggestionReason(evidence.ReportKey, evidence)
		now := time.Now().UTC()
		suggestion := models.DashboardWidgetSuggestion{
			CompanyID:    companyID,
			UserID:       userID,
			WidgetKey:    widgetKey,
			Title:        title,
			Reason:       reason,
			EvidenceJSON: jsonString(evidence),
			Confidence:   dashboardSuggestionConfidence(evidence),
			Source:       models.DashboardSuggestionSourceSystem,
			Status:       models.DashboardSuggestionPending,
			JobRunID:     opts.JobRunID,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := db.Create(&suggestion).Error; err != nil {
			return out, err
		}
		slog.Info("dashboard.suggestion_generated", "company_id", companyID, "widget_key", widgetKey, "reason", reason)
		out = append(out, suggestion)
	}
	return out, nil
}

func RunDashboardRecommendation(db *gorm.DB, companyID uint, userID *uuid.UUID, opts DashboardSuggestionOptions) (*models.AIJobRun, []models.DashboardWidgetSuggestion, error) {
	companyIDPtr := companyID
	run, err := StartAIJobRun(db, AIJobRunInput{
		CompanyID:         &companyIDPtr,
		JobType:           models.AIJobDashboardRecommendation,
		TriggerType:       models.AIJobTriggerSystem,
		TriggeredByUserID: opts.TriggeredByUserID,
		SourceWindowStart: &opts.WindowStart,
		SourceWindowEnd:   &opts.WindowEnd,
		InputSummaryJSON: jsonString(map[string]any{
			"company_id": companyID,
			"user_id":    userIDString(userID),
		}),
	})
	if err != nil {
		return nil, nil, err
	}
	opts.JobRunID = &run.ID
	suggestions, err := GenerateDashboardWidgetSuggestions(db, companyID, userID, opts)
	if err != nil {
		_ = FinishAIJobRun(db, run, models.AIJobStatusFailed, nil, nil, err.Error())
		return run, suggestions, err
	}
	_ = FinishAIJobRun(db, run, models.AIJobStatusSucceeded, map[string]any{"suggestions_generated": len(suggestions)}, nil, "")
	return run, suggestions, nil
}

func AcceptDashboardWidgetSuggestion(db *gorm.DB, companyID uint, userID *uuid.UUID, suggestionID uuid.UUID) (*models.DashboardWidgetSuggestion, error) {
	var suggestion models.DashboardWidgetSuggestion
	err := db.Where("id = ? AND company_id = ?", suggestionID, companyID).First(&suggestion).Error
	if err != nil {
		return nil, err
	}
	if suggestion.UserID != nil && (userID == nil || *suggestion.UserID != *userID) {
		return nil, gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	err = db.Transaction(func(tx *gorm.DB) error {
		suggestion.Status = models.DashboardSuggestionAccepted
		suggestion.AcceptedAt = &now
		suggestion.UpdatedAt = now
		if err := tx.Save(&suggestion).Error; err != nil {
			return err
		}
		if !dashboardWidgetActive(tx, companyID, userID, suggestion.WidgetKey) {
			widget := models.DashboardUserWidget{
				CompanyID:  companyID,
				UserID:     userID,
				WidgetKey:  suggestion.WidgetKey,
				Title:      suggestion.Title,
				ConfigJSON: "{}",
				Source:     models.DashboardWidgetSourceSuggestion,
				Active:     true,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if err := tx.Create(&widget).Error; err != nil {
				return err
			}
		}
		reportKey := reportKeyFromSuggestionEvidence(suggestion.EvidenceJSON)
		if reportKey != "" {
			return RecordReportUsage(tx, ReportUsageInput{
				CompanyID:   companyID,
				UserID:      userID,
				ReportKey:   reportKey,
				EventType:   models.ReportUsageSuggestionAccepted,
				SourceRoute: "/api/dashboard/suggestions/" + suggestion.ID.String() + "/accept",
				Metadata:    map[string]any{"widget_key": suggestion.WidgetKey},
				CreatedAt:   now,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.Info("dashboard.suggestion_accepted", "company_id", companyID, "widget_key", suggestion.WidgetKey)
	return &suggestion, nil
}

func DismissDashboardWidgetSuggestion(db *gorm.DB, companyID uint, userID *uuid.UUID, suggestionID uuid.UUID) error {
	return transitionDashboardSuggestion(db, companyID, userID, suggestionID, models.DashboardSuggestionDismissed, nil)
}

func SnoozeDashboardWidgetSuggestion(db *gorm.DB, companyID uint, userID *uuid.UUID, suggestionID uuid.UUID, until time.Time) error {
	return transitionDashboardSuggestion(db, companyID, userID, suggestionID, models.DashboardSuggestionSnoozed, &until)
}

type ActionCenterOptions struct {
	Now               time.Time
	TriggeredByUserID *uuid.UUID
}

func GenerateActionCenterTasks(db *gorm.DB, companyID uint, userID *uuid.UUID, opts ActionCenterOptions) ([]models.ActionCenterTask, error) {
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	candidates := []models.ActionCenterTask{}
	candidates = append(candidates, overdueInvoiceTasks(db, companyID, userID, now)...)
	candidates = append(candidates, billsDueSoonTasks(db, companyID, userID, now)...)
	candidates = append(candidates, companySetupTasks(db, companyID, userID, now)...)
	slog.Warn("action_center.provider_noop", "company_id", companyID, "provider", "bank_reconciliation", "reason", "bank statement transaction domain is not wired into Action Center V1")
	slog.Warn("action_center.provider_noop", "company_id", companyID, "provider", "sales_tax_filing", "reason", "no reliable filing calendar configured")

	out := []models.ActionCenterTask{}
	for _, candidate := range candidates {
		task, err := upsertActionCenterTask(db, candidate, userID)
		if err != nil {
			return out, err
		}
		out = append(out, *task)
	}
	return out, nil
}

func RunActionCenterGeneration(db *gorm.DB, companyID uint, userID *uuid.UUID, opts ActionCenterOptions) (*models.AIJobRun, []models.ActionCenterTask, error) {
	companyIDPtr := companyID
	run, err := StartAIJobRun(db, AIJobRunInput{
		CompanyID:         &companyIDPtr,
		JobType:           models.AIJobActionCenterGeneration,
		TriggerType:       models.AIJobTriggerSystem,
		TriggeredByUserID: opts.TriggeredByUserID,
		InputSummaryJSON:  jsonString(map[string]any{"company_id": companyID, "user_id": userIDString(userID)}),
	})
	if err != nil {
		return nil, nil, err
	}
	tasks, err := GenerateActionCenterTasks(db, companyID, userID, opts)
	if err != nil {
		_ = FinishAIJobRun(db, run, models.AIJobStatusFailed, nil, nil, err.Error())
		return run, tasks, err
	}
	_ = FinishAIJobRun(db, run, models.AIJobStatusSucceeded, map[string]any{"tasks_generated": len(tasks)}, nil, "")
	return run, tasks, nil
}

func StartActionCenterTask(db *gorm.DB, companyID uint, userID *uuid.UUID, taskID uuid.UUID) error {
	return transitionActionCenterTask(db, companyID, userID, taskID, models.ActionTaskStatusInProgress, models.ActionTaskEventStarted, nil)
}

func CompleteActionCenterTask(db *gorm.DB, companyID uint, userID *uuid.UUID, taskID uuid.UUID) error {
	now := time.Now().UTC()
	return transitionActionCenterTask(db, companyID, userID, taskID, models.ActionTaskStatusDone, models.ActionTaskEventCompleted, &now)
}

func DismissActionCenterTask(db *gorm.DB, companyID uint, userID *uuid.UUID, taskID uuid.UUID) error {
	now := time.Now().UTC()
	return transitionActionCenterTask(db, companyID, userID, taskID, models.ActionTaskStatusDismissed, models.ActionTaskEventDismissed, &now)
}

func SnoozeActionCenterTask(db *gorm.DB, companyID uint, userID *uuid.UUID, taskID uuid.UUID, until time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var task models.ActionCenterTask
		if err := tx.Where("id = ? AND company_id = ?", taskID, companyID).First(&task).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		task.Status = models.ActionTaskStatusSnoozed
		task.SnoozedUntil = &until
		task.UpdatedAt = now
		if err := tx.Save(&task).Error; err != nil {
			return err
		}
		return createActionCenterTaskEvent(tx, companyID, taskID, userID, models.ActionTaskEventSnoozed, map[string]any{"snoozed_until": until.UTC().Format(time.RFC3339)})
	})
}

func overdueInvoiceTasks(db *gorm.DB, companyID uint, userID *uuid.UUID, now time.Time) []models.ActionCenterTask {
	var invoices []models.Invoice
	if err := db.Where("company_id = ? AND status IN ? AND balance_due > 0 AND due_date IS NOT NULL AND due_date < ?",
		companyID,
		[]models.InvoiceStatus{models.InvoiceStatusIssued, models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid, models.InvoiceStatusOverdue},
		actionDateOnly(now)).
		Order("due_date ASC").
		Limit(50).
		Find(&invoices).Error; err != nil {
		slog.Warn("action_center.overdue_invoices_failed", "company_id", companyID, "error", err)
		return nil
	}
	if len(invoices) == 0 {
		return nil
	}
	total := decimal.Zero
	oldest := invoices[0].DueDate
	samples := []map[string]string{}
	for i, inv := range invoices {
		total = total.Add(inv.BalanceDue)
		if inv.DueDate != nil && (oldest == nil || inv.DueDate.Before(*oldest)) {
			oldest = inv.DueDate
		}
		if i < 3 {
			due := ""
			if inv.DueDate != nil {
				due = inv.DueDate.Format("2006-01-02")
			}
			samples = append(samples, map[string]string{
				"number":   inv.InvoiceNumber,
				"due_date": due,
				"amount":   inv.BalanceDue.StringFixed(2),
			})
		}
	}
	priority := models.ActionTaskPriorityMedium
	if oldest != nil && actionDateOnly(*oldest).Before(actionDateOnly(now).AddDate(0, 0, -30)) {
		priority = models.ActionTaskPriorityHigh
	}
	reason := fmt.Sprintf("There are %d unpaid invoices past due.", len(invoices))
	return []models.ActionCenterTask{newActionCenterTask(companyID, userID, "invoices_overdue", "ar_engine", "rule", "Review overdue invoices", reason, priority, "/reports/ar-aging", "ar:invoices_overdue:v1", map[string]any{
		"count":           len(invoices),
		"total_amount":    total.StringFixed(2),
		"oldest_due_date": datePtrString(oldest),
		"sample_invoices": samples,
	})}
}

func billsDueSoonTasks(db *gorm.DB, companyID uint, userID *uuid.UUID, now time.Time) []models.ActionCenterTask {
	windowEnd := actionDateOnly(now).AddDate(0, 0, 7)
	var bills []models.Bill
	if err := db.Where("company_id = ? AND status IN ? AND balance_due > 0 AND due_date IS NOT NULL AND due_date <= ?",
		companyID,
		[]models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid},
		windowEnd).
		Order("due_date ASC").
		Limit(50).
		Find(&bills).Error; err != nil {
		slog.Warn("action_center.bills_due_failed", "company_id", companyID, "error", err)
		return nil
	}
	if len(bills) == 0 {
		return nil
	}
	total := decimal.Zero
	overdue := false
	samples := []map[string]string{}
	for i, bill := range bills {
		total = total.Add(bill.BalanceDue)
		if bill.DueDate != nil && actionDateOnly(*bill.DueDate).Before(actionDateOnly(now)) {
			overdue = true
		}
		if i < 3 {
			due := ""
			if bill.DueDate != nil {
				due = bill.DueDate.Format("2006-01-02")
			}
			samples = append(samples, map[string]string{
				"number":   bill.BillNumber,
				"due_date": due,
				"amount":   bill.BalanceDue.StringFixed(2),
			})
		}
	}
	priority := models.ActionTaskPriorityMedium
	if overdue {
		priority = models.ActionTaskPriorityHigh
	}
	reason := fmt.Sprintf("There are %d unpaid bills due within the next 7 days or overdue.", len(bills))
	return []models.ActionCenterTask{newActionCenterTask(companyID, userID, "bills_due_soon", "ap_engine", "rule", "Review bills due soon", reason, priority, "/banking/pay-bills", "ap:bills_due_soon:v1", map[string]any{
		"count":        len(bills),
		"total_amount": total.StringFixed(2),
		"overdue":      overdue,
		"sample_bills": samples,
	})}
}

func companySetupTasks(db *gorm.DB, companyID uint, userID *uuid.UUID, now time.Time) []models.ActionCenterTask {
	var company models.Company
	if err := db.Where("id = ?", companyID).First(&company).Error; err != nil {
		return nil
	}
	out := []models.ActionCenterTask{}
	if strings.TrimSpace(company.City) == "" || strings.TrimSpace(company.AddressLine) == "" {
		out = append(out, newActionCenterTask(companyID, userID, "company_profile_incomplete", "settings_engine", "rule", "Complete company profile", "Company profile is missing address information used on documents and reports.", models.ActionTaskPriorityLow, "/settings/company/profile", "settings:company_profile_incomplete:v1", map[string]any{
			"missing_city":    strings.TrimSpace(company.City) == "",
			"missing_address": strings.TrimSpace(company.AddressLine) == "",
		}))
	}
	var notif models.CompanyNotificationSettings
	err := db.Where("company_id = ?", companyID).First(&notif).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (!notif.EmailEnabled || !notif.EmailVerificationReady) {
		out = append(out, newActionCenterTask(companyID, userID, "smtp_not_ready", "settings_engine", "rule", "Configure email delivery", "Company email delivery is not verified, so invoice sending and email reminders may not work.", models.ActionTaskPriorityLow, "/settings/company/notifications", "settings:smtp_not_ready:v1", map[string]any{
			"email_enabled":            notif.EmailEnabled,
			"email_verification_ready": notif.EmailVerificationReady,
		}))
	}
	_ = now
	return out
}

func newActionCenterTask(companyID uint, userID *uuid.UUID, taskType, sourceEngine, sourceType, title, reason, priority, actionURL, fingerprint string, evidence any) models.ActionCenterTask {
	now := time.Now().UTC()
	return models.ActionCenterTask{
		CompanyID:      companyID,
		AssignedUserID: userID,
		TaskType:       taskType,
		SourceEngine:   sourceEngine,
		SourceType:     sourceType,
		Title:          title,
		Reason:         reason,
		EvidenceJSON:   jsonString(evidence),
		Priority:       priority,
		ActionURL:      actionURL,
		Status:         models.ActionTaskStatusOpen,
		Fingerprint:    fingerprint,
		AIGenerated:    false,
		Confidence:     decimal.Zero,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func upsertActionCenterTask(db *gorm.DB, candidate models.ActionCenterTask, userID *uuid.UUID) (*models.ActionCenterTask, error) {
	var existing models.ActionCenterTask
	err := db.Where("company_id = ? AND fingerprint = ?", candidate.CompanyID, candidate.Fingerprint).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&candidate).Error; err != nil {
				return err
			}
			return createActionCenterTaskEvent(tx, candidate.CompanyID, candidate.ID, userID, models.ActionTaskEventCreated, map[string]any{"fingerprint": candidate.Fingerprint})
		}); err != nil {
			return nil, err
		}
		slog.Info("action_center.task_generated", "company_id", candidate.CompanyID, "task_type", candidate.TaskType, "fingerprint", candidate.Fingerprint)
		return &candidate, nil
	}
	if existing.Status == models.ActionTaskStatusOpen || existing.Status == models.ActionTaskStatusInProgress || existing.Status == models.ActionTaskStatusSnoozed || existing.Status == models.ActionTaskStatusBlocked {
		existing.Title = candidate.Title
		existing.Description = candidate.Description
		existing.Reason = candidate.Reason
		existing.EvidenceJSON = candidate.EvidenceJSON
		existing.Priority = candidate.Priority
		existing.ActionURL = candidate.ActionURL
		existing.UpdatedAt = time.Now().UTC()
		if err := db.Save(&existing).Error; err != nil {
			return nil, err
		}
	}
	slog.Info("action_center.task_deduped", "company_id", existing.CompanyID, "task_type", existing.TaskType, "fingerprint", existing.Fingerprint)
	return &existing, nil
}

func transitionActionCenterTask(db *gorm.DB, companyID uint, userID *uuid.UUID, taskID uuid.UUID, status string, eventType string, completedOrDismissedAt *time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var task models.ActionCenterTask
		if err := tx.Where("id = ? AND company_id = ?", taskID, companyID).First(&task).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		task.Status = status
		task.UpdatedAt = now
		switch status {
		case models.ActionTaskStatusDone:
			task.CompletedAt = completedOrDismissedAt
		case models.ActionTaskStatusDismissed:
			task.DismissedAt = completedOrDismissedAt
		}
		if err := tx.Save(&task).Error; err != nil {
			return err
		}
		return createActionCenterTaskEvent(tx, companyID, taskID, userID, eventType, map[string]any{"status": status})
	})
}

func createActionCenterTaskEvent(tx *gorm.DB, companyID uint, taskID uuid.UUID, userID *uuid.UUID, eventType string, metadata any) error {
	event := models.ActionCenterTaskEvent{
		CompanyID:    companyID,
		TaskID:       taskID,
		UserID:       userID,
		EventType:    eventType,
		MetadataJSON: jsonString(metadata),
		CreatedAt:    time.Now().UTC(),
	}
	return tx.Create(&event).Error
}

func dashboardSuggestionEvidence(db *gorm.DB, companyID uint, userID *uuid.UUID, start, end time.Time) ([]dashboardReportEvidence, error) {
	type row struct {
		ReportKey      string
		OpenCount30D   int
		ExportCount30D int
		DrillCount30D  int
	}
	q := db.Model(&models.ReportUsageEvent{}).
		Select(`
			report_key,
			SUM(CASE WHEN event_type = ? THEN 1 ELSE 0 END) AS open_count30_d,
			SUM(CASE WHEN event_type = ? THEN 1 ELSE 0 END) AS export_count30_d,
			SUM(CASE WHEN event_type = ? THEN 1 ELSE 0 END) AS drill_count30_d`,
			models.ReportUsageOpened, models.ReportUsageExported, models.ReportUsageDrilldownClicked).
		Where("company_id = ? AND created_at BETWEEN ? AND ?", companyID, start, end).
		Group("report_key")
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	var rows []row
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardReportEvidence, 0, len(rows))
	for _, r := range rows {
		var lastOpened models.ReportUsageEvent
		lastQ := db.Where("company_id = ? AND report_key = ? AND event_type = ? AND created_at BETWEEN ? AND ?", companyID, r.ReportKey, models.ReportUsageOpened, start, end).
			Order("created_at DESC")
		if userID != nil {
			lastQ = lastQ.Where("user_id = ?", *userID)
		}
		var lastOpenedAt *time.Time
		if err := lastQ.First(&lastOpened).Error; err == nil {
			lastOpenedAt = &lastOpened.CreatedAt
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		out = append(out, dashboardReportEvidence{
			ReportKey:      r.ReportKey,
			OpenCount30D:   r.OpenCount30D,
			ExportCount30D: r.ExportCount30D,
			DrillCount30D:  r.DrillCount30D,
			LastOpenedAt:   lastOpenedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReportKey < out[j].ReportKey })
	return out, nil
}

func dashboardWidgetForReport(reportKey string) (string, string) {
	switch reportKey {
	case "ar-aging":
		return "ar_aging", "Add AR Aging to dashboard"
	case "ap-aging":
		return "ap_aging", "Add AP Aging to dashboard"
	case "income-statement":
		return "profit_loss", "Add Profit & Loss to dashboard"
	case "sales-tax":
		return "sales_tax_payable", "Add Sales Tax Payable to dashboard"
	case "cash-flow":
		return "cash_flow", "Add Cash Flow to dashboard"
	default:
		return "", ""
	}
}

func dashboardSuggestionReason(reportKey string, evidence dashboardReportEvidence) string {
	entry := ReportByKey(reportKey)
	title := reportKey
	if entry != nil {
		title = entry.Title
	}
	switch {
	case evidence.OpenCount30D >= 3:
		return fmt.Sprintf("You viewed %s %d times in the last 30 days.", title, evidence.OpenCount30D)
	case evidence.ExportCount30D >= 1:
		return fmt.Sprintf("You exported %s recently.", title)
	default:
		return fmt.Sprintf("You drilled into %s %d times in the last 30 days.", title, evidence.DrillCount30D)
	}
}

func dashboardSuggestionConfidence(e dashboardReportEvidence) decimal.Decimal {
	score := float64(e.OpenCount30D)*0.15 + float64(e.ExportCount30D)*0.35 + float64(e.DrillCount30D)*0.2
	if score > 0.95 {
		score = 0.95
	}
	if score < 0.25 {
		score = 0.25
	}
	return decimal.NewFromFloat(score)
}

func dashboardWidgetActive(db *gorm.DB, companyID uint, userID *uuid.UUID, widgetKey string) bool {
	q := db.Model(&models.DashboardUserWidget{}).Where("company_id = ? AND widget_key = ? AND active = ?", companyID, widgetKey, true)
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	var count int64
	return q.Count(&count).Error == nil && count > 0
}

func dashboardSuggestionExists(db *gorm.DB, companyID uint, userID *uuid.UUID, widgetKey string) bool {
	q := db.Model(&models.DashboardWidgetSuggestion{}).
		Where("company_id = ? AND widget_key = ? AND status IN ?", companyID, widgetKey, []string{
			models.DashboardSuggestionPending,
			models.DashboardSuggestionAccepted,
			models.DashboardSuggestionSnoozed,
		})
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	var count int64
	return q.Count(&count).Error == nil && count > 0
}

func transitionDashboardSuggestion(db *gorm.DB, companyID uint, userID *uuid.UUID, suggestionID uuid.UUID, status string, snoozedUntil *time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var suggestion models.DashboardWidgetSuggestion
		if err := tx.Where("id = ? AND company_id = ?", suggestionID, companyID).First(&suggestion).Error; err != nil {
			return err
		}
		if suggestion.UserID != nil && (userID == nil || *suggestion.UserID != *userID) {
			return gorm.ErrRecordNotFound
		}
		now := time.Now().UTC()
		suggestion.Status = status
		suggestion.UpdatedAt = now
		if status == models.DashboardSuggestionDismissed {
			suggestion.DismissedAt = &now
		}
		if status == models.DashboardSuggestionSnoozed {
			suggestion.SnoozedUntil = snoozedUntil
		}
		if err := tx.Save(&suggestion).Error; err != nil {
			return err
		}
		reportKey := reportKeyFromSuggestionEvidence(suggestion.EvidenceJSON)
		if status == models.DashboardSuggestionDismissed && reportKey != "" {
			return RecordReportUsage(tx, ReportUsageInput{
				CompanyID:   companyID,
				UserID:      userID,
				ReportKey:   reportKey,
				EventType:   models.ReportUsageSuggestionDismissed,
				SourceRoute: "/api/dashboard/suggestions/" + suggestion.ID.String() + "/dismiss",
				Metadata:    map[string]any{"widget_key": suggestion.WidgetKey},
				CreatedAt:   now,
			})
		}
		return nil
	})
}

func reportKeyFromSuggestionEvidence(raw string) string {
	var evidence dashboardReportEvidence
	if err := json.Unmarshal([]byte(raw), &evidence); err == nil {
		return strings.TrimSpace(evidence.ReportKey)
	}
	return ""
}

func validReportUsageEvent(eventType string) bool {
	switch eventType {
	case models.ReportUsageOpened, models.ReportUsageFiltered, models.ReportUsageExported, models.ReportUsagePrinted,
		models.ReportUsageDrilldownClicked, models.ReportUsageAddedToDashboard, models.ReportUsageRemovedFromDashboard,
		models.ReportUsageSuggestionAccepted, models.ReportUsageSuggestionDismissed:
		return true
	default:
		return false
	}
}

func jsonString(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func emptyJSONIfBlank(v string) string {
	if strings.TrimSpace(v) == "" {
		return "{}"
	}
	return v
}

func hashJSONLike(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func userIDString(userID *uuid.UUID) string {
	if userID == nil {
		return ""
	}
	return userID.String()
}

func actionDateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func datePtrString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}
