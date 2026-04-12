# Gobooks Execution Summary v1

## For Claude / Cursor / Codex

## 0. Purpose

This document is the short execution companion to the Project Guide.

Use it when implementing, reviewing, or refactoring Gobooks features.

Priority order:

**Project Guide v5 > this Execution Summary > feature-specific task notes > temporary implementation habits**

This summary does not replace the Project Guide. It compresses the most important implementation rules into an execution-ready format.

---

# 1. Product Identity

Gobooks is:

* a strictly isolated multi-company accounting system
* a correctness-first accounting engine
* a control-oriented financial system
* a modular, engine-centric business platform
* an AI suggestion system, not an AI execution system

Do not treat Gobooks as a loose CRUD app or a feature pile.

---

# 2. Non-Negotiable Rules

## 2.1 Always preserve

* Correctness > Flexibility
* Backend Authority > Frontend Assumptions
* Structure > Convenience
* Auditability > Performance Tricks
* Company Isolation > Everything
* Engine Truth > UI Presentation
* Historical Honesty > Cosmetic Neatness
* Cache = Acceleration ONLY
* AI = Suggestion Layer ONLY

## 2.2 Never allowed

* cross-company accounting contamination
* frontend becoming accounting truth
* direct ledger writing that bypasses the Posting Engine
* AI changing books
* cache becoming accounting truth or authorization truth
* cosmetically rewriting uncertain historical truth into false certainty
* provider data being treated as accounting truth

---

# 3. Default Build Order

Always implement in this order unless there is a very strong reason not to:

**Data model → Validation → Engine / Service → Handler / API → View model → UI → Tests**

Do not start from UI-first if the feature affects accounting truth.

---

# 4. Company Isolation Checklist

Every feature must answer YES to all of these before it is considered complete:

1. Are all core reads scoped by `company_id`?
2. Are all writes scoped by `active_company_id` or equivalent trusted backend context?
3. Are all related objects validated against the same company?
4. Are all reports, exports, caches, and AI contexts company-scoped?
5. Are all customer/vendor/account/tax/party references rejected when cross-company?

If any answer is NO, the implementation is incomplete.

---

# 5. Posting Engine Rules

All formal accounting must go through the Posting Engine.

Official flow:

**Document → Validation → Tax Calculation → FX / Currency Resolution → Posting Fragments → Aggregation → Journal Entry → Ledger Entries**

Required guarantees:

* DB transaction
* row locking where needed
* duplicate-post prevention
* atomic source status / JE / ledger creation
* rollback on failure

Never:

* bypass the Posting Engine
* write ledger truth directly from handlers/UI
* let source lifecycle diverge from JE lifecycle

---

# 6. Module Boundary Rules

## 6.1 Core engines

### Posting Engine

Owns formal accounting production.

### Tax Engine

Owns line-level tax truth and aggregation behavior.

### FX Conversion Engine

Owns tx → base conversion, line conversion, totals conversion, precision, and rounding policy.

### Numbering Engine

Owns backend numbering truth and identity-safe numbering behavior.

### Reconciliation Control Engine

Owns reconciliation control states and completion rules.

## 6.2 Reusable modules

### MultiCurrencyModule

Owns:

* company base currency
* multi-currency enablement
* allowed transaction currencies
* base vs foreign determination
* reusable FX context for forms and read paths

### ExchangeRateModule

Owns:

* local-first rate lookup
* company override vs system precedence
* provider fetch/store lifecycle
* source semantics
* refresh behavior
* fallback behavior

### SmartPickerModule

Owns:

* legal-candidate resolution
* entity/provider resolution
* company scope enforcement
* context filtering
* Search / GetByID legality semantics

### SmartPickerAccelerationModule

Owns:

* recent/hot retrieval
* short TTL query cache
* usage signals
* ranking
* picker metrics

Rules:

* rank only within backend-supplied legal candidates
* acceleration only, never legality truth

### ReportAccelerationModule

Owns:

* result cache
* aggregate/export/drill-down cache
* invalidation hooks
* freshness/source semantics
* optional warmup scaffolding

Rules:

* cache never replaces report truth
* relevant writes must invalidate report cache

### AIAssistPlatform

Owns:

* provider abstraction
* prompt registry
* safety rules
* audit logging
* fallback / timeout / retry governance

## 6.3 Page-level rules

Pages may own:

* local state
* row add/remove
* local draft persistence
* UI previews
* warnings and dialogs

Pages may not own:

* accounting truth
* FX source truth
* posting truth
* legality truth
* tax truth
* cross-company validity

---

# 7. SmartPicker Execution Rules

When implementing SmartPicker:

* SmartPicker decides legal candidates
* SmartPickerAcceleration improves speed/ranking only
* backend revalidation remains mandatory
* usage/ranking must never bypass legality checks
* write-side invalidation is required after relevant master-data mutations

Required continuity:

* open
* search
* loading
* empty state
* stale-response handling
* selection
* backend validation
* error state
* tests

---

# 8. Report Execution Rules

When implementing ReportAcceleration:

* backend services still compute report truth
* cache may accelerate only
* source/freshness semantics must be visible on supported report surfaces
* all relevant write paths must invalidate
* report HTML/print/CSV/export must remain semantically aligned

Required tests:

* cache hit path
* cache miss path
* invalidation after write
* HTML/export consistency

---

# 9. AI Execution Rules

AI is advisory only.

Allowed:

* suggestions
* ranking
* explanations
* anomaly hints
* writing assistance
* report interpretation

Forbidden:

* posting books
* reconciling books automatically
* mutating accounting truth
* bypassing validation
* becoming source of truth

When adding AI features:

* route through AIAssistPlatform
* make suggestion acceptance explicit
* preserve auditability
* add fallback or explicit disabled behavior
* test success path, disabled path, and isolation path

---

# 10. Multi-Currency Execution Rules

## 10.1 Core rules

* one JE = one transaction currency
* always persist explicit `transaction_currency_code`
* base-currency JE still stores explicit base ISO code
* provider is lookup-only, never accounting truth
* save/post must not call live provider
* backend derives base amounts from tx amounts
* posted FX snapshot is immutable

## 10.2 JE persistence

JE header must persist:

* `transaction_currency_code`
* `exchange_rate`
* `exchange_rate_date`
* `exchange_rate_source`

JE lines must persist:

* `tx_debit`
* `tx_credit`
* `debit`
* `credit`

Meaning:

* `debit/credit` = ledger truth in base currency
* `tx_debit/tx_credit` = source amounts in transaction currency

## 10.3 FX source semantics

Keep storage semantics separate from UI labels.

Suggested exchange-rate row origin semantics:

* `manual`
* `provider_fetched`
* `legacy_unknown`

Suggested JE snapshot source semantics:

* `identity`
* `manual`
* `company_override`
* `system_stored`
* `provider_fetched`

UI labels such as “Latest” or “Manual” are presentation-only.

## 10.4 Save-time validation

For non-manual foreign-currency saves:

* validate the exact locally shown/accepted snapshot identity
* do not compare only to “latest current rate”
* do not call live provider

## 10.5 Rounding policy

Phase 1:

* line-by-line banker’s rounding to 2 decimals
* if base totals do not balance exactly, block save

Do not auto-round into a synthetic JE line until a governed system-owned FX rounding account exists.

## 10.6 Historical honesty

If old FX truth can be reconstructed confidently, show resolved truth.
If it cannot, show unavailable / unknown / legacy-unavailable.

Do not relabel uncertain legacy truth as identity/base truth.

## 10.7 Read-path consistency

Detail, list, and reversal flows must not disagree about FX truth.
If legacy FX uses a resolver, all supported read/reversal surfaces should use that same resolver.

---

# 11. AR/AP Foreign-Currency Routing Rules

## 11.1 Single-currency mode

* Sales / Invoices → default `AR`
* Bills → default `AP`

## 11.2 Foreign-currency mode

When a foreign currency like USD is enabled:

* system auto-creates `AR-USD`
* system auto-creates `AP-USD`

These are system-owned control accounts.

## 11.3 Customer/Vendor currency rules

* each customer has exactly one default transaction currency
* each vendor has exactly one default transaction currency
* customer USD → invoices route to `AR-USD`
* vendor USD → bills route to `AP-USD`

## 11.4 Edit restrictions

* if customer/vendor has no historical transactions, default currency may change
* if history exists, default currency is locked

## 11.5 System-owned account rules

System-owned foreign-currency control accounts:

* are auto-created by backend workflow
* are mapped by backend control-account mapping
* are not user-deletable
* are not user-repurposable
* must respect reserved account-code namespaces

---

# 12. Historical Honesty Rules

This is a formal product rule.

When old data is incomplete or partially reconstructable:

* prefer honest uncertainty over false clarity
* do not overwrite history into cleaner but incorrect semantics
* do not let detail/read/list/reversal disagree

If a legacy action cannot be safely performed because FX truth is unavailable:

* block the action
* return a stable product-facing explanation
* preserve auditability

---

# 13. UI / UX Rules

UI must be:

* clean
* stable
* business-first
* professional
* low-glare in dark mode

Dark mode must not be simple inversion.

For accounting-heavy pages:

* no inconsistent white controls in dark mode
* numeric inputs right-aligned with tabular numerals
* transaction currency vs base currency must be clearly distinguishable
* do not hide critical FX semantics in ambiguous labels

---

# 14. Testing Checklist for Claude / Cursor / Codex

Before calling a feature “done”, confirm tests exist for the relevant risks.

## 14.1 Core

* happy path
* cross-company rejection
* nil / empty safety
* ordering stability

## 14.2 Posting / accounting

* source lifecycle alignment
* posted / reversed / voided transitions
* partial payment or partial state where applicable

## 14.3 FX / multi-currency

* provider contract correctness
* no-live-provider-at-save
* explicit base-currency snapshot behavior
* foreign snapshot persistence
* exact local snapshot validation
* rounding block behavior
* reversal snapshot continuity
* honest legacy read semantics

## 14.4 Reports / acceleration

* cache hit
* cache miss
* invalidation after relevant write
* source/freshness semantics
* export consistency

## 14.5 AI

* success path
* disabled path
* company isolation path
* explicit user acceptance flow where applicable

---

# 15. Execution Checklist Before Merge

Before merge, verify all of the following:

1. Company isolation is preserved
2. Backend remains source of truth
3. Posting Engine is not bypassed
4. Historical honesty is preserved
5. Cache is acceleration only
6. Provider is not accounting truth
7. AI is suggestion only
8. Read surfaces agree on the same truth model
9. System-owned accounts and reserved code rules are preserved
10. Tests cover the highest-risk logic, not just happy paths

---

# 16. Final Summary

Gobooks is a strictly isolated, correctness-first, engine-centered accounting platform.

When implementing any feature:

* protect accounting truth
* protect company isolation
* protect auditability
* protect historical honesty
* keep engines reusable
* keep modules clean
* keep cache, AI, and providers subordinate to backend truth
