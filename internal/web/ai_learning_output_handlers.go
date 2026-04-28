package web

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

type reportUsageRequest struct {
	ReportKey    string         `json:"report_key"`
	EventType    string         `json:"event_type"`
	DateRangeKey string         `json:"date_range_key"`
	Filters      map[string]any `json:"filters"`
	SourceRoute  string         `json:"source_route"`
	Metadata     map[string]any `json:"metadata"`
}

func (s *Server) handleReportUsage(c *fiber.Ctx) error {
	if !s.Cfg.ReportUsageLearningEnabled {
		return c.JSON(fiber.Map{"ok": true, "skipped": true})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	var body reportUsageRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	if err := services.RecordReportUsage(s.DB, services.ReportUsageInput{
		CompanyID:    companyID,
		UserID:       smartPickerUserID(c),
		ReportKey:    body.ReportKey,
		EventType:    body.EventType,
		DateRangeKey: body.DateRangeKey,
		Filters:      body.Filters,
		SourceRoute:  body.SourceRoute,
		Metadata:     body.Metadata,
	}); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) handleDashboardSuggestionsRun(c *fiber.Ctx) error {
	if !s.Cfg.DashboardRecommendationEnabled {
		return c.JSON(fiber.Map{"ok": true, "skipped": true})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	run, suggestions, err := services.RunDashboardRecommendation(s.DB, companyID, smartPickerUserID(c), services.DashboardSuggestionOptions{
		WindowEnd:         time.Now().UTC(),
		TriggeredByUserID: smartPickerUserID(c),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "dashboard recommendation failed"})
	}
	return c.JSON(fiber.Map{"ok": true, "job_run_id": run.ID.String(), "suggestions_generated": len(suggestions)})
}

func (s *Server) handleDashboardSuggestionsList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	status := c.Query("status", models.DashboardSuggestionPending)
	var rows []models.DashboardWidgetSuggestion
	q := s.DB.Where("company_id = ? AND status = ?", companyID, status)
	if userID := smartPickerUserID(c); userID != nil {
		q = q.Where("(user_id IS NULL OR user_id = ?)", *userID)
	} else {
		q = q.Where("user_id IS NULL")
	}
	if err := q.Order("created_at DESC").Limit(50).Find(&rows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "dashboard suggestions failed"})
	}
	return c.JSON(fiber.Map{"ok": true, "suggestions": rows})
}

func (s *Server) handleDashboardSuggestionAccept(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid suggestion id"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	suggestion, err := services.AcceptDashboardWidgetSuggestion(s.DB, companyID, smartPickerUserID(c), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "suggestion not found"})
	}
	return c.JSON(fiber.Map{"ok": true, "suggestion": suggestion})
}

func (s *Server) handleDashboardSuggestionDismiss(c *fiber.Ctx) error {
	return s.handleDashboardSuggestionTransition(c, models.DashboardSuggestionDismissed)
}

func (s *Server) handleDashboardSuggestionSnooze(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid suggestion id"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	days := 7
	if raw := c.Query("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	if err := services.SnoozeDashboardWidgetSuggestion(s.DB, companyID, smartPickerUserID(c), id, time.Now().UTC().AddDate(0, 0, days)); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "suggestion not found"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) handleDashboardSuggestionTransition(c *fiber.Ctx, status string) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid suggestion id"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	if status == models.DashboardSuggestionDismissed {
		err = services.DismissDashboardWidgetSuggestion(s.DB, companyID, smartPickerUserID(c), id)
	}
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "suggestion not found"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) handleActionCenterRun(c *fiber.Ctx) error {
	if !s.Cfg.ActionCenterEnabled {
		return c.JSON(fiber.Map{"ok": true, "skipped": true})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	run, tasks, err := services.RunActionCenterGeneration(s.DB, companyID, smartPickerUserID(c), services.ActionCenterOptions{
		TriggeredByUserID: smartPickerUserID(c),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "action center generation failed"})
	}
	return c.JSON(fiber.Map{"ok": true, "job_run_id": run.ID.String(), "tasks_generated": len(tasks)})
}

func (s *Server) handleActionCenterTasks(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	status := c.Query("status", models.ActionTaskStatusOpen)
	var rows []models.ActionCenterTask
	q := s.DB.Where("company_id = ? AND status = ?", companyID, status)
	if userID := smartPickerUserID(c); userID != nil {
		q = q.Where("(assigned_user_id IS NULL OR assigned_user_id = ?)", *userID)
	} else {
		q = q.Where("assigned_user_id IS NULL")
	}
	if err := q.Order("priority DESC, due_date ASC, created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "action center tasks failed"})
	}
	return c.JSON(fiber.Map{"ok": true, "tasks": rows})
}

func (s *Server) handleActionCenterTaskStart(c *fiber.Ctx) error {
	return s.handleActionCenterTaskTransition(c, "start")
}

func (s *Server) handleActionCenterTaskDone(c *fiber.Ctx) error {
	return s.handleActionCenterTaskTransition(c, "done")
}

func (s *Server) handleActionCenterTaskDismiss(c *fiber.Ctx) error {
	return s.handleActionCenterTaskTransition(c, "dismiss")
}

func (s *Server) handleActionCenterTaskSnooze(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid task id"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	days := 7
	if raw := c.Query("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	if err := services.SnoozeActionCenterTask(s.DB, companyID, smartPickerUserID(c), id, time.Now().UTC().AddDate(0, 0, days)); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "task not found"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) handleActionCenterTaskTransition(c *fiber.Ctx, action string) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid task id"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	switch action {
	case "start":
		err = services.StartActionCenterTask(s.DB, companyID, smartPickerUserID(c), id)
	case "done":
		err = services.CompleteActionCenterTask(s.DB, companyID, smartPickerUserID(c), id)
	case "dismiss":
		err = services.DismissActionCenterTask(s.DB, companyID, smartPickerUserID(c), id)
	}
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "task not found"})
	}
	return c.JSON(fiber.Map{"ok": true})
}
