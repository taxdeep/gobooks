// 遵循project_guide.md
package web

// investigation_workspace_handlers.go — Batch 24/25: Investigation workspace UI.
//
// Routes (registered in routes.go):
//
//   GET /settings/payment-gateways/investigation
//         — unified investigation workspace showing both exception domains
//           with filtering, summary counts, pagination, and navigation to detail pages.
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// This handler is a read-only aggregation view.  It:
//   - Reads filter and pagination parameters from query string
//   - Builds per-domain type option lists for the type dropdown
//   - Calls the workspace service to get rows + bucket counts
//   - Renders the template
//
// It does NOT:
//   - Execute any resolution hooks
//   - Modify any exception, payout, bank entry, or JE
//   - Replace the existing per-domain list / detail handlers

import (
	"math"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

const (
	investigationWorkspaceBase     = "/settings/payment-gateways/investigation"
	investigationWorkspacePageSize = 50
)

// handleInvestigationWorkspace renders the unified investigation workspace.
// GET /settings/payment-gateways/investigation
func (s *Server) handleInvestigationWorkspace(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// ── Parse pagination ──────────────────────────────────────────────────────

	page := 1
	if p, err := strconv.Atoi(strings.TrimSpace(c.Query("page"))); err == nil && p > 0 {
		page = p
	}
	offset := (page - 1) * investigationWorkspacePageSize

	// ── Parse filter ──────────────────────────────────────────────────────────

	filter := services.WorkspaceFilter{
		Domain:            services.OperationalExceptionDomain(strings.TrimSpace(c.Query("domain"))),
		Status:            strings.TrimSpace(c.Query("status")),
		TypeStr:           strings.TrimSpace(c.Query("type")),
		HasAvailableHooks: c.Query("hooks") == "1",
		NoAttempts:        c.Query("no_attempts") == "1",
		HasLinkedPayout:   c.Query("has_payout") == "1",
		Limit:             investigationWorkspacePageSize,
		Offset:            offset,
	}

	// Sanitize domain: only accept known values.
	if filter.Domain != services.DomainReconciliation && filter.Domain != services.DomainPaymentReverse {
		filter.Domain = ""
	}

	// Sanitize TypeStr: must be a known type for the selected domain.
	filter.TypeStr = sanitizeWorkspaceTypeStr(filter.Domain, filter.TypeStr)

	if cursorToken := strings.TrimSpace(c.Query("cursor")); cursorToken != "" {
		if cursor, ok := services.DecodeCursor(cursorToken); ok {
			filter.CursorAfter = &cursor
			filter.Offset = 0
		}
	}

	// ── Load workspace rows (filtered + paginated) ────────────────────────────

	wp, err := services.ListWorkspacePage(s.DB, companyID, filter)
	if err != nil {
		wp = services.WorkspacePage{}
	}
	rows, total := wp.Rows, wp.Total

	totalPages := 1
	if investigationWorkspacePageSize > 0 && total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(investigationWorkspacePageSize)))
	}

	if filter.CursorAfter == nil && total > 0 && page > totalPages {
		page = totalPages
		filter.Offset = (page - 1) * investigationWorkspacePageSize
		wp, err = services.ListWorkspacePage(s.DB, companyID, filter)
		if err != nil {
			wp = services.WorkspacePage{}
		}
		rows, total = wp.Rows, wp.Total
	}

	// ── Load bucket counts (unfiltered, always full company state) ────────────

	buckets, err := services.CountOperationalBuckets(s.DB, companyID)
	if err != nil {
		buckets = services.OperationalBuckets{}
	}

	// ── Compute pagination metadata ───────────────────────────────────────────

	totalPages = 1
	if investigationWorkspacePageSize > 0 && total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(investigationWorkspacePageSize)))
	}
	if filter.CursorAfter != nil && total > 0 && page > totalPages {
		page = totalPages
	}

	vm := pages.InvestigationWorkspaceVM{
		HasCompany:  true,
		Rows:        rows,
		Buckets:     buckets,
		Filter:      filter,
		TypeOptions: workspaceTypeOptions(filter.Domain),
		TotalCount:  total,
		CurrentPage: page,
		PageSize:    investigationWorkspacePageSize,
		TotalPages:  totalPages,
		HasMore:     wp.HasMore,
		NextCursor:  wp.NextCursor,
	}
	return pages.InvestigationWorkspace(vm).Render(c.Context(), c)
}

// ── Type option helpers ───────────────────────────────────────────────────────

// workspaceTypeOptions returns the type dropdown options for the given domain.
// When domain is empty, returns a combined list of all types from both domains.
func workspaceTypeOptions(domain services.OperationalExceptionDomain) []pages.WorkspaceTypeOption {
	switch domain {
	case services.DomainReconciliation:
		return reconTypeOptions()
	case services.DomainPaymentReverse:
		return prTypeOptions()
	default:
		// Both domains combined.
		opts := reconTypeOptions()
		opts = append(opts, prTypeOptions()...)
		return opts
	}
}

func reconTypeOptions() []pages.WorkspaceTypeOption {
	types := models.AllReconciliationExceptionTypes()
	opts := make([]pages.WorkspaceTypeOption, len(types))
	for i, t := range types {
		opts[i] = pages.WorkspaceTypeOption{
			Value: string(t),
			Label: models.ReconciliationExceptionTypeLabel(t),
		}
	}
	return opts
}

func prTypeOptions() []pages.WorkspaceTypeOption {
	types := models.AllPaymentReverseExceptionTypes()
	opts := make([]pages.WorkspaceTypeOption, len(types))
	for i, t := range types {
		opts[i] = pages.WorkspaceTypeOption{
			Value: string(t),
			Label: models.PaymentReverseExceptionTypeLabel(t),
		}
	}
	return opts
}

// sanitizeWorkspaceTypeStr returns typeStr only when it matches a known type
// for the given domain.  Returns "" for unknown values to prevent SQL injection
// via the type filter.
func sanitizeWorkspaceTypeStr(domain services.OperationalExceptionDomain, typeStr string) string {
	if typeStr == "" {
		return ""
	}
	for _, opt := range workspaceTypeOptions(domain) {
		if opt.Value == typeStr {
			return typeStr
		}
	}
	return ""
}
