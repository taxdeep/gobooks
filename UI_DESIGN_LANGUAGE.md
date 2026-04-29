# Balanciz UI Design Language

## Purpose

This document is the practical UI contract for Balanciz page work. It turns the existing visual direction into rules we can apply one page at a time, so screens stop drifting in layout, density, interaction patterns, and component styling.

Related document: `UI_SYSTEM_PLAN.md` covers token/component unification at a technical level. This file covers product-facing page language and page migration decisions.

## Current State Summary

Balanciz already has a usable token foundation in Tailwind and template helpers:

- Semantic colors such as `bg-background`, `bg-surface`, `border-border`, `border-border-input`, `text-text`, `text-text-muted2`, `text-primary`, and `bg-primary`.
- Semantic type sizes such as `text-title`, `text-section`, `text-body`, and `text-small`.
- Shared UI templates in `internal/web/templates/ui`, including buttons, cards, drawers, document editor shell, header grids, and line item tables.
- Admin-specific helper classes in `internal/web/templates/admintmpl/helpers.go`.

The main inconsistency is at the page/template layer:

- Many pages still inline long Tailwind class strings for buttons, tables, filters, forms, and action bars.
- Detail pages mix read-only display, inline editing, tabbed editing, and drawer editing.
- Some pages use dense operational layouts, while others feel like marketing or generic card layouts.
- Transaction pages vary in how totals, line items, memo, attachments, actions, and sticky footers are arranged.
- Admin pages have their own visual language, which is acceptable, but should still share density, control hierarchy, and feedback behavior.

## Product Tone

Balanciz is an operational accounting application. The UI should feel calm, dense, predictable, and fast to scan.

Prefer:

- Compact information hierarchy.
- Clear labels and values.
- Tables and two-column detail grids for accounting data.
- Drawers for editing records from read-only profile/detail pages.
- Sticky or clearly anchored action bars for long forms.
- Bordered, quiet surfaces over decorative layouts.

Avoid:

- Landing-page composition inside the app.
- Large decorative cards where a dense table or details grid is better.
- Nested cards.
- One-off button classes.
- Oversized headings inside cards or panels.
- Hidden critical actions inside ambiguous text links.

## Page Archetypes

### List Pages

Use list pages for scanning, filtering, and opening records.

Rules:

- Header: title, short subtitle only when it adds operational context, primary action on the right.
- Filters: compact, one row on desktop when possible, wrapping cleanly on mobile.
- Table: dense rows, clear sort/filter affordances, right-aligned numeric columns.
- Row actions: use consistent action buttons or a compact action menu, not mixed text/link styles.
- Empty state: one sentence plus a direct creation action when relevant.

### Detail Pages

Use detail pages for reading and acting on a single record.

Rules:

- Default state is read-only.
- Put identity and status in the header, with primary lifecycle actions grouped consistently.
- Use dense label/value grids for contact, address, terms, currency, status, and totals.
- Edit opens a right drawer unless the page is a full document editor.
- Avoid full-page edit forms for profile-style records.

### Create/Edit Document Pages

Use full-page editors for invoices, bills, expenses, orders, quotes, payments, and inventory documents.

Rules:

- Keep the first viewport useful: key counterparties, dates, currency, and total should be visible without scrolling.
- Line items are the center of the page, not a card buried below metadata.
- Totals are right-aligned and visually stable.
- Memo and attachments belong below line items unless the workflow needs them earlier.
- Save/post/issue actions should live in a predictable footer or header action area.
- Currency should react to selected customer/vendor defaults when applicable.

#### Document Editor Footer Actions

适用于 New Invoice、New Bill、New Expense、New Purchase Order、New Sales Order、New Quote 等单据创建/编辑页面。

Rules:

- 操作按钮统一放在页面底部的 action bar。
- `Cancel` 放在左侧。
- `Save`、`Save Draft`、`Submit`、`Issue`、`Post`、`Approve` 等推进流程的按钮放在右侧。
- 右侧按钮从左到右按流程强度排列：先保存草稿，再提交/发出/过账。
- 最主要的推进动作使用 primary button，并放在最右侧。
- 危险动作如 `Delete`、`Void` 不混入常规保存区；这类动作更适合放在详情页 lifecycle action 区域。
- 移动端可以上下换行，但语义位置保持：`Cancel` 先出现，主操作最后出现。

### Settings Pages

Use settings pages for configuration, not storytelling.

Rules:

- Group by task, not by implementation detail.
- Use section cards only when a group needs a boundary.
- Save buttons are local to the setting group if groups can be saved independently.
- Explain side effects in small helper text close to the control.

### SysAdmin Pages

SysAdmin can keep a slightly distinct "system mode" shell, but should follow the same density and control hierarchy.

Rules:

- Dangerous actions require confirm text and audit logging.
- System state should be visible before controls.
- Operational tools should show capability state, last run/result, and clear error messages.
- Do not hide unsupported server capabilities; show disabled controls with a reason.

## Interaction Patterns

### Read-Only First, Drawer Edit

Profile/detail pages should render read-only by default. Editing opens a right-side drawer.

Applies to:

- Customer profile.
- Vendor profile.
- Company/account profile-style settings.
- Chart/account detail when editing a compact record.

Rules:

- The drawer edits the same record context without navigating away.
- The drawer can switch modes internally when the user selects a nearby action, such as "Add shipping address".
- Do not open a separate page for small profile edits unless the workflow is complex enough to need a full editor.
- The read-only page should update its layout to remain useful even without edit mode.

