package web

import (
	"encoding/json"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

type smartPickerScoreComponents struct {
	EntityID              string  `json:"entity_id"`
	FinalScore            float64 `json:"final_score"`
	TextMatchScore        float64 `json:"text_match_score"`
	ContextFitScore       float64 `json:"context_fit_score"`
	UserFrequencyScore    float64 `json:"user_frequency_score"`
	CompanyFrequencyScore float64 `json:"company_frequency_score"`
	RecencyScore          float64 `json:"recency_score"`
	PairScore             float64 `json:"pair_score"`
	AliasScore            float64 `json:"alias_score"`
	AIHintScore           float64 `json:"ai_hint_score"`
	StatusScore           float64 `json:"status_score"`
	PenaltyScore          float64 `json:"penalty_score"`
	Reason                string  `json:"reason"`
	RankPosition          int     `json:"rank_position"`
}

type smartPickerRankedItem struct {
	item     SmartPickerItem
	original int
	score    smartPickerScoreComponents
}

type smartPickerRankingResult struct {
	Applied bool
	TraceID string
}

func rankSmartPickerCandidates(db *gorm.DB, ctx SmartPickerContext, entityType string, result *SmartPickerResult) smartPickerRankingResult {
	if result == nil || len(result.Candidates) == 0 {
		return smartPickerRankingResult{}
	}

	ids := make([]uint, 0, len(result.Candidates))
	idByString := make(map[string]uint, len(result.Candidates))
	for _, item := range result.Candidates {
		id64, err := strconv.ParseUint(item.ID, 10, 64)
		if err != nil || id64 == 0 {
			continue
		}
		id := uint(id64)
		ids = append(ids, id)
		idByString[item.ID] = id
	}
	if len(ids) == 0 {
		return smartPickerRankingResult{}
	}

	userStats := loadSmartPickerUsageStats(db, ctx, entityType, ids, true)
	companyStats := loadSmartPickerUsageStats(db, ctx, entityType, ids, false)
	userPairs := loadSmartPickerPairStats(db, ctx, entityType, ids, true)
	companyPairs := loadSmartPickerPairStats(db, ctx, entityType, ids, false)
	hints := loadSmartPickerRankingHints(db, ctx, entityType, ids)
	aliases := loadSmartPickerAliases(db, ctx, entityType, ids)

	nq := normalizeSmartPickerQuery(ctx.Query)
	now := time.Now().UTC()
	ranked := make([]smartPickerRankedItem, 0, len(result.Candidates))
	hasLearningSignal := false
	for idx, item := range result.Candidates {
		id := idByString[item.ID]
		components := smartPickerScoreComponents{
			EntityID:        item.ID,
			TextMatchScore:  textMatchScore(item, nq),
			ContextFitScore: 0,
			StatusScore:     5,
		}
		if aliasScore := aliasMatchScore(aliases[id], nq); aliasScore > 0 {
			components.AliasScore = aliasScore
			hasLearningSignal = true
		}
		if stat, ok := userStats[id]; ok {
			components.UserFrequencyScore = math.Min(math.Log(float64(stat.SelectCount)+1)*8, 30)
			components.RecencyScore = math.Max(components.RecencyScore, recencyScore(stat.LastSelectedAt, now))
			hasLearningSignal = hasLearningSignal || stat.SelectCount > 0
		}
		if stat, ok := companyStats[id]; ok {
			components.CompanyFrequencyScore = math.Min(math.Log(float64(stat.SelectCount)+1)*4, 20)
			components.RecencyScore = math.Max(components.RecencyScore, recencyScore(stat.LastSelectedAt, now))
			hasLearningSignal = hasLearningSignal || stat.SelectCount > 0
		}
		if pair, ok := userPairs[id]; ok {
			components.PairScore = math.Max(components.PairScore, math.Min(pair.ConfidenceScore.InexactFloat64()*20, 25))
			hasLearningSignal = true
		}
		if pair, ok := companyPairs[id]; ok {
			components.PairScore = math.Max(components.PairScore, math.Min(pair.ConfidenceScore.InexactFloat64()*20, 25))
			hasLearningSignal = true
		}
		if hint, ok := hints[id]; ok {
			boost := hint.BoostScore.InexactFloat64()
			capValue := 10.0
			if hint.Source == models.SmartPickerSourceAI {
				capValue = 5
			}
			components.AIHintScore = math.Min(math.Max(boost, 0), capValue)
			hasLearningSignal = true
		}
		components.FinalScore = components.TextMatchScore +
			components.ContextFitScore +
			components.UserFrequencyScore +
			components.CompanyFrequencyScore +
			components.RecencyScore +
			components.PairScore +
			components.AliasScore +
			components.AIHintScore +
			components.StatusScore -
			components.PenaltyScore
		components.Reason = smartPickerReason(components)
		item.Score = roundScore(components.FinalScore)
		item.Reason = components.Reason
		ranked = append(ranked, smartPickerRankedItem{item: item, original: idx, score: components})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score.FinalScore != ranked[j].score.FinalScore {
			return ranked[i].score.FinalScore > ranked[j].score.FinalScore
		}
		return ranked[i].original < ranked[j].original
	})

	traceRows := make([]smartPickerScoreComponents, 0, len(ranked))
	for idx := range ranked {
		ranked[idx].item.RankPosition = idx + 1
		ranked[idx].score.RankPosition = idx + 1
		ranked[idx].score.FinalScore = roundScore(ranked[idx].score.FinalScore)
		ranked[idx].score.TextMatchScore = roundScore(ranked[idx].score.TextMatchScore)
		ranked[idx].score.UserFrequencyScore = roundScore(ranked[idx].score.UserFrequencyScore)
		ranked[idx].score.CompanyFrequencyScore = roundScore(ranked[idx].score.CompanyFrequencyScore)
		ranked[idx].score.RecencyScore = roundScore(ranked[idx].score.RecencyScore)
		ranked[idx].score.PairScore = roundScore(ranked[idx].score.PairScore)
		ranked[idx].score.AliasScore = roundScore(ranked[idx].score.AliasScore)
		ranked[idx].score.AIHintScore = roundScore(ranked[idx].score.AIHintScore)
		result.Candidates[idx] = ranked[idx].item
		traceRows = append(traceRows, ranked[idx].score)
	}

	traceID := maybeStoreSmartPickerTrace(db, ctx, entityType, len(result.Candidates), traceRows)
	return smartPickerRankingResult{Applied: hasLearningSignal, TraceID: traceID}
}

