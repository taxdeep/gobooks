# Balanciz UI Unification Plan

## Authority

This document governs all UI/CSS/template work in Balanciz.  
Priority: **Project Guide v5 > UI System Plan > page-specific notes**

---

## 0. Executive Summary

The design token system in `input.css` and `tailwind.config.js` is already well-structured. The dark mode palette is good. **The failure is in the template layer**: every page re-declares its own control class strings instead of using the shared `ui/` components. The result is drift — especially on `bg-surface` in inputs, `text-text` on selects, and error-state duplication.

The unification work is primarily:
1. **Extend shared components** so every control variant has a home
2. **Eliminate inline class duplication** in all pages
3. **Fix the missing `bg-surface` / `text-text`** on date and select controls
4. **Standardize page-level layout patterns** via documented shell rules
5. **Add missing tokens** where the system has gaps

---

## 1. Design Token Audit

### 1.1 Existing Tokens (Keep as-is)

The following are correct and must not be changed:

| Token | Purpose |
|---|---|
| `bg-background` | Page wash — deepest layer |
| `bg-surface` | Cards, panels, sidebar, header, inputs |
| `border-border` | Card/divider borders |
| `border-border-input` | Input/select control borders |
| `text-text` | Primary reading text |
| `text-text-muted2` | Secondary labels, subtitles |
| `text-text-muted3` | Icons, nav items |
| `text-primary` | Interactive links, active states |
| `bg-primary` | Primary action buttons |
| `bg-danger` | Destructive action buttons |

### 1.2 Missing Tokens — Add to `input.css` and `tailwind.config.js`

```css
/* Add to :root and .dark sections */

/* Table-specific surface: slightly different from card surface */
--gb-surface-table-header:  /* light: gray-50 = 249 250 251 | dark: 33 44 62 */
--gb-surface-table-row-alt: /* light: white   = 255 255 255  | dark: same as surface */
--gb-surface-row-hover:     /* light: 239 246 255            | dark: 23 45 88 (primary-soft) */
--gb-surface-summary-row:   /* light: 243 244 246 (gray-100) | dark: 33 44 62 */

/* Input read-only / locked state */
--gb-surface-readonly:      /* light: 249 250 251 | dark: 21 27 39 (background) */
--gb-text-readonly:         /* light: 75 85 99    | dark: 100 114 133 */

/* Badge / status pill backgrounds */
--gb-badge-neutral-bg:      /* light: 243 244 246 | dark: 42 55 74 */
--gb-badge-neutral-text:    /* light: 55 65 81    | dark: 181 194 210 */
```

Add corresponding Tailwind tokens:
```js
// In tailwind.config.js theme.extend.colors:
surface: {
  DEFAULT:      'rgb(var(--gb-surface) / <alpha-value>)',
  tableHeader:  'rgb(var(--gb-surface-table-header) / <alpha-value>)',
  tableRowAlt:  'rgb(var(--gb-surface-table-row-alt) / <alpha-value>)',
  rowHover:     'rgb(var(--gb-surface-row-hover) / <alpha-value>)',
  summaryRow:   'rgb(var(--gb-surface-summary-row) / <alpha-value>)',
  readonly:     'rgb(var(--gb-surface-readonly) / <alpha-value>)',
},
text: {
  // existing...
  readonly: 'rgb(var(--gb-text-readonly) / <alpha-value>)',
},
badge: {
  bg:   'rgb(var(--gb-badge-neutral-bg) / <alpha-value>)',
  text: 'rgb(var(--gb-badge-neutral-text) / <alpha-value>)',
},
```

### 1.3 Typography Scale (Keep, Document Precisely)

| Class | Size | Weight | Usage |
|---|---|---|---|
| `text-title` | 24px / 2rem | `font-semibold` | Page H1 only |
| `text-section` | 16px / 1rem | `font-semibold` | Card/section H2 |
| `text-body` | 14px / 0.875rem | varies | All labels, values, controls |
| `text-small` | 12px / 0.75rem | varies | Captions, hints, badges, table secondary |

**Rule: Never use Tailwind's raw `text-sm`, `text-base`, `text-lg` etc. in templates. Always use the semantic scale above.**

