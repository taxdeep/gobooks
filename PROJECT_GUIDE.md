# Gobooks Project Guide v4
## Final · Executable Unified Version

## ⚠️ Supreme Authority

This document is the highest-priority product and engineering authority for Gobooks.

All code, database schema, APIs, services, UI, reports, permissions, admin behavior, AI behavior, and future modules must comply with this guide.

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

- a **multi-company accounting and business system**
- a **correctness-first accounting engine**
- a **control-oriented financial system**
- an **AI-assisted understanding system**, not an AI execution system
- a **modular, engine-centric, long-term platform**

## 1.3 Product Goal

Gobooks aims to provide a system that is:

- suitable for small businesses
- controllable for bookkeepers and accountants
- naturally ready for multi-company use
- disciplined in AI usage
- stable enough for long-term expansion

---

# 2. Core Principles

The following principles are non-negotiable.

## 2.1 Immutable Principles

- **Correctness > Flexibility**
- **Backend Authority > Frontend Assumptions**
- **Structure > Convenience**
- **Auditability > Performance Tricks**
- **Company Isolation > Everything**
- **Engine Truth > UI Presentation**
- **AI = Suggestion Layer ONLY**

## 2.2 Principle Clarifications

### Correctness > Flexibility
The system may limit user freedom in order to protect accounting correctness.

### Backend Authority > Frontend Assumptions
Validation, numbering, lifecycle, posting, and accounting truth must be decided by the backend.

### Structure > Convenience
Stable structure and consistent logic are more important than local convenience.

### Auditability > Performance Tricks
No shortcut is allowed if it weakens traceability.

### Company Isolation > Everything
Multi-company isolation is the highest operational boundary.

### AI = Suggestion Layer ONLY
AI may suggest, explain, rank, and warn.  
AI may not post, reconcile, or alter books directly.

---

# 3. System Architecture

## 3.1 Two-Layer System

### 1) Business App
The main product used by business users.

This is where accounting, reporting, reconciliation, customers, vendors, invoices, bills, tax, templates, settings, and notifications belong.

### 2) SysAdmin
A fully separate administration system.

It has independent authentication and does not participate in normal business posting flows.

SysAdmin controls:

- company lifecycle
- users
- system mode / maintenance mode
- runtime observability
- system-level administration

## 3.2 Architecture Direction

Gobooks must remain:

- **engine-centric**
- **module-based**
- **connector-ready**
- **AI-assisted, not AI-driven**

Core truth belongs to engines.  
Business workflows belong to modules.  
External integrations belong to connectors.  
AI belongs to the suggestion layer.

---

# 4. Multi-Company Architecture

## 4.1 Basic Model

- one user can belong to multiple companies
- one company can have multiple users
- every authenticated session must have a clear active company context

Session must include:

- `active_company_id`

## 4.2 Mandatory Data Rules

All core accounting and business objects must have:

- `company_id NOT NULL`

All reads, writes, relations, reports, and exports must be company-scoped.

This applies to, at minimum:

- accounts
- journal entries
- ledger entries
- invoices
- bills
- customers
- vendors
- taxes / tax codes
- numbering configs
- templates
- reconciliations
- audit logs
- tasks
- products/services
- notification configs
- security configs

## 4.3 Mandatory Write Validation

Every write path must validate company consistency, including:

- `document.company_id == session.active_company_id`
- `account.company_id == session.active_company_id`
- `tax.company_id == session.active_company_id`
- `customer/vendor.company_id == session.active_company_id`
- `journal_entry.company_id == source.company_id`

Any cross-company reference must be rejected.

## 4.4 Forbidden by Default

The following are forbidden:

- cross-company journal entries
- cross-company ledger entries
- shared chart of accounts
- shared customers
- shared vendors
- shared tax objects
- business documents referencing accounting objects from another company

## 4.5 UI Behavior

Users must always know which company they are in.

The UI must clearly provide:

- current company display
- company switcher
- full company-context switching

When switching company:

- UI shell may stay stable
- all data, permissions, reports, settings, numbering, and templates must switch

---

# 5. Authorization, Roles, and System Control

## 5.1 Business Roles

The Business App must support at least:

- `owner`
- `user`

Rules:

- each company must always have at least one owner
- owners can manage company users and permissions
- user permissions should be configurable by domain

Minimum recommended permission domains:

- AR
- AP
- approve
- reports
- settings access
- reconciliation-related access

## 5.2 SysAdmin Role

SysAdmin is not a business-company extension.

It is a separate system identity and must not reuse the business user model for company write operations.

SysAdmin capabilities include:

- company delete / inactive / lifecycle control
- user edit / disable / reset password / role management
- maintenance mode
- runtime/system error visibility
- platform-level administration

## 5.3 Maintenance Mode

The system must support maintenance / restart mode.

When enabled:

- normal users cannot log in or perform writes
- maintenance state must be visible
- SysAdmin remains available

---

# 6. Posting Engine

## 6.1 Single Official Entry Path

All formal accounting must go through the Posting Engine.

Standard flow:

**Document → Validation → Tax Calculation → Posting Fragments → Aggregation → Journal Entry → Ledger Entries**

## 6.2 Prohibited Behavior

The following are forbidden:

- bypassing the Posting Engine
- writing formal ledger entries directly
- letting source documents change without keeping JE in sync
- creating formal JE without source linkage

## 6.3 Journal Entry Requirements

Journal Entry must include at least:

- `company_id`
- `status`
- `source_type`
- `source_id`
- totals / summary fields
- posting metadata
- auditability metadata

Required JE statuses:

- `draft`
- `posted`
- `voided`
- `reversed`

Business document lifecycle remains the source of truth.  
JE status must stay consistent with the source lifecycle.

## 6.4 Concurrency and Atomicity

Posting must run in a DB transaction and must ensure:

- source row locking
- duplicate-post prevention
- atomic source status / JE / ledger creation
- full rollback on failure

---

# 7. Data Identity and Numbering

## 7.1 Entity Number

System identity uses:

**`ENYYYY########`**

Rules:

- globally unique
- immutable
- backend-generated
- cannot be overridden by frontend
- unaffected by rename / void / reverse

## 7.2 Display Number

Display numbers are human-facing business numbers, not identity truth.

Examples include:

- invoice number
- bill number
- customer ID
- vendor ID
- receipt number
- payment number
- JE display number

Rules:

- configurable
- duplicate-detectable
- not identity
- cannot replace internal references

## 7.3 Numbering Settings

Numbering is a formal company-level capability.

It should support:

- prefix
- next number
- padding
- preview
- enabled/suggestion behavior

Entity number and display number must never be confused.

---

# 8. Chart of Accounts

## 8.1 Positioning

The COA is structured accounting infrastructure, not a free-form list.

## 8.2 Root Account Types

Root types are fixed:

- asset
- liability
- equity
- revenue
- cost_of_sales
- expense

## 8.3 Detail Account Types

Detail types exist under root types to support:

- recommendations
- reporting semantics
- AI suggestions
- default system behavior

Detail types may not break root-type accounting meaning.

## 8.4 Code Rules

Account code must follow structured rules.

Default directional mapping:

- `1xxxx` → asset
- `2xxxx` → liability
- `3xxxx` → equity
- `4xxxx` → revenue
- `5xxxx` → cost_of_sales
- `6xxxx` → expense

Company-level code length rules must be enforced consistently.

## 8.5 Delete and Status Rules

Historical accounting accounts should not be hard-deleted.

- ❌ delete with history
- ✅ inactive

## 8.6 COA Template

The system must support a system-default COA template.

New companies may be provisioned from that template.

System default records should be clearly marked, for example:

- `is_system_default = true`

---

# 9. Tax Engine

## 9.1 Core Principle

**Tax = line-level calculation → account-level aggregation**

Tax truth starts at the line level and is then aggregated.

## 9.2 Sales Side

For sales:

- revenue posts to revenue
- tax posts to tax payable

## 9.3 Purchase Side

For purchases:

- recoverable tax → receivable / recoverable tax account
- partially recoverable tax → split behavior
- non-recoverable tax → absorbed into expense or inventory as appropriate

## 9.4 Consistency Rules

Tax logic must be:

- backend-owned
- posting-engine aligned
- consistent across invoice, bill, JE, and reports
- never invented by UI

---

# 10. Journal Entry Rules

## 10.1 Aggregation Principle

Formal JE should be aggregated by account / account-code semantics.

Gobooks should produce JE that is:

- readable
- reviewable
- traceable

## 10.2 Source Link Principle

JE must stay strongly linked to source:

- source_type
- source_id
- company consistency
- lifecycle synchronization

## 10.3 Prohibited

- JE without source
- source changed but JE unchanged
- hard deletion of posted truth
- accounting truth detached from business truth

---

# 11. Business Modules and Product Scope

## 11.1 Current Core Product Areas

Current formal product direction includes:

- Dashboard
- Journal Entry
- Invoices
- Bills
- Customers
- Vendors
- Receive Payment
- Pay Bills
- Reconciliation
- Reports
- Settings