func loadSmartPickerUsageStats(db *gorm.DB, ctx SmartPickerContext, entityType string, ids []uint, userScope bool) map[uint]models.SmartPickerUsageStat {
	out := map[uint]models.SmartPickerUsageStat{}
	q := db.Model(&models.SmartPickerUsageStat{}).
		Where("company_id = ? AND context = ? AND entity_type = ? AND entity_id IN ?", ctx.CompanyID, ctx.Context, entityType, ids)
	if userScope {
		if ctx.UserID == nil {
			return out
		}
		q = q.Where("scope_type = ? AND user_id = ?", models.SmartPickerScopeUser, *ctx.UserID)
	} else {
		q = q.Where("scope_type = ? AND user_id IS NULL", models.SmartPickerScopeCompany)
	}
	var rows []models.SmartPickerUsageStat
	if err := q.Find(&rows).Error; err != nil {
		slog.Warn("smart_picker.rank_usage_query_failed", "company_id", ctx.CompanyID, "context", ctx.Context, "entity_type", entityType, "error", err)
		return out
	}
	for _, row := range rows {
		out[row.EntityID] = row
	}
	return out
}

func loadSmartPickerPairStats(db *gorm.DB, ctx SmartPickerContext, entityType string, ids []uint, userScope bool) map[uint]models.SmartPickerPairStat {
	out := map[uint]models.SmartPickerPairStat{}
	if ctx.AnchorEntityID == nil || ctx.AnchorContext == "" || ctx.AnchorEntityType == "" {
		return out
	}
	q := db.Model(&models.SmartPickerPairStat{}).
		Where("company_id = ? AND source_context = ? AND anchor_entity_type = ? AND anchor_entity_id = ? AND target_context = ? AND target_entity_type = ? AND target_entity_id IN ?",
			ctx.CompanyID, ctx.AnchorContext, ctx.AnchorEntityType, *ctx.AnchorEntityID, ctx.Context, entityType, ids)
	if userScope {
		if ctx.UserID == nil {
			return out
		}
		q = q.Where("scope_type = ? AND user_id = ?", models.SmartPickerScopeUser, *ctx.UserID)
	} else {
		q = q.Where("scope_type = ? AND user_id IS NULL", models.SmartPickerScopeCompany)
	}
	var rows []models.SmartPickerPairStat
	if err := q.Find(&rows).Error; err != nil {
		slog.Warn("smart_picker.rank_pair_query_failed", "company_id", ctx.CompanyID, "context", ctx.Context, "error", err)
		return out
	}
	for _, row := range rows {
		out[row.TargetEntityID] = row
	}
	return out
}

func loadSmartPickerRankingHints(db *gorm.DB, ctx SmartPickerContext, entityType string, ids []uint) map[uint]models.SmartPickerRankingHint {
	out := map[uint]models.SmartPickerRankingHint{}
	now := time.Now().UTC()
	var rows []models.SmartPickerRankingHint
	if err := db.Model(&models.SmartPickerRankingHint{}).
		Where("company_id = ? AND context = ? AND entity_type = ? AND entity_id IN ? AND status = ? AND validation_status = ?",
			ctx.CompanyID, ctx.Context, entityType, ids, models.SmartPickerSuggestionActive, models.SmartPickerValidationValid).
		Where("(expires_at IS NULL OR expires_at > ?)", now).
		Find(&rows).Error; err != nil {
		slog.Warn("smart_picker.rank_hint_query_failed", "company_id", ctx.CompanyID, "context", ctx.Context, "error", err)
		return out
	}
	for _, row := range rows {
		existing, ok := out[row.EntityID]
		if !ok || row.Confidence.GreaterThan(existing.Confidence) {
			out[row.EntityID] = row
		}
	}
	return out
}