---

## 2. Spacing System

All form spacing follows this ladder. No arbitrary values.

| Context | Token |
|---|---|
| Label → input gap | `mt-1.5` (6px) — **standardize from current mix of mt-1/mt-2** |
| Field → field gap in grid | `gap-5` (20px) — **standardize from current mix of gap-4/gap-6** |
| Section card internal padding | `p-5` (20px) |
| Section card top margin | `mt-5` |
| Page title → first card | `mt-5` |
| Footer action bar top border | `pt-4 mt-2 border-t border-border` |

**Current problem:** pages use `mt-1`, `mt-2`, `gap-4`, `gap-6`, `p-4`, `p-6` inconsistently. All template files must be normalized to the above ladder.

---

## 3. Dark Mode Rules (Enforcement)

### 3.1 Required on every input control

Every `<input>`, `<select>`, `<textarea>` must carry:
- `bg-surface` — prevents browser-default white background in dark mode
- `text-text` — prevents browser-default black text in dark mode  
- `border-border-input` — uses design token, not ad-hoc border
- `placeholder:text-text-muted` — consistent placeholder contrast
- `outline-none focus:ring-2 focus:ring-primary-focus` — unified focus ring

**Current violation:** date inputs in `pay_bills.templ`, `receive_payment.templ`, and `report_toolbar.templ` are missing `bg-surface text-text`. This causes white boxes in dark mode.

### 3.2 Date inputs require `color-scheme` override

All `<input type="date">` must add:
```html
style="color-scheme: dark"
```
This forces the browser's native date picker chrome (calendar icon, popup) to use dark styling. Without it, the popup calendar remains white in dark mode regardless of CSS.

### 3.3 Select controls

All `<select>` must add `appearance-none` and a custom chevron SVG background, **or** use a standard wrapper pattern. Currently selects show inconsistent OS-native styling. Until a custom wrapper is built, at minimum ensure:
- `bg-surface text-text border-border-input` are always present
- Native styling inconsistency is a known Phase 2 item

### 3.4 Read-only display values

When a field is informational/locked (e.g. the base currency display in Vendors):
```html
class="... bg-surface-readonly text-text-readonly border-border-subtle"
```
Not `bg-background` (wrong layer) and not `bg-surface` (looks editable).

### 3.5 Forbidden in dark mode

- `bg-white` anywhere in forms — use `bg-surface`
- `text-gray-*` raw Tailwind values — use token equivalents
- Hard-coded hex colors in `style=""` — use CSS variables
- `bg-gray-50`, `bg-gray-100`, `bg-gray-200` — use `bg-background` or `bg-surface`

---

## 4. Shared Component Specification

### 4.1 Current components (extend, do not rebuild)

These exist in `internal/web/templates/ui/` and are the correct direction. They need extensions:

#### `ui/input.templ` — extend with:

```go
// FormField: standard labeled text/number/email/date input.
// Replaces all the inline label+input+err patterns throughout pages.
templ FormField(vm FormFieldVM)

// SelectField: labeled <select> with error state.
templ SelectField(vm SelectFieldVM)

// TextareaField: labeled <textarea> with error state.
templ TextareaField(vm TextareaFieldVM)

// ReadonlyField: display-only labeled value (locked, not editable).
templ ReadonlyField(label string, value string, hint string)

// CurrencyAmountField: number input with currency prefix badge.
templ CurrencyAmountField(vm CurrencyAmountFieldVM)
```

**FormFieldVM struct:**
```go
type FormFieldVM struct {
    Label       string
    Name        string
    Value       string
    Placeholder string
    Type        string  // "text", "email", "date", "number", "tel"
    Required    bool
    Error       string
    Hint        string  // optional caption below input
    Disabled    bool
    AutoFocus   bool
    InputMode   string  // "decimal", "numeric", etc.
    MaxLength   int
}
```

**SelectFieldVM struct:**
```go
type SelectFieldVM struct {
    Label    string
    Name     string
    Options  []SelectOption
    Selected string
    Required bool
    Error    string
    Hint     string
    Disabled bool
    // AlpineModel: if non-empty, adds x-model="..." to the select
    AlpineModel  string
    AlpineChange string
}
type SelectOption struct {
    Value string
    Label string
}
```

