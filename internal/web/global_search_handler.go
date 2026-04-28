// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services/search_engine"
)

// globalSearchResponse is the JSON shape returned from /api/global-search.
// Mirrors the internal search_engine.SearchResponse but with tag overrides
// so the JSON keys match the frontend's expectations (snake_case).
type globalSearchResponse struct {
	Candidates []globalSearchCandidate `json:"candidates"`
	Source     string                  `json:"source,omitempty"`
	Mode       string                  `json:"mode"`
}

// globalSearchCandidate is the public serialisation of
// search_engine.Candidate. Split from the internal type so the JSON
// contract doesn't silently churn whenever the internal struct evolves.
type globalSearchCandidate struct {
	ID         string            `json:"id"`
	Primary    string            `json:"primary"`
	Secondary  string            `json:"secondary,omitempty"`
	GroupKey   string            `json:"group_key,omitempty"`
	GroupLabel string            `json:"group_label,omitempty"`
	ActionKind string            `json:"action_kind,omitempty"`
	URL        string            `json:"url,omitempty"`
	EntityType string            `json:"entity_type,omitempty"`
	Payload    map[string]string `json:"payload,omitempty"`
}

// handleGlobalSearch serves GET /api/global-search. Drives the upgraded
// header dropdown. Always company-scoped via the authenticated session;
// the `q` query param is the only user input that affects ranking.
//
// Response shape:
//
//	{
//	  "candidates": [
//	    {
//	      "id":          "42",
//	      "primary":     "POSX US INC.",
//	      "secondary":   "Invoice INV-202604 · 2026-04-22 · CAD 3600.00",
//	      "group_key":   "transactions",
//	      "group_label": "Transactions",
//	      "action_kind": "navigate",
//	      "url":         "/invoices/42",
//	      "entity_type": "invoice",
//	      "payload":     {"status":"issued","amount":"3600.00", ...}
//	    }, ...
//	  ],
//	  "source": "ranked",
//	  "mode":   "ent"
//	}
func (s *Server) handleGlobalSearch(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).
			JSON(fiber.Map{"error": "no active company"})
	}
	if s.SearchSelector == nil {
		return c.Status(fiber.StatusServiceUnavailable).
			JSON(fiber.Map{"error": "search engine not configured"})
	}

	q := strings.TrimSpace(c.Query("q"))
	limit := 20
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 50 {
		limit = v
	}

	resp, err := s.SearchSelector.Search(c.Context(), search_engine.SearchRequest{
		CompanyID: companyID,
		Query:     q,
		Limit:     limit,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).
			JSON(fiber.Map{"error": "search failed"})
	}

	out := globalSearchResponse{
		Source: resp.Source,
		Mode:   string(s.SearchSelector.Mode()),
	}
	out.Candidates = make([]globalSearchCandidate, 0, len(resp.Candidates))
	for _, cnd := range resp.Candidates {
		out.Candidates = append(out.Candidates, globalSearchCandidate{
			ID:         cnd.ID,
			Primary:    cnd.Primary,
			Secondary:  cnd.Secondary,
			GroupKey:   cnd.GroupKey,
			GroupLabel: cnd.GroupLabel,
			ActionKind: cnd.ActionKind,
			URL:        cnd.URL,
			EntityType: cnd.EntityType,
			Payload:    cnd.Payload,
		})
	}
	return c.JSON(out)
}
