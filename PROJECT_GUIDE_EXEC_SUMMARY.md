# Gobooks Project Guide v4
## Execution Summary for Claude / Cursor / Codex

This file is the implementation summary of Gobooks Project Guide v4.
Use it as an execution contract when generating code, migrations, UI, APIs, reports, or tests.

If anything conflicts with this summary, defer to the full `PROJECT_GUIDE.md`.

---

# 1. Product Identity

Gobooks is:

- a **strict multi-company accounting system**
- a **correctness-first accounting engine**
- a **control-oriented financial platform**
- an **AI suggestion layer**, not an AI execution layer
- a **modular, engine-centric architecture**

Do not treat Gobooks as a loose CRUD app.

---

# 2. Absolute Non-Negotiables

Always obey:

- **Correctness > Flexibility**
- **Backend Authority > Frontend**
- **Structure > Convenience**
- **Auditability > Shortcuts**
- **Company Isolation > Everything**
- **AI = Suggestion Layer ONLY**

Never let frontend, temporary UI state, or AI become accounting truth.

---

# 3. Multi-Company Rules

Required:

- every core business/accounting object must have `company_id NOT NULL`
- session must contain `active_company_id`
- all reads/writes/reports/exports must be company-scoped

Must validate on writes:

- document.company_id == active_company_id
- account.company_id == active_company_id
- tax.company_id == active_company_id
- customer/vendor.company_id == active_company_id
- JE.company_id == source.company_id

Forbidden:

- cross-company JE
- cross-company ledger
- shared COA
- shared customers/vendors/tax objects
- cross-company document/account references

---

# 4. Architecture Rules

Business App:
- accounting
- reporting
- invoices
- bills
- customers/vendors
- reconciliation
- settings

SysAdmin:
- separate auth
- company lifecycle
- user management
- maintenance mode
- system-level observability

Core truth belongs to engines.  
Business workflows belong to modules.  
AI belongs to the suggestion layer.

---

# 5. Posting Engine Rules

All formal accounting must go through:

**Document → Validation → Tax → Posting Fragments → Aggregation → Journal Entry → Ledger Entries**

Never:

- bypass Posting Engine
- write ledger directly
- create formal JE without source
- allow source change without JE synchronization

JE must include:

- company_id
- status
- source_type
- source_id
- totals
- posting metadata

JE status:
- draft
- posted
- voided
- reversed

Posting must be transactional and atomic.

---

# 6. Identity and Numbering

Entity identity:
- `ENYYYY########`
- globally unique
- immutable
- backend-generated

Display number:
- configurable
- human-facing
- duplicate-detectable
- not identity truth

Never confuse display number with entity identity.

---

# 7. Chart of Accounts Rules

Root types are fixed:

- asset
- liability
- equity
- revenue
- cost_of_sales
- expense

Code direction:

- 1xxxx asset
- 2xxxx liability
- 3xxxx equity
- 4xxxx revenue
- 5xxxx cost_of_sales
- 6xxxx expense

Historical accounting accounts:
- do not hard-delete
- use inactive

System default COA template is allowed and encouraged.

---

# 8. Tax Rules

Tax truth must be:

**line-level calculation → account-level aggregation**

Sales:
- revenue → revenue
- tax → tax payable

Purchases:
- recoverable → tax receivable
- partially recoverable → split
- non-recoverable → expense/inventory absorption

Never let UI invent tax truth.

---

# 9. Journal Entry Rules

JE must be:

- source-linked
- company-consistent
- lifecycle-synchronized
- reviewable
- traceable

Prefer account-level aggregation for formal JE presentation.

Never allow:
- JE without source
- source mutated but JE unchanged
- posted truth hard-deleted

---

# 10. Navigation Rules

Official sidebar structure:

## Core
- Dashboard
- Journal Entry
- Invoices
- Bills

## Sales & Get Paid
- Customers
- Receive Payment

## Expense & Bills
- Vendors
- Pay Bills

## Accounting
- Chart of Accounts
- Reconciliation
- Reports

## Settings
- structured internal settings domains

Forbidden:
- top-level Contacts
- top-level Banking
- moving Reports elsewhere

---

# 11. Settings and Security Rules

Company settings direction:

- Profile
- Templates
- Sales Tax
- Numbering
- Notifications
- Security

User menu:
- Profile
- Log out

Email/password changes:
- verification required

Verification codes:
- 6 chars
- case-insensitive
- single-use
- time-limited
- backend-validated

SMTP:
- must be verified before verification sending is allowed

---

# 12. Reconciliation Rules

Reconciliation is an **accounting control layer**.

Status direction:
- draft
- in_progress
- completed
- reopened
- cancelled

Must support:
- one-to-one
- one-to-many
- many-to-one
- split

Can only complete when:
- difference == 0

Void:
- only latest completed reconciliation
- never delete history
- preserve void metadata

AI auto-match:
- Rules → Scoring → AI Enhancement
- explainable suggestions only
- user accept/reject required

---

# 13. AI Rules

AI is an advisor, not an executor.

Allowed:
- suggestions
- ranking
- explanation
- anomaly hints
- report interpretation
- tax/account hints

Forbidden:
- AI posting
- AI changing books
- AI finalizing reconciliation
- AI bypassing validation

Long-term direction:
- AI CFO / external accountant style support

---

# 14. Reporting Rules

Reports are product outputs, not temporary pages.

Must preserve:
- backend-generated truth
- HTML / print / CSV consistency
- business-status consistency
- stable ordering
- nil safety

A/R Aging direction:
- summary/detail consistency
- product-grade export
- customer finance visibility support

Templates render only.  
They must not create accounting truth.

---

# 15. Current Product State and Planning Context

Already substantially advanced:
- Task + Billable Expense core loop
- Customer Workspace
- Payment visibility
- formal AR Aging
- AR Aging detail rows

Task module status:
- main flow is basically complete
- future Task / Quote overlap must be reconsidered later

Next planning priority:
- continue strengthening Invoice / AR mainline
- improve invoice template/sending/product-service-tax linkage
- continue report/export consistency
- begin long-term UI theme work including low-glare dark mode

---

# 16. UI / UX Direction

Gobooks should feel:

- clean
- stable
- business-first
- professional
- restrained

Dashboard:
- operational overview, not complex BI

Tables/forms/reports:
- must support long-duration work

Dark mode direction:
- not simple inversion
- low-glare
- soft dark surfaces
- readable tables/reports/forms
- suitable for accounting workflows

---

# 17. Implementation Order

Default implementation order:

**Data model → Validation → Engine/service → Handler/API → View model → UI → Tests**

Do not start from UI-first assumptions.

---

# 18. Required Review Checklist

Before finalizing any change, check:

1. company isolation preserved
2. posting path preserved
3. backend remains source of truth
4. auditability preserved
5. UI did not become truth source
6. no unrelated module pollution
7. export / HTML / CSV stay consistent
8. ordering and nil-safety are covered
9. AI did not cross into execution
10. tests cover actual business truth, not only happy-path text

---

# 19. Final Instruction to Coding Agents

When implementing Gobooks:

- think like a product engineer, not a page builder
- protect accounting truth first
- protect company isolation first
- keep source/business/accounting/reporting logic aligned
- prefer minimal necessary changes
- avoid scope creep
- keep the system engine-centric and modular