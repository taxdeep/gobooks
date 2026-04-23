// 遵循project_guide.md
package web

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

// globalSearchRateLimit is the per-user request cap on
// /api/global-search. Phase 5.2 ships a conservative 20 req/s per user
// — enough for aggressive typing in the dropdown (fetch debounced to
// 200ms on the frontend = max 5 req/s under normal use) plus a 4x
// safety margin for spurious reconnects, back/forward navigation, and
// dev-tool reload storms.
//
// Window is a 1-second sliding window so burst traffic is handled
// smoothly (5 req in 500ms + 5 req in 500ms = OK; 20 req in 100ms then
// quiet = OK as long as aggregate stays under Max).
//
// Key per USER id (not IP) because multiple users behind the same NAT
// would otherwise throttle each other. Anonymous requests — the
// middleware chain 401s them before reaching the limiter — fall back
// to the authenticated company_id for defence in depth.
const (
	globalSearchRateLimitMax    = 20
	globalSearchRateLimitWindow = time.Second
)

// NewGlobalSearchLimiter returns the fiber.Handler to register as
// middleware on /api/global-search. Keyed by user ID when available
// (falls back to companyID then to IP so unauthenticated traffic still
// has a limit).
//
// Exceeded requests receive HTTP 429 with Retry-After: 1 and a JSON
// body the frontend can render as a friendly "slow down" toast.
func NewGlobalSearchLimiter() fiber.Handler {
	return limiter.New(limiter.Config{
		Max:               globalSearchRateLimitMax,
		Expiration:        globalSearchRateLimitWindow,
		LimiterMiddleware: limiter.SlidingWindow{},

		// KeyGenerator — user ID is the correct isolation boundary.
		// Avoids NAT'd offices throttling colleagues; avoids a single
		// user opening 20 tabs to get 20x the budget.
		KeyGenerator: func(c *fiber.Ctx) string {
			if u := UserFromCtx(c); u != nil {
				return "gs:u:" + u.ID.String()
			}
			if cid, ok := ActiveCompanyIDFromCtx(c); ok {
				return "gs:c:" + strconv.FormatUint(uint64(cid), 10)
			}
			// Fallback: IP — should rarely hit since the auth middleware
			// runs first, but defence in depth.
			return "gs:ip:" + c.IP()
		},

		LimitReached: func(c *fiber.Ctx) error {
			c.Set("Retry-After", "1")
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":   "rate_limit_exceeded",
				"message": "Too many search requests. Please slow down.",
			})
		},

		// SkipFailedRequests: count all — even 500s — against the budget
		// because a misbehaving query shouldn't unlock extra retries.
		// SkipSuccessfulRequests: also off, for the same reason.
	})
}