## 11.2 Task Module Position

The Task module currently serves as:

- a business-work tracking layer
- a billable-work / billable-expense support layer
- a bridge into invoice / AR visibility
- a support layer for customer workspace

Current status:

- Task main flow is basically complete
- future Task / Quote boundary must be reconsidered together
- long-term semantic overlap must not be allowed to drift

## 11.3 Invoice Direction

Invoice is one of the most important future product lines.

It must continue to improve in:

- editable templates
- sending capability
- product/service integration
- revenue-account linkage
- sales-tax integration
- AR lifecycle consistency

## 11.4 Payment Gateway Layer

Gobooks should evolve toward a provider-agnostic payment gateway layer.

Planned direction includes:

- Stripe
- PayPal
- other providers

Rules:

- connectors are modular
- accounting truth remains system-owned
- payment integration must not corrupt AR or posting consistency

## 11.5 Channel / Integration Strategy

External channel integration must remain platform-agnostic.

Target directions include:

- Shopify
- Temu
- WooCommerce / WordPress
- other sales channels

Rules:

- channel-specific connectors
- shared engine truth
- no pollution of core accounting engine by connector logic

---

# 12. Reconciliation

## 12.1 Product Meaning

**Reconciliation = Accounting Control Layer**

It is not merely a checkbox workflow.

## 12.2 Recommended Status Flow

- `draft`
- `in_progress`
- `completed`
- `reopened`
- `cancelled`

## 12.3 Matching Capability

The system must support:

- one-to-one
- one-to-many
- many-to-one
- split

## 12.4 Completion Rule

Reconciliation may only complete when:

- `difference == 0`

## 12.5 UI Direction

Reconciliation UI should be:

- QuickBooks-like in clarity
- control-oriented
- summary-bar driven
- inflow / outflow separated

---

# 13. Void Reconciliation

Only the latest completed reconciliation may be voided.

Voiding is not deletion.

Required fields include:

- `is_voided`
- `voided_by`
- `voided_at`
- `void_reason`

Void means rollback of control state while preserving history.

---

# 14. Audit and Observability

## 14.1 Audit Is System-Wide

The system must record key actions including:

- match / unmatch
- suggestion accept / reject
- reconciliation finish
- reconciliation void
- auto-match run
- posting events
- status transitions
- sensitive settings changes
- sysadmin actions

## 14.2 Observability

The platform should progressively support:

- runtime error logs
- maintenance-state visibility
- system health visibility
- future CPU / storage / attachment observability

---

# 15. Notifications and Communication Infrastructure

## 15.1 Positioning

Notifications are formal infrastructure, not a small utility.

They support:

- verification codes
- password/email changes
- invoice sending
- system notifications
- future SMS capabilities

## 15.2 Required State

At minimum, the system should track:

- config presence
- test_status
- last_tested_at
- verification_ready

## 15.3 Rules

- SMTP not verified → verification sending is blocked
- config changed → previous readiness becomes invalid
- sensitive flows depend on real notification readiness

---

# 16. User Security

## 16.1 Required Verification

The following actions must require verification:

- email change
- password change

## 16.2 Verification Code Rules

Verification codes must be:

- 6 characters
- case-insensitive
- single-use
- time-limited
- validated on the backend

## 16.3 Security Settings Direction

Settings should reserve room for future rules such as:

- unusual IP login alert
- more security policies
- notification readiness dependency

---

# 17. Settings Architecture

## 17.1 Principle

Settings is a structured control surface, not a dumping ground.

## 17.2 Company Settings Direction

Settings > Company should progressively organize into clear domains such as:

- Profile
- Templates
- Sales Tax
- Numbering
- Notifications
- Security

These are company-level controlled areas.

## 17.3 User Menu

User menu should provide:

- Profile
- Log out

Profile changes involving email/password must go through verification.

---

# 18. UI / UX Design Principles

## 18.1 Overall Style

Gobooks must feel:

- clean
- stable
- business-first
- professional
- restrained

No flashy, noisy, or game-like UI direction.

## 18.2 Core UX Rules

- left sidebar is the main navigation anchor
- Dashboard is an operational overview, not heavy BI
- Reports is the standard reporting home
- users must always know current company context
- tables and forms must support long-duration work

## 18.3 Long-Use Comfort

The design system should progressively support:

- low glare
- stable hierarchy
- report readability
- table readability
- eye-friendly dark mode

Dark mode should not be simple inversion.  
It should be a professional low-glare theme suitable for accounting workflows.

---

# 19. Sidebar and Navigation

The sidebar must remain business-driven.

