package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/search_engine"
)

type globalSearchUsageInput struct {
	Query              string `json:"query"`
	EventType          string `json:"event_type"`
	SelectedEntityType string `json:"selected_entity_type"`
	SelectedEntityID   string `json:"selected_entity_id"`
	RankPosition       *int   `json:"rank_position"`
	ResultCount        *int   `json:"result_count"`
	SourceRoute        string `json:"source_route"`
}

func (s *Server) handleGlobalSearchUsage(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}
	if s.DB == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "usage recorder not configured"})
	}

	var input globalSearchUsageInput
	if err := json.Unmarshal(c.Body(), &input); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	userID := smartPickerUserID(c)
	sessionID := ""
	if sess := SessionFromCtx(c); sess != nil {
		sessionID = sess.ID.String()
	}
	if err := recordGlobalSearchUsageEvent(s.DB, companyID, userID, sessionID, input); err != nil {
		var usageErr globalSearchUsageError
		if errors.As(err, &usageErr) {
			return c.Status(usageErr.status).JSON(fiber.Map{"error": usageErr.message})
		}
		slog.Warn("global_search.usage_record_failed", "company_id", companyID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "usage event failed"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

type globalSearchUsageError struct {
	status  int
	message string
}

func (e globalSearchUsageError) Error() string { return e.message }

func recordGlobalSearchUsageEvent(db *gorm.DB, companyID uint, userID *uuid.UUID, sessionID string, input globalSearchUsageInput) error {
	eventType := strings.TrimSpace(input.EventType)
	if eventType == "" {
		eventType = models.GlobalSearchEventSelect
	}
	if eventType != models.GlobalSearchEventSelect {
		return globalSearchUsageError{status: fiber.StatusBadRequest, message: "invalid global search event_type"}
	}
	entityType := strings.TrimSpace(input.SelectedEntityType)
	if entityType == "" {
		return globalSearchUsageError{status: fiber.StatusBadRequest, message: "selected_entity_type required"}
	}
	entityID64, err := strconv.ParseUint(strings.TrimSpace(input.SelectedEntityID), 10, 64)
	if err != nil || entityID64 == 0 {
		return globalSearchUsageError{status: fiber.StatusBadRequest, message: "invalid selected_entity_id"}
	}
	entityID := uint(entityID64)

	var exists int64
	if err := db.Table("search_documents").
		Where("company_id = ? AND entity_type = ? AND entity_id = ?", companyID, entityType, entityID).
		Count(&exists).Error; err != nil {
		return fmt.Errorf("validate global search selection: %w", err)
	}
	if exists == 0 {
		return globalSearchUsageError{status: fiber.StatusForbidden, message: "selected search result is not valid for this company"}
	}

	now := time.Now().UTC()
	query := trimUsageQuery(input.Query)
	normalized := normalizeGlobalSearchQuery(query)
	queryKind := classifyGlobalSearchQuery(query)
	return db.Transaction(func(tx *gorm.DB) error {
		event := models.GlobalSearchEvent{
			CompanyID:          companyID,
			UserID:             userID,
			SessionID:          sessionID,
			Query:              query,
			NormalizedQuery:    normalized,
			QueryKind:          queryKind,
			EventType:          eventType,
			SelectedEntityType: entityType,
			SelectedEntityID:   entityID,
			RankPosition:       input.RankPosition,
			ResultCount:        input.ResultCount,
			SourceRoute:        strings.TrimSpace(input.SourceRoute),
			CreatedAt:          now,
		}
		if err := tx.Create(&event).Error; err != nil {
			return fmt.Errorf("insert global search event: %w", err)
		}
		if err := upsertGlobalSearchTypeStat(tx, companyID, nil, models.SmartPickerScopeCompany, queryKind, entityType, query, now); err != nil {
			return err
		}
		if userID != nil {
			if err := upsertGlobalSearchTypeStat(tx, companyID, userID, models.SmartPickerScopeUser, queryKind, entityType, query, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertGlobalSearchTypeStat(tx *gorm.DB, companyID uint, userID *uuid.UUID, scopeType, queryKind, entityType, query string, now time.Time) error {
	var stat models.GlobalSearchTypeStat
	q := tx.Where("company_id = ? AND scope_type = ? AND query_kind = ? AND selected_entity_type = ?",
		companyID, scopeType, queryKind, entityType)
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	err := q.First(&stat).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load global search type stat: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		stat = models.GlobalSearchTypeStat{
			CompanyID:          companyID,
			ScopeType:          scopeType,
			UserID:             userID,
			QueryKind:          queryKind,
			SelectedEntityType: entityType,
			WeightSource:       "behavior",
			UpdatedAt:          now,
		}
	}
	stat.SelectCount++
	stat.SelectCount30D++
	stat.LastSelectedAt = &now
	stat.LastQuery = trimUsageQuery(query)
	stat.UpdatedAt = now
	if stat.ID == uuid.Nil {
		if err := tx.Create(&stat).Error; err != nil {
			return fmt.Errorf("create global search type stat: %w", err)
		}
		return nil
	}
	if err := tx.Save(&stat).Error; err != nil {
		return fmt.Errorf("update global search type stat: %w", err)
	}
	return nil
}

func (s *Server) applyGlobalSearchUsageBoosts(companyID uint, userID *uuid.UUID, query string, candidates []search_engine.Candidate) []search_engine.Candidate {
	if s == nil || s.DB == nil || len(candidates) < 2 {
		return candidates
	}
	queryKind := classifyGlobalSearchQuery(query)
	if queryKind != models.GlobalSearchQueryKindAmount {
		return candidates
	}

	boosts := map[string]float64{
		"journal_entry":    0.35,
		"bill":             0.14,
		"invoice":          0.12,
		"expense":          0.10,
		"customer_receipt": 0.08,
	}
	var stats []models.GlobalSearchTypeStat
	q := s.DB.Where("company_id = ? AND query_kind = ?", companyID, queryKind).
		Where("(scope_type = ? AND user_id IS NULL) OR scope_type = ?", models.SmartPickerScopeCompany, models.SmartPickerScopeUser)
	if userID != nil {
		q = q.Where("user_id IS NULL OR user_id = ?", *userID)
	} else {
		q = q.Where("user_id IS NULL")
	}
	if err := q.Find(&stats).Error; err == nil {
		for _, st := range stats {
			if st.SelectCount <= 0 || st.SelectedEntityType == "" {
				continue
			}
			weight := math.Log1p(float64(st.SelectCount)) * 0.16
			if st.ScopeType == models.SmartPickerScopeUser && userID != nil && st.UserID != nil && *st.UserID == *userID {
				weight *= 1.8
			}
			boosts[st.SelectedEntityType] += weight
			if !st.AIWeight.IsZero() && st.AIConfidence.IsPositive() {
				aiWeight := st.AIWeight.Mul(st.AIConfidence).InexactFloat64()
				boosts[st.SelectedEntityType] += math.Min(aiWeight, 0.8)
			}
		}
	}

	out := append([]search_engine.Candidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		left := boosts[out[i].EntityType]
		right := boosts[out[j].EntityType]
		if left == right {
			return false
		}
		return left > right
	})
	return out
}

func classifyGlobalSearchQuery(q string) string {
	if isGlobalSearchAmountQuery(q) {
		return models.GlobalSearchQueryKindAmount
	}
	return models.GlobalSearchQueryKindText
}

func normalizeGlobalSearchQuery(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if isGlobalSearchAmountQuery(q) {
		return normalizeGlobalSearchAmount(q)
	}
	return strings.Join(strings.Fields(q), " ")
}

func isGlobalSearchAmountQuery(q string) bool {
	n := normalizeGlobalSearchAmount(q)
	if n == "" {
		return false
	}
	digits := 0
	dotCount := 0
	for _, r := range n {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dotCount++
		default:
			return false
		}
	}
	return digits >= 2 && dotCount <= 1 && (strings.Contains(n, ".") || digits >= 4)
}

func normalizeGlobalSearchAmount(q string) string {
	q = strings.TrimSpace(q)
	var b strings.Builder
	for _, r := range q {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == ',':
			b.WriteRune(r)
		case unicode.IsSpace(r), unicode.IsLetter(r), unicode.IsSymbol(r):
			continue
		default:
			continue
		}
	}
	out := strings.ReplaceAll(b.String(), ",", "")
	out = strings.Trim(out, ".")
	return out
}
