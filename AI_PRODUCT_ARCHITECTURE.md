# Balanciz AI Product Architecture V1

This document formalizes Balanciz as an AI-assisted accounting platform.

It is an architecture lock, not an automation permission slip. AI can assist, recommend, summarize, explain, extract, and prepare drafts. Accounting truth remains owned by Balanciz backend engines.

## 1. Product Philosophy

Balanciz is an accounting system first.

The source of truth remains:

- Accounting Engine
- Posting Engine
- Tax Engine
- Journal Entry Engine
- AR/AP Engines
- Inventory and Costing Engines
- Payment and Reconciliation Engines
- Permission System
- Company Isolation
- Audit Trail

AI must not become the accountant of record.

The required product flow is:

```text
User intent
-> AI understands
-> AI Learning / Recommendation provides company-specific habits
-> Backend validates company, permission, tax, accounting, lifecycle, and posting rules
-> System creates suggestion or draft
-> User confirms when needed
-> Posting Engine owns accounting truth
-> Audit trail records everything
```

## 2. Four-Layer Architecture

Balanciz AI is organized into four layers:

1. Business Truth Layer
2. AI Learning Module
3. AI Output Module
4. AI Infrastructure Layer

The layers are intentionally asymmetric. Business Truth has authority. AI Learning observes. AI Output assists. AI Infrastructure governs safety, routing, cost, audit, and explainability.

```text
User / UI
  |
  v
AI Output Module
  |
  v
Business Truth Layer <---- AI Learning Module
  ^
  |
AI Infrastructure Layer governs AI calls, jobs, validation, traces, and audit
```

AI Output may call Business Truth services to validate or create drafts. AI Learning may read company-scoped behavior and aggregate it. Neither layer may bypass Business Truth.

## 3. Business Truth Layer

The Business Truth Layer owns accounting correctness.

It includes:

- Accounting Engine
- Posting Engine
- Tax Engine
- Journal Entry Engine
- AR Engine
- AP Engine
- Inventory Engine
- Costing Engine
- Payment Engine
- Reconciliation Engine
- Sales Tax Filing / Obligation Engine
- Permission Engine
- Company Isolation
- Audit Trail

Responsibilities:

- Decide whether an accounting action is legal.
- Validate company scope and active company context.
- Validate user permission.
- Validate accounts, tax codes, customers, vendors, product/services, inventory items, bank accounts, and documents.
- Validate document lifecycle.
- Validate balanced posting.
- Validate open or closed accounting periods.
- Validate inventory availability and costing.
- Validate payment application.
- Validate reconciliation status.
- Record audit trail.

Rules:

- AI cannot override this layer.
- Frontend cannot decide accounting legality.
- Cache, search, ranking, OCR, and AI output cannot become posting truth.
- Any write to accounting records must pass existing backend services and domain rules.

## 4. AI Learning Module

The AI Learning Module learns company and user habits. It does not modify accounting truth.

It produces:

- behavior events
- usage stats
- pair stats
- report usage stats
- dashboard preference stats
- task interaction stats
- learning profiles
- ranking hints
- widget suggestions
- AI summaries
- task handling patterns

Submodules:

- Behavior Learning Engine: records user actions across Balanciz.
- SmartPicker Learning: learns selection habits for vendors, customers, accounts, items, tax codes, payment accounts, and related pairings.
- Report Usage Learning: learns which reports users open, filter, export, print, drill into, and revisit.
- Dashboard Preference Learning: learns which widgets users add, remove, accept, dismiss, or ignore.
- Task / Action Pattern Learning: learns how users process work, without creating compliance obligations by itself.
- AI Summary Worker: periodically summarizes behavior into structured learning profiles.
- Learning Profiles: company-scoped learned memory used by output modules.

Rules:

- Learning results must be company-scoped.
- Learning observes, aggregates, and summarizes.
- Learning does not post transactions, file taxes, pay bills, reconcile bank accounts, or mutate accounting truth.
- AI-generated learning output is pending or advisory unless a safe deterministic rule explicitly activates it.

## 5. AI Output Module

The AI Output Module turns rules, learning, and AI analysis into visible product assistance.

It includes:

- SmartPicker Recommendation
- Dashboard Widget Suggestions
- Action Center / Task List
- Report Insights
- Accounting Copilot
- OCR / Document Extraction Output
- Natural Language Draft Builder
- AI Explanation Output

Allowed output behavior:

- suggest
- prepare
- explain
- summarize
- rank
- create reviewable drafts through backend services
- link to reports, review screens, or workflows

Forbidden output behavior:

