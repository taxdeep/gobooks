// 遵循project_guide.md
package web

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"gobooks/internal/cache"
)

// SmartPickerAcceleration wraps the provider registry with a TTL cache.
// It is acceleration-only: the cache is never the authority for correctness
// or access control. A cache miss falls through to the provider DB query;
// a cache hit skips that query.
//
// Cache TTL is 2 minutes — short enough to surface newly added entities
// without user-visible lag after a create, long enough to absorb burst
// traffic on the search endpoint.
//
// Key format:  "sp:c<companyID>|<entity>|<context>|<q>|<limit>"
//
// The "sp:c<id>" prefix allows targeted per-company invalidation via
// cache.FlushWhere without touching entries for other companies.
// Company isolation is baked into the key; no cross-company leakage is
// possible even on a shared cache instance.
type SmartPickerAcceleration struct {
	cache *cache.TTLCache[string, *SmartPickerResult]
}

// smartPickerCacheMaxEntries caps the in-memory cache so a hostile or
// buggy client can't blow up the heap by issuing many distinct queries.
// 10k entries × ~1KB per SmartPickerResult ≈ 10MB worst case — generous
// for typical mid-size tenants (active companies × ~5 contexts × ~10
// distinct queries per minute easily fits). LRU eviction picks off the
// least-recently-written when the cap is hit.
const smartPickerCacheMaxEntries = 10000

// NewSmartPickerAcceleration returns a ready-to-use acceleration layer.
func NewSmartPickerAcceleration() *SmartPickerAcceleration {
	return &SmartPickerAcceleration{
		cache: cache.NewBounded[string, *SmartPickerResult](2*time.Minute, smartPickerCacheMaxEntries),
	}
}

// Search executes the provider query, using a cached result when available.
// It returns the result, a source label ("cache" or "db"), and any error.
// The returned *SmartPickerResult must not be mutated by the caller
// (particularly the RequestID field, which is set by the handler after return).
// Safe to call on a nil receiver — behaves as a direct provider call with no caching.
func (a *SmartPickerAcceleration) Search(
	db *gorm.DB,
	provider SmartPickerProvider,
	ctx SmartPickerContext,
	q string,
) (*SmartPickerResult, string, error) {
	if a == nil {
		result, err := provider.Search(db, ctx, q)
		return result, "db", err
	}

	key := spCacheKeyForContext(provider.EntityType(), ctx, q)

	if !ctx.TraceEnabled {
		if cached, ok := a.cache.Get(key); ok {
			slog.Debug("smart_picker.cache_hit",
				"entity", provider.EntityType(),
				"context", ctx.Context,
				"company_id", ctx.CompanyID,
				"q", q,
			)
			// Return a shallow copy so the handler can safely assign RequestID.
			cp := *cached
			return &cp, "cache", nil
		}
	}

	result, err := provider.Search(db, ctx, q)
	if err != nil {
		return nil, "db", err
	}
	ctx.Query = q
	ranked := rankSmartPickerCandidates(db, ctx, provider.EntityType(), result)
	if ranked.TraceID != "" {
		result.TraceID = ranked.TraceID
	}
	if ranked.Applied {
		if !ctx.TraceEnabled {
			a.cache.Set(key, result)
		}
		return result, "ranked", nil
	}

	if !ctx.TraceEnabled {
		a.cache.Set(key, result)
	}
	return result, "db", nil
}

// InvalidateCompany flushes only cached results for the given companyID.
// Keys are prefixed with "sp:c<id>|" so we can target exactly one company
// without evicting entries for other companies.
//
// Call this after any mutation that changes entities returned by the picker
// (account/customer/vendor/product_service create, update, or inactivate).
// Safe to call on a nil receiver — no-op.
func (a *SmartPickerAcceleration) InvalidateCompany(companyID uint) {
	if a == nil {
		return
	}
	prefix := fmt.Sprintf("sp:c%d|", companyID)
	a.cache.FlushWhere(func(k string) bool {
		return strings.HasPrefix(k, prefix)
	})
	slog.Debug("smart_picker.cache_company_flushed", "company_id", companyID)
}

func spCacheKey(entity, context string, companyID uint, q string, limit int) string {
	return fmt.Sprintf("sp:c%d|%s|%s|%s|%d", companyID, entity, context, q, limit)
}

func spCacheKeyForContext(entity string, ctx SmartPickerContext, q string) string {
	key := spCacheKey(entity, ctx.Context, ctx.CompanyID, q, ctx.Limit)
	if ctx.UserID != nil {
		key += "|u:" + ctx.UserID.String()
	}
	if ctx.AnchorEntityID != nil {
		key += fmt.Sprintf("|a:%s:%s:%d", ctx.AnchorContext, ctx.AnchorEntityType, *ctx.AnchorEntityID)
	}
	return key
}
