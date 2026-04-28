// 遵循project_guide.md
package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services/search_engine"
)

// stubAdvancedEngine captures the AdvancedRequest the selector dispatches
// so tests can assert query-param parsing without spinning up a database.
type stubAdvancedEngine struct {
	mode   search_engine.Mode
	gotReq search_engine.AdvancedRequest
	resp   *search_engine.AdvancedResponse
	err    error
}

func (s *stubAdvancedEngine) Mode() search_engine.Mode { return s.mode }
func (*stubAdvancedEngine) Search(_ context.Context, _ search_engine.SearchRequest) (*search_engine.SearchResponse, error) {
	return &search_engine.SearchResponse{}, nil
}
func (s *stubAdvancedEngine) SearchAdvanced(_ context.Context, req search_engine.AdvancedRequest) (*search_engine.AdvancedResponse, error) {
	s.gotReq = req
	if s.resp != nil {
		return s.resp, s.err
	}
	return &search_engine.AdvancedResponse{Page: req.Page, PageSize: req.PageSize}, s.err
}

// newAdvancedSearchTestApp wires a Fiber app with handleAdvancedSearch
// and a synthetic ResolveActiveCompany middleware (companyID=42), same
// pattern as newGlobalSearchTestApp.
func newAdvancedSearchTestApp(t *testing.T, sel *search_engine.Selector) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	s := &Server{SearchSelector: sel}
	app.Get("/advanced-search", func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, uint(42))
		return s.handleAdvancedSearch(c)
	})
	return app
}

// Smoke contract: handler renders 200 and forwards every query param into
// the AdvancedRequest (entity_type narrowing, date bounds, status, paging).
func TestHandleAdvancedSearch_ForwardsQueryParams(t *testing.T) {
	stub := &stubAdvancedEngine{mode: search_engine.ModeEnt}
	sel := search_engine.NewSelector(search_engine.ModeEnt, search_engine.NewLegacyEngine(), nil, stub)
	app := newAdvancedSearchTestApp(t, sel)

	status, body := runGet(t, app, "/advanced-search?q=lighting&type=invoice&from=2026-04-01&to=2026-04-22&status=paid&page=2&size=25")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, string(body))
	}
	got := stub.gotReq
	if got.CompanyID != 42 {
		t.Errorf("CompanyID = %d, want 42", got.CompanyID)
	}
	if got.Query != "lighting" {
		t.Errorf("Query = %q, want lighting", got.Query)
	}
	if got.EntityType != "invoice" {
		t.Errorf("EntityType = %q, want invoice", got.EntityType)
	}
	if got.Status != "paid" {
		t.Errorf("Status = %q, want paid", got.Status)
	}
	if got.Page != 2 || got.PageSize != 25 {
		t.Errorf("Page/Size = %d/%d, want 2/25", got.Page, got.PageSize)
	}
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !got.DateFrom.Equal(want) {
		t.Errorf("DateFrom = %v, want %v", got.DateFrom, want)
	}
	// DateTo is bumped to end-of-day so a row dated 2026-04-22 is included.
	if got.DateTo.Year() != 2026 || got.DateTo.Month() != 4 || got.DateTo.Day() != 22 || got.DateTo.Hour() != 23 {
		t.Errorf("DateTo = %v, want 2026-04-22T23:59:59", got.DateTo)
	}
	// Sanity: page rendered without HTTP error and contains the page heading.
	if !strings.Contains(string(body), "Advanced search") {
		t.Errorf("response body missing page title; body=%s", string(body))
	}
}

// Unknown entity_type values must be silently dropped — stops the URL bar
// from being a typo-poisoning vector.
func TestHandleAdvancedSearch_DropsUnknownEntityType(t *testing.T) {
	stub := &stubAdvancedEngine{mode: search_engine.ModeEnt}
	sel := search_engine.NewSelector(search_engine.ModeEnt, search_engine.NewLegacyEngine(), nil, stub)
	app := newAdvancedSearchTestApp(t, sel)

	status, _ := runGet(t, app, "/advanced-search?type=not_a_real_entity")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if stub.gotReq.EntityType != "" {
		t.Errorf("EntityType = %q, want empty (unknown value should drop)", stub.gotReq.EntityType)
	}
}

// Engine errors render an empty page rather than a 500 — the operator can
// tweak filters and retry without losing the editor session.
func TestHandleAdvancedSearch_EngineErrorRendersEmptyPage(t *testing.T) {
	stub := &stubAdvancedEngine{mode: search_engine.ModeEnt, err: context.Canceled}
	sel := search_engine.NewSelector(search_engine.ModeEnt, search_engine.NewLegacyEngine(), nil, stub)
	app := newAdvancedSearchTestApp(t, sel)

	status, body := runGet(t, app, "/advanced-search?q=x")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (engine errors should not 500); body=%s", status, string(body))
	}
	if !strings.Contains(string(body), "No results match your filters.") {
		t.Errorf("expected empty-state copy in body, got: %s", string(body))
	}
}