- silently modify accounting records
- silently add dashboard widgets
- silently create compliance obligations
- directly post journal entries
- directly file tax
- directly pay bills
- bypass permission, lifecycle, period close, or company validation

High-risk actions require user confirmation and backend validation.

## 6. AI Infrastructure Layer

The AI Infrastructure Layer makes AI safe, provider-agnostic, traceable, and non-black-box.

It includes:

- AI Gateway
- AI Model Router
- Prompt Registry
- Tool Registry
- AI Job Runs
- AI Request Logs
- Decision Traces
- Cost Tracking
- Feature Flags
- Structured Output Validator
- Safety Policy Engine
- Audit Integration

Responsibilities:

- Route task types to providers and model classes.
- Keep prompts versioned.
- Keep AI calls auditable.
- Store background job runs.
- Store redacted request and response logs.
- Validate structured AI output.
- Enforce feature flags and company settings.
- Prevent silent background behavior.
- Track cost, latency, failure, skipped calls, and invalid outputs.

Business modules must not call a provider directly.

Correct dependency:

```text
Business module -> AI Gateway -> Model Router -> Provider Adapter
```

Incorrect dependency:

```text
Invoice module -> OpenAI directly
SmartPicker -> Claude directly
OCR module -> Gemini directly
```

## 7. AI Learning vs AI Output

AI Learning answers:

```text
What does this company or user usually do?
```

Examples:

- User often selects Amazon in the Expense vendor picker.
- Amazon usually maps to Office Supplies.
- Customer ABC usually uses Monthly Bookkeeping service.
- User checks AR Aging every Monday.
- User exports Profit & Loss at month-end.
- User dismissed Cash Flow widget suggestions several times.
- User usually handles bills before overdue invoices.

AI Output answers:

```text
How should Balanciz help the user now?
```

Examples:

- SmartPicker puts Amazon first.
- SmartPicker recommends Office Supplies after Amazon is selected.
- Dashboard suggests adding AR Aging.
- Action Center shows "3 bills due this week."
- AI summarizes unusual spending changes.
- Copilot creates an expense draft from a natural-language command.
- OCR extracts vendor/date/amount/tax from a receipt.

Output must be transparent, explainable, and reversible where appropriate.

## 8. SmartPicker Closed Loop

SmartPicker is both Learning and Output.

### 8.1 Learning Side

SmartPicker records:

- query
- selected result
- rank position
- result count
- no-match query
- create-new action
- context
- entity type
- selected entity
- anchor entity
- user
- company

SmartPicker learns:

- frequent selections
- recent selections
- vendor -> category
- vendor -> tax code
- vendor -> payment account
- customer -> product/service
- product/service -> revenue account
- account pairings
- alias terms
- no-match search terms

### 8.2 Output Side

SmartPicker outputs:

- recommended vendors
- recommended customers
- recommended accounts
- recommended product/services
- recommended tax codes
- recommended payment accounts
- reason text
- score explanations
- learned pattern suggestions

Example:

```text
User selects Vendor = Amazon.
SmartPicker later recommends:
- Category: Office Supplies
- Tax Code: GST
- Payment Account: RBC Visa

Reason:
"Frequently used with Amazon in Expense entries."
```

### 8.3 Boundaries

Rules:

- Context scope comes first.
- Ranking comes second.
- AI hints cannot expand scope.
- AI hints cannot recommend cross-company entities.
- AI hints cannot recommend inactive or invalid objects.
- Final validation remains backend-owned.
- Frontend must not decide accounting legality.
- Live SmartPicker search must not call external AI.

## 9. Dashboard Intelligence

Dashboard Intelligence is part of AI Output and draws from Report Usage Learning, Dashboard Preference Learning, and deterministic business signals.

Purpose:

- help users see what matters
- recommend useful dashboard widgets
- explain why a widget is useful
- respect user control over layout

### 9.1 Report Usage Learning

Track report events:

- report_opened
- report_filtered
- report_exported
- report_printed
- report_drilldown_clicked
- report_added_to_dashboard
- report_removed_from_dashboard
- report_suggestion_accepted
- report_suggestion_dismissed

Learn:

- frequently used reports
- month-end report habits
- exported reports
- drilldown-heavy reports
- reports important by user role
- reports that should be suggested as dashboard widgets

### 9.2 Dashboard Widget Suggestions

The system may suggest widgets such as:

- AR Aging
- AP Aging
- Cash Balance
- Profit & Loss
- Revenue This Month
- Expenses This Month
- GST/HST/PST/QST Payable
- Bills Due
- Open Invoices
- Bank Reconciliation Status
- Unmatched Bank Transactions
- Sales Tax Filing Status

