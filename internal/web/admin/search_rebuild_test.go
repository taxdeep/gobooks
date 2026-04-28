// 遵循project_guide.md
package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"balanciz/internal/config"
	"balanciz/internal/models"
	"balanciz/internal/searchprojection"
	"balanciz/internal/searchprojection/backfill"
)

// testRebuildDB returns a sqlite store with just the audit-log tables —
// the rebuild handler only writes to audit logs and the in-memory
// rebuildState tracker, so no business tables are required.
func testRebuildDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:rebuild_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.AuditLog{}, &models.SysadminUser{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// withTestAdminUser injects a synthetic SysadminUser into the request
// context so handleAdminSearchRebuild can audit-log the actor email.
// Mirrors the real auth middleware without the cookie/session machinery.
func withTestAdminUser(handler fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(LocalsAdminUser, &models.SysadminUser{Email: "test@example.com"})
		return handler(c)
	}
}

// TestHandleAdminSearchRebuild_RejectsWithoutProjector — when the
// projector is nil (NoopProjector fallback didn't even land), the
// handler must short-circuit with a redirect rather than spawn a
// goroutine that can't do anything useful.
func TestHandleAdminSearchRebuild_RejectsWithoutProjector(t *testing.T) {
	s := &Server{
		DB:                 testRebuildDB(t),
		Cfg:                config.Config{Env: "test"},
		SearchProjector:    nil,
		searchRebuildState: newRebuildState(),
	}
	app := fiber.New()
	app.Post("/admin/system/search-rebuild", withTestAdminUser(s.handleAdminSearchRebuild))

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/admin/system/search-rebuild", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/system?flash=search_rebuild_disabled" {
		t.Errorf("Location = %q, want /admin/system?flash=search_rebuild_disabled", loc)
	}
}

// TestHandleAdminSearchRebuild_RejectsConcurrent — second click while
// the first sweep is still running must surface the "already running"
// flash, not start a duplicate goroutine. We pin the state to running
// directly via markStart rather than racing a real sweep.
func TestHandleAdminSearchRebuild_RejectsConcurrent(t *testing.T) {
	state := newRebuildState()
	if !state.markStart() {
		t.Fatal("expected first markStart to return true")
	}
	// Don't markDone — leave the running flag set for the request.

	s := &Server{
		DB:                 testRebuildDB(t),
		Cfg:                config.Config{Env: "test"},
		SearchProjector:    searchprojection.NoopProjector{},
		searchRebuildState: state,
	}
	app := fiber.New()
	app.Post("/admin/system/search-rebuild", withTestAdminUser(s.handleAdminSearchRebuild))

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/admin/system/search-rebuild", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/system?flash=search_rebuild_already_running" {
		t.Errorf("Location = %q, want already-running redirect", loc)
	}
}

// TestRebuildState_SnapshotConcurrentSafety races many goroutines
// against markStart + Snapshot. Race detector catches missing locking.
func TestRebuildState_SnapshotConcurrentSafety(t *testing.T) {
	state := newRebuildState()
	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		go func() {
			state.markStart()
			_, _, _ = state.Snapshot()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
	running, _, _ := state.Snapshot()
	if !running {
		t.Errorf("expected running=true after concurrent markStart calls")
	}

	state.markDone(backfill.Result{Families: []backfill.FamilyResult{{Family: backfill.FamilyCustomer, Rows: 1}}})
	running2, _, last := state.Snapshot()
	if running2 {
		t.Errorf("expected running=false after markDone")
	}
	if last == nil || last.TotalRows() != 1 {
		t.Errorf("expected lastResult with 1 row, got %+v", last)
	}
}
