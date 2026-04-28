package web

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
)

func (s *Server) handleSmartPickerLearningRun(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	userID := smartPickerUserID(c)
	end := time.Now().UTC()
	run, err := s.RunSmartPickerLearning(c.Context(), companyID, smartPickerLearningOptions{
		WindowStart:       end.AddDate(0, 0, -30),
		WindowEnd:         end,
		TriggerType:       models.AIJobTriggerManual,
		TriggeredByUserID: userID,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "learning run failed", "job_run_id": runIDString(run)})
	}
	return c.JSON(fiber.Map{
		"ok":         true,
		"job_run_id": run.ID.String(),
		"status":     run.Status,
	})
}

func runIDString(run *models.AIJobRun) string {
	if run == nil {
		return ""
	}
	return run.ID.String()
}