Rules:

- Suggestions are pending by default.
- User must accept before a widget is added.
- Do not silently change dashboard layout.
- Store reason and evidence.
- Allow dismiss and snooze.
- Record accept and dismiss events for learning.

Example:

```text
You viewed AR Aging 8 times in the last 30 days. Add it to your dashboard?
```

## 10. Action Center / Task List

Action Center is the operational guidance layer. It shows what the user should do next.

Task generation is rule-driven first. AI may explain, summarize, prioritize, or suggest soft tasks, but deterministic business rules own compliance and accounting obligations.

### 10.1 Rule-Based Tasks

Rule-based tasks come from backend business rules.

Examples:

- GST/HST/PST/QST filing period approaching
- sales tax balance needs review
- payroll remittance due
- invoices overdue
- draft invoices not sent
- customer payments received but unapplied
- bills due soon
- overdue bills
- vendor credits available
- unmatched bank transactions
- reconciliation overdue
- old unreconciled items
- SMTP not configured
- sales tax setup incomplete
- low stock
- pending receipts
- inventory costing discrepancy

### 10.2 AI-Assisted Tasks

AI-assisted tasks are advisory and must be clearly marked.

Examples:

- "Advertising expenses increased sharply. Review Expense Report?"
- "Customer ABC is paying later than usual. Review AR Aging?"
- "You often check GST Payable near month-end. Add GST Payable widget?"
- "Several bills are due soon and cash balance is low. Review cash position?"

AI-assisted tasks must:

- have source = ai
- have confidence
- have reason
- be dismissible
- not mutate accounting data
- link to a review page or report

### 10.3 Task Explainability

Every task must answer:

```text
Why am I seeing this?
```

Task fields should include:

- company_id
- assigned_user_id nullable
- task_type
- source_engine
- source_type
- source_object_id nullable
- title
- description
- reason
- evidence_json
- priority
- due_date nullable
- action_url
- status
- fingerprint
- created_at
- updated_at

Statuses:

- open
- in_progress
- done
- dismissed
- snoozed
- expired
- blocked

Use fingerprints to avoid duplicate tasks.

## 11. Accounting Copilot Direction

Accounting Copilot is a future AI Output module. It must remain backend-validated and user-confirmed.

Example command:

```text
Yesterday I used RBC Visa to buy office supplies from Amazon for 35.20 including GST.
```

Future flow:

1. Parse user intent.
2. Determine action type: create_expense.
3. Resolve vendor through SmartPicker.
4. Resolve payment account through SmartPicker.
5. Resolve category through SmartPicker.
6. Resolve tax code through rules and learned behavior.
7. Build draft.
8. Validate through backend.
9. Show preview.
10. User confirms.
11. Backend saves draft or posts according to explicit policy.
12. Audit trail records action.

### 11.1 AI Action Levels

Level 0: Read-only

- explain
- summarize
- answer questions

Level 1: Suggest-only

- recommend vendor/account/tax/report/task

Level 2: Create Draft

- create draft invoice, bill, expense, journal entry draft
- do not post

Level 3: Prepare Posting

- generate posting preview
- wait for confirmation

Level 4: Auto-post with Policy

- future only
- requires explicit company setting
- low-risk actions only
- amount thresholds
- strong audit trail
- owner/admin approval

V1 must not implement Level 4.

## 12. AI Gateway / Model Router

AI task types should include:

- smartpicker_learning_summary
- smartpicker_alias_suggestion
- smartpicker_ranking_hint_generation
- report_usage_summary
- dashboard_widget_recommendation
- dashboard_summary
- task_priority_summary
- business_action_suggestion
- accounting_command_parse
- receipt_ocr_extract
- invoice_field_extract
- bill_field_extract
- bank_memo_parse
- financial_insight_summary
- anomaly_explanation
- email_draft_generation

Model classes:

- Cheap / mid model: SmartPicker learning summary, report usage summary, widget recommendation, task wording, alias suggestion, no-match query classification.
- Advanced model: natural-language accounting command, financial insight, anomaly explanation, complex business summary, multi-step planning.
- Vision model: receipt OCR, invoice image extraction, bill image extraction.
- Embedding / reranking model: semantic search, document matching, future knowledge retrieval.

Gateway responsibilities:

- provider selection
- model selection
- task type
- prompt version
- structured output validation
- token and cost tracking
- timeout
- retry
- fallback
- redaction
- request logging
- response logging
- safety policy
- feature flags

## 13. Data Boundary Rules

### 13.1 Global / System-Level Data

These may be global:

- SmartPicker context definitions
- ranking algorithm code
- AI task type definitions
- AI provider type definitions
- prompt templates
- tool schemas
- default Chart of Accounts templates
- default tax framework templates
- module definitions
- generic OCR capability definitions
- system default dashboard widget definitions

These are product capabilities and templates, not company behavior data.

### 13.2 Company-Owned Data

These must be company-scoped:

- actual Chart of Accounts
- customers
- vendors
- products/services
- tax codes
- invoices
- bills
- payments
- journal entries
- bank accounts
- inventory records
- receipts/documents
- SmartPicker events
- SmartPicker usage stats
- SmartPicker pair stats
- report usage stats
- dashboard widget preferences
- dashboard suggestions
- task list items
- task interaction history
- AI learning profiles
- ranking hints
- alias suggestions
- AI job runs
- AI request logs
- natural-language command history
- AI-generated draft suggestions

No company-owned learning data may be used to recommend entities inside another company.

### 13.3 User Preference vs Accounting Behavior

Global user preferences may include:

- theme
- language
- layout preference
- table density

Company-scoped user behavior includes:

- frequently selected vendors
- frequently selected accounts
- frequently selected reports
- task handling habits
- dashboard usage
- tax/account/payment habits

A user with access to multiple companies must have separate learned behavior for each company.

### 13.4 Cross-Company Learning

Do not implement cross-company learning in V1.

Future cross-company learning may only use:

- anonymized data
- aggregated data
- opt-in policy
- no company names
- no customer/vendor names
- no raw transaction details
- no direct amounts unless bucketed or anonymized

For now, all learned behavior is company-owned.

## 14. Company Isolation Rules

Every learning event, aggregate, recommendation, hint, dashboard suggestion, task, job run, request log, trace, and copilot command history must carry company scope when it relates to company behavior or accounting data.

Service rules:

- Company ID comes from authenticated server context, not frontend authority.
- Queries must include company_id at database level.
- Candidate retrieval must exclude cross-company objects.
- AI hints must be validated against the current company before use.
- AI output cannot carry source company identity into another company.
- A user belonging to multiple companies gets separate learned behavior per company.

## 15. Non-Black-Box Requirements

AI and background learning must not be silent or mysterious.

Implement visibility for:

- AI job runs
- background learning runs
- AI request logs
- AI output validation
- generated hints
- accepted hints
- rejected hints
- ignored AI suggestions
- dashboard suggestions
- task generation evidence
- ranking decision traces

Every AI-generated or learning-generated output should answer:

- What generated this?
- When was it generated?
- Which company does it belong to?
- Which job run generated it?
- What evidence was used?
- What confidence score was assigned?
- Was it system-generated or AI-generated?
- Is it pending, active, dismissed, rejected, or expired?
- Why was it accepted or rejected?

Recommended tables:

- ai_job_runs
- ai_request_logs
- ai_learning_profiles
- ai_decision_traces
- smart_picker_decision_traces
- dashboard_widget_suggestions
- action_center_tasks
- report_usage_events

## 16. Observability / Audit Requirements

Audit and observability must include:

- usage event rejected
- stats update failed
- pair stat update failed
- learning run started
- learning run completed
- learning run failed
- AI call skipped
- AI call failed
- AI output invalid
- AI suggestion rejected
- cross-company hint rejected
- ranking hint applied when debug mode is enabled
- dashboard suggestion generated
- dashboard suggestion accepted/dismissed
- action center task generated/dismissed/completed

Logs should include company_id, job_run_id where applicable, context, task_type, and reason/error.

Do not log sensitive raw transaction contents.

AI request logs should store redacted/summarized input and output plus hashes for correlation.

## 17. Feature Flags And Safe Defaults

Recommended feature flags:

- AI_GATEWAY_ENABLED
- SMART_PICKER_LEARNING_ENABLED
- SMART_PICKER_AI_LEARNING_ENABLED
- SMART_PICKER_TRACE_ENABLED
- REPORT_USAGE_LEARNING_ENABLED
- DASHBOARD_RECOMMENDATION_ENABLED
- ACTION_CENTER_ENABLED
- AI_TASK_SUGGESTIONS_ENABLED

Safe defaults:

- deterministic learning enabled where safe
- external AI calls disabled by default
- AI-generated hints pending by default
- decision traces disabled by default or sampled
- dashboard changes require user acceptance
- no auto-posting

## 18. Recommended Module Structure

Preferred long-term module direction:

```text
internal/ai
internal/ai/gateway
internal/ai/router
internal/ai/prompts
internal/ai/tools
internal/ai/jobs
internal/ai/audit
internal/ai/validation

internal/learning
internal/learning/behavior
internal/learning/smartpicker
internal/learning/reports
internal/learning/dashboard
internal/learning/tasks

internal/recommendation
internal/recommendation/smartpicker
internal/recommendation/dashboard
internal/recommendation/reports

internal/actioncenter
internal/actioncenter/tasks
internal/actioncenter/rules
internal/actioncenter/service
internal/actioncenter/handlers

internal/copilot
internal/copilot/commands
internal/copilot/planner
internal/copilot/drafts
internal/copilot/validation

internal/dashboard
internal/dashboard/widgets
internal/dashboard/suggestions
internal/dashboard/layout
```

Do not force this structure where existing Balanciz patterns provide a smaller, safer home. New implementation should adapt to the current modular monolith and avoid broad rewrites.

## 19. Current Implementation Alignment

Current foundation already includes:

- SmartPicker context validation and provider registry.
- SmartPicker behavior events, usage stats, pair stats, recent queries, learning profiles, ranking hints, alias suggestions, AI job runs, request logs, and decision traces.
- Deterministic SmartPicker ranking with no live AI calls.
- AI Gateway interfaces, Noop provider, model router, prompt registry, structured output validation.
- Accounting Copilot V1 interfaces with no-op behavior and no auto-posting.

This document expands those foundations into a whole-product architecture covering reports, dashboard intelligence, Action Center, insights, OCR, and future copilot workflows.

## 20. Roadmap

Phase 0: Architecture Lock

- finalize this architecture
- update authority docs
- define boundaries
- define module names
- define non-goals
- define audit/trace requirements

Phase 1: SmartPicker Learning + Recommendation

- behavior event tracking
- usage stats
- pair stats
- ranking engine
- decision traces
- no AI real-time calls

Phase 2: AI Gateway Foundation

- AI Gateway
- Model Router
- Prompt Registry
- Noop provider
- AI job runs
- request logs
- feature flags

Phase 3: Report Usage Learning

- track report usage
- learn report habits
- summarize report usage
- create dashboard widget suggestions

Phase 4: Dashboard Intelligence

- manual dashboard widgets
- widget suggestions
- accept/dismiss
- dashboard preference learning

Phase 5: Action Center

- rule-based tasks
- GST/sales tax reminders
- bills due
- invoices overdue
- bank reconciliation tasks
- system setup tasks
- task explainability

Phase 6: AI Insight Summary

- AI summary of dashboard
- AI priority explanation
- AI soft suggestions
- no accounting mutation

Phase 7: Accounting Copilot Drafts

- natural-language command parse
- entity resolution through SmartPicker
- draft creation only
- user confirmation
- backend validation

Phase 8: OCR / Document AI

- receipt OCR
- invoice field extraction
- bill field extraction
- structured validation
- draft creation only

Phase 9: Controlled Automation

- optional auto-draft
- future low-risk auto-posting only with explicit company policy
- full audit trail
- strict thresholds

## 21. Explicit Non-Goals

Do not implement these as part of the architecture lock:

- AI auto-posting
- AI direct journal entry creation
- AI direct tax filing
- AI direct bill payment
- AI directly changing dashboard without user approval
- real-time AI calls inside SmartPicker search
- cross-company behavior learning
- vector database
- complex ML training
- full OCR pipeline
- full natural-language accounting UI
- full AI chat interface
- replacing existing accounting/posting/tax engines
- bypassing backend validation
- changing document lifecycle rules
- large UI redesign

## 22. Acceptance Criteria

This architecture is accepted when:

- The four layers are clear and non-overlapping.
- Business Truth remains final authority.
- AI Learning and AI Output are separated.
- SmartPicker is described as both Learning and Output.
- Dashboard Intelligence has learning, suggestion, accept/dismiss, and evidence rules.
- Action Center is rule-first, explainable, and AI-assisted only where marked.
- Accounting Copilot direction is draft/review/confirm first, not auto-posting.
- Data boundaries are clear.
- Company isolation is explicit.
- Non-black-box controls are required for jobs, requests, outputs, and traces.
- Safe defaults are documented.
- Roadmap phases are defined.

## 23. Verification Checklist

For architecture-only changes:

- Confirm the document does not grant AI posting authority.
- Confirm no cross-company learning is allowed.
- Confirm external AI calls are optional and feature-flagged.
- Confirm dashboard suggestions require user acceptance.
- Confirm Action Center compliance tasks are rule-based first.
- Confirm AI-generated suggestions have source, confidence, status, reason, and evidence requirements.
- Confirm future copilot writes must call backend services and require confirmation unless future explicit policy allows otherwise.