#### Standard class strings for all controls (use exactly these)

```
Normal state:
  mt-1.5 block w-full rounded-md border border-border-input bg-surface px-3 py-2
  text-body text-text placeholder:text-text-muted outline-none
  focus:ring-2 focus:ring-primary-focus

Error state (replace border-border-input with):
  border-danger focus:ring-danger-focus

Date input adds: style="color-scheme:dark"

Select adds: appearance-none (until custom chevron component is ready)

Disabled state adds: disabled:bg-surface-readonly disabled:text-text-readonly
  disabled:cursor-not-allowed
```

#### `ui/button.templ` — extend with:

```go
// ButtonToolbar: ghost/outline button for toolbar actions (Print, Export, etc.)
templ ButtonToolbar(label string, typeAttr string, disabled bool)

// ButtonLink: styled as secondary button but renders as <a>
templ ButtonLink(label string, href string)

// ButtonIconText: button with leading SVG icon slot + text label
// Used for: "← Back", "＋ Add", "↓ Export" patterns
templ ButtonIconText(label string, typeAttr string, variant string)
```

**Class standards:**

```
Primary:   rounded-md bg-primary px-4 py-2 text-body font-semibold text-onPrimary
           hover:bg-primary-hover disabled:bg-disabled-bg disabled:text-disabled-text
           disabled:cursor-not-allowed

Secondary: rounded-md border border-border-input bg-surface px-4 py-2 text-body
           font-semibold text-text hover:bg-background
           disabled:bg-disabled-bg disabled:text-disabled-text disabled:cursor-not-allowed

Danger:    rounded-md bg-danger px-4 py-2 text-body font-semibold text-onPrimary
           hover:bg-danger-hover disabled:bg-disabled-bg disabled:text-disabled-text
           disabled:cursor-not-allowed

Toolbar:   rounded-md border border-border px-3 py-2 text-body font-medium text-text
           hover:bg-background
```

#### `ui/card.templ` — extend with:

```go
// SectionCard: card with title + optional subtitle + content slot.
// The standard wrapper for all major form sections.
templ SectionCard(title string, subtitle string, content templ.Component)

// PageShell: page title + subtitle + optional back link + content.
// Standardizes the top area of every page.
templ PageShell(vm PageShellVM, content templ.Component)
```

**SectionCard renders as:**
```html
<div class="rounded-lg border border-border bg-surface p-5 shadow-sm">
  <h2 class="text-section font-semibold text-text">{ title }</h2>
  if subtitle != "" {
    <p class="mt-1 text-small text-text-muted2">{ subtitle }</p>
  }
  <div class="mt-4">
    @content
  </div>
</div>
```

**PageShell renders as:**
```html
<div class="max-w-[95%] space-y-5">
  <div class="flex flex-wrap items-start justify-between gap-4">
    <div>
      <h1 class="text-title font-semibold text-text">{ vm.Title }</h1>
      if vm.Subtitle != "" {
        <p class="mt-1 text-small text-text-muted2">{ vm.Subtitle }</p>
      }
    </div>
    if vm.BackHref != "" {
      <a href="{ vm.BackHref }" class="text-body text-text-muted3 hover:text-text">
        ← { vm.BackLabel }
      </a>
    }
  </div>
  @content
</div>
```

#### New: `ui/alert.templ`

Currently every page inlines its own success/error alert divs. Centralize:

```go
templ AlertSuccess(message string)
templ AlertError(message string)
templ AlertWarning(message string)
templ AlertInfo(message string)
```

Standard class for success alert:
```
rounded-md border border-success-border bg-success-soft p-4 text-body text-success
```

Standard class for error alert:
```
rounded-md border border-danger-border bg-danger-soft p-4 text-body text-danger
```

#### New: `ui/badge.templ`

For status pills, freshness labels, source labels:

```go
templ Badge(label string, variant string)
// variant: "neutral" | "success" | "warning" | "danger" | "info"
```