func loadSmartPickerAliases(db *gorm.DB, ctx SmartPickerContext, entityType string, ids []uint) map[uint][]models.SmartPickerAliasSuggestion {
	out := map[uint][]models.SmartPickerAliasSuggestion{}
	if normalizeSmartPickerQuery(ctx.Query) == "" {
		return out
	}
	var rows []models.SmartPickerAliasSuggestion
	if err := db.Model(&models.SmartPickerAliasSuggestion{}).
		Where("company_id = ? AND context = ? AND entity_type = ? AND entity_id IN ? AND status = ? AND validation_status = ?",
			ctx.CompanyID, ctx.Context, entityType, ids, models.SmartPickerSuggestionActive, models.SmartPickerValidationValid).
		Find(&rows).Error; err != nil {
		slog.Warn("smart_picker.rank_alias_query_failed", "company_id", ctx.CompanyID, "context", ctx.Context, "error", err)
		return out
	}
	for _, row := range rows {
		out[row.EntityID] = append(out[row.EntityID], row)
	}
	return out
}

func textMatchScore(item SmartPickerItem, normalizedQuery string) float64 {
	if normalizedQuery == "" {
		return 0
	}
	labels := []string{item.Primary, item.Secondary}
	if item.Meta != nil {
		for _, value := range item.Meta {
			labels = append(labels, value)
		}
	}
	best := 0.0
	for _, label := range labels {
		n := normalizeSmartPickerQuery(label)
		if n == "" {
			continue
		}
		switch {
		case n == normalizedQuery:
			best = math.Max(best, 100)
		case strings.HasPrefix(n, normalizedQuery):
			best = math.Max(best, 80)
		case strings.Contains(n, normalizedQuery):
			best = math.Max(best, 50)
		}
	}
	return best
}

func aliasMatchScore(aliases []models.SmartPickerAliasSuggestion, normalizedQuery string) float64 {
	if normalizedQuery == "" {
		return 0
	}
	for _, alias := range aliases {
		na := normalizeSmartPickerQuery(alias.NormalizedAlias)
		if na == "" {
			na = normalizeSmartPickerQuery(alias.Alias)
		}
		if na == normalizedQuery || strings.Contains(na, normalizedQuery) || strings.Contains(normalizedQuery, na) {
			return 40
		}
	}
	return 0
}

func recencyScore(lastSelectedAt *time.Time, now time.Time) float64 {
	if lastSelectedAt == nil {
		return 0
	}
	age := now.Sub(lastSelectedAt.UTC())
	switch {
	case age <= 24*time.Hour:
		return 15
	case age <= 7*24*time.Hour:
		return 10
	case age <= 30*24*time.Hour:
		return 5
	default:
		return 0
	}
}

func smartPickerReason(c smartPickerScoreComponents) string {
	switch {
	case c.TextMatchScore >= 100:
		return "Exact match"
	case c.AliasScore > 0:
		return "Alias match"
	case c.PairScore > 0:
		return "Often used with selected item"
	case c.RecencyScore > 0 && c.UserFrequencyScore > 0:
		return "Recently and frequently used"
	case c.UserFrequencyScore > 0:
		return "Frequently used by you"
	case c.CompanyFrequencyScore > 0:
		return "Frequently used in this company"
	case c.AIHintScore > 0:
		return "Suggested from learned pattern"
	case c.TextMatchScore >= 80:
		return "Prefix match"
	case c.TextMatchScore >= 50:
		return "Text match"
	default:
		return "Valid result"
	}
}

func roundScore(v float64) float64 {
	return math.Round(v*100) / 100
}

func decimalFromFloat(v float64) decimal.Decimal {
	return decimal.NewFromFloat(roundScore(v))
}

func maybeStoreSmartPickerTrace(db *gorm.DB, ctx SmartPickerContext, entityType string, returnedCount int, rows []smartPickerScoreComponents) string {
	if !ctx.TraceEnabled || len(rows) == 0 {
		return ""
	}
	rate := ctx.TraceSampleRate
	if rate <= 0 {
		return ""
	}
	if rate < 1 && rand.Float64() > rate {
		return ""
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return ""
	}
	count := returnedCount
	trace := models.SmartPickerDecisionTrace{
		CompanyID:       ctx.CompanyID,
		UserID:          ctx.UserID,
		Context:         ctx.Context,
		EntityType:      entityType,
		Query:           ctx.Query,
		NormalizedQuery: normalizeSmartPickerQuery(ctx.Query),
		ReturnedCount:   &count,
		TraceJSON:       string(payload),
		CreatedAt:       time.Now().UTC(),
	}
	if err := db.Create(&trace).Error; err != nil {
		slog.Warn("smart_picker.trace_persist_failed", "company_id", ctx.CompanyID, "context", ctx.Context, "error", err)
		return ""
	}
	return trace.ID.String()
}
