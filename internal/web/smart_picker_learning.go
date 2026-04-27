package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	appai "gobooks/internal/ai"
	"gobooks/internal/models"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type smartPickerLearningOptions struct {
	WindowStart       time.Time
	WindowEnd         time.Time
	TriggerType       string
	TriggeredByUserID *uuid.UUID
	Gateway           *appai.Gateway
}

type smartPickerLearningAggregate struct {
	CompanyID uint                         `json:"company_id"`
	Window    map[string]string            `json:"window"`
	Contexts  []smartPickerLearningContext `json:"contexts"`
}

type smartPickerLearningContext struct {
	Context             string                       `json:"context"`
	TopSelectedEntities []smartPickerLearningEntity  `json:"top_selected_entities"`
	NoMatchQueries      []smartPickerLearningNoMatch `json:"no_match_queries"`
	CommonPairs         []smartPickerLearningPair    `json:"common_pairs"`
}

type smartPickerLearningEntity struct {
	EntityID        uint     `json:"entity_id"`
	EntityLabel     string   `json:"entity_label"`
	EntityType      string   `json:"entity_type"`
	SelectCount     int      `json:"select_count"`
	AvgRankPosition float64  `json:"avg_rank_position"`
	CommonQueries   []string `json:"common_queries,omitempty"`
}

type smartPickerLearningNoMatch struct {
	Query string `json:"query"`
	Count int    `json:"count"`
}

type smartPickerLearningPair struct {
	AnchorEntityID   uint    `json:"anchor_entity_id"`
	AnchorLabel      string  `json:"anchor_label"`
	TargetContext    string  `json:"target_context"`
	TargetEntityID   uint    `json:"target_entity_id"`
	TargetLabel      string  `json:"target_label"`
	TargetEntityType string  `json:"target_entity_type"`
	Count            int     `json:"count"`
	Confidence       float64 `json:"confidence"`
}

func (s *Server) RunSmartPickerLearning(ctx context.Context, companyID uint, opts smartPickerLearningOptions) (*models.AIJobRun, error) {
	if opts.WindowEnd.IsZero() {
		opts.WindowEnd = time.Now().UTC()
	}
	if opts.WindowStart.IsZero() {
		opts.WindowStart = opts.WindowEnd.AddDate(0, 0, -30)
	}
	if opts.TriggerType == "" {
		opts.TriggerType = models.AIJobTriggerManual
	}
	gateway := appai.NewGateway(s.Cfg, appai.NoopAIProvider{})
	if opts.Gateway != nil {
		gateway = *opts.Gateway
	}

	now := time.Now().UTC()
	companyIDPtr := companyID
	run := models.AIJobRun{
		CompanyID:         &companyIDPtr,
		JobType:           models.AIJobSmartPickerLearning,
		Status:            models.AIJobStatusRunning,
		TriggerType:       opts.TriggerType,
		TriggeredByUserID: opts.TriggeredByUserID,
		StartedAt:         &now,
		SourceWindowStart: &opts.WindowStart,
		SourceWindowEnd:   &opts.WindowEnd,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.DB.Create(&run).Error; err != nil {
		return nil, err
	}

	slog.Info("smart_picker.learning_started", "company_id", companyID, "job_run_id", run.ID.String())

	warnings := []string{}
	aggregate, err := s.buildSmartPickerLearningAggregate(companyID, opts.WindowStart, opts.WindowEnd)
	if err != nil {
		s.finishSmartPickerLearningRun(&run, models.AIJobStatusFailed, "", "", []string{err.Error()})
		return &run, err
	}
	inputJSON := mustJSON(aggregate)
	run.InputSummaryJSON = inputJSON

	profileCount, err := s.storeDeterministicLearningProfiles(companyID, run.ID, opts.WindowStart, opts.WindowEnd, aggregate)
	if err != nil {
		s.finishSmartPickerLearningRun(&run, models.AIJobStatusFailed, inputJSON, "", []string{err.Error()})
		return &run, err
	}
	hintCount, err := s.storeDeterministicRankingHints(companyID, run.ID, aggregate)
	if err != nil {
		s.finishSmartPickerLearningRun(&run, models.AIJobStatusFailed, inputJSON, "", []string{err.Error()})
		return &run, err
	}

	aiHintCount := 0
	aiAliasCount := 0
	aiAttempted := s.Cfg.SmartPickerAILearningEnabled && s.Cfg.AIGatewayEnabled
	if !aiAttempted {
		if err := s.createAIRequestLog(companyID, run.ID, appai.TaskSmartPickerLearningSummary, inputJSON, "{}", models.AIRequestStatusSkipped, "AI learning disabled"); err != nil {
			warnings = append(warnings, err.Error())
		}
	} else {
		req := appai.StructuredTaskRequest{
			CompanyID:             companyID,
			JobRunID:              run.ID.String(),
			TaskType:              appai.TaskSmartPickerLearningSummary,
			Capability:            appai.CapabilityStructuredOutput,
			InputJSON:             inputJSON,
			RequestSchemaVersion:  "smartpicker-learning-input.v1",
			ResponseSchemaVersion: "smartpicker-learning-output.v1",
			PromptVersion:         "smartpicker-learning.v1",
			RiskLevel:             appai.RiskLow,
		}
		resp, gatewayErr := gateway.RunStructuredTask(ctx, req)
		status := aiRequestStatusFromGateway(resp.Status)
		if gatewayErr != nil && status == models.AIRequestStatusSucceeded {
			status = models.AIRequestStatusInvalidOutput
		}
		if err := s.createAIRequestLog(companyID, run.ID, appai.TaskSmartPickerLearningSummary, inputJSON, resp.OutputJSON, status, resp.ErrorMessage); err != nil {
			warnings = append(warnings, err.Error())
		}
		if gatewayErr != nil {
			warnings = append(warnings, gatewayErr.Error())
		}
		if status == models.AIRequestStatusSucceeded {
			storedHints, storedAliases, suggestionWarnings := s.processSmartPickerAIOutput(companyID, run.ID, resp.OutputJSON)
			aiHintCount += storedHints
			aiAliasCount += storedAliases
			warnings = append(warnings, suggestionWarnings...)
		}
	}

	output := map[string]any{
		"context_count":      len(aggregate.Contexts),
		"learning_profiles":  profileCount,
		"system_hints":       hintCount,
		"ai_hints_pending":   aiHintCount,
		"ai_aliases_pending": aiAliasCount,
		"warnings_count":     len(warnings),
	}
	status := models.AIJobStatusSucceeded
	if len(warnings) > 0 && aiAttempted {
		status = models.AIJobStatusPartial
	}
	s.finishSmartPickerLearningRun(&run, status, inputJSON, mustJSON(output), warnings)
	slog.Info("smart_picker.learning_completed", "company_id", companyID, "job_run_id", run.ID.String(), "status", status)
	return &run, nil
}

func (s *Server) buildSmartPickerLearningAggregate(companyID uint, start, end time.Time) (smartPickerLearningAggregate, error) {
	aggregate := smartPickerLearningAggregate{
		CompanyID: companyID,
		Window: map[string]string{
			"start": start.UTC().Format(time.RFC3339),
			"end":   end.UTC().Format(time.RFC3339),
		},
	}

	var stats []models.SmartPickerUsageStat
	if err := s.DB.Where("company_id = ? AND scope_type = ? AND user_id IS NULL", companyID, models.SmartPickerScopeCompany).
		Order("context ASC, select_count DESC, updated_at DESC").
		Limit(200).
		Find(&stats).Error; err != nil {
		return aggregate, err
	}

	contexts := map[string]*smartPickerLearningContext{}
	getContext := func(contextName string) *smartPickerLearningContext {
		if contexts[contextName] == nil {
			contexts[contextName] = &smartPickerLearningContext{Context: contextName}
		}
		return contexts[contextName]
	}
	for _, stat := range stats {
		label := s.smartPickerEntityLabel(companyID, stat.Context, stat.EntityType, stat.EntityID)
		c := getContext(stat.Context)
		if len(c.TopSelectedEntities) < 20 {
			c.TopSelectedEntities = append(c.TopSelectedEntities, smartPickerLearningEntity{
				EntityID:        stat.EntityID,
				EntityLabel:     label,
				EntityType:      stat.EntityType,
				SelectCount:     stat.SelectCount,
				AvgRankPosition: stat.AvgRankPosition.InexactFloat64(),
				CommonQueries:   recentQueriesForEntity(s.DB, companyID, stat.Context, stat.EntityType, stat.EntityID, start, end),
			})
		}
	}

	var pairs []models.SmartPickerPairStat
	if err := s.DB.Where("company_id = ? AND scope_type = ? AND user_id IS NULL", companyID, models.SmartPickerScopeCompany).
		Order("source_context ASC, confidence_score DESC, select_count DESC").
		Limit(200).
		Find(&pairs).Error; err != nil {
		return aggregate, err
	}
	for _, pair := range pairs {
		c := getContext(pair.SourceContext)
		if len(c.CommonPairs) >= 20 {
			continue
		}
		c.CommonPairs = append(c.CommonPairs, smartPickerLearningPair{
			AnchorEntityID:   pair.AnchorEntityID,
			AnchorLabel:      s.smartPickerEntityLabel(companyID, pair.SourceContext, pair.AnchorEntityType, pair.AnchorEntityID),
			TargetContext:    pair.TargetContext,
			TargetEntityID:   pair.TargetEntityID,
			TargetLabel:      s.smartPickerEntityLabel(companyID, pair.TargetContext, pair.TargetEntityType, pair.TargetEntityID),
			TargetEntityType: pair.TargetEntityType,
			Count:            pair.SelectCount,
			Confidence:       pair.ConfidenceScore.InexactFloat64(),
		})
	}

	for contextName, entries := range recentNoMatchQueries(s.DB, companyID, start, end) {
		c := getContext(contextName)
		c.NoMatchQueries = entries
	}

	keys := make([]string, 0, len(contexts))
	for key := range contexts {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		aggregate.Contexts = append(aggregate.Contexts, *contexts[key])
	}
	if len(aggregate.Contexts) == 0 {
		aggregate.Contexts = append(aggregate.Contexts, smartPickerLearningContext{Context: "all"})
	}
	return aggregate, nil
}

func (s *Server) storeDeterministicLearningProfiles(companyID uint, jobRunID uuid.UUID, start, end time.Time, aggregate smartPickerLearningAggregate) (int, error) {
	count := 0
	for _, contextSummary := range aggregate.Contexts {
		profileJSON := mustJSON(contextSummary)
		summary := fmt.Sprintf("%s: %d frequent selections, %d common pairs, %d no-match queries",
			contextSummary.Context, len(contextSummary.TopSelectedEntities), len(contextSummary.CommonPairs), len(contextSummary.NoMatchQueries))
		profile := models.SmartPickerLearningProfile{
			CompanyID:         companyID,
			Context:           contextSummary.Context,
			ProfileJSON:       profileJSON,
			SummaryText:       summary,
			SourceWindowStart: start,
			SourceWindowEnd:   end,
			Source:            models.SmartPickerSourceSystem,
			Confidence:        decimal.NewFromFloat(1),
			JobRunID:          &jobRunID,
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		if err := s.DB.Create(&profile).Error; err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Server) storeDeterministicRankingHints(companyID uint, jobRunID uuid.UUID, aggregate smartPickerLearningAggregate) (int, error) {
	count := 0
	for _, contextSummary := range aggregate.Contexts {
		for _, entity := range contextSummary.TopSelectedEntities {
			if entity.SelectCount < 2 {
				continue
			}
			if err := validateSmartPickerEntityID(s.DB, companyID, nil, contextSummary.Context, entity.EntityType, entity.EntityID); err != nil {
				continue
			}
			boost := math.Min(math.Log(float64(entity.SelectCount)+1)*2, 10)
			confidence := math.Min(float64(entity.SelectCount)/10, 1)
			hint := models.SmartPickerRankingHint{
				CompanyID:        companyID,
				Context:          contextSummary.Context,
				EntityType:       entity.EntityType,
				EntityID:         entity.EntityID,
				BoostScore:       decimal.NewFromFloat(boost),
				Confidence:       decimal.NewFromFloat(confidence),
				Reason:           "Deterministic frequent selection pattern",
				Source:           models.SmartPickerSourceSystem,
				Status:           models.SmartPickerSuggestionActive,
				ValidationStatus: models.SmartPickerValidationValid,
				JobRunID:         &jobRunID,
				CreatedAt:        time.Now().UTC(),
				UpdatedAt:        time.Now().UTC(),
			}
			if err := s.DB.Create(&hint).Error; err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

type smartPickerAIOutput struct {
	RankingSuggestions []smartPickerAIRankingSuggestion `json:"ranking_suggestions"`
	AliasSuggestions   []smartPickerAIAliasSuggestion   `json:"alias_suggestions"`
	Summary            string                           `json:"summary"`
}

type smartPickerAIRankingSuggestion struct {
	Context        string       `json:"context"`
	EntityType     string       `json:"entity_type"`
	EntityID       flexibleUint `json:"entity_id"`
	SuggestedBoost float64      `json:"suggested_boost"`
	Confidence     float64      `json:"confidence"`
	Reason         string       `json:"reason"`
}

type smartPickerAIAliasSuggestion struct {
	Context    string       `json:"context"`
	EntityType string       `json:"entity_type"`
	EntityID   flexibleUint `json:"entity_id"`
	Alias      string       `json:"alias"`
	Confidence float64      `json:"confidence"`
	Reason     string       `json:"reason"`
}

func (s *Server) processSmartPickerAIOutput(companyID uint, jobRunID uuid.UUID, outputJSON string) (int, int, []string) {
	var output smartPickerAIOutput
	if err := json.Unmarshal([]byte(outputJSON), &output); err != nil {
		return 0, 0, []string{"AI output JSON rejected: " + err.Error()}
	}
	warnings := []string{}
	hints := 0
	aliases := 0
	for _, suggestion := range output.RankingSuggestions {
		def, err := validateSmartPickerContext(suggestion.EntityType, suggestion.Context)
		if err != nil {
			warnings = append(warnings, "AI ranking suggestion rejected: "+err.Error())
			continue
		}
		if suggestion.EntityID.Value == 0 {
			warnings = append(warnings, "AI ranking suggestion rejected: missing entity_id")
			continue
		}
		if suggestion.Confidence < 0 || suggestion.Confidence > 1 {
			warnings = append(warnings, "AI ranking suggestion rejected: invalid confidence")
			continue
		}
		if err := validateSmartPickerEntityID(s.DB, companyID, nil, def.ProviderContext, suggestion.EntityType, suggestion.EntityID.Value); err != nil {
			warnings = append(warnings, "AI ranking suggestion rejected: "+err.Error())
			continue
		}
		status := models.SmartPickerSuggestionPending
		if s.Cfg.SmartPickerAIHintAutoApply && suggestion.Confidence >= 0.75 {
			status = models.SmartPickerSuggestionActive
		}
		hint := models.SmartPickerRankingHint{
			CompanyID:        companyID,
			Context:          def.ProviderContext,
			EntityType:       suggestion.EntityType,
			EntityID:         suggestion.EntityID.Value,
			BoostScore:       decimal.NewFromFloat(math.Min(math.Max(suggestion.SuggestedBoost, 0), 5)),
			Confidence:       decimal.NewFromFloat(suggestion.Confidence),
			Reason:           trimReason(suggestion.Reason),
			Source:           models.SmartPickerSourceAI,
			Status:           status,
			ValidationStatus: models.SmartPickerValidationValid,
			JobRunID:         &jobRunID,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
		if err := s.DB.Create(&hint).Error; err != nil {
			warnings = append(warnings, "AI ranking suggestion store failed: "+err.Error())
			continue
		}
		hints++
	}
	for _, suggestion := range output.AliasSuggestions {
		def, err := validateSmartPickerContext(suggestion.EntityType, suggestion.Context)
		if err != nil {
			warnings = append(warnings, "AI alias suggestion rejected: "+err.Error())
			continue
		}
		alias := strings.TrimSpace(suggestion.Alias)
		if alias == "" || suggestion.EntityID.Value == 0 {
			warnings = append(warnings, "AI alias suggestion rejected: missing alias/entity")
			continue
		}
		if suggestion.Confidence < 0 || suggestion.Confidence > 1 {
			warnings = append(warnings, "AI alias suggestion rejected: invalid confidence")
			continue
		}
		if err := validateSmartPickerEntityID(s.DB, companyID, nil, def.ProviderContext, suggestion.EntityType, suggestion.EntityID.Value); err != nil {
			warnings = append(warnings, "AI alias suggestion rejected: "+err.Error())
			continue
		}
		aliasRow := models.SmartPickerAliasSuggestion{
			CompanyID:        companyID,
			Context:          def.ProviderContext,
			EntityType:       suggestion.EntityType,
			EntityID:         suggestion.EntityID.Value,
			Alias:            alias,
			NormalizedAlias:  normalizeSmartPickerQuery(alias),
			Confidence:       decimal.NewFromFloat(suggestion.Confidence),
			Reason:           trimReason(suggestion.Reason),
			Source:           models.SmartPickerSourceAI,
			Status:           models.SmartPickerSuggestionPending,
			ValidationStatus: models.SmartPickerValidationValid,
			JobRunID:         &jobRunID,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
		if err := s.DB.Create(&aliasRow).Error; err != nil {
			warnings = append(warnings, "AI alias suggestion store failed: "+err.Error())
			continue
		}
		aliases++
	}
	return hints, aliases, warnings
}

func (s *Server) finishSmartPickerLearningRun(run *models.AIJobRun, status string, inputJSON string, outputJSON string, warnings []string) {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":      status,
		"finished_at": now,
		"updated_at":  now,
	}
	if inputJSON != "" {
		updates["input_summary_json"] = inputJSON
	}
	if outputJSON != "" {
		updates["output_summary_json"] = outputJSON
	}
	if len(warnings) > 0 {
		updates["warnings_json"] = mustJSON(warnings)
		if status == models.AIJobStatusFailed {
			updates["error_message"] = warnings[0]
		}
	}
	_ = s.DB.Model(run).Updates(updates).Error
	run.Status = status
	run.FinishedAt = &now
	run.UpdatedAt = now
	run.InputSummaryJSON = inputJSON
	run.OutputSummaryJSON = outputJSON
	if len(warnings) > 0 {
		run.WarningsJSON = mustJSON(warnings)
	}
}

func (s *Server) createAIRequestLog(companyID uint, jobRunID uuid.UUID, taskType appai.TaskType, inputJSON string, outputJSON string, status string, errMsg string) error {
	companyIDPtr := companyID
	log := models.AIRequestLog{
		CompanyID:             &companyIDPtr,
		JobRunID:              &jobRunID,
		TaskType:              string(taskType),
		Provider:              "noop",
		Model:                 "",
		RequestSchemaVersion:  "smartpicker-learning-input.v1",
		ResponseSchemaVersion: "smartpicker-learning-output.v1",
		InputHash:             hashString(inputJSON),
		InputRedactedJSON:     inputJSON,
		OutputRedactedJSON:    outputJSON,
		Status:                status,
		ErrorMessage:          errMsg,
		PromptVersion:         "smartpicker-learning.v1",
		CreatedAt:             time.Now().UTC(),
	}
	return s.DB.Create(&log).Error
}

func (s *Server) smartPickerEntityLabel(companyID uint, context string, entityType string, entityID uint) string {
	provider, ok := defaultSmartPickerRegistry.get(entityType)
	if !ok {
		return strconv.FormatUint(uint64(entityID), 10)
	}
	item, err := provider.GetByID(s.DB, SmartPickerContext{CompanyID: companyID, Context: context}, strconv.FormatUint(uint64(entityID), 10))
	if err != nil || item == nil {
		return strconv.FormatUint(uint64(entityID), 10)
	}
	return strings.TrimSpace(item.Primary)
}

func recentQueriesForEntity(db *gorm.DB, companyID uint, context string, entityType string, entityID uint, start, end time.Time) []string {
	var rows []models.SmartPickerRecentQuery
	if err := db.Where("company_id = ? AND context = ? AND clicked_entity_type = ? AND clicked_entity_id = ? AND created_at BETWEEN ? AND ?",
		companyID, context, entityType, entityID, start, end).
		Order("created_at DESC").
		Limit(5).
		Find(&rows).Error; err != nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		q := strings.TrimSpace(row.Query)
		if q != "" && !seen[q] {
			out = append(out, q)
			seen[q] = true
		}
	}
	return out
}

func recentNoMatchQueries(db *gorm.DB, companyID uint, start, end time.Time) map[string][]smartPickerLearningNoMatch {
	type row struct {
		Context string
		Query   string
		Count   int
	}
	var rows []row
	if err := db.Model(&models.SmartPickerRecentQuery{}).
		Select("context, normalized_query AS query, COUNT(*) AS count").
		Where("company_id = ? AND result_clicked = false AND created_at BETWEEN ? AND ?", companyID, start, end).
		Group("context, normalized_query").
		Order("count DESC").
		Limit(100).
		Scan(&rows).Error; err != nil {
		return map[string][]smartPickerLearningNoMatch{}
	}
	out := map[string][]smartPickerLearningNoMatch{}
	for _, row := range rows {
		if strings.TrimSpace(row.Query) == "" {
			continue
		}
		if len(out[row.Context]) >= 10 {
			continue
		}
		out[row.Context] = append(out[row.Context], smartPickerLearningNoMatch{Query: row.Query, Count: row.Count})
	}
	return out
}

type flexibleUint struct {
	Value uint
}

func (u *flexibleUint) UnmarshalJSON(b []byte) error {
	var n uint
	if err := json.Unmarshal(b, &n); err == nil {
		u.Value = n
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return err
	}
	u.Value = uint(parsed)
	return nil
}

func aiRequestStatusFromGateway(status string) string {
	switch status {
	case appai.GatewayStatusSucceeded:
		return models.AIRequestStatusSucceeded
	case appai.GatewayStatusFailed:
		return models.AIRequestStatusFailed
	case appai.GatewayStatusInvalidOutput:
		return models.AIRequestStatusInvalidOutput
	default:
		return models.AIRequestStatusSkipped
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func hashString(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func trimReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		return reason[:500]
	}
	return reason
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
