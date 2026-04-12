# Gobooks Project Guide v5

## Final · Executable Unified Version

## ⚠️ Supreme Authority

This document is the highest-priority product and engineering authority for Gobooks.

All code, database schema, APIs, services, UI, reports, permissions, admin behavior, AI behavior, FX behavior, cache behavior, and future modules must comply with this guide.

If any conflict exists, follow this priority order:

**This document > other requirement docs > task-specific notes > temporary implementation habits**

Any implementation that conflicts with this document must be corrected.

---

# 1. Product Definition

## 1.1 Core Definition

**Gobooks = a strictly isolated multi-company accounting system + a strong-rule core engine + a control layer + an AI suggestion layer + modular business capabilities.**

## 1.2 Product Nature

Gobooks is not a loose bookkeeping tool and not a feature pile.

It is:

* a **multi-company accounting and business system**
* a **correctness-first accounting engine**
* a **control-oriented financial system**
* an **AI-assisted understanding system**, not an AI execution system
* a **modular, engine-centric, long-term platform**

## 1.3 Product Goal

Gobooks aims to provide a system that is:

* suitable for small businesses
* controllable for bookkeepers and accountants
* naturally ready for multi-company use
* disciplined in AI usage
* stable enough for long-term expansion

---

# 2. Core Principles

The following principles are non-negotiable.

## 2.1 Immutable Principles

* **Correctness > Flexibility**
* **Backend Authority > Frontend Assumptions**
* **Structure > Convenience**
* **Auditability > Performance Tricks**
* **Company Isolation > Everything**
* **Engine Truth > UI Presentation**
* **Historical Honesty > Cosmetic Neatness**
* **Cache = Acceleration ONLY**
* **AI = Suggestion Layer ONLY**

## 2.2 Principle Clarifications

### Correctness > Flexibility

The system may limit user freedom in order to protect accounting correctness.

### Backend Authority > Frontend Assumptions

Validation, numbering, lifecycle, posting, FX conversion, and accounting truth must be decided by the backend.

### Structure > Convenience

Stable structure and consistent logic are more important than local convenience.

### Auditability > Performance Tricks

No shortcut is allowed if it weakens traceability.

### Company Isolation > Everything

Multi-company isolation is the highest operational boundary.

### Historical Honesty > Cosmetic Neatness

If historical truth cannot be reconstructed with confidence, the system must show it honestly as unavailable / unknown / legacy-unavailable rather than invent a cleaner story.

### Cache = Acceleration ONLY

Cache may accelerate reads, ranking, and reports. Cache may not become accounting truth, authorization truth, or validation truth.

### AI = Suggestion Layer ONLY

AI may suggest, explain, rank, warn, and summarize.
AI may not post, reconcile, or alter books directly.

---

# 3. System Architecture

## 3.1 Two-Layer System

### 1) Business App

The main product used by business users.

This is where accounting, reporting, reconciliation, customers, vendors, invoices, bills, payments, tax, templates, settings, notifications, and operational workflows belong.

### 2) SysAdmin

A fully separate administration system.

It has independent authentication and does not participate in normal business posting flows.

SysAdmin controls:

* company lifecycle
* users
* system mode / maintenance mode
* runtime observability
* system-level administration

## 3.2 Architecture Direction

Gobooks must remain:

* **engine-centric**
* **module-based**
* **connector-ready**
* **AI-assisted, not AI-driven**

Core truth belongs to engines.
Business workflows belong to modules.
External integrations belong to connectors.
AI belongs to the suggestion layer.

## 3.3 Shared Architecture Layers

The platform should progressively standardize into these reusable layers:

* **Core Engines**

  * Posting Engine
  * Tax Engine
  * FX Conversion Engine
  * Numbering Engine
  * Reconciliation Control Engine
* **Business Modules**

  * Invoices
  * Bills
  * Customers
  * Vendors
  * Journal Entry
  * Reports
  * Tasks
  * Payment / Collection flows
