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
	"github.com/google/uuid"

	"balanciz/internal/models"
	"balanciz/internal/services/search_engine"
)

// newGlobalSearchLimiterApp wires an app with the limiter middleware +
// a minimal handler. Avoids the full session middleware chain — instead
// injects user / company via Locals directly so the limiter's
// KeyGenerator sees what it expects.
func newGlobalSearchLimiterApp(t *testing.T, userIDs []uuid.UUID) *fiber.App {
	t.Helper()
	app := fiber.New()
	// Stub handler that returns a real response so we can verify both
	// 200 (allowed) and 429 (throttled) status codes.
	stub := &stubEngineForHandler{
		mode: search_engine.ModeEnt,
		resp: &search_engine.SearchResponse{Candidates: nil, Source: "recent"},
	}
	s := &Server{
		SearchSelector: search_engine.NewSelector(search_engine.ModeEnt, search_engine.NewLegacyEngine(), nil, stub),
	}

	// Test-only middleware that picks a user id per request based on
	// an X-Test-User-Idx header. Lets a single test drive multiple
	// users through the same app + limiter state.
	app.Use(func(c *fiber.Ctx) error {
		idx := 0
		if h := c.Get("X-Test-User-Idx"); h != "" {
			if v := int(h[0] - '0'); v >= 0 && v < len(userIDs) {
				idx = v
			}
		}
		c.Locals(LocalsActiveCompanyID, uint(42))
		c.Locals(LocalsUser, &models.User{ID: userIDs[idx]})
		return c.Next()
	})
	app.Use(NewGlobalSearchLimiter())
	app.Get("/api/global-search", s.handleGlobalSearch)
	return app
}

func hitGlobalSearch(t *testing.T, app *fiber.App, userIdx byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/global-search?q=x", nil)
	req.Header.Set("X-Test-User-Idx", string([]byte{'0' + userIdx}))
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// Phase 5.2 contract #1: a single user firing Max+N requests within the
// window gets 429 for the overflow and the response body matches the
// documented JSON shape.
func TestGlobalSearchLimiter_TripsAtMax(t *testing.T) {
	userIDs := []uuid.UUID{uuid.New()}
	app := newGlobalSearchLimiterApp(t, userIDs)

	// Fire the full budget first — all should succeed.
	for i := 0; i < globalSearchRateLimitMax; i++ {
		status, body := hitGlobalSearch(t, app, 0)
		if status != http.StatusOK {
			t.Fatalf("req %d: status=%d, want 200; body=%s", i+1, status, body)
		}
	}
	// Next request should be throttled.
	status, body := hitGlobalSearch(t, app, 0)
	if status != http.StatusTooManyRequests {
		t.Fatalf("overflow: status=%d, want 429; body=%s", status, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("429 body not JSON: %v; body=%s", err, body)
	}
	if got["error"] != "rate_limit_exceeded" {
		t.Errorf("error code = %q, want rate_limit_exceeded", got["error"])
	}
	if got["message"] == "" {
		t.Error("429 response missing human-readable message")
	}
}

// Phase 5.2 contract #2: different users have independent budgets.
// User A exhausting their quota must not affect User B.
func TestGlobalSearchLimiter_PerUserIsolation(t *testing.T) {
	userIDs := []uuid.UUID{uuid.New(), uuid.New()}
	app := newGlobalSearchLimiterApp(t, userIDs)

	// Exhaust user 0 completely.
	for i := 0; i < globalSearchRateLimitMax; i++ {
		if status, _ := hitGlobalSearch(t, app, 0); status != http.StatusOK {
			t.Fatalf("user 0 setup req %d failed with status %d", i+1, status)
		}
	}
	if status, _ := hitGlobalSearch(t, app, 0); status != http.StatusTooManyRequests {
		t.Fatalf("user 0 should be throttled, got %d", status)
	}

	// User 1 should be completely unaffected — gets the full budget.
	for i := 0; i < globalSearchRateLimitMax; i++ {
		status, body := hitGlobalSearch(t, app, 1)
		if status != http.StatusOK {
			t.Fatalf("user 1 req %d leaked throttling from user 0: status=%d body=%s",
				i+1, status, body)
		}
	}
}

// Sanity that the limiter isn't accidentally short-circuiting the
// response shape of successful requests.
func TestGlobalSearchLimiter_PreservesHandlerResponse(t *testing.T) {
	userIDs := []uuid.UUID{uuid.New()}
	app := newGlobalSearchLimiterApp(t, userIDs)

	status, body := hitGlobalSearch(t, app, 0)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	var got struct {
		Source string `json:"source"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse: %v; body=%s", err, body)
	}
	if got.Mode != "ent" {
		t.Errorf("mode = %q, want ent", got.Mode)
	}
}

// Keep the limiter package actually reachable at the test-only level,
// guards against someone removing the package from go.mod.
var _ = context.Background