// 遵循project_guide.md
package admin

import (
	"context"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/logging"
	"balanciz/internal/searchprojection/backfill"
	"balanciz/internal/services"
)

// rebuildState is the single in-process tracker for the "Rebuild search
// index" button. The admin page reads it on every render to show the
// last-run summary; the worker goroutine writes it twice per run (start
// + finish). Lifetime matches admin.Server — survives the rebuild run
// but is reset on process restart (which is fine; the audit log has the
// authoritative history).
//
// In-memory only by design — running the rebuild on every replica isn't
// useful (they all read the same projection), and the audit log captures
// the historical record.
type rebuildState struct {
	mu sync.Mutex

	// running is true between the moment a worker goroutine starts and
	// when it returns. Guards against concurrent rebuild requests — a
	// second click while one is in flight is rejected with a flash.
	running bool

	// lastResult is the most recent completed run's summary. Nil until
	// the first run finishes. Pointer so the zero-value of the struct
	// reads cleanly as "no run yet" in the templ.
	lastResult *backfill.Result

	// lastTriggered is when the most recent run was kicked off, regardless
	// of whether it has completed. Used so the admin page can show
	// "running since…" while a sweep is in progress.
	lastTriggered time.Time
}

func newRebuildState() *rebuildState {
	return &rebuildState{}
}

// Snapshot returns a copy of the current state for read-only callers.
// Returned pointer fields are NOT copied — the lastResult is treated as
// immutable once set, so a concurrent writer creating a new Result
// object can't tear what the renderer is reading.
func (s *rebuildState) Snapshot() (running bool, lastTriggered time.Time, lastResult *backfill.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.lastTriggered, s.lastResult
}

// markStart records that a rebuild is in flight. Returns false if one is
// already running — caller must abort with a "rebuild already in progress"
// message rather than spawn a duplicate goroutine.
func (s *rebuildState) markStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	s.lastTriggered = time.Now()
	return true
}

// markDone publishes the finished Result + clears the running flag.
func (s *rebuildState) markDone(res backfill.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	cp := res
	s.lastResult = &cp
}

// handleAdminSearchRebuild kicks off a full search-index rebuild in the
// background. Returns immediately with a flash redirect so the admin
// can keep using the page while the sweep runs. Rejects concurrent
// triggers (one rebuild at a time per process).
//
// The rebuild itself is idempotent (Upsert OnConflict UpdateNewValues),
// safe under live traffic — the only reason to gate it is to avoid
// piling on duplicate scans of the same tables.
func (s *Server) handleAdminSearchRebuild(c *fiber.Ctx) error {
	if s.SearchProjector == nil {
		return c.Redirect("/admin/system?flash=search_rebuild_disabled", fiber.StatusSeeOther)
	}
	if !s.searchRebuildState.markStart() {
		return c.Redirect("/admin/system?flash=search_rebuild_already_running", fiber.StatusSeeOther)
	}

	admin := AdminUserFromCtx(c).Email
	services.TryWriteAuditLog(s.DB, "admin.system.search_rebuild_started", "system", 0,
		admin,
		map[string]any{"actor_type": "sysadmin"},
	)

	// Spawn the actual scan in a goroutine. We deliberately use a fresh
	// context.Background() rather than c.Context() — Fiber cancels the
	// request context as soon as the handler returns its redirect, which
	// would kill the rebuild mid-sweep.
	go func() {
		ctx := context.Background()
		start := time.Now()
		res := backfill.RunAll(ctx, s.DB, s.SearchProjector, backfill.Options{})
		s.searchRebuildState.markDone(res)

		fields := map[string]any{
			"actor_type":  "sysadmin",
			"elapsed_ms":  time.Since(start).Milliseconds(),
			"total_rows":  res.TotalRows(),
			"family_runs": len(res.Families),
		}
		if err := res.FirstErr(); err != nil {
			fields["error"] = err.Error()
			logging.L().Error("admin search rebuild finished with error", "err", err, "actor", admin)
		} else {
			logging.L().Info("admin search rebuild finished", "rows", res.TotalRows(), "elapsed_ms", time.Since(start).Milliseconds(), "actor", admin)
		}
		services.TryWriteAuditLog(s.DB, "admin.system.search_rebuild_completed", "system", 0, admin, fields)
	}()

	return c.Redirect("/admin/system?flash=search_rebuild_started", fiber.StatusSeeOther)
}
