# Codex Task Template
## Final · Executable Version

---

## ⚠️ Authority

This template is the standard task wrapper for using Codex on Balanciz.

Priority order:

**PROJECT_GUIDE.md > PROJECT_GUIDE_EXEC_SUMMARY.md > BUG_FIX_PROTOCOL.md > CODEX_TASK_TEMPLATE.md > task-specific notes**

This file does not replace the Project Guide.  
It standardizes how implementation, bug-fix, and review tasks should be given to Codex.

---

# 1. Purpose

Balanciz is not a generic CRUD app.

It is:

- a correctness-first accounting system
- a multi-company system
- an engine-centered platform
- a controlled financial product
- an AI-assisted but not AI-driven application

Therefore, every Codex task must be:

- scoped
- explicit
- safety-aware
- module-aware
- test-aware
- honest about uncertainty

---

# 2. Universal Instructions for Codex

Use these rules for every Balanciz task.

## 2.1 Core Rules

- Backend = source of truth
- Cache = acceleration only
- AI = suggestion only
- Company isolation > everything
- Historical honesty > cosmetic neatness
- Smallest safe change > broad cleanup
- No opportunistic refactor unless explicitly requested
- No UI/backend disconnect
- No provider data used as accounting truth at save/post time

## 2.2 Required Development Order

Unless there is a strong reason not to, work in this order:

**Data model → Validation → Engine / Service → Handler / API → View model → UI → Tests**

## 2.3 Protected Core Zones

Do not modify these unless the task explicitly requires it and the root cause is proven to be there:

- Posting Engine
- Tax Engine
- FX Conversion Engine
- Exchange-rate snapshot validation
- AR/AP settlement logic
- Reconciliation completion / void logic
- Company isolation / permission guards
- Report truth services

If you touch any protected core zone, you must explicitly explain:
- why it was necessary
- which test proves safety
- which invariant is preserved

---

# 3. Required Output Format for Any Task

Every Codex task must return this structure unless explicitly told otherwise:

1. Task Classification  
2. Root Cause or Implementation Scope  
3. Path Mapping To Real Repo  
4. Blast Radius  
5. Exact Plan  
6. Files To Change  
7. Tests To Add / Update  
8. Risks / Edge Cases  
9. Code changes or patch output  
10. Final Self-Audit  

---

# 4. Common Context Block

Use this block in every task.

## Balanciz Context
- Balanciz is a strictly isolated multi-company accounting system
- Backend is the accounting truth
- Posting goes through the Posting Engine
- FX is governed by snapshot semantics
- Historical uncertainty must not be rewritten into false certainty
- SmartPicker legality stays in backend
- ReportAcceleration and SmartPickerAcceleration are acceleration only
- AI can suggest, rank, explain, or draft text, but cannot become truth

## Required invariants
- company isolation preserved
- no live provider call at save/post for accounting truth
- no silent mutation of posted/accounting truth
- no UI-only business fields without backend contract
- no cross-company leakage
- no cache becoming authority
- no AI becoming execution

---

# 5. Task Modes

Choose one mode per task.

---

## Mode A — New Feature / New Module

Use this when implementing a feature or module that does not yet exist.

### Prompt Template

You are implementing a new Balanciz feature in full-stack mode.

This is a feature implementation task, not a cleanup task.

Use the Balanciz Project Guide, Execution Summary, and Bug Fix Protocol as governing rules.

### Task
[Describe the feature clearly]

### Business goal
[What user/business problem this feature solves]

### Required scope
- data model
- validation
- service/engine
- handler/API
- VM
- UI
- tests

### Constraints
- preserve company isolation
- preserve backend truth
- preserve module boundaries
- preserve historical honesty
- no UI/backend gap

### Protected areas
[List any protected core zones that must not be touched unless absolutely necessary]

### Relevant files/modules
[List likely files or directories]

### Required output
Return:
1. Feature classification
2. Path mapping to real repo
3. Scope and blast radius
4. Implementation plan
5. File change plan
6. Validation rules
7. Tests to add
8. Risks / fallbacks
9. Code changes or patch output
10. Final self-audit against Balanciz principles

### Completion gate
Do not declare completion unless:
- backend contract exists
- UI path exists
- validation exists
- persistence exists if needed
- tests exist
- no company-isolation gap exists

---

## Mode B — Surgical Bug Fix

Use this when fixing a bug with the smallest safe change.

### Prompt Template

You are fixing a Balanciz bug in surgical-fix mode.

This is NOT a refactor task.
This is NOT a cleanup task.
This is NOT a feature expansion task.

Your goal is to fix exactly one bug with the smallest safe change set.

### Bug
[Describe the bug]

### Expected behavior
[What should happen]

### Reproduction
[Steps / failing path]

### Required classification
Classify as one of:
- ui-only
- ui-contract
- module-integration
- engine-adjacent
- core-truth

### Protected core zones
[List protected areas that should not be touched unless root cause is proven there]

### Relevant files/modules
[List likely files]

### Required process
1. Classify the bug
2. Explain root cause
3. Explain blast radius
4. State protected modules that should not be touched
5. Make the smallest safe fix
6. Add or update regression tests
7. Explain whether core logic was touched
8. Explain why the fix is safe

### Required output
Return:
1. Bug classification
2. Root cause
3. Blast radius
4. Minimal fix plan
5. Files changed
6. Tests added/updated
7. Whether protected core logic was touched
8. Why the fix is safe
9. Code changes or patch output
10. Final self-audit

### Completion gate
Do not declare completion unless:
- the bug is reproduced or clearly evidenced
- the fix is minimal
- regression tests exist
- no unrelated refactor was done
- invariants remain preserved

---

## Mode C — Blocker Closeout

Use this when most work is done and only a few known blockers remain.

### Prompt Template

You are closing the remaining blockers for a Balanciz feature.

Do NOT redesign the whole feature again.
Do NOT reopen already-fixed areas unless code directly contradicts the prior conclusion.

### Current status
[READY / NOT READY and why]

### Known fixed areas
[List what should not be reopened]

### Remaining blockers
[List the exact remaining blockers]

### Required focus
Only work on the blocker set above.

### Relevant files/modules
[List files]

### Required process
1. Map blocker → real file path
2. Produce blocker closure matrix
3. Plan exact fix
4. Implement or patch
5. Add blocker-specific tests
6. Re-audit only the blocker area

### Required output
Return:
1. Path Mapping To Real Repo
2. Blocker Closure Matrix
3. Exact Plan
4. File Change Plan
5. Tests Added / Updated
6. Code changes or patch output
7. Final self-audit against blocker list

### Completion gate
Do not declare completion unless each blocker is either:
- fixed, or
- patch-planned precisely with file-level instructions

---

## Mode D — Re-Review / Audit

Use this when Codex is reviewing an implementation rather than writing one.

### Prompt Template

You are the Balanciz implementation auditor.

Your job is to verify whether an implementation truly satisfies Balanciz rules.

Do not be lenient.
Do not confuse “directionally correct” with “safe to merge”.

### Review target
[Paste diff / patch / summary / files]

### Required checks
- backend truth
- company isolation
- module boundaries
- UI/backend continuity
- historical honesty
- no live provider at save/post
- no cache or AI authority drift
- test coverage for high-risk logic

### Required output
Return:
1. Scope Mapping
2. Critical Checks
3. Interaction Coverage Matrix
4. Critical Disconnects
5. Safety Risks
6. Boundary Violations
7. Missing Tests
8. Exact Remediation Plan
9. Final verdict

### Final verdict format
- Overall Verdict: READY | NOT READY
- Highest-Risk Area:
- Must-Fix Before Merge:
- Should-Fix Soon:
- Nice-to-Have Improvements:

---

# 6. Specialized Add-On Blocks

These can be appended to a task when relevant.

---

## 6.1 FX / Multi-Currency Add-On

Append when the task touches FX, JE, invoices/bills in foreign currency, AR/AP foreign routing, or exchange rates.

### Extra rules
- Always persist explicit transaction currency
- Do not use empty string for base currency semantics
- Save/post must not call live provider
- Validate exact accepted local snapshot, not just current latest rate
- Base amounts are backend-derived only
- Reversal must reuse snapshot
- If history is uncertain, show legacy-unavailable / unknown honestly
- Phase 1 rounding = block save on base imbalance
- No auto-rounding until governed FX rounding account exists

### Extra required tests
- no-live-provider-at-save
- base currency explicit snapshot
- foreign snapshot persistence
- exact local snapshot validation
- reversal continuity
- honest legacy read semantics

---

## 6.2 SmartPicker Add-On

Append when task touches searchable selectors.

### Extra rules
- legality remains backend-owned
- acceleration/ranking only within legal candidates
- write-side invalidation required after relevant data changes
- usage signals must not bypass backend legality
- request/response contracts must stay aligned

### Extra required tests
- search contract
- stale response handling
- usage persistence
- invalidation
- backend validation after selection

---

## 6.3 Reports / Acceleration Add-On

Append when task touches reports.

### Extra rules
- report truth stays in backend services
- cache is acceleration only
- source/freshness semantics must be visible where supported
- relevant writes must invalidate report cache

### Extra required tests
- cache hit
- cache miss
- invalidation after write
- HTML/export consistency

---

## 6.4 AI Add-On

Append when task touches AI drafting, ranking, explanation, or suggestion flows.

### Extra rules
- AI is advisory only
- acceptance must be explicit
- no auto-write to accounting truth
- must go through AIAssistPlatform
- add disabled-path behavior
- preserve auditability

### Extra required tests
- success path
- disabled path
- company isolation
- acceptance/dismiss/reject flow where relevant

---

# 7. Minimal Repo Context Block

When giving a task to Codex, always attach this short repo context:

## Repo Context
- `PROJECT_GUIDE.md` is the supreme authority
- `PROJECT_GUIDE_EXEC_SUMMARY.md` is the short execution companion
- `BUG_FIX_PROTOCOL.md` governs bug safety
- This task must follow the Balanciz principles:
  - backend truth
  - company isolation
  - historical honesty
  - cache = acceleration only
  - AI = suggestion only

---

# 8. Copy-Paste Quick Start Templates

## 8.1 Quick Start — Feature
```text
Use PROJECT_GUIDE.md, PROJECT_GUIDE_EXEC_SUMMARY.md, and CODEX_TASK_TEMPLATE.md.

Mode: New Feature / New Module

Task:
[fill in]

Relevant files:
[fill in]

Protected areas:
[fill in]

Use the required output format from CODEX_TASK_TEMPLATE.md.