-- Balanciz AI Learning & Output Platform V1.
-- Additive only: company-scoped report usage, dashboard suggestions/widgets,
-- and Action Center task visibility. AI remains advisory.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE INDEX IF NOT EXISTS idx_ai_jobs_company_type_created
  ON ai_job_runs(company_id, job_type, created_at);
CREATE INDEX IF NOT EXISTS idx_ai_jobs_company_status_created
  ON ai_job_runs(company_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_ai_jobs_type_status_created
  ON ai_job_runs(job_type, status, created_at);

CREATE INDEX IF NOT EXISTS idx_ai_request_company_task_created
  ON ai_request_logs(company_id, task_type, created_at);
CREATE INDEX IF NOT EXISTS idx_ai_request_status_created
  ON ai_request_logs(status, created_at);

CREATE TABLE IF NOT EXISTS report_usage_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  report_key text NOT NULL,
  event_type text NOT NULL,
  date_range_key text NULL,
  filters_json jsonb NULL,
  source_route text NULL,
  metadata_json jsonb NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_report_usage_company_user_report_created
  ON report_usage_events(company_id, user_id, report_key, created_at);
CREATE INDEX IF NOT EXISTS idx_report_usage_company_report_event_created
  ON report_usage_events(company_id, report_key, event_type, created_at);
CREATE INDEX IF NOT EXISTS idx_report_usage_company_event_created
  ON report_usage_events(company_id, event_type, created_at);

CREATE TABLE IF NOT EXISTS report_usage_stats (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  scope_type text NOT NULL,
  user_id uuid NULL,
  report_key text NOT NULL,
  open_count integer NOT NULL DEFAULT 0,
  export_count integer NOT NULL DEFAULT 0,
  print_count integer NOT NULL DEFAULT 0,
  drilldown_count integer NOT NULL DEFAULT 0,
  filter_count integer NOT NULL DEFAULT 0,
  last_opened_at timestamptz NULL,
  last_used_at timestamptz NULL,
  common_date_range_key text NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_report_usage_company_scope
  ON report_usage_stats(company_id, scope_type, report_key)
  WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_report_usage_user_scope
  ON report_usage_stats(company_id, scope_type, user_id, report_key)
  WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS dashboard_user_widgets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  widget_key text NOT NULL,
  title text NULL,
  config_json jsonb NULL,
  position integer NULL,
  source text NOT NULL,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_dashboard_widget_company
  ON dashboard_user_widgets(company_id, widget_key)
  WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_dashboard_widget_user
  ON dashboard_user_widgets(company_id, user_id, widget_key)
  WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS dashboard_widget_suggestions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  user_id uuid NULL,
  widget_key text NOT NULL,
  title text NOT NULL,
  reason text NOT NULL,
  evidence_json jsonb NULL,
  confidence numeric(8,4) NOT NULL DEFAULT 0,
  source text NOT NULL,
  status text NOT NULL,
  job_run_id uuid NULL,
  accepted_at timestamptz NULL,
  dismissed_at timestamptz NULL,
  snoozed_until timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dashboard_suggestion_status
  ON dashboard_widget_suggestions(company_id, user_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_dashboard_suggestion_widget_status
  ON dashboard_widget_suggestions(company_id, widget_key, status);

CREATE TABLE IF NOT EXISTS action_center_tasks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  assigned_user_id uuid NULL,
  task_type text NOT NULL,
  source_engine text NOT NULL,
  source_type text NOT NULL,
  source_object_id bigint NULL,
  title text NOT NULL,
  description text NULL,
  reason text NOT NULL,
  evidence_json jsonb NULL,
  priority text NOT NULL,
  due_date date NULL,
  action_url text NULL,
  status text NOT NULL,
  fingerprint text NOT NULL,
  ai_generated boolean NOT NULL DEFAULT false,
  confidence numeric(8,4) NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz NULL,
  dismissed_at timestamptz NULL,
  snoozed_until timestamptz NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_action_task_fingerprint
  ON action_center_tasks(company_id, fingerprint);
CREATE INDEX IF NOT EXISTS idx_action_task_status_due
  ON action_center_tasks(company_id, status, due_date);
CREATE INDEX IF NOT EXISTS idx_action_task_user_status
  ON action_center_tasks(company_id, assigned_user_id, status);
CREATE INDEX IF NOT EXISTS idx_action_task_type_status
  ON action_center_tasks(company_id, task_type, status);

CREATE TABLE IF NOT EXISTS action_center_task_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id bigint NOT NULL,
  task_id uuid NOT NULL,
  user_id uuid NULL,
  event_type text NOT NULL,
  metadata_json jsonb NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_action_task_event_task_created
  ON action_center_task_events(company_id, task_id, created_at);
CREATE INDEX IF NOT EXISTS idx_action_task_event_user_created
  ON action_center_task_events(company_id, user_id, created_at);