* **Infrastructure Modules**

  * Notification infrastructure
  * Shared Cache Infrastructure
  * AI Assist Platform
  * SmartPicker Acceleration
  * Report Acceleration
* **Connector Layers**

  * payment providers
  * channels
  * external rate providers

---

# 4. Multi-Company Architecture

## 4.1 Basic Model

* one user can belong to multiple companies
* one company can have multiple users
* every authenticated session must have a clear active company context

Session must include:

* `active_company_id`

## 4.2 Mandatory Data Rules

All core accounting and business objects must have:

* `company_id NOT NULL`

All reads, writes, relations, reports, exports, caches, and AI context must be company-scoped.

This applies to, at minimum:

* accounts
* journal entries
* journal lines
* ledger entries
* invoices
* bills
* customers
* vendors
* taxes / tax codes
* numbering configs
* templates
* reconciliations
* audit logs
* tasks
* products/services
* currencies
* exchange rates
* notification configs
* security configs

## 4.3 Mandatory Write Validation

Every write path must validate company consistency, including:

* `document.company_id == session.active_company_id`
* `account.company_id == session.active_company_id`
* `tax.company_id == session.active_company_id`
* `customer/vendor.company_id == session.active_company_id`
* `journal_entry.company_id == source.company_id`
* `party.company_id == session.active_company_id`

Any cross-company reference must be rejected.

## 4.4 Forbidden by Default

The following are forbidden:

* cross-company journal entries
* cross-company ledger entries
* shared chart of accounts
* shared customers
* shared vendors
* shared tax objects
* shared business documents
* business documents referencing accounting objects from another company

## 4.5 UI Behavior

Users must always know which company they are in.

The UI must clearly provide:

* current company display
* company switcher
* full company-context switching

When switching company:

* UI shell may stay stable
* all data, permissions, reports, settings, numbering, templates, currencies, and FX context must switch

---

# 5. Authorization, Roles, and System Control

## 5.1 Business Roles

The Business App must support at least:

* `owner`
* `user`

Rules:

* each company must always have at least one owner
* owners can manage company users and permissions
* user permissions should be configurable by domain

Minimum recommended permission domains:

* AR
* AP
* approve
* reports
* settings access
* reconciliation-related access

## 5.2 SysAdmin Role

SysAdmin is not a business-company extension.

It is a separate system identity and must not reuse the business user model for company write operations.

SysAdmin capabilities include:

* company delete / inactive / lifecycle control
* user edit / disable / reset password / role management
* maintenance mode
* runtime/system error visibility
* platform-level administration

## 5.3 Maintenance Mode

The system must support maintenance / restart mode.

When enabled:

* normal users cannot log in or perform writes
* maintenance state must be visible
* SysAdmin remains available

---

# 6. Posting Engine

## 6.1 Single Official Entry Path

All formal accounting must go through the Posting Engine.

Standard flow:

**Document → Validation → Tax Calculation → FX / Currency Resolution → Posting Fragments → Aggregation → Journal Entry → Ledger Entries**

## 6.2 Prohibited Behavior

The following are forbidden:

* bypassing the Posting Engine
* writing formal ledger entries directly
* letting source documents change without keeping JE in sync
* creating formal JE without source linkage
* using provider data or UI preview as ledger truth

## 6.3 Journal Entry Requirements

Journal Entry must include at least:

* `company_id`
* `status`
* `source_type`
* `source_id`
* totals / summary fields
* posting metadata
* auditability metadata

Required JE statuses:

* `draft`
* `posted`
* `voided`
* `reversed`

Business document lifecycle remains the source of truth.
JE status must stay consistent with the source lifecycle.

## 6.4 Concurrency and Atomicity

Posting must run in a DB transaction and must ensure:

* source row locking
* duplicate-post prevention
* atomic source status / JE / ledger creation
* full rollback on failure

---

# 7. Data Identity and Numbering

## 7.1 Entity Number

System identity uses:

**`ENYYYY########`**

Rules:

* globally unique
* immutable
* backend-generated
* cannot be overridden by frontend
* unaffected by rename / void / reverse