## 19.1 Official Structure

### Core
- Dashboard
- Journal Entry
- Invoices
- Bills

### Sales & Get Paid
- Customers
- Receive Payment

### Expense & Bills
- Vendors
- Pay Bills

### Accounting
- Chart of Accounts
- Reconciliation
- Reports

### Settings
Settings remains a distinct entry point, with structured internal subsections.

## 19.2 Explicitly Forbidden

- ❌ reintroducing top-level Contacts
- ❌ reintroducing top-level Banking
- ❌ moving Reports elsewhere
- ❌ breaking business meaning in navigation

---

# 20. AI Layer

## 20.1 Definition

**AI = advisor / external accountant style assistant, not executor**

AI should help:

- supervise bookkeeping
- explain business
- interpret reports
- identify anomalies
- support better decisions

## 20.2 Strictly Forbidden

- AI changing books
- AI auto-posting
- AI auto-completing reconciliation
- AI bypassing validation
- AI becoming accounting truth

## 20.3 Currently Allowed AI Capabilities

- suggestions
- rankings
- explanations
- anomaly hints
- report interpretation
- tax reasonableness hints
- account recommendations

## 20.4 Long-Term AI Vision

The long-term AI direction is closer to an **AI CFO / external accountant layer** than to OCR automation.

It should help small business owners understand their business more deeply.

---

# 21. AI for Reconciliation

## 21.1 Suggested Structure

**Rules → Scoring → AI Enhancement**

## 21.2 Suggestion Entities

Formal suggestion records should exist as dedicated entities, such as:

- `reconciliation_match_suggestions`
- `suggestion_lines`

## 21.3 User Control

- Accept → perform match
- Reject → no accounting truth change

Every suggestion must be explainable.

## 21.4 Reconciliation Memory

The system may learn historical behavior to improve suggestion quality, but must remain:

- explainable
- auditable
- non-black-box
- subordinate to user control

---

# 22. Intercompany Strategy

## 22.1 Current Stage

Currently forbidden:

- intercompany transactions
- cross-company posting
- due to / due from automation
- group consolidation accounting

## 22.2 Future Unlock Conditions

Intercompany may only be considered after:

- Posting Engine is stable
- Reconciliation is mature
- Audit is complete
- Company isolation is robust
- report/control consistency is stable

## 22.3 Possible Future Direction

Later possibilities may include:

- intercompany JE links
- due to / due from pairing
- mismatch alerts
- group reporting
- elimination entries
- consolidation assist

This is strictly later-stage work.

---

# 23. Reporting Principles

## 23.1 Reporting Is a Product Output

Reports are not temporary pages.

They must have:

- consistent logic
- alignment with engine truth
- alignment with business status
- semantic consistency across HTML / print / CSV / export

## 23.2 AR Reporting Direction

A/R Aging has entered the formal product-grade path and should continue improving in:

- summary/detail consistency
- export consistency
- print readability
- customer finance visibility support

## 23.3 General Rule

Report truth must be generated in backend services.  
Templates may render but must not invent accounting meaning.

---

# 24. Data Principles

## 24.1 Must Always Hold

- company_id isolation
- entity_number immutability
- backend authority
- JE traceability
- source-linked accounting truth
- auditability
- explicit lifecycle

## 24.2 Never Allowed

- deleting historical truth
- AI changing books
- bypassing validation
- JE detached from business truth
- cross-company contamination
- frontend state replacing backend truth

---

# 25. Implementation Discipline

## 25.1 Required Development Checklist

Before implementing any feature, verify:

1. does it respect company isolation
2. does it preserve engine truth
3. does it avoid bypassing posting rules
4. does it preserve auditability
5. does it prevent UI from becoming source of truth
6. does it avoid polluting unrelated modules

## 25.2 Default Build Order

Recommended implementation order:

**Data model → Validation → Engine/service → Handler/API → View model → UI → Tests**

## 25.3 Testing Requirements

Important capabilities should cover:

- happy path
- status transitions
- partial payment / partial state
- void / reverse exclusion
- cross-company rejection
- export / HTML / CSV consistency
- nil / empty safety
- ordering stability

---

# 26. Final Product Summary

Gobooks is:

- a **strictly isolated multi-company system**
- a **strong-rule accounting engine**
- a **control-layer-driven finance platform**
- a **modular business application**
- an **AI suggestion layer, not an AI execution layer**
- a **long-term extensible architecture**

It must simultaneously preserve:

- accounting correctness
- company isolation
- business/accounting consistency
- auditability and control
- modular extensibility
- disciplined AI integration