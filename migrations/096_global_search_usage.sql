-- Global search behavior signals.
-- These tables are tenant-scoped ranking signals only; accounting truth remains
-- in the canonical business tables and journal/ledger records.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS global_search_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  session_id text NULL,
  query text NULL,
  normalized_query text NULL,
  query_kind text NOT NULL,
  event_type text NOT NULL,
  selected_entity_type text NOT NULL,
  selected_entity_id bigint NOT NULL,
  rank_position integer NULL,
  result_count integer NULL,
  source_route text NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_gs_events_company_created
  ON global_search_events(company_id, created_at);
CREATE INDEX IF NOT EXISTS idx_gs_events_kind_created
  ON global_search_events(company_id, query_kind, created_at);
CREATE INDEX IF NOT EXISTS idx_gs_events_user_kind_created
  ON global_search_events(company_id, user_id, query_kind, created_at);
CREATE INDEX IF NOT EXISTS idx_gs_events_entity_selected
  ON global_search_events(company_id, selected_entity_type, selected_entity_id);

CREATE TABLE IF NOT EXISTS global_search_type_stats (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  scope_type text NOT NULL,
  user_id uuid NULL,
  query_kind text NOT NULL,
  selected_entity_type text NOT NULL,
  select_count integer NOT NULL DEFAULT 0,
  select_count_30d integer NOT NULL DEFAULT 0,
  ai_weight numeric(8,4) NOT NULL DEFAULT 0,
  ai_confidence numeric(8,4) NOT NULL DEFAULT 0,
  weight_source text NOT NULL DEFAULT 'behavior',
  last_selected_at timestamptz NULL,
  last_query text NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_gs_type_stats_company_scope
  ON global_search_type_stats(company_id, scope_type, query_kind, selected_entity_type)
  WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_gs_type_stats_user_scope
  ON global_search_type_stats(company_id, scope_type, user_id, query_kind, selected_entity_type)
  WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_gs_type_stats_lookup
  ON global_search_type_stats(company_id, query_kind, selected_entity_type);