## 7.2 Display Number

Display numbers are human-facing business numbers, not identity truth.

Examples include:

* invoice number
* bill number
* customer ID
* vendor ID
* receipt number
* payment number
* JE display number

Rules:

* configurable
* duplicate-detectable
* not identity
* cannot replace internal references

## 7.3 Numbering Settings

Numbering is a formal company-level capability.

It should support:

* prefix
* next number
* padding
* preview
* enabled/suggestion behavior

Entity number and display number must never be confused.

---

# 8. Chart of Accounts

## 8.1 Positioning

The COA is structured accounting infrastructure, not a free-form list.

## 8.2 Root Account Types

Root types are fixed:

* asset
* liability
* equity
* revenue
* cost_of_sales
* expense

## 8.3 Detail Account Types

Detail types exist under root types to support:

* recommendations
* reporting semantics
* AI suggestions
* default system behavior

Detail types may not break root-type accounting meaning.

## 8.4 Code Rules

Account code must follow structured rules.

Default directional mapping:

* `1xxxx` → asset
* `2xxxx` → liability
* `3xxxx` → equity
* `4xxxx` → revenue
* `5xxxx` → cost_of_sales
* `6xxxx` → expense

Company-level code length rules must be enforced consistently.

## 8.5 System-Reserved Accounts and Codes

Some account-code ranges and some accounts are reserved for system use.

This is required for:

* system control accounts
* foreign-currency AR/AP control accounts
* future FX gain/loss / rounding / revaluation accounts
* other governed accounting infrastructure

Rules:

* users must not create accounts in reserved code ranges
* users must not repurpose system-reserved accounts
* system identity must not rely on code string alone

System-owned accounts should be identified by durable backend fields such as:

* `is_system`
* `system_key`
* `system_role`
* `currency_code` where applicable
* `allow_manual_posting`

## 8.6 Delete and Status Rules

Historical accounting accounts should not be hard-deleted.

* ❌ delete with history
* ✅ inactive

System-owned control accounts should not be user-deletable or user-inactivatable.

## 8.7 COA Template

The system must support a system-default COA template.

New companies may be provisioned from that template.

System default records should be clearly marked, for example:

* `is_system_default = true`

---

# 9. Tax Engine

## 9.1 Core Principle

**Tax = line-level calculation → account-level aggregation**

Tax truth starts at the line level and is then aggregated.

## 9.2 Sales Side

For sales:

* revenue posts to revenue
* tax posts to tax payable

## 9.3 Purchase Side

For purchases:

* recoverable tax → receivable / recoverable tax account
* partially recoverable tax → split behavior
* non-recoverable tax → absorbed into expense or inventory as appropriate

## 9.4 Consistency Rules

Tax logic must be:

* backend-owned
* posting-engine aligned
* consistent across invoice, bill, JE, and reports
* never invented by UI

---

# 10. Journal Entry and FX Rules

## 10.1 Aggregation Principle

Formal JE should be aggregated by account / account-code semantics.

Gobooks should produce JE that is:

* readable
* reviewable
* traceable

## 10.2 Source Link Principle

JE must stay strongly linked to source:

* source_type
* source_id
* company consistency
* lifecycle synchronization

## 10.3 Prohibited

* JE without source
* source changed but JE unchanged
* hard deletion of posted truth
* accounting truth detached from business truth

## 10.4 Multi-Currency Journal Entry Rules

Journal Entry must support a single transaction currency per JE.

Rules:

* every JE must persist the actual `transaction_currency_code`
* base-currency JE must still persist explicit base ISO code
* JE header must persist a snapshot of:

  * `exchange_rate`
  * `exchange_rate_date`
  * `exchange_rate_source`
* JE lines must persist both:

  * transaction-currency amounts (`tx_debit`, `tx_credit`)
  * base-currency amounts (`debit`, `credit`)
* base debit/credit remain ledger truth
* tx amounts are the source amounts used to derive base truth

## 10.5 FX Source Semantics

Exchange-rate storage semantics and JE snapshot semantics must be normalized and separated.

