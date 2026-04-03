// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleClearingReport(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// Load all channel accounts.
	accounts, _ := services.ListChannelAccounts(s.DB, companyID)
	var warnings []string
	warningSet := map[string]struct{}{}
	appendWarning := func(msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" {
			return
		}
		if _, exists := warningSet[msg]; exists {
			return
		}
		warningSet[msg] = struct{}{}
		warnings = append(warnings, msg)
	}

	// Build summaries for each channel account.
	var summaries []services.ClearingSummary
	for _, a := range accounts {
		summary, err := services.GetClearingSummary(s.DB, companyID, a.ID)
		if err != nil {
			appendWarning(err.Error())
			continue
		}
		if summary == nil {
			continue
		}
		summaries = append(summaries, *summary)
	}

	// If a specific channel is selected, load movements.
	var selectedMovements []services.ClearingMovement
	var selectedChannelID uint
	if chRaw := c.Query("channel"); chRaw != "" {
		if id, err := strconv.ParseUint(chRaw, 10, 64); err == nil && id > 0 {
			selectedChannelID = uint(id)
			movements, mvErr := services.ListClearingMovements(s.DB, companyID, selectedChannelID, 100)
			if mvErr != nil {
				appendWarning(mvErr.Error())
			} else {
				selectedMovements = movements
			}
		}
	}

	vm := pages.ClearingReportVM{
		HasCompany:         true,
		Summaries:          summaries,
		SelectedChannelID:  selectedChannelID,
		Movements:          selectedMovements,
		Warnings:           warnings,
	}
	return pages.ClearingReport(vm).Render(c.Context(), c)
}
