// 遵循project_guide.md
package web

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"gobooks/internal/cache"
	"gobooks/internal/models"
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

	key := spCacheKey(provider.EntityType(), ctx.Context, ctx.CompanyID, q, ctx.Limit)

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

	result, err := provider.Search(db, ctx, q)
	if err != nil {
		return nil, "db", err
	}
	if ranked := a.rankCandidates(db, ctx.CompanyID, provider.EntityType(), ctx.Context, result); ranked {
		a.cache.Set(key, result)
		return result, "ranked", nil
	}

	a.cache.Set(key, result)
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

func (a *SmartPickerAcceleration) rankCandidates(
	db *gorm.DB,
	companyID uint,
	entity string,
	context string,
	result *SmartPickerResult,
) bool {
	if result == nil || len(result.Candidates) < 2 {
		return false
	}

	itemIDs := make([]uint, 0, len(result.Candidates))
	originalOrder := make(map[string]int, len(result.Candidates))
	for idx, item := range result.Candidates {
		originalOrder[item.ID] = idx
		id64, err := strconv.ParseUint(item.ID, 10, 64)
		if err != nil || id64 == 0 {
			continue
		}
		itemIDs = append(itemIDs, uint(id64))
	}
	if len(itemIDs) == 0 {
		return false
	}

	type usageCountRow struct {
		ItemID uint
		Count  int64
	}
	var rows []usageCountRow
	if err := db.Model(&models.SmartPickerUsage{}).
		Select("item_id, COUNT(*) AS count").
		Where("company_id = ? AND entity = ? AND context = ? AND item_id IN ?", companyID, entity, context, itemIDs).
		Group("item_id").
		Scan(&rows).Error; err != nil {
		slog.Warn("smart_picker.rank_usage_query_failed",
			"entity", entity,
			"context", context,
			"company_id", companyID,
			"error", err,
		)
		return false
	}

	counts := make(map[string]int64, len(rows))
	hasUsage := false
	for _, row := range rows {
		if row.Count <= 0 {
			continue
		}
		hasUsage = true
		counts[strconv.FormatUint(uint64(row.ItemID), 10)] = row.Count
	}
	if !hasUsage {
		return false
	}

	sort.SliceStable(result.Candidates, func(i, j int) bool {
		left := counts[result.Candidates[i].ID]
		right := counts[result.Candidates[j].ID]
		if left != right {
			return left > right
		}
		return originalOrder[result.Candidates[i].ID] < originalOrder[result.Candidates[j].ID]
	})
	return true
}
