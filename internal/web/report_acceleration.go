// 遵循project_guide.md
package web

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"balanciz/internal/cache"
	"balanciz/internal/services"
)

// ReportAcceleration wraps P&L and AR Aging report results in a shared TTL cache.
//
// Cache TTL is 5 minutes — long enough to absorb burst traffic on report pages,
// short enough that users see updated figures within an acceptable lag window
// after posting journal entries.
//
// Key format: "rpt:c<companyID>|<type>|<params>"
// The "rpt:c<id>" prefix allows targeted per-company invalidation via
// cache.FlushWhere. Company isolation is baked into the key.
//
// Invalidation:
//   - Call InvalidateCompany(id) after any operation that changes ledger data
//     (journal entry posted, voided, or reversed).
//   - TTL provides a safety net even if a call site is missed.
type ReportAcceleration struct {
	plCache  *cache.TTLCache[string, services.IncomeStatement]
	arCache  *cache.TTLCache[string, services.ARAgingReport]
}

// reportCacheMaxEntries caps each report cache. Reports are heavier
// than SmartPicker results — a single P&L can hold dozens of accounts
// with computed totals, so we cap at half the SmartPicker number per
// cache (5k × 2 caches = 10k slots total, comparable budget). The cap
// also protects against per-report-parameter combinatorial explosions
// (date range × group-by mode × cost centre etc.).
const reportCacheMaxEntries = 5000

// NewReportAcceleration returns a ready-to-use acceleration layer.
func NewReportAcceleration() *ReportAcceleration {
	ttl := 5 * time.Minute
	return &ReportAcceleration{
		plCache: cache.NewBounded[string, services.IncomeStatement](ttl, reportCacheMaxEntries),
		arCache: cache.NewBounded[string, services.ARAgingReport](ttl, reportCacheMaxEntries),
	}
}

// GetIncomeStatement returns a cached P&L result when available, or runs the
// report and caches the result. Returns the report and the cache source label.
// Safe to call on a nil receiver — falls through to compute() with no caching.
func (r *ReportAcceleration) GetIncomeStatement(
	companyID uint,
	fromDate, toDate time.Time,
	compute func() (services.IncomeStatement, error),
) (services.IncomeStatement, string, error) {
	if r == nil {
		result, err := compute()
		return result, "recomputed", err
	}

	key := fmt.Sprintf("rpt:c%d|pl|%s|%s",
		companyID, fromDate.Format("2006-01-02"), toDate.Format("2006-01-02"))

	if cached, ok := r.plCache.Get(key); ok {
		slog.Debug("report_cache.hit", "type", "income_statement", "company_id", companyID)
		return cached, "cache", nil
	}

	result, err := compute()
	if err != nil {
		return result, "recomputed", err
	}
	r.plCache.Set(key, result)
	return result, "recomputed", nil
}

// GetARAgingReport returns a cached AR Aging result when available, or runs the
// report and caches the result. Returns the report and the cache source label.
// Safe to call on a nil receiver — falls through to compute() with no caching.
func (r *ReportAcceleration) GetARAgingReport(
	companyID uint,
	asOf time.Time,
	compute func() (services.ARAgingReport, error),
) (services.ARAgingReport, string, error) {
	if r == nil {
		result, err := compute()
		return result, "recomputed", err
	}

	key := fmt.Sprintf("rpt:c%d|ar|%s", companyID, asOf.Format("2006-01-02"))

	if cached, ok := r.arCache.Get(key); ok {
		slog.Debug("report_cache.hit", "type", "ar_aging", "company_id", companyID)
		return cached, "cache", nil
	}

	result, err := compute()
	if err != nil {
		return result, "recomputed", err
	}
	r.arCache.Set(key, result)
	return result, "recomputed", nil
}

// InvalidateCompany flushes all cached reports for the given companyID.
// Call this after any mutation that affects ledger data.
// Safe to call on a nil receiver — no-op.
func (r *ReportAcceleration) InvalidateCompany(companyID uint) {
	if r == nil {
		return
	}
	prefix := fmt.Sprintf("rpt:c%d|", companyID)
	matchFn := func(k string) bool { return strings.HasPrefix(k, prefix) }
	r.plCache.FlushWhere(matchFn)
	r.arCache.FlushWhere(matchFn)
	slog.Debug("report_cache.company_flushed", "company_id", companyID)
}