### Split Button Actions

Use split buttons when there is one primary action and a small set of related alternatives.

Example:

- `Edit` primary action.
- Dropdown option: `Add shipping address`.

Rules:

- Left side runs the default action.
- Right side opens a small menu.
- Menu options switch the same drawer into the selected form when possible.

### Drawer Rules

Drawers are for contextual editing.

Rules:

- Open from the right.
- Width should be stable and responsive.
- Header contains title and close button.
- Footer contains cancel/save actions.
- First editable field should receive focus when practical.
- Escape closes only when there are no unsaved changes, or after confirm.
- The underlying page remains the source of context.

### Feedback Rules

User-facing errors must say what failed and what to fix next.

Rules:

- Avoid generic messages like "Could not issue invoice" when the backend knows the reason.
- Error messages should be specific, actionable, and written for the user, not for the developer.
- When validation fails, tell the user which field or business rule failed and how to correct it.
- When a workflow action fails, include the record/action context when possible, such as invoice issue, bill post, payment allocation, PDF preview, or email send.
- If multiple issues block an action, show the most important issue first and preserve enough detail for the user to fix the rest without guessing.
- If the failure comes from a system dependency, name the dependency in plain language, such as PDF renderer, database backup tool, email server, payment gateway, or exchange rate lookup.
- Do not expose secrets, raw SQL, stack traces, access tokens, or internal file paths in user-facing errors.
- Log technical details server-side, and show a concise user-facing explanation with a next step.
- Show validation errors near the field when possible.
- Show top-level flash errors for cross-record or system failures.
- Destructive or irreversible actions require confirmation.
- Long-running admin tasks should show started/running/last result states.

## Component Rules

### Buttons

Use shared button helpers/components where available.

Hierarchy:

- Primary: one main forward action per page or section.
- Secondary: navigation, cancel, preview, download, or safe utility actions.
- Danger: delete, void, deactivate, destructive maintenance.
- Link-style action: low-risk table actions only.

Rules:

- Do not create new one-off button class strings unless there is no shared helper yet.
- Buttons must have stable height and readable labels on mobile.
- Icon-only buttons need a tooltip or accessible label.

### Cards and Sections

Use cards for bounded tools, repeated items, or true panels. Do not use cards as page decoration.

Rules:

- No cards inside cards.
- Section cards use restrained radius and border.
- Detail pages may use unframed grids when the information is naturally dense.
- Full-width bands or table sections are preferred for high-density accounting data.

### Forms

Rules:

- Labels use `text-small` or `text-body` consistently.
- Inputs must use tokenized background, text, and border colors.
- Related fields align in grids.
- Read-only fields should look read-only, not disabled unless they truly cannot be focused/copied.
- Required indicators should be consistent and not noisy.

### Tables

Rules:

- Numeric values align right.
- Amounts use tabular numerals when available.
- Header labels are compact and consistent.
- Row hover should be subtle.
- Empty rows should not stretch the layout awkwardly.
- Actions should live in the far-right column or a predictable action area.

### Totals and Financial Summaries

Rules:

- Totals belong near the action area or line items they summarize.
- Subtotal, tax, and total order must be consistent.
- Currency labels must be explicit when multi-currency is possible.
- The final total gets stronger visual weight than intermediate rows.

## Visual Constraints

These are hard rules for future page work:

- Use semantic tokens instead of raw color values.
- Use `text-title`, `text-section`, `text-body`, and `text-small` instead of raw text sizes.
- Keep letter spacing at normal unless matching an existing table-label convention.
- Do not use decorative gradient/orb backgrounds.
- Do not use oversized hero layouts inside the app.
- Do not allow text to overflow buttons, cards, tabs, or table cells.
- Use responsive constraints for fixed-format UI such as grids, toolbars, totals, and line item rows.
- Check both desktop and mobile when a page layout changes.

## Migration Workflow

Each page migration should follow this sequence:

1. Classify the page archetype.
2. Identify the primary user job on the page.
3. Preserve working business behavior before touching layout.
4. Replace one-off controls with shared helpers/components where practical.
5. Normalize page header, action hierarchy, tables, forms, and feedback.
6. Verify desktop and mobile layout.
7. Add or update tests if behavior changes.
8. Record any new reusable pattern in this document.

## First Migration Candidates

Recommended order:

1. Customer profile.
2. Vendor profile.
3. New Expense.
4. Invoice detail and invoice editor.
5. Sales Order and Purchase Order editors.
6. Quote editor.
7. Settings hub and company setup pages.
8. SysAdmin system pages.
9. Reports and dashboard pages.

## Page Review Checklist

Before a migrated page is considered done:

- The page has one clear primary action.
- Read-only vs edit state is obvious.
- Controls use shared helpers or documented local variants.
- Tables and amount columns align consistently.
- Empty, loading, success, warning, and error states are handled.
- Mobile layout does not overlap or hide critical actions.
- The page does not introduce a new visual pattern without documenting it.
- The implementation does not regress dark mode.

## Open Decisions

These should be resolved as we migrate the first few pages:

- Final shared split-button component API.
- Final drawer component API for mode switching inside the same drawer.
- Standard compact detail grid component.
- Standard transaction editor shell for expenses, invoices, bills, orders, and quotes.
- Standard attachment block.
- Standard top-level error summary component.
