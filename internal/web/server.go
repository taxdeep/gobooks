// 遵循project_guide.md
package web

import (
	"gobooks/ent"
	"gobooks/internal/ai"
	"gobooks/internal/config"
	"gobooks/internal/logging"
	"gobooks/internal/searchprojection"
	"gobooks/internal/services/search_engine"
	"gobooks/internal/web/admin"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Server holds dependencies for handlers.
type Server struct {
	Cfg config.Config
	DB  *gorm.DB

	// SPAcceleration is the SmartPicker cache + usage-tracking layer.
	// Initialised by NewServer; never nil.
	SPAcceleration *SmartPickerAcceleration

	// ReportCache accelerates expensive P&L and AR Aging report queries.
	// TTL-backed; call InvalidateCompany after journal entry posts/voids.
	// Initialised by NewServer; never nil.
	ReportCache *ReportAcceleration

	// AIAssist is the application-level AI platform.
	// All AI completions in handlers must go through this — never call
	// services.OpenAICompatibleChatCompletion directly from a handler.
	// Initialised by NewServer; never nil.
	AIAssist *ai.Platform

	// EntClient drives the search_documents projection. Nil when
	// ent initialisation fails at startup (see initSearchProjection);
	// handlers should guard with `if s.EntClient != nil` or just use
	// SearchProjector which gracefully degrades.
	EntClient *ent.Client

	// SearchProjector is always non-nil: either an EntProjector when the
	// ent client wired up, or a NoopProjector fallback. Handlers can call
	// producers.ProjectCustomer / ProjectVendor / … without nil-checking.
	SearchProjector searchprojection.Projector

	// SearchSelector routes /api/global-search through legacy | dual |
	// ent based on the SEARCH_ENGINE config flag. Nil when ent wiring
	// failed — handlers should guard explicitly.
	SearchSelector *search_engine.Selector
}

// NewServer creates a Fiber app with basic middleware and routes.
func NewServer(cfg config.Config, db *gorm.DB) *fiber.App {
	entClient, projector := initSearchProjection(db)
	selector := initSearchSelector(cfg, entClient)

	s := &Server{
		Cfg:             cfg,
		DB:              db,
		SPAcceleration:  NewSmartPickerAcceleration(),
		ReportCache:     NewReportAcceleration(),
		AIAssist:        ai.New(db),
		EntClient:       entClient,
		SearchProjector: projector,
		SearchSelector:  selector,
	}

	app := fiber.New(fiber.Config{
		AppName:      "GoBooks",
		// 自定义错误处理器：5xx 持久化到 system_logs，4xx 仅 WARN 日志
		ErrorHandler: NewErrorHandler(db),
	})

	s.registerMiddleware(app)
	s.registerRoutes(app)

	// SysAdmin 路由：独立认证链，挂载在 /admin/* 下
	adminSrv := admin.NewServer(cfg, db)
	adminSrv.RegisterRoutes(app)

	return app
}

// initSearchProjection wires ent + the EntProjector, falling back to a
// NoopProjector if ent-client construction fails (e.g. unusual DB driver
// configuration). Always returns a non-nil Projector so handlers never
// have to guard per-call.
//
// The ent client shares the *sql.DB pool with GORM — no separate
// connection pool is opened. See searchprojection.OpenEntFromGorm for
// lifecycle notes.
func initSearchProjection(db *gorm.DB) (*ent.Client, searchprojection.Projector) {
	client, err := searchprojection.OpenEntFromGorm(db)
	if err != nil {
		logging.L().Warn("search projection disabled: ent client init failed", "err", err)
		return nil, searchprojection.NoopProjector{}
	}
	p, err := searchprojection.NewEntProjector(client, searchprojection.AsciiNormalizer{})
	if err != nil {
		logging.L().Warn("search projection disabled: projector init failed", "err", err)
		return client, searchprojection.NoopProjector{}
	}
	return client, p
}

// initSearchSelector assembles the read-side engine. Phase 5 default
// is ent — the legacy engine returns empty results so a misconfigured
// fallback doesn't 500 the header dropdown.
//
// cfg.SearchEngine is already validated by config.Load (unknown values
// fail there); the ParseMode call here is purely a string→Mode adapter
// and any error path is a defensive log.Fatal.
func initSearchSelector(cfg config.Config, entClient *ent.Client) *search_engine.Selector {
	mode, err := search_engine.ParseMode(cfg.SearchEngine)
	if err != nil {
		// Should never reach here — config.Load validated this string
		// at startup. If it does, fail loudly rather than silently
		// degrade to legacy.
		logging.L().Error("search engine: invalid SEARCH_ENGINE slipped past config validation", "err", err, "raw", cfg.SearchEngine)
		mode = search_engine.DefaultMode
	}
	legacy := search_engine.NewLegacyEngine()

	var entEng search_engine.Engine
	if entClient != nil {
		if e, err := search_engine.NewEntEngine(entClient, searchprojection.AsciiNormalizer{}); err == nil {
			entEng = e
		} else {
			logging.L().Warn("search engine: ent impl unavailable, legacy fallback only", "err", err)
		}
	}

	// DualEngine is currently a Phase 5.5+ stub; requesting mode=dual
	// dispatches through Selector's fallback to legacy.
	var dualEng search_engine.Engine
	return search_engine.NewSelector(mode, legacy, dualEng, entEng)
}
