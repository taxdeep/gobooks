// 遵循project_guide.md
package web

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/ai"
	"balanciz/internal/models"
)

// handleAIMemoAssist serves POST /api/ai/invoice-memo-assist.
//
// It reads the invoice from the database (company-scoped), builds context
// from the customer name and line descriptions, calls the AI platform, and
// returns a suggested memo.
//
// The suggestion is advisory only — the user must accept it manually.
// Business data (customer, amounts) is always read from the DB;
// the request body only carries the invoice ID.
//
// JSON request:  {"invoice_id": 123}
// JSON response: {"suggestion": "..."} | {"error": "..."}
func (s *Server) handleAIMemoAssist(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}

	var body struct {
		InvoiceID uint `json:"invoice_id"`
	}
	if err := c.BodyParser(&body); err != nil || body.InvoiceID == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invoice_id required"})
	}

	// Load invoice + customer + lines, strictly scoped to the authenticated company.
	var inv models.Invoice
	err := s.DB.
		Preload("Customer").
		Preload("Lines").
		Where("id = ? AND company_id = ?", body.InvoiceID, companyID).
		First(&inv).Error
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "invoice not found"})
	}

	// Build services description from line descriptions (non-blank, de-duplicated).
	serviceSet := make(map[string]struct{})
	var serviceDescs []string
	for _, l := range inv.Lines {
		desc := strings.TrimSpace(l.Description)
		if desc == "" {
			continue
		}
		if _, seen := serviceSet[desc]; !seen {
			serviceSet[desc] = struct{}{}
			serviceDescs = append(serviceDescs, desc)
		}
	}
	services := strings.Join(serviceDescs, ", ")
	if services == "" {
		services = "professional services"
	}

	vars := map[string]string{
		"customer": inv.Customer.Name,
		"services": services,
		"total":    fmt.Sprintf("$%s", inv.Amount.StringFixed(2)),
	}

	suggestion, err := s.AIAssist.Complete(c.Context(), companyID, ai.PromptInvoiceMemoAssist, vars)
	if err != nil {
		if errors.Is(err, ai.ErrAIDisabled) {
			slog.Info("ai.memo_assist.disabled",
				"company_id", companyID,
				"invoice_id", inv.ID,
			)
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
				"error": "AI is not configured for this company",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "AI assist unavailable",
		})
	}
	slog.Info("ai.memo_assist.succeeded",
		"company_id", companyID,
		"invoice_id", inv.ID,
		"suggestion_len", len(suggestion),
	)

	return c.JSON(fiber.Map{"suggestion": suggestion})
}

// handleAIEmailAssist serves POST /api/ai/invoice-email-assist.
//
// Reads the posted invoice from the database, builds a reminder-email draft using
// the AI platform, and returns the suggested body text. The user must click "Apply"
// in the send modal UI to copy it into the body textarea.
//
// Business data (customer, amounts, due date) is always read from the DB;
// the request body only carries the invoice ID.
//
// JSON request:  {"invoice_id": 123}
// JSON response: {"suggestion": "..."} | {"error": "..."}
func (s *Server) handleAIEmailAssist(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}

	var body struct {
		InvoiceID uint `json:"invoice_id"`
	}
	if err := c.BodyParser(&body); err != nil || body.InvoiceID == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invoice_id required"})
	}

	var inv models.Invoice
	err := s.DB.
		Preload("Customer").
		Where("id = ? AND company_id = ?", body.InvoiceID, companyID).
		First(&inv).Error
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "invoice not found"})
	}

	overdueDays := 0
	dueLabel := ""
	if inv.DueDate != nil && !inv.DueDate.IsZero() {
		dueLabel = inv.DueDate.Format("Jan 2, 2006")
		if days := int(time.Since(*inv.DueDate).Hours() / 24); days > 0 {
			overdueDays = days
		}
	}

	vars := map[string]string{
		"customer":       inv.Customer.Name,
		"invoice_number": inv.InvoiceNumber,
		"amount":         fmt.Sprintf("$%s", inv.Amount.StringFixed(2)),
		"due_date":       dueLabel,
		"overdue_days":   fmt.Sprintf("%d", overdueDays),
	}

	suggestion, err := s.AIAssist.Complete(c.Context(), companyID, ai.PromptInvoiceEmailDraft, vars)
	if err != nil {
		if errors.Is(err, ai.ErrAIDisabled) {
			slog.Info("ai.email_assist.disabled",
				"company_id", companyID,
				"invoice_id", inv.ID,
			)
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
				"error": "AI is not configured for this company",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "AI assist unavailable",
		})
	}
	slog.Info("ai.email_assist.succeeded",
		"company_id", companyID,
		"invoice_id", inv.ID,
		"suggestion_len", len(suggestion),
	)

	return c.JSON(fiber.Map{"suggestion": suggestion})
}