```
neutral: rounded-full bg-badge-bg px-2 py-0.5 text-small font-medium text-badge-text
success: rounded-full bg-success-soft px-2 py-0.5 text-small font-medium text-success
warning: rounded-full bg-warning-soft px-2 py-0.5 text-small font-medium text-warning
danger:  rounded-full bg-danger-soft  px-2 py-0.5 text-small font-medium text-danger
```

#### `ui/table.templ` — extend with:

Inspect and standardize:

```go
// TableSummaryRow: right-aligned label+value pair for totals footer
templ TableSummaryRow(label string, value string, variant string)
// variant: "" (normal) | "total" (bold, heavier border) | "highlight" (primary color)

// TableEmptyRow: full-width "no records" row
templ TableEmptyRow(message string, colSpan int)

// TableNumericCell: right-aligned numeric value
templ TableNumericCell(value string, muted bool)
```

**Table class standards:**
```
Table wrapper:    w-full text-body
Header row:       bg-surface-tableHeader text-text-muted2 text-small font-medium
                  border-b border-border
Header cell:      px-4 py-2.5 text-left (or text-right for numeric)
Body row:         border-b border-border-subtle hover:bg-surface-rowHover
                  transition-colors duration-75
Body cell:        px-4 py-2.5 text-text
Numeric cell:     px-4 py-2.5 text-right font-mono text-text tabular-nums
Summary row:      bg-surface-summaryRow border-t border-border
Total row:        bg-surface-summaryRow border-t-2 border-border font-semibold
```

---

## 5. Form Architecture Rules

Every form page must follow this exact structure. No exceptions.

### 5.1 Page shell
```
PageShell(title, subtitle, backLink?)
  ↓
AlertSuccess / AlertError (conditional, from vm)
  ↓
<form> with space-y-5
  ↓
  SectionCard(s)
  ↓
  Footer action bar
```

### 5.2 Section card internal grid

Standard grid patterns (pick the most natural for the content):

```html
<!-- 4-col metadata bar (dates, IDs, status) -->
<div class="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-4">

<!-- 3-col standard form -->
<div class="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">

<!-- 2-col balanced form -->
<div class="grid grid-cols-1 gap-5 sm:grid-cols-2">

<!-- Full-width single field -->
<div class="grid grid-cols-1 gap-5">
```

**Never mix `md:` and `lg:` breakpoints for the same form grid. Choose one.** For the primary form content grid, prefer `sm:` + `lg:` (skips the awkward medium tablet range).

### 5.3 Footer action bar

Every form must have a footer bar (not just a floating button):
```html
<div class="flex items-center justify-between border-t border-border pt-4">
  <!-- Left: cancel/back -->
  <a href="..." class="...ButtonSecondary...">Cancel</a>
  <!-- Right: primary + optional secondary -->
  <div class="flex items-center gap-3">
    @ui.ButtonSecondary("Save as Draft", "submit", false)
    @ui.ButtonPrimary("Post / Save", "submit", false)
  </div>
</div>
```

For simple single-action forms (like Receive Payment), one centered or right-aligned primary button is acceptable.

### 5.4 Numeric input formatting

- Use `inputmode="decimal"` on all decimal amount fields
- Use `font-mono` class on all amount display values (read-only or inside tables)
- Align amount columns right: `text-right tabular-nums`
- Do not use `type="number"` for currency amounts — use `type="text"` with `inputmode="decimal"`

---

## 6. Table Architecture Rules

### 6.1 Reporting tables

```
<div class="rounded-lg border border-border bg-surface overflow-hidden shadow-sm">
  <table class="w-full text-body">
    <thead>
      <tr class="bg-surface-tableHeader border-b border-border">
        <th class="px-4 py-2.5 text-left text-small font-semibold text-text-muted2">
        <!-- numeric header: text-right -->
      </tr>
    </thead>
    <tbody class="divide-y divide-border-subtle">
      <tr class="hover:bg-surface-rowHover transition-colors duration-75">
        <td class="px-4 py-2.5 text-text">
        <!-- numeric: text-right font-mono tabular-nums -->
      </tr>
    </tbody>
    <!-- Section subtotals -->
    <tr class="bg-surface-summaryRow border-t border-border">
      <td class="px-4 py-2.5 font-semibold text-text" colspan="...">Subtotal</td>
      <td class="px-4 py-2.5 text-right font-mono font-semibold tabular-nums">0.00</td>
    </tr>
    <!-- Grand total -->
    <tr class="bg-surface-summaryRow border-t-2 border-border">
      <td class="px-4 py-2.5 font-bold text-text">Net Income</td>
      <td class="px-4 py-2.5 text-right font-mono font-bold tabular-nums text-primary">0.00</td>
    </tr>
  </table>
</div>
```

