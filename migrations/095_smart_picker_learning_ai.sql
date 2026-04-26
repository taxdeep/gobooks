-- Gobooks SmartPicker recommendation + AI learning foundation V1.
-- Accounting truth remains in existing backend services; these tables only
-- store company-scoped behavior signals, recommendations, and audit trails.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS smart_picker_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  session_id text NULL,
  context text NOT NULL,
  entity_type text NOT NULL,
  query text NULL,
  normalized_query text NULL,
  event_type text NOT NULL,
  selected_entity_id bigint NULL,
  rank_position integer NULL,
  result_count integer NULL,
  source_route text NULL,
  anchor_context text NULL,
  anchor_entity_type text NULL,
  anchor_entity_id bigint NULL,
  metadata_json jsonb NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sp_events_company_created ON smart_picker_events(company_id, created_at);
CREATE INDEX IF NOT EXISTS idx_sp_events_context_created ON smart_picker_events(company_id, context, created_at);
CREATE INDEX IF NOT EXISTS idx_sp_events_user_context_created ON smart_picker_events(company_id, user_id, context, created_at);
CREATE INDEX IF NOT EXISTS idx_sp_events_entity_selected ON smart_picker_events(company_id, entity_type, selected_entity_id);
CREATE INDEX IF NOT EXISTS idx_sp_events_type_created ON smart_picker_events(company_id, event_type, created_at);