Recommended row-origin semantics for stored exchange-rate rows:

* `manual`
* `provider_fetched`
* `legacy_unknown` when old provenance cannot be reconstructed honestly

Recommended JE snapshot semantics:

* `identity`
* `manual`
* `company_override`
* `system_stored`
* `provider_fetched`

UI labels such as “Latest” or “Manual” are display labels only and must not become drifting accounting truth.

## 10.6 Save-Time FX Rules

At JE save/post time:

* live provider calls are forbidden
* backend must validate an acceptable locally stored snapshot or a manual override
* backend must derive base amounts from tx amounts
* client-submitted base amounts must not be ledger truth

For non-manual foreign-currency saves:

* validation must be against the exact locally shown / accepted snapshot identity, or an explicitly allowed equivalent local snapshot state
* validation must not be based on “current latest rate” equality

## 10.7 Rounding Policy

Phase 1 policy:

* convert each line individually using banker’s rounding to 2 decimals
* if resulting base totals do not balance exactly, block save

This is intentional.

Controlled auto-rounding may only be considered later, and only after a governed system-owned FX rounding account exists.

## 10.8 Historical Honesty

Historical FX truth must be shown honestly.

Rules:

* if historical FX semantics can be reconstructed with confidence, they may be displayed as resolved truth
* if they cannot be reconstructed, they must be shown as unavailable / unknown / legacy-unavailable
* the system must not cosmetically relabel uncertain historical FX truth as identity/base truth

## 10.9 Posted JE FX Read Path

Every posted JE must have an immutable read-only FX snapshot display path.

This path must show, where applicable:

* transaction currency
* exchange rate
* effective date
* source label
* transaction/base amounts
* any legacy-unavailable marker when historical truth cannot be reconstructed

List, detail, and reversal flows must not disagree about legacy FX truth.

---

# 11. Multi-Currency Architecture Beyond JE

## 11.1 Multi-Currency Positioning

Multi-currency is not a page feature.
It is a governed accounting capability.

It must be implemented through reusable modules and engines, not duplicated across forms.

## 11.2 Core Multi-Currency Modules

### MultiCurrencyModule

Owns:

* company base currency
* multi-currency enablement
* allowed transaction currencies
* base vs foreign determination
* reusable FX form/read context

### ExchangeRateModule

Owns:

* local-first exchange-rate lookup
* company override vs system precedence
* provider fetch/store lifecycle
* provider adapter(s)
* source semantics
* refresh behavior
* fallback behavior

### FXConversionEngine

Owns:

* tx → base conversion
* line-level conversion
* totals conversion
* rounding policy
* save-time balance enforcement

## 11.3 External Provider Rule

Frankfurter may be used as the default free rate provider.

Rules:

* provider is for lookup / refresh only
* provider is never accounting truth
* provider result becomes usable only after local persistence and JE snapshot persistence
* manual override must never mutate shared rate tables

---

# 12. AR/AP Multi-Currency Control Accounts

## 12.1 Default Single-Currency Behavior

When multi-currency is not in use:

* Sales / Invoices post to the company default `AR`
* Bills post to the company default `AP`

## 12.2 Foreign-Currency Control Accounts

When a foreign currency such as USD is enabled:

* Gobooks automatically creates the corresponding foreign-currency control accounts, for example:

  * `AR-USD`
  * `AP-USD`

These are system-owned control accounts.

## 12.3 Customer/Vendor Routing Rules

Customer and Vendor each have exactly one default transaction currency.

Rules:

* if a customer’s default transaction currency is USD, new sales / invoices route to `AR-USD`
* if a vendor’s default transaction currency is USD, new bills route to `AP-USD`
* base-currency customers/vendors continue to use default `AR` / `AP`

## 12.4 Edit Rules

* a customer/vendor may change default transaction currency only if they have no historical transaction records
* once historical records exist, default transaction currency becomes locked

## 12.5 System Ownership Rules

System-owned foreign-currency control accounts must be:

* auto-created by system workflow
* mapped by backend control-account mapping, not guessed from UI text
* protected from user deletion / repurposing
* not freely selectable for arbitrary manual posting unless explicitly allowed by governed system behavior

---

# 13. Business Modules and Product Scope

## 13.1 Current Core Product Areas

Current formal product direction includes:

* Dashboard
* Journal Entry
* Invoices
* Bills
* Customers
* Vendors
* Receive Payment
* Pay Bills
* Reconciliation
* Reports
* Settings

## 13.2 Task Module Position

The Task module currently serves as:

* a business-work tracking layer
* a billable-work / billable-expense support layer
* a bridge into invoice / AR visibility
* a support layer for customer workspace

Current status:

* Task main flow is basically complete
* future Task / Quote boundary must be reconsidered together
* long-term semantic overlap must not be allowed to drift

## 13.3 Invoice Direction

Invoice is one of the most important future product lines.

It must continue to improve in:

* editable templates
* sending capability
* product/service integration
* revenue-account linkage
* sales-tax integration
* AR lifecycle consistency
* future compatibility with foreign-currency AR routing

## 13.4 Payment Gateway Layer

Gobooks should evolve toward a provider-agnostic payment gateway layer.

Planned direction includes:

* Stripe
* PayPal
* other providers

Rules:

* connectors are modular
* accounting truth remains system-owned
* payment integration must not corrupt AR or posting consistency

## 13.5 Channel / Integration Strategy

External channel integration must remain platform-agnostic.

Target directions include:

* Shopify
* Temu
* WooCommerce / WordPress
* other sales channels

Rules:

* channel-specific connectors
* shared engine truth
* no pollution of core accounting engine by connector logic

---

# 14. Reconciliation

## 14.1 Product Meaning

**Reconciliation = Accounting Control Layer**

It is not merely a checkbox workflow.

## 14.2 Recommended Status Flow

* `draft`
* `in_progress`
* `completed`
* `reopened`
* `cancelled`

## 14.3 Matching Capability

The system must support:

* one-to-one
* one-to-many
* many-to-one
* split

## 14.4 Completion Rule

Reconciliation may only complete when:

* `difference == 0`

## 14.5 UI Direction

Reconciliation UI should be:

* QuickBooks-like in clarity
* control-oriented
* summary-bar driven
* inflow / outflow separated

---

# 15. Void Reconciliation

Only the latest completed reconciliation may be voided.

Voiding is not deletion.

Required fields include:

* `is_voided`
* `voided_by`
* `voided_at`
* `void_reason`

Void means rollback of control state while preserving history.

---

# 16. Audit and Observability

## 16.1 Audit Is System-Wide

The system must record key actions including:

* match / unmatch
* suggestion accept / reject
* reconciliation finish
* reconciliation void
* auto-match run
* posting events
* status transitions
* sensitive settings changes
* sysadmin actions
* FX snapshot selection / override where appropriate
* legacy reversal block decisions where applicable

## 16.2 Observability

The platform should progressively support:

* runtime error logs
* maintenance-state visibility
* system health visibility
* future CPU / storage / attachment observability
* cache source / invalidation visibility
* provider / FX lookup visibility

---

# 17. Notifications and Communication Infrastructure

## 17.1 Positioning

Notifications are formal infrastructure, not a small utility.

They support:

* verification codes
* password/email changes
* invoice sending
* system notifications
* future SMS capabilities

## 17.2 Required State

At minimum, the system should track:

* config presence
* test_status
* last_tested_at
* verification_ready

## 17.3 Rules

* SMTP not verified → verification sending is blocked
* config changed → previous readiness becomes invalid
* sensitive flows depend on real notification readiness

---

# 18. User Security

## 18.1 Required Verification

The following actions must require verification:

* email change
* password change

## 18.2 Verification Code Rules

Verification codes must be:

* 6 characters
* case-insensitive
* single-use
* time-limited
* validated on the backend

## 18.3 Security Settings Direction

Settings should reserve room for future rules such as:

* unusual IP login alert
* more security policies
* notification readiness dependency

