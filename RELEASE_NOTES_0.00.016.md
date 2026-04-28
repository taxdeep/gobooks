# Balanciz 0.00.016 Release Notes

## Summary

Balanciz 0.00.016 adds the SmartPicker Recommendation + AI Learning Platform V1 foundation. The batch keeps accounting truth in backend services while adding explainable, company-isolated learning signals for faster SmartPicker recommendations.

## What Changed

- Added company-scoped SmartPicker behavior models and migration:
  - `smart_picker_events`
  - `smart_picker_usage_stats`
  - `smart_picker_pair_stats`
  - `smart_picker_recent_queries`
  - `smart_picker_learning_profiles`
  - `smart_picker_ranking_hints`
  - `smart_picker_alias_suggestions`
  - `smart_picker_decision_traces`
- Added AI audit and job visibility models:
  - `ai_job_runs`
  - `ai_request_logs`
- Reworked SmartPicker search ranking to use deterministic aggregate signals only.
- Extended usage logging to validate context/entity/company ownership before recording selectable behavior.
- Added pair-stat learning for anchor-to-target recommendations.
- Added feature-flagged decision traces with compact score components.
- Added provider-agnostic AI Gateway interfaces with a no-op provider as the safe default.
- Added a SmartPicker learning worker that stores system learning profiles and pending AI suggestions.
- Added future accounting copilot interfaces without auto-posting or direct accounting mutation.
- Updated the shared Alpine SmartPicker component to report search/select/no-match/create-new/clear events.

## Safety Rules Preserved

- Live SmartPicker search does not call AI.
- AI learning is disabled by default.
- AI-generated ranking hints and aliases are pending by default.
- Ranking cannot expand provider scope.
- Cross-company behavior, pair stats, hints, aliases, and traces are isolated by `company_id`.
- AI does not mutate accounting truth or create/post journal entries.

## Feature Flags

- `SMART_PICKER_LEARNING_ENABLED=true`
- `SMART_PICKER_AI_LEARNING_ENABLED=false`
- `SMART_PICKER_AI_HINT_AUTO_APPLY=false`
- `SMART_PICKER_TRACE_ENABLED=false`
- `SMART_PICKER_DECISION_TRACE_SAMPLE_RATE=0`
- `AI_GATEWAY_ENABLED=false`

## Validation

- `go test ./internal/web -run "SmartPicker|AIGateway" -count=1`
- `go test ./internal/ai -count=1`
- `go test ./...`
- `node --check internal/web/static/smart_picker.js`

## Known Limits

- No external AI provider adapter is enabled in this batch.
- Recent 7/30/90 day counters increment on select but do not yet perform rolling-window recomputation.
- Alias suggestions rank returned candidates; they do not expand retrieval scope.
- Pending hint/alias review UI is not yet implemented, but all records are inspectable in the database.
