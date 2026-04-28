# Balanciz Bug Fix Protocol
## Final · Executable Version

---

## ⚠️ Authority

This protocol governs how bugs are analyzed, fixed, reviewed, and merged in Balanciz.

Priority order:

**PROJECT_GUIDE.md > BUG_FIX_PROTOCOL.md > task-specific bug notes > temporary implementation habits**

This document does NOT replace the Project Guide.  
It defines the **safe operating procedure for bug fixing**, especially when touching:

- accounting truth
- FX / multi-currency
- AR/AP
- reconciliation
- company isolation

---

# 1. Purpose

Balanciz is a correctness-first accounting system.

In Balanciz:
> A bad bug fix is more dangerous than the original bug.

This protocol exists to prevent:

- accidental mutation of accounting truth
- silent corruption of FX / multi-currency logic
- breaking company isolation
- UI fixes that drift backend semantics
- opportunistic refactors that destabilize the system

---

# 2. Core Principles

## 2.1 Non-Negotiable

- **Correctness > Convenience**
- **Backend Truth > Frontend Assumptions**
- **Historical Honesty > Cosmetic Neatness**
- **Company Isolation > Everything**
- **Smallest Safe Fix > Broad Cleanup**
- **Proof Before Core Changes**
- **Tests Before Trust**
- **Cache = Acceleration ONLY**
- **AI = Suggestion Layer ONLY**

---

## 2.2 Bug-Fix Interpretation

### Smallest Safe Fix
Fix the bug with the smallest change set that closes the issue safely.

### Proof Before Core Changes
Do NOT modify core logic unless:
- root cause is proven
- failing test exists
- blast radius is understood

### Historical Honesty
Never “clean up” legacy data into false certainty.

If truth is unknown → show unknown.

---

# 3. Bug Classification (MANDATORY)

Every bug must be classified before fixing:

### A. UI-only
Pure visual/interaction issues

### B. UI-contract
UI ↔ backend mismatch

### C. Module-integration
Modules are correct, integration is wrong

### D. Engine-adjacent
Near core logic, but not core truth itself

### E. Core-truth
Affects:
- Posting
- FX
- AR/AP
- Reconciliation
- Company isolation
- Reports

---

# 4. Protected Core Zones

These areas are **protected**:

## 4.1 Accounting Core
- Posting Engine
- Tax Engine
- FX Conversion Engine
- Exchange-rate snapshot validation
- AR/AP settlement
- JE reversal logic
- Reconciliation completion/void

## 4.2 Control & Isolation
- company isolation
- permission enforcement
- active company scoping

## 4.3 Historical Truth
- legacy FX resolver
- snapshot persistence
- read-path semantics

---

# 5. Default Strategy

## 5.1 Always Fix at Highest Safe Layer

Order:

1. UI
2. UI-contract
3. module integration
4. service layer
5. core engine (only if proven)

---

## 5.2 No Opportunistic Refactor

Bug fix must NOT:

- rename unrelated files
- reorganize modules
- change naming conventions
- refactor shared systems
- “clean up” unrelated code

---

# 6. Required Workflow

## Step 1 — Classify Bug
Must label: ui / contract / integration / engine-adjacent / core-truth

---

## Step 2 — Root Cause

Must explicitly state:
- what is wrong
- where
- why
- which layer owns it

---

## Step 3 — Blast Radius

List impact on:

- UI
- handlers
- services
- engines
- persistence
- reports
- company isolation

---

## Step 4 — Freeze Invariants

Must list:

- backend truth
- company isolation
- no-live-provider-at-save
- snapshot immutability
- cache ≠ truth
- AI ≠ execution

---

## Step 5 — Add Failing Test (for critical bugs)

Required for:
- FX
- Posting
- Reversal
- AR/AP
- Reconciliation

---

## Step 6 — Minimal Fix

- smallest change
- no unrelated edits

---

## Step 7 — Verification

Two levels:

### A. Targeted
Bug path works

### B. Core Regression
- posting still correct
- FX still correct
- snapshot still correct
- isolation still correct

---

# 7. FX-Specific Rules

## 7.1 Never Allowed

- calling live provider at save/post
- client deciding base amounts
- recomputing snapshot on reversal
- hiding legacy uncertainty

---

## 7.2 Snapshot Rule

- snapshot must be stable
- must not depend on mutable shared rows
- must survive later rate updates

---

## 7.3 Rounding Rule

Phase 1:

- banker’s rounding
- block save if imbalance

NO auto-rounding allowed yet

---

## 7.4 Legacy FX Rule

- reconstruct if possible
- else mark:
  - `legacy_unavailable`
- do NOT show as base/identity

---

# 8. Special Guardrails for Codex

When using Codex:

## Required prompt constraints

- “This is a surgical bug fix”
- “Do not refactor unrelated code”
- “Do not modify protected core zones unless proven necessary”

---

## Required output from Codex

1. Bug classification  
2. Root cause  
3. Blast radius  
4. Minimal fix plan  
5. Files changed  
6. Tests added  
7. Core touched? (yes/no + why)  
8. Safety explanation  

---

# 9. Completion Gate

A bug fix is NOT complete if:

- core logic changed without proof
- no regression tests exist
- FX snapshot behavior changed silently
- company isolation not re-verified
- UI/backend mismatch remains
- legacy truth is cosmetically rewritten

---

# 10. Golden Rule

> If you are unsure whether a change might affect accounting truth — do not make the change until you prove it is safe.