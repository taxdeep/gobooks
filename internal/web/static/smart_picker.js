// smart_picker.js — GoBooks universal SmartPicker Alpine component.
// v=2
//
// IMPORTANT — entity semantics:
//   entity="account" in Phase 1 maps to ExpenseAccountProvider, which returns
//   only expense-root active accounts for the authenticated company.
//   It does NOT return all GL accounts. The actual result scope is always
//   determined by the backend provider (entity + context together).
//   Never assume entity="account" means "all accounts" on the frontend.
//
// Usage:
//   <div x-data="gobooksSmartPicker()"
//        data-field-name="expense_account_id"
//        data-entity="account"
//        data-context="expense_form_category"
//        ...more data-* attrs...>
//
// Config is read entirely from data-* attributes in init(); the object
// returned by gobooksSmartPicker() never receives direct function arguments.
// This matches the pattern used by gobooksAccountDrawerSuggest() and
// gobooksJournalEntryDraft().
//
function gobooksSmartPicker() {
  return {
    // ── Config (read from data-* attrs in init(); immutable after) ──
    entity:      "",
    context:     "",
    fieldName:   "",
    limit:       10,
    required:    false,
    createUrl:   "",
    createLabel: "Add new",
    placeholder: "Search\u2026",

    // ── Selection state ──
    selectedId:    "",   // value written to hidden input; what the form submits
    selectedLabel: "",   // text shown in visible input when something is selected

    // ── Search state ──
    query:    "",        // bound to visible input via x-model
    open:     false,
    loading:  false,
    failed:   false,
    // items shape: [{id: string, primary: string, secondary: string, meta: object|null}]
    // primary   — main display label (e.g. account name)
    // secondary — supplementary info (e.g. account code, tax rate)
    // meta      — reserved key-value bag for Batch C+ (customer email, product sku, etc.)
    items:       [],
    highlighted: -1,

    // ── Internal ──
    _lastFetchQuery: null,  // dedup: skip identical back-to-back requests

    init() {
      const el = this.$el;
      this.entity      = el.dataset.entity      || "";
      this.context     = el.dataset.context     || "";
      this.fieldName   = el.dataset.fieldName   || "";
      this.limit       = parseInt(el.dataset.limit, 10) || 10;
      this.required    = el.dataset.required    === "true";
      this.createUrl   = el.dataset.createUrl   || "";
      this.createLabel = el.dataset.createLabel || "Add new";
      this.placeholder = el.dataset.placeholder || "Search\u2026";

      // Edit-page rehydration: server pre-populates data-value + data-selected-label.
      // selectedLabel MUST come from the server; we never fall back to displaying the
      // raw database ID as visible text. If SelectedLabel is empty, visible input stays
      // blank (shows placeholder) even though hidden input retains the ID.
      this.selectedId    = el.dataset.value         || "";
      this.selectedLabel = el.dataset.selectedLabel || "";
      this.query         = this.selectedLabel;  // "" if no label, never the raw id

      // Assign name to hidden input here, not server-side.
      // The hidden input is rendered without a static name attribute so that a no-JS
      // fallback select using the same field name does not cause a double-submit.
      // With JS active this is the sole authority for form submission.
      const hidden = el.querySelector('input[type=hidden]');
      if (hidden) hidden.name = this.fieldName;
    },

    // ── CSS helpers ──

    hasError() {
      return this.$el.dataset.hasError === "true";
    },

    inputClass() {
      const base = "mt-2 block w-full rounded-md border bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted outline-none focus:ring-2";
      return this.hasError()
        ? base + " border-danger focus:ring-danger-focus"
        : base + " border-border-input focus:ring-primary-focus";
    },

    // ── Dropdown lifecycle ──

    async onFocus() {
      this.open = true;
      // Fetch defaults on first open if we have no items yet.
      if (this.items.length === 0) {
        await this._fetch(this.query.trim());
      }
    },

    async onInput() {
      const q = this.query.trim();
      // If the user edited the visible text away from the committed label,
      // clear the committed selection so a stale ID is never submitted.
      if (this.query !== this.selectedLabel) {
        this.selectedId    = "";
        this.selectedLabel = "";
      }
      this.open        = true;
      this.highlighted = -1;
      await this._fetch(q);
    },

    async _fetch(q) {
      // Dedup: same trimmed query with results already loaded → skip.
      if (this._lastFetchQuery === q && this.items.length > 0) return;
      this._lastFetchQuery = q;
      this.loading = true;
      this.failed  = false;
      try {
        const params = new URLSearchParams({
          entity:  this.entity,
          context: this.context,
          q:       q,
          limit:   String(this.limit),
        });
        const res = await fetch("/api/smart-picker/search?" + params.toString(), {
          credentials: "same-origin",
        });
        if (!res.ok) {
          this.failed = true;
          this.items  = [];
          return;
        }
        const data  = await res.json();
        this.items  = Array.isArray(data.items) ? data.items : [];
        this.failed = false;
      } catch (_) {
        this.failed = true;
        this.items  = [];
      } finally {
        this.loading = false;
      }
    },

    select(item) {
      this.selectedId    = item.id;
      this.selectedLabel = item.primary;
      this.query         = item.primary;
      this.open          = false;
      this.highlighted   = -1;
      // Dispatch a bubbling event so parent Alpine components can react.
      // `payload` carries machine-readable data (e.g. default_price) that
      // providers embed in SmartPickerItem.Payload — not shown in the dropdown UI.
      this.$dispatch("gobooks-picker-select", {
        entity:  this.entity,
        context: this.context,
        id:      item.id,
        payload: item.payload || {},
      });
    },

    close() {
      this.open        = false;
      this.highlighted = -1;
      // Restore visible input to committed label (or blank if nothing selected).
      // Never fall back to the raw selectedId.
      this.query = this.selectedLabel;
    },

    // clear() is only reachable when required=false (clear button not rendered for required fields).
    clear() {
      this.selectedId      = "";
      this.selectedLabel   = "";
      this.query           = "";
      this.items           = [];
      this.open            = false;
      this.highlighted     = -1;
      this._lastFetchQuery = null;
    },

    // ── Keyboard navigation ──

    onKeydown(event) {
      if (!this.open) {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault();
          this.open = true;
          if (this.items.length === 0) this._fetch(this.query.trim());
        }
        return;
      }
      switch (event.key) {
        case "ArrowDown":
          event.preventDefault();
          this.highlighted = Math.min(this.highlighted + 1, this.items.length - 1);
          break;
        case "ArrowUp":
          event.preventDefault();
          this.highlighted = Math.max(this.highlighted - 1, 0);
          break;
        case "Enter":
          event.preventDefault();
          if (this.highlighted >= 0 && this.highlighted < this.items.length) {
            this.select(this.items[this.highlighted]);
          }
          break;
        case "Escape":
          event.preventDefault();
          this.close();
          break;
        case "Tab":
          // If an item is highlighted, select it before allowing focus to move.
          if (this.highlighted >= 0 && this.highlighted < this.items.length) {
            event.preventDefault();
            this.select(this.items[this.highlighted]);
          } else {
            this.close();
          }
          break;
      }
    },
  };
}

// gobooksTaskRateSync — Alpine component for the Task Form.
//
// Listens for gobooks-picker-select events bubbling up from any SmartPicker
// inside the form. When the user picks a service item (context =
// "task_form_service_item"), and the item carries a non-zero default_price in
// its payload, the Rate field is auto-filled. The user can still type over it.
//
// Usage in templ:
//   <form x-data="gobooksTaskRateSync()" data-init-rate="0.00"
//         @gobooks-picker-select="onServiceItemSelect($event)">
//     <input name="rate" x-model="rate" ...>
//   </form>
function gobooksTaskRateSync() {
  return {
    rate: "0.00",

    init() {
      this.rate = this.$el.dataset.initRate || "0.00";
    },

    onServiceItemSelect(event) {
      const d = event.detail || {};
      if (d.context !== "task_form_service_item") return;
      const raw = (d.payload || {}).default_price;
      if (!raw) return;
      const price = parseFloat(raw);
      if (!isNaN(price) && price > 0) {
        this.rate = price.toFixed(2);
      }
    },
  };
}
