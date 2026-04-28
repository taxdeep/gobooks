// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services/search_engine"
	"balanciz/internal/web/templates/pages"
)

// handleAdvancedSearch serves GET /advanced-search — the full-page filter
// view reachable from the topbar dropdown's "Advanced transactions search"
// link. Drives a paginated flat list off the same search_documents
// projection that powers the dropdown, with optional entity_type / date /
// status / query filters layered on top.
//
// Query params (all optional, all echoed back into the form so the page is
// shareable and the URL fully describes the result set):
//
//   - q          free-text query (matched against doc_number / title / memo)
//   - type       entity_type narrow ("invoice", "customer", …); empty = all
//   - from / to  YYYY-MM-DD inclusive doc_date bounds
//   - status     native status string ("paid", "voided", …)
//   - page       1-indexed page (defaults 1)
//   - size       page size, capped at 200 (defaults 50)
//
// Results are produced by Selector.SearchAdvanced — so legacy / dual / ent
// modes all transparently work, and the operator-facing Mode label in the
// page footer reflects whichever engine actually served the request.
func (s *Server) handleAdvancedSearch(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	if s.SearchSelector == nil {
		return c.Status(fiber.StatusServiceUnavailable).
			SendString("search engine not configured")
	}

	q := strings.TrimSpace(c.Query("q"))
	entityType := strings.TrimSpace(c.Query("type"))
	dateFromStr := strings.TrimSpace(c.Query("from"))
	dateToStr := strings.TrimSpace(c.Query("to"))
	status := strings.TrimSpace(c.Query("status"))

	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.Query("size"))
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}

	// Validate entity_type against the canonical option list — drop unknown
	// values silently rather than passing them straight through to the
	// projection (which would just return an empty page anyway). Keeps the
	// URL bar honest.
	if entityType != "" && !isKnownAdvancedEntityType(entityType) {
		entityType = ""
	}

	// Date parsing: tolerate empty / unparseable inputs by treating them as
	// "no bound". We don't surface a parse error in the UI because the date
	// inputs are <input type=date>, which only ever submits valid YYYY-MM-DD.
	var dateFrom, dateTo time.Time
	if dateFromStr != "" {
		if t, err := time.Parse("2006-01-02", dateFromStr); err == nil {
			dateFrom = t
		}
	}
	if dateToStr != "" {
		if t, err := time.Parse("2006-01-02", dateToStr); err == nil {
			// Inclusive upper bound — bump to end-of-day so a row dated
			// "to" itself isn't silently excluded by a < comparison.
			dateTo = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
		}
	}

	resp, err := s.SearchSelector.SearchAdvanced(c.Context(), search_engine.AdvancedRequest{
		CompanyID:  companyID,
		Query:      q,
		EntityType: entityType,
		DateFrom:   dateFrom,
		DateTo:     dateTo,
		Status:     status,
		Page:       page,
		PageSize:   size,
	})
	if err != nil {
		// Engine failure shouldn't 500 the whole page — render with empty
		// results so the operator can adjust filters and retry. The Mode
		// label still surfaces so they know which backend failed.
		resp = &search_engine.AdvancedResponse{
			Rows:     nil,
			Total:    0,
			Page:     page,
			PageSize: size,
		}
	}

	vm := pages.AdvancedSearchVM{
		HasCompany:        true,
		Query:             q,
		EntityType:        entityType,
		DateFrom:          dateFromStr,
		DateTo:            dateToStr,
		Status:            status,
		Rows:              resp.Rows,
		Total:             resp.Total,
		Page:              resp.Page,
		PageSize:          resp.PageSize,
		EntityTypeOptions: pages.AdvancedSearchEntityOptions(),
		Mode:              string(s.SearchSelector.Mode()),
	}
	return pages.AdvancedSearch(vm).Render(c.Context(), c)
}

// isKnownAdvancedEntityType is the validation guard for the `type=` query
// param. Sourced from the same canonical option list the dropdown
// renders, so the two can never drift.
func isKnownAdvancedEntityType(t string) bool {
	for _, opt := range pages.AdvancedSearchEntityOptions() {
		if opt.Value == t {
			return true
		}
	}
	return false
}
