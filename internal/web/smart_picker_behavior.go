package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

type smartPickerUsageEventInput struct {
	Entity           string         `json:"entity"`
	EntityType       string         `json:"entity_type"`
	Context          string         `json:"context"`
	Query            string         `json:"query"`
	EventType        string         `json:"event_type"`
	SelectedEntityID string         `json:"selected_entity_id"`
	ItemID           string         `json:"item_id"`
	RankPosition     *int           `json:"rank_position"`
	ResultCount      *int           `json:"result_count"`
	SourceRoute      string         `json:"source_route"`
	AnchorContext    string         `json:"anchor_context"`
	AnchorEntityType string         `json:"anchor_entity_type"`
	AnchorEntityID   string         `json:"anchor_entity_id"`
	RequestID        string         `json:"request_id"`
	Metadata         map[string]any `json:"metadata"`
}

type smartPickerUsageError struct {
	status  int
	message string
}

func (e smartPickerUsageError) Error() string { return e.message }

func recordSmartPickerUsageEvent(db *gorm.DB, companyID uint, userID *uuid.UUID, sessionID string, input smartPickerUsageEventInput) error {
	entityType := strings.TrimSpace(input.EntityType)
	if entityType == "" {
		entityType = strings.TrimSpace(input.Entity)
	}
	if input.EventType == "" {
		input.EventType = models.SmartPickerEventSelect
	}
	if !validSmartPickerEventType(input.EventType) {
		return smartPickerUsageError{status: 400, message: "invalid smart picker event_type"}
	}
	def, err := validateSmartPickerContext(entityType, input.Context)
	if err != nil {
		return smartPickerUsageError{status: 400, message: err.Error()}
	}
	input.Context = def.ProviderContext

	selectedID, err := parseSmartPickerEntityID(firstNonEmpty(input.SelectedEntityID, input.ItemID))
	if err != nil {
		return smartPickerUsageError{status: 400, message: "invalid selected_entity_id"}
	}
	if selectedID != nil {
		if err := validateSmartPickerEntityID(db, companyID, def.ProviderContext, entityType, *selectedID); err != nil {
			return err
		}
	}

	anchorID, err := parseSmartPickerEntityID(input.AnchorEntityID)
	if err != nil {
		return smartPickerUsageError{status: 400, message: "invalid anchor_entity_id"}
	}
	anchorContext := strings.TrimSpace(input.AnchorContext)
	anchorEntityType := strings.TrimSpace(input.AnchorEntityType)
	if anchorID != nil || anchorContext != "" || anchorEntityType != "" {
		anchorDef, err := validateSmartPickerContext(anchorEntityType, anchorContext)
		if err != nil {
			return smartPickerUsageError{status: 400, message: "invalid anchor context"}
		}
		anchorContext = anchorDef.ProviderContext
		if anchorID == nil {
			return smartPickerUsageError{status: 400, message: "anchor_entity_id required when anchor context is provided"}
		}
		if err := validateSmartPickerEntityID(db, companyID, anchorContext, anchorEntityType, *anchorID); err != nil {
			return err
		}
	}

	metadataJSON := "{}"
	if len(input.Metadata) > 0 {
		if b, err := json.Marshal(sanitizeSmartPickerMetadata(input.Metadata)); err == nil {
			metadataJSON = string(b)
		}
	}

	now := time.Now().UTC()
	normalizedQuery := normalizeSmartPickerQuery(input.Query)
	err = db.Transaction(func(tx *gorm.DB) error {
		event := models.SmartPickerEvent{
			CompanyID:        companyID,
			UserID:           userID,
			SessionID:        sessionID,
			Context:          input.Context,
			EntityType:       entityType,
			Query:            strings.TrimSpace(input.Query),
			NormalizedQuery:  normalizedQuery,
			EventType:        input.EventType,
			SelectedEntityID: selectedID,
			RankPosition:     input.RankPosition,
			ResultCount:      input.ResultCount,
			SourceRoute:      strings.TrimSpace(input.SourceRoute),
			AnchorContext:    anchorContext,
			AnchorEntityType: anchorEntityType,
			AnchorEntityID:   anchorID,
			MetadataJSON:     metadataJSON,
			CreatedAt:        now,
		}
		if err := tx.Create(&event).Error; err != nil {
			return fmt.Errorf("insert smart picker event: %w", err)
		}
		if normalizedQuery != "" {
			recent := models.SmartPickerRecentQuery{
				CompanyID:         companyID,
				UserID:            userID,
				Context:           input.Context,
				Query:             strings.TrimSpace(input.Query),
				NormalizedQuery:   normalizedQuery,
				ResultClicked:     input.EventType == models.SmartPickerEventSelect,
				ClickedEntityType: entityType,
				ClickedEntityID:   selectedID,
				ResultCount:       input.ResultCount,
				CreatedAt:         now,
			}
			if err := tx.Create(&recent).Error; err != nil {
				return fmt.Errorf("insert smart picker recent query: %w", err)
			}
		}
		if input.EventType != models.SmartPickerEventSelect || selectedID == nil {
			return nil
		}
		legacy := models.SmartPickerUsage{
			CompanyID: companyID,
			Entity:    entityType,
			Context:   input.Context,
			ItemID:    *selectedID,
			RequestID: strings.TrimSpace(input.RequestID),
		}
		if err := tx.Create(&legacy).Error; err != nil {
			slog.Warn("smart_picker.legacy_usage_persist_failed", "company_id", companyID, "context", input.Context, "entity_type", entityType, "error", err)
		}
		if err := upsertSmartPickerUsageStat(tx, companyID, nil, models.SmartPickerScopeCompany, input.Context, entityType, *selectedID, input.RankPosition, input.Query, now); err != nil {
			return err
		}
		if userID != nil {
			if err := upsertSmartPickerUsageStat(tx, companyID, userID, models.SmartPickerScopeUser, input.Context, entityType, *selectedID, input.RankPosition, input.Query, now); err != nil {
				return err
			}
		}
		if anchorID != nil && anchorContext != "" && anchorEntityType != "" {
			if err := incrementSmartPickerPairStat(tx, companyID, nil, models.SmartPickerScopeCompany, anchorContext, anchorEntityType, *anchorID, input.Context, entityType, *selectedID, now); err != nil {
				return err
			}
			if userID != nil {
				if err := incrementSmartPickerPairStat(tx, companyID, userID, models.SmartPickerScopeUser, anchorContext, anchorEntityType, *anchorID, input.Context, entityType, *selectedID, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("smart_picker.usage_record_failed", "company_id", companyID, "context", input.Context, "entity_type", entityType, "event_type", input.EventType, "error", err)
	}
	return err
}

func validateSmartPickerEntityID(db *gorm.DB, companyID uint, context string, entityType string, id uint) error {
	provider, ok := defaultSmartPickerRegistry.get(entityType)
	if !ok {
		return smartPickerUsageError{status: 400, message: "unknown smart picker entity type"}
	}
	item, err := provider.GetByID(db, SmartPickerContext{CompanyID: companyID, Context: context}, strconv.FormatUint(uint64(id), 10))
	if err != nil {
		return fmt.Errorf("validate smart picker entity: %w", err)
	}
	if item == nil {
		return smartPickerUsageError{status: 403, message: "selected entity is not valid for this company/context"}
	}
	return nil
}

func upsertSmartPickerUsageStat(tx *gorm.DB, companyID uint, userID *uuid.UUID, scopeType, context, entityType string, entityID uint, rankPosition *int, query string, now time.Time) error {
	var stat models.SmartPickerUsageStat
	q := tx.Where("company_id = ? AND scope_type = ? AND context = ? AND entity_type = ? AND entity_id = ?",
		companyID, scopeType, context, entityType, entityID)
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	err := q.First(&stat).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load smart picker usage stat: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		stat = models.SmartPickerUsageStat{
			CompanyID:       companyID,
			ScopeType:       scopeType,
			UserID:          userID,
			Context:         context,
			EntityType:      entityType,
			EntityID:        entityID,
			LastSelectedAt:  &now,
			LastQuery:       trimUsageQuery(query),
			UpdatedAt:       now,
			AvgRankPosition: decimal.Zero,
		}
	}
	oldCount := stat.SelectCount
	stat.SelectCount++
	stat.SelectCount7D++
	stat.SelectCount30D++
	stat.SelectCount90D++
	stat.LastSelectedAt = &now
	stat.LastQuery = trimUsageQuery(query)
	stat.UpdatedAt = now
	if rankPosition != nil && *rankPosition > 0 {
		oldAvg := stat.AvgRankPosition.InexactFloat64()
		newAvg := (oldAvg*float64(oldCount) + float64(*rankPosition)) / float64(stat.SelectCount)
		stat.AvgRankPosition = decimal.NewFromFloat(newAvg)
	}
	if stat.ID == uuid.Nil {
		if err := tx.Create(&stat).Error; err != nil {
			return fmt.Errorf("create smart picker usage stat: %w", err)
		}
		return nil
	}
	if err := tx.Save(&stat).Error; err != nil {
		return fmt.Errorf("update smart picker usage stat: %w", err)
	}
	return nil
}

func incrementSmartPickerPairStat(tx *gorm.DB, companyID uint, userID *uuid.UUID, scopeType, sourceContext, anchorEntityType string, anchorEntityID uint, targetContext, targetEntityType string, targetEntityID uint, now time.Time) error {
	var stat models.SmartPickerPairStat
	q := tx.Where("company_id = ? AND scope_type = ? AND source_context = ? AND anchor_entity_type = ? AND anchor_entity_id = ? AND target_context = ? AND target_entity_type = ? AND target_entity_id = ?",
		companyID, scopeType, sourceContext, anchorEntityType, anchorEntityID, targetContext, targetEntityType, targetEntityID)
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	err := q.First(&stat).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load smart picker pair stat: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		stat = models.SmartPickerPairStat{
			CompanyID:        companyID,
			ScopeType:        scopeType,
			UserID:           userID,
			SourceContext:    sourceContext,
			AnchorEntityType: anchorEntityType,
			AnchorEntityID:   anchorEntityID,
			TargetContext:    targetContext,
			TargetEntityType: targetEntityType,
			TargetEntityID:   targetEntityID,
			UpdatedAt:        now,
		}
	}
	stat.SelectCount++
	stat.LastSelectedAt = &now
	stat.UpdatedAt = now
	if stat.ID == uuid.Nil {
		if err := tx.Create(&stat).Error; err != nil {
			return fmt.Errorf("create smart picker pair stat: %w", err)
		}
	} else if err := tx.Save(&stat).Error; err != nil {
		return fmt.Errorf("update smart picker pair stat: %w", err)
	}
	return refreshSmartPickerPairConfidence(tx, companyID, userID, scopeType, sourceContext, anchorEntityType, anchorEntityID, targetContext, targetEntityType)
}

func refreshSmartPickerPairConfidence(tx *gorm.DB, companyID uint, userID *uuid.UUID, scopeType, sourceContext, anchorEntityType string, anchorEntityID uint, targetContext, targetEntityType string) error {
	var rows []models.SmartPickerPairStat
	q := tx.Where("company_id = ? AND scope_type = ? AND source_context = ? AND anchor_entity_type = ? AND anchor_entity_id = ? AND target_context = ? AND target_entity_type = ?",
		companyID, scopeType, sourceContext, anchorEntityType, anchorEntityID, targetContext, targetEntityType)
	if userID == nil {
		q = q.Where("user_id IS NULL")
	} else {
		q = q.Where("user_id = ?", *userID)
	}
	if err := q.Find(&rows).Error; err != nil {
		return fmt.Errorf("load smart picker pair rows: %w", err)
	}
	total := 0
	for _, row := range rows {
		total += row.SelectCount
	}
	if total == 0 {
		return nil
	}
	for _, row := range rows {
		confidence := decimal.NewFromFloat(float64(row.SelectCount) / float64(total))
		if err := tx.Model(&models.SmartPickerPairStat{}).
			Where("id = ?", row.ID).
			Updates(map[string]any{
				"total_anchor_select_count": total,
				"confidence_score":          confidence,
			}).Error; err != nil {
			return fmt.Errorf("update smart picker pair confidence: %w", err)
		}
	}
	return nil
}

func parseSmartPickerEntityID(raw string) (*uint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id64, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id64 == 0 {
		return nil, fmt.Errorf("invalid entity id")
	}
	id := uint(id64)
	return &id, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func trimUsageQuery(q string) string {
	q = strings.TrimSpace(q)
	if len(q) > 200 {
		return q[:200]
	}
	return q
}

func sanitizeSmartPickerMetadata(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		if strings.Contains(key, "description") || strings.Contains(key, "note") || strings.Contains(key, "memo") || strings.Contains(key, "receipt") || strings.Contains(key, "document") {
			continue
		}
		out[key] = v
	}
	return out
}
