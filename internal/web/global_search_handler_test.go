// 遵循project_guide.md
package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services/search_engine"
)

// stubEngineForHandler is the minimum stub the global-search handler
// needs: returns a fixed response so we can verify the JSON shape +
// status code without a Postgres / ent fixture.
type stubEngineForHandler struct {
	mode search_engine.Mode
	resp *search_engine.SearchResponse
	err  error
}

func (s *stubEngineForHandler) Mode() search_engine.Mode { return s.mode }
func (s *stubEngineForHandler) Search(_ context.Context, _ search_engine.SearchRequest) (*search_engine.SearchResponse, error) {
	return s.resp, s.err
}
func (s *stubEngineForHandler) SearchAdvanced(_ context.Context, _ search_engine.AdvancedRequest) (*search_engine.AdvancedResponse, error) {
	return &search_engine.AdvancedResponse{}, s.err
}

// newGlobalSearchTestApp wires a Fiber app with handleGlobalSearch and
// a synthetic ResolveActiveCompany middleware that injects companyID=42
// without touching auth tables. Lets us drive the handler in isolation.
func newGlobalSearchTestApp(t *testing.T, sel *search_engine.Selector) *fiber.App {
	t.Helper()
	app := fiber.New()
	s := &Server{SearchSelector: sel}
	app.Get("/api/global-search", func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, uint(42))
		return s.handleGlobalSearch(c)
	})
	return app
}

func runGet(t *testing.T, app *fiber.App, url string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// Phase 5.1 smoke contract: ent-mode search returns 200 + valid JSON
// shape with grouped candidates.
func TestHandleGlobalSearch_EntModeReturnsJSON(t *testing.T) {
	stub := &stubEngineForHandler{
		mode: search_engine.ModeEnt,
		resp: &search_engine.SearchResponse{
			Source: "ranked",
			Candidates: []search_engine.Candidate{
				{
					ID:         "1",
					Primary:    "POSX US INC.",
					Secondary:  "Invoice INV-1 · 2026-04-22 · CAD 100.00",
					GroupKey:   search_engine.GroupTransactions,
					GroupLabel: "Transactions",
					ActionKind: search_engine.ActionNavigate,
					URL:        "/invoices/1",
					EntityType: "invoice",
				},
			},
		},
	}
	sel := search_engine.NewSelector(search_engine.ModeEnt, search_engine.NewLegacyEngine(), nil, stub)
	app := newGlobalSearchTestApp(t, sel)

	status, body := runGet(t, app, "/api/global-search?q=posx&limit=5")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var got struct {
		Candidates []map[string]any `json:"candidates"`
		Source     string           `json:"source"`
		Mode       string           `json:"mode"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json: %v; body=%s", err, body)
	}
	if got.Mode != "ent" {
		t.Errorf("mode = %q, want ent", got.Mode)
	}
	if got.Source != "ranked" {
		t.Errorf("source = %q, want ranked", got.Source)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(got.Candidates))
	}
	c := got.Candidates[0]
	for _, key := range []string{"id", "primary", "group_key", "url", "entity_type"} {
		if _, ok := c[key]; !ok {
			t.Errorf("missing JSON key %q in candidate: %+v", key, c)
		}
	}
}

// Phase 5.0 contract repeated end-to-end through the handler: legacy
// mode returns 200 + empty candidates, never 500. This guards the
// dropdown UI from breaking when an operator pins SEARCH_ENGINE=legacy.
func TestHandleGlobalSearch_LegacyModeReturnsEmpty200(t *testing.T) {
	sel := search_engine.NewSelector(
		search_engine.ModeLegacy,
		search_engine.NewLegacyEngine(),
		nil, nil,
	)
	app := newGlobalSearchTestApp(t, sel)

	status, body := runGet(t, app, "/api/global-search?q=anything")
	if status != http.StatusOK {
		t.Fatalf("legacy mode must return 200, got %d; body=%s", status, body)
	}
	var got struct {
		Candidates []any  `json:"candidates"`
		Source     string `json:"source"`
		Mode       string `json:"mode"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.Mode != "legacy" {
		t.Errorf("mode = %q, want legacy", got.Mode)
	}
	if len(got.Candidates) != 0 {
		t.Errorf("candidates len = %d, want 0", len(got.Candidates))
	}
}

func TestHandleGlobalSearch_NoSelectorReturns503(t *testing.T) {
	app := fiber.New()
	s := &Server{} // SearchSelector intentionally nil
	app.Get("/api/global-search", func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, uint(42))
		return s.handleGlobalSearch(c)
	})
	status, _ := runGet(t, app, "/api/global-search?q=x")
	if status != http.StatusServiceUnavailable {
		t.Errorf("missing selector should return 503, got %d", status)
	}
}

func TestHandleGlobalSearch_NoCompanyReturns400(t *testing.T) {
	app := fiber.New()
	s := &Server{SearchSelector: search_engine.NewSelector(search_engine.ModeLegacy, search_engine.NewLegacyEngine(), nil, nil)}
	// No company injection — the handler should reject.
	app.Get("/api/global-search", s.handleGlobalSearch)
	status, _ := runGet(t, app, "/api/global-search?q=x")
	if status != http.StatusBadRequest {
		t.Errorf("missing active company should return 400, got %d", status)
	}
}