### 6.2 Payment selection tables (Pay Bills)

Same wrapper, but body rows contain form controls. Rules:
- Row checkbox: `w-4 h-4 accent-primary`
- Amount input inside row: `w-28 rounded border border-border-input bg-surface px-2 py-1 text-body text-right font-mono text-text outline-none focus:ring-1 focus:ring-primary-focus`
- Do not use the full-size control class inside table cells — use a compact variant

### 6.3 Row density

Standard row height: `py-2.5` on cells (approx 40px per row).  
Compact mode (for inline-data tables): `py-1.5` (approx 32px per row).  
Never mix densities within the same table.

### 6.4 Footer totals bar

A totals bar below a selection table is **not** part of `<table>`. It is a separate summary card:

```html
<div class="flex items-center justify-between rounded-b-lg border border-t-0 border-border bg-surface-summaryRow px-5 py-3">
  <span class="text-body text-text-muted2">{ N } bill(s) selected</span>
  <div class="flex items-center gap-6">
    <div class="text-right">
      <div class="text-small text-text-muted2">Total Amount</div>
      <div class="text-body font-mono font-semibold tabular-nums text-text">1,234.56</div>
    </div>
    @ui.ButtonPrimary("Record Payment", "submit", false)
  </div>
</div>
```

---

## 7. Page-Specific Redesign Guidance

### 7.A Receive Payment

**Current problems (code-confirmed):**
- Grid jumps to `lg:grid-cols-4` immediately — looks empty at medium widths
- `<select>` and `<input type="date">` are missing `bg-surface text-text` (white-box bug)
- The invoice allocation table (if present) is visually disconnected from the payment header section
- The if/else duplication for error states is ~120 lines for 4 fields

**Redesign rules:**

Structure:
```
PageShell("Receive Payment", "Record a customer payment against an open invoice.")
  AlertSuccess / AlertError
  <form>
    SectionCard("Payment Details")         ← 4 fields: Customer / Method / Date / Ref#
    SectionCard("Invoice Allocation", subtitle="Select invoices to apply this payment to.")
      ← allocation table (conditionally shown when customer selected)
    Footer action bar (right-aligned: Cancel + Record Payment)
```

Grid inside Payment Details: `sm:grid-cols-2 lg:grid-cols-4`

Missing fix — all four inputs need `bg-surface text-text`:
```
date input: add bg-surface text-text style="color-scheme:dark"
select:     add bg-surface text-text appearance-none
```

The error state duplication must be eliminated by using `FormField` and `SelectField` components.

### 7.B New Expense / Edit Expense

**Current problems (code-confirmed):**
- Uses `taskInputClass()` helper (good) but the helper may produce inconsistent results
- `style="color-scheme:dark"` is present on the date field (good) — keep it
- Layout is `md:grid-cols-3` for main fields — acceptable but verify breakpoints
- Payment section exists and is correct conceptually — preserve it
- SmartPicker is used correctly — keep it

**Redesign rules:**

Structure:
```
PageShell("New Expense" / "Edit Expense", subtitle, backLink="← Back to Expenses")
  AlertError
  <form>
    SectionCard("Expense Details")        ← Vendor / Date / Amount / Description / Category
    SectionCard("Payment")                ← Payment Method / Paid From account / paid vs bill toggle
    SectionCard("Customer Task Link")     ← optional, show only if company has tasks
    Footer action bar: Cancel + Save
```

`taskInputClass()` should be removed and replaced with `FormField` component usage so the class logic is centralized in one place.

### 7.C Vendors

**Current problems (code-confirmed):**
- Add Vendor form uses `md:grid-cols-2` with 6+ fields — this is appropriate, keep it
- Email and Phone fields are missing `bg-surface` — white-box bug in dark mode
- The `<textarea>` for address is missing `bg-surface text-text`
- The currency locked-state div uses `bg-background` which is the correct approach but needs `text-text-readonly` instead of `text-text-muted2` (subtle distinction — locked feels different from optional)
- The vendor list below the add form: visually needs `mt-6` separation with a clear `SectionCard` wrapper

**Redesign rules:**

Fix immediately (dark mode correctness):
```
email input:    add bg-surface
phone input:    add bg-surface
textarea:       add bg-surface text-text
```

Layout:
```
PageShell("Vendors", "Manage vendors for accounts payable and expense tracking.")
  AlertCreated / AlertError
  SectionCard("Add Vendor")
    grid sm:grid-cols-2 — Name / Payment Term / Email / Phone / Currency / Address(full-width)
    Footer inside card: right-aligned "Add Vendor" primary button
  SectionCard("Vendor List")  ← with search/filter row at top
    table
```

### 7.D Pay Bills

**Current problems (code-confirmed):**
- Payment settings section: `<input type="date">` missing `bg-surface text-text`
- AP Account field (`ap_account_id`) in a 4-col settings bar is confusing — users don't know why they're selecting it
- The bills table inline amount inputs (if present) need compact styling
- The totals/summary area is not visually anchored

**Redesign rules:**

Relabel the AP Account field: **"Accounts Payable Account"** with a helper text: *"The A/P control account to credit. Normally your default A/P account."* Reduce visual prominence: smaller label, `text-small text-text-muted2`.

Better: move AP Account to a collapsible "Advanced" section since most users never change it.

Settings section grid: `sm:grid-cols-2 lg:grid-cols-3` (not 4 — the AP field being last made the row feel unbalanced).

Totals bar uses the footer totals pattern defined in §6.4.

Fix: date input needs `bg-surface text-text style="color-scheme:dark"`.

### 7.E Reports

**Current problems (code-confirmed):**
- Report toolbar card: filter inputs (`border-border-input`) are missing `bg-surface text-text` — white boxes in dark mode on date inputs
- Source/freshness metadata at the bottom of the toolbar is understated — `text-small text-text-muted2` is hard to see
- The report hub cards are good structurally but the left-column section title area has no visual weight difference from the report links

**Redesign rules:**

Report toolbar fixes:
- All three date inputs need `bg-surface text-text style="color-scheme:dark"`
- The select for Report Period needs `bg-surface text-text`
- Freshness/source metadata: use `Badge` components instead of plain text spans
  ```
  Badge("Live", "success")   // when freshness is real-time
  Badge("Cached", "neutral") // when cached
  Badge("Manual", "warning") // when manual
  ```
- Toolbar wrapping: the `flex flex-wrap items-end gap-3` layout is correct, keep it

Report hub improvements:
- Section title column: add a left accent border: `border-l-2 border-primary pl-4` inside the left div
- Report link rows: the chevron arrow should be `text-primary` not `text-text-muted3` to indicate interactivity

---

## 8. Component Standardization Checklist

For each control, define the canonical reusable implementation:

| Control | Component | File | Status |
|---|---|---|---|
| Text input | `FormField` | `ui/input.templ` | Extend existing `InputField` |
| Number/amount input | `CurrencyAmountField` | `ui/input.templ` | New |
| Date input | `FormField(type="date")` | `ui/input.templ` | Extend — add `color-scheme:dark` |
| Select | `SelectField` | `ui/input.templ` | New |
| Searchable picker | `SmartPicker` | `ui/smart_picker.templ` | Exists, correct |
| Textarea | `TextareaField` | `ui/input.templ` | New |
| Checkbox | Inline — `accent-primary` class | — | Standardize class only |
| Toggle | Alpine `x-data` pattern | — | Document standard pattern |
| Primary button | `ButtonPrimary` | `ui/button.templ` | Exists |
| Secondary button | `ButtonSecondary` | `ui/button.templ` | Exists |
| Danger button | `ButtonDanger` | `ui/button.templ` | Exists |
| Toolbar button | `ButtonToolbar` | `ui/button.templ` | New |
| Section card | `SectionCard` | `ui/card.templ` | New — extend Card |
| Page shell | `PageShell` | `ui/card.templ` | New |
| Success/error alert | `AlertSuccess/Error` | `ui/alert.templ` | New file |
| Status badge | `Badge` | `ui/badge.templ` | New file |
| Table summary row | `TableSummaryRow` | `ui/table.templ` | Extend existing |
| Empty state row | `TableEmptyRow` | `ui/table.templ` | Extend existing |
| Read-only field | `ReadonlyField` | `ui/input.templ` | New |

---

## 9. Page-Local Hacks to Remove

The following patterns appear across multiple pages and must be eliminated:

### 9.1 The error-state if/else duplication

Current pattern (repeated in receive_payment, pay_bills, vendors, etc.):
```go
if vm.SomeError != "" {
    <input class="... border-danger ... focus:ring-danger-focus">
    <div class="mt-1 text-small text-danger">{ vm.SomeError }</div>
} else {
    <input class="... border-border-input ... focus:ring-primary-focus">
}
```

Replace with `FormField(FormFieldVM{..., Error: vm.SomeError})` everywhere.

### 9.2 `taskInputClass()` helper in expense_form.templ

This is a page-local function producing class strings. Once `FormField` and `SelectField` components exist, remove `taskInputClass()` and migrate the expense form to use components.

### 9.3 Inconsistent `max-w-[95%]` page container

All pages use `max-w-[95%]` as the outer container. This is not a design token — it's a magic number. Replace with `PageShell` wrapper which standardizes this.

If `95%` is correct for the current viewport strategy, encode it as a Tailwind class via config:
```js
// tailwind.config.js
maxWidth: {
  'page': '95%',
}
// Usage: max-w-page
```

### 9.4 Inline style `color-scheme: dark` scattered in some templates but missing in others

This must be in the `FormField` component for `type="date"` so it is never forgotten again.

### 9.5 Raw `mt-2` label-to-input gaps

Currently `mt-2` everywhere. Standardize to `mt-1.5` via components so it is never inlined.

---

## 10. Implementation Priority and Rollout

### Phase 1 — Token and Component Foundation (Do first)
*Unblocks all subsequent work. No page templates change in this phase.*

1. Add missing CSS tokens to `input.css` (`--gb-surface-table-header`, `--gb-surface-row-hover`, `--gb-surface-summary-row`, `--gb-surface-readonly`, `--gb-text-readonly`, `--gb-badge-neutral-*`)
2. Add corresponding Tailwind tokens to `tailwind.config.js`
3. Add `max-w-page` to Tailwind config
4. Create `ui/alert.templ` with `AlertSuccess`, `AlertError`, `AlertWarning`, `AlertInfo`
5. Create `ui/badge.templ` with `Badge`
6. Extend `ui/input.templ`: add `FormField`, `SelectField`, `TextareaField`, `ReadonlyField`, `CurrencyAmountField`
7. Extend `ui/button.templ`: add `ButtonToolbar`, `ButtonLink`
8. Extend `ui/card.templ`: add `SectionCard`, `PageShell`
9. Extend `ui/table.templ`: add `TableSummaryRow`, `TableEmptyRow`, `TableNumericCell`

### Phase 2 — Dark Mode Correctness Fixes (Critical bugs)
*Fix the white-box dark mode regressions immediately. These are bugs, not design work.*

Pages to fix (in order of user visibility):
1. `receive_payment.templ` — date + select missing `bg-surface text-text`
2. `pay_bills.templ` — date inputs missing `bg-surface text-text style="color-scheme:dark"`
3. `report_toolbar.templ` — all date inputs + period select missing `bg-surface text-text`
4. `vendors.templ` — email, phone, textarea missing `bg-surface`
5. `expense_form.templ` — verify date input has `bg-surface` (it has `color-scheme:dark` but may be missing `bg-surface`)

These fixes can be applied before Phase 1 completes — they are direct class additions, not component migrations.

### Phase 3 — Form Page Migration
*Migrate pages to use Phase 1 components. Eliminates the error-state duplication.*

Priority order (most user-visible first):
1. `receive_payment.templ`
2. `pay_bills.templ`
3. `expense_form.templ`
4. `vendors.templ`
5. `customers.templ`
6. `bill_editor.templ`
7. `invoice_editor.templ`
8. All Settings pages

For each migration:
- Replace inline alert divs with `AlertSuccess` / `AlertError`
- Replace outer `<div class="max-w-[95%]">` + `<h1>` with `PageShell`
- Replace `<div class="rounded-lg border border-border bg-surface p-6">` sections with `SectionCard`
- Replace inline input/select/textarea with `FormField` / `SelectField` / `TextareaField`
- Replace inline button class strings with button components
- Remove `taskInputClass()` and similar helpers

### Phase 4 — Table / Report Surface Migration

1. Standardize all reporting table class strings to Phase 1 tokens
2. Migrate report hub and individual report pages to use `Badge` for freshness/source
3. Add left accent to report hub section titles
4. Apply `TableSummaryRow` / `TableEmptyRow` to all reporting pages
5. Fix `pay_bills` selection table — compact input variant + footer totals bar pattern

### Phase 5 — Polish and Remaining Surfaces

1. Normalize all spacing to the ladder defined in §2 (audit `gap-4` vs `gap-6`, `p-4` vs `p-6`)
2. Verify `color-scheme:dark` is on every date input via FormField
3. Verify all selects have `appearance-none` (or a custom styled select wrapper)
4. Verify all typography uses the semantic scale (audit for raw `text-sm`, `text-base` etc.)
5. Audit for any remaining `bg-white`, `text-gray-*`, or raw hex colors in templates
6. Cross-page consistency review: open each page in dark mode, verify no white boxes

---

## 11. Testing Protocol for UI Changes

After each phase:
1. Open every modified page in **dark mode** — no white inputs should be visible
2. Open every modified page in **light mode** — contrast should be normal
3. Resize to `sm` breakpoint (640px) — grids must collapse correctly
4. Tab through all form controls — focus rings must be visible
5. Submit each form with all fields blank — error states must render correctly

---

## 12. Anti-Patterns (Never Do)

| Pattern | Reason |
|---|---|
| `bg-white` in forms | Forces white boxes in dark mode |
| Raw `text-sm` / `text-base` | Bypasses the semantic typography scale |
| Duplicating error-state class via if/else on every control | Use FormField component |
| Inline `style="background: #fff"` | Bypasses CSS variable system |
| New page without PageShell | Inconsistent page header hierarchy |
| Hardcoding `max-w-[95%]` inline | Should use `max-w-page` token |
| `bg-gray-50`, `bg-gray-100` etc. | Use `bg-background` or `bg-surface` |
| Different `gap-` values within the same form | Normalise to `gap-5` |
| Different `p-` values within section cards | Normalise to `p-5` |
| Amounts displayed in proportional font | Use `font-mono tabular-nums` |

---

## 13. File Change Summary by Phase

### Phase 1 files:
- `internal/web/assets/input.css` — add tokens
- `tailwind.config.js` — add token mappings
- `internal/web/templates/ui/input.templ` — extend
- `internal/web/templates/ui/button.templ` — extend
- `internal/web/templates/ui/card.templ` — extend
- `internal/web/templates/ui/table.templ` — extend
- `internal/web/templates/ui/alert.templ` — new
- `internal/web/templates/ui/badge.templ` — new

### Phase 2 files (dark mode bug fixes):
- `internal/web/templates/pages/receive_payment.templ`
- `internal/web/templates/pages/pay_bills.templ`
- `internal/web/templates/pages/report_toolbar.templ`
- `internal/web/templates/pages/vendors.templ`
- `internal/web/templates/pages/expense_form.templ`

### Phase 3 files (form migrations):
- All files listed under Phase 3 in §10

### Phase 4 files (report/table migrations):
- `internal/web/templates/pages/reports_hub.templ`
- `internal/web/templates/pages/report_toolbar.templ`
- `internal/web/templates/pages/ar_aging_templ.go` → `.templ`
- `internal/web/templates/pages/trial_balance.templ`
- `internal/web/templates/pages/balance_sheet.templ`
- `internal/web/templates/pages/pay_bills.templ`