---

# 19. Settings Architecture

## 19.1 Principle

Settings is a structured control surface, not a dumping ground.

## 19.2 Company Settings Direction

Settings > Company should progressively organize into clear domains such as:

* Profile
* Templates
* Sales Tax
* Numbering
* Notifications
* Security
* Currencies / Multi-Currency controls

These are company-level controlled areas.

## 19.3 User Menu

User menu should provide:

* Profile
* Log out

Profile changes involving email/password must go through verification.

---

# 20. UI / UX Design Principles

## 20.1 Overall Style

Gobooks must feel:

* clean
* stable
* business-first
* professional
* restrained

No flashy, noisy, or game-like UI direction.

## 20.2 Core UX Rules

* left sidebar is the main navigation anchor
* Dashboard is an operational overview, not heavy BI
* Reports is the standard reporting home
* users must always know current company context
* tables and forms must support long-duration work
* multi-currency surfaces must make transaction currency vs base currency clear without turning forms into clutter

## 20.3 Long-Use Comfort

The design system should progressively support:

* low glare
* stable hierarchy
* report readability
* table readability
* eye-friendly dark mode

Dark mode should not be simple inversion.
It should be a professional low-glare theme suitable for accounting workflows.

---

# 21. Sidebar and Navigation

The sidebar must remain business-driven.

## 21.1 Official Structure

### Core

* Dashboard
* Journal Entry
* Invoices
* Bills

### Sales & Get Paid

* Customers
* Receive Payment

### Expense & Bills

* Vendors
* Pay Bills

### Accounting

* Chart of Accounts
* Reconciliation
* Reports

### Settings

Settings remains a distinct entry point, with structured internal subsections.

## 21.2 Explicitly Forbidden

* ❌ reintroducing top-level Contacts
* ❌ reintroducing top-level Banking
* ❌ moving Reports elsewhere
* ❌ breaking business meaning in navigation

---

# 22. SmartPicker and Acceleration Infrastructure

## 22.1 SmartPicker Positioning

SmartPicker is the legal-candidate entry surface for controlled selection fields.

It must remain responsible for:

* entity/provider resolution
* company scope enforcement
* context filtering
* active/type guard
* Search / GetByID legality semantics

It must not become the home of unrelated AI or persistence truth.

## 22.2 SmartPicker Acceleration

SmartPicker Acceleration is a separate enhancement layer.

It may own:

* recent retrieval
* hot-candidate retrieval
* short TTL query cache
* usage signal collection
* ranking
* picker metrics

Rules:

* ranking only within backend-supplied legal candidates
* cache only accelerates
* backend legality remains authoritative
* write-side invalidation is required after relevant master-data changes

## 22.3 Shared Cache Infrastructure

Shared cache infrastructure should support:

* namespacing
* versioning or equivalent invalidation primitives
* company-safe invalidation
* acceleration semantics for picker and reports

Global flush should be avoided as a default company-scoped invalidation strategy.

---

# 23. Reports and Report Acceleration

## 23.1 Reporting Is a Product Output

Reports are not temporary pages.

They must have:

* consistent logic
* alignment with engine truth
* alignment with business status
* semantic consistency across HTML / print / CSV / export

## 23.2 AR Reporting Direction

A/R Aging has entered the formal product-grade path and should continue improving in:

* summary/detail consistency
* export consistency
* print readability
* customer finance visibility support

## 23.3 General Rule

Report truth must be generated in backend services.
Templates may render but must not invent accounting meaning.

## 23.4 Report Acceleration

Report acceleration is allowed as a separate layer.

It may own:

* result cache
* aggregate cache
* export cache
* drill-down cache
* invalidation hooks
* freshness/source semantics
* warmup / prediction scaffolding

Rules:

* report acceleration must not replace report truth
* write-side invalidation is required on all relevant mutation paths
* cached/source/freshness semantics must be visible on supported report surfaces

---

# 24. AI Layer

## 24.1 Definition

**AI = advisor / external accountant style assistant, not executor**

AI should help:

* supervise bookkeeping
* explain business
* interpret reports
* identify anomalies
* support better decisions

## 24.2 Strictly Forbidden

* AI changing books
* AI auto-posting
* AI auto-completing reconciliation
* AI bypassing validation
* AI becoming accounting truth

## 24.3 Currently Allowed AI Capabilities

* suggestions
* rankings
* explanations
* anomaly hints
* report interpretation
* tax reasonableness hints
* account recommendations
* writing assistance for controlled text fields

## 24.4 AI Assist Platform

AI access should be centralized through an AI Assist Platform.

This layer may own:

* provider abstraction
* prompt registry
* safety rules
* audit logging
* fallback behavior
* latency / timeout / retry governance

## 24.5 Long-Term AI Vision

The long-term AI direction is closer to an **AI CFO / external accountant layer** than to OCR automation.

It should help small business owners understand their business more deeply.

---

# 25. AI for Reconciliation

## 25.1 Suggested Structure

**Rules → Scoring → AI Enhancement**

## 25.2 Suggestion Entities

Formal suggestion records should exist as dedicated entities, such as:

* `reconciliation_match_suggestions`
* `suggestion_lines`

## 25.3 User Control

* Accept → perform match
* Reject → no accounting truth change

Every suggestion must be explainable.

## 25.4 Reconciliation Memory

The system may learn historical behavior to improve suggestion quality, but must remain:

* explainable
* auditable
* non-black-box
* subordinate to user control

---

# 26. Intercompany Strategy

## 26.1 Current Stage

Currently forbidden:

* intercompany transactions
* cross-company posting
* due to / due from automation
* group consolidation accounting

## 26.2 Future Unlock Conditions

Intercompany may only be considered after:

* Posting Engine is stable
* Reconciliation is mature
* Audit is complete
* Company isolation is robust
* report/control consistency is stable

## 26.3 Possible Future Direction

Later possibilities may include:

* intercompany JE links
* due to / due from pairing
* mismatch alerts
* group reporting
* elimination entries
* consolidation assist

This is strictly later-stage work.

---

# 27. Data Principles

## 27.1 Must Always Hold

* company_id isolation
* entity_number immutability
* backend authority
* JE traceability
* source-linked accounting truth
* auditability
* explicit lifecycle
* FX snapshot honesty
* system-owned account governance

## 27.2 Never Allowed

* deleting historical truth
* AI changing books
* bypassing validation
* JE detached from business truth
* cross-company contamination
* frontend state replacing backend truth
* provider data being treated as accounting truth
* cosmetically hiding historical uncertainty as false certainty

---

# 28. Implementation Discipline

## 28.1 Required Development Checklist

Before implementing any feature, verify:

1. does it respect company isolation
2. does it preserve engine truth
3. does it avoid bypassing posting rules
4. does it preserve auditability
5. does it prevent UI from becoming source of truth
6. does it avoid polluting unrelated modules
7. does it preserve historical honesty when data is uncertain
8. does it keep cache / AI / provider layers subordinate to backend truth

## 28.2 Default Build Order

Recommended implementation order:

**Data model → Validation → Engine/service → Handler/API → View model → UI → Tests**

## 28.3 Testing Requirements

Important capabilities should cover:

* happy path
* status transitions
* partial payment / partial state
* void / reverse exclusion
* cross-company rejection
* export / HTML / CSV consistency
* nil / empty safety
* ordering stability
* provider contract correctness where applicable
* no-live-provider-at-save where applicable
* honest legacy read semantics where applicable

---

# 29. Final Product Summary

Gobooks is:

* a **strictly isolated multi-company system**
* a **strong-rule accounting engine**
* a **control-layer-driven finance platform**
* a **modular business application**
* an **AI suggestion layer, not an AI execution layer**
* a **long-term extensible architecture**

It must simultaneously preserve:

* accounting correctness
* company isolation
* business/accounting consistency
* auditability and control
* modular extensibility
* disciplined AI integration
* historical honesty
* governed multi-currency behavior