CREATE TABLE IF NOT EXISTS smart_picker_usage_stats (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  scope_type text NOT NULL,
  user_id uuid NULL,
  context text NOT NULL,
  entity_type text NOT NULL,
  entity_id bigint NOT NULL,
  select_count integer NOT NULL DEFAULT 0,
  select_count_7d integer NOT NULL DEFAULT 0,
  select_count_30d integer NOT NULL DEFAULT 0,
  select_count_90d integer NOT NULL DEFAULT 0,
  last_selected_at timestamptz NULL,
  last_query text NULL,
  avg_rank_position numeric(18,4) NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sp_usage_company_scope_entity
  ON smart_picker_usage_stats(company_id, scope_type, context, entity_type, entity_id)
  WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_sp_usage_user_scope_entity
  ON smart_picker_usage_stats(company_id, scope_type, user_id, context, entity_type, entity_id)
  WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sp_usage_company_context ON smart_picker_usage_stats(company_id, context);

CREATE TABLE IF NOT EXISTS smart_picker_pair_stats (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  scope_type text NOT NULL,
  user_id uuid NULL,
  source_context text NOT NULL,
  anchor_entity_type text NOT NULL,
  anchor_entity_id bigint NOT NULL,
  target_context text NOT NULL,
  target_entity_type text NOT NULL,
  target_entity_id bigint NOT NULL,
  select_count integer NOT NULL DEFAULT 0,
  total_anchor_select_count integer NOT NULL DEFAULT 0,
  confidence_score numeric(8,4) NOT NULL DEFAULT 0,
  last_selected_at timestamptz NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sp_pair_company_scope
  ON smart_picker_pair_stats(company_id, scope_type, source_context, anchor_entity_type, anchor_entity_id, target_context, target_entity_type, target_entity_id)
  WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_sp_pair_user_scope
  ON smart_picker_pair_stats(company_id, scope_type, user_id, source_context, anchor_entity_type, anchor_entity_id, target_context, target_entity_type, target_entity_id)
  WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sp_pair_company_source ON smart_picker_pair_stats(company_id, source_context);

CREATE TABLE IF NOT EXISTS smart_picker_recent_queries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  context text NOT NULL,
  query text NOT NULL,
  normalized_query text NOT NULL,
  result_clicked boolean NOT NULL DEFAULT false,
  clicked_entity_type text NULL,
  clicked_entity_id bigint NULL,
  result_count integer NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sp_recent_user_context_created ON smart_picker_recent_queries(company_id, user_id, context, created_at);
CREATE INDEX IF NOT EXISTS idx_sp_recent_context_query ON smart_picker_recent_queries(company_id, context, normalized_query);
CREATE INDEX IF NOT EXISTS idx_sp_recent_clicked_created ON smart_picker_recent_queries(company_id, context, result_clicked, created_at);

CREATE TABLE IF NOT EXISTS smart_picker_learning_profiles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  context text NOT NULL,
  profile_json jsonb NOT NULL,
  summary_text text NULL,
  source_window_start timestamptz NOT NULL,
  source_window_end timestamptz NOT NULL,
  source text NOT NULL,
  model_name text NULL,
  model_version text NULL,
  confidence numeric(8,4) NOT NULL DEFAULT 0,
  job_run_id uuid NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sp_learning_company_context ON smart_picker_learning_profiles(company_id, context);
CREATE INDEX IF NOT EXISTS idx_sp_learning_job ON smart_picker_learning_profiles(job_run_id);

CREATE TABLE IF NOT EXISTS smart_picker_ranking_hints (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  context text NOT NULL,
  entity_type text NOT NULL,
  entity_id bigint NOT NULL,
  boost_score numeric(8,4) NOT NULL DEFAULT 0,
  confidence numeric(8,4) NOT NULL DEFAULT 0,
  reason text NULL,
  source text NOT NULL,
  status text NOT NULL,
  validation_status text NOT NULL,
  validation_error text NULL,
  activated_by_user_id uuid NULL,
  rejected_by_user_id uuid NULL,
  job_run_id uuid NULL,
  expires_at timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sp_hint_lookup ON smart_picker_ranking_hints(company_id, context, entity_type, entity_id, status, validation_status);
CREATE INDEX IF NOT EXISTS idx_sp_hint_job ON smart_picker_ranking_hints(job_run_id);

CREATE TABLE IF NOT EXISTS smart_picker_alias_suggestions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  context text NOT NULL,
  entity_type text NOT NULL,
  entity_id bigint NOT NULL,
  alias text NOT NULL,
  normalized_alias text NOT NULL,
  confidence numeric(8,4) NOT NULL DEFAULT 0,
  reason text NULL,
  source text NOT NULL,
  status text NOT NULL,
  validation_status text NOT NULL,
  validation_error text NULL,
  approved_by_user_id uuid NULL,
  rejected_by_user_id uuid NULL,
  job_run_id uuid NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sp_alias_lookup ON smart_picker_alias_suggestions(company_id, context, entity_type, entity_id, normalized_alias, status, validation_status);
CREATE INDEX IF NOT EXISTS idx_sp_alias_job ON smart_picker_alias_suggestions(job_run_id);

CREATE TABLE IF NOT EXISTS ai_job_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NULL,
  job_type text NOT NULL,
  status text NOT NULL,
  trigger_type text NOT NULL,
  triggered_by_user_id uuid NULL,
  started_at timestamptz NULL,
  finished_at timestamptz NULL,
  source_window_start timestamptz NULL,
  source_window_end timestamptz NULL,
  input_summary_json jsonb NULL,
  output_summary_json jsonb NULL,
  error_message text NULL,
  warnings_json jsonb NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ai_jobs_company_type_status ON ai_job_runs(company_id, job_type, status);

CREATE TABLE IF NOT EXISTS ai_request_logs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NULL,
  job_run_id uuid NULL,
  task_type text NOT NULL,
  provider text NULL,
  model text NULL,
  request_schema_version text NULL,
  response_schema_version text NULL,
  input_hash text NULL,
  input_redacted_json jsonb NULL,
  output_redacted_json jsonb NULL,
  status text NOT NULL,
  error_message text NULL,
  prompt_version text NULL,
  token_input_count integer NULL,
  token_output_count integer NULL,
  estimated_cost numeric(18,6) NOT NULL DEFAULT 0,
  latency_ms integer NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ai_request_job ON ai_request_logs(job_run_id);
CREATE INDEX IF NOT EXISTS idx_ai_request_company_task ON ai_request_logs(company_id, task_type, status);

CREATE TABLE IF NOT EXISTS smart_picker_decision_traces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  context text NOT NULL,
  entity_type text NOT NULL,
  query text NULL,
  normalized_query text NULL,
  selected_entity_id bigint NULL,
  returned_count integer NULL,
  trace_json jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sp_trace_company_context ON smart_picker_decision_traces(company_id, context, created_at);
