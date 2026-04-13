// smart_picker.js — GoBooks universal SmartPicker Alpine component.
// v=9
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
    allowCreate: false,
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
    // items shape: [{id: string, primary: string, secondary: string, meta: object|null, payload: object|null}]
    // Populated from data.candidates (backend field renamed from data.items in v3).
    // primary   — main display label (e.g. account name)
    // secondary — supplementary info (e.g. account code, tax rate)
    // meta      — key-value bag rendered in the dropdown
    // payload   — machine-readable data for downstream components (e.g. default_price); never rendered
    items:       [],
    highlighted: -1,

    // ── Internal ──
    _lastFetchQuery: null,  // dedup: skip identical back-to-back requests
    _fetchSeq:       0,     // monotonic counter; used to discard stale out-of-order responses
    _lastRequestId:  "",    // request_id from last backend response; correlated into usage ping
    requiresBackendValidation: true,

    init() {
      const el = this.$el;
      this.entity      = el.dataset.entity      || "";
      this.context     = el.dataset.context     || "";
      this.fieldName   = el.dataset.fieldName   || "";
      this.limit       = parseInt(el.dataset.limit, 10) || 10;
      this.required    = el.dataset.required    === "true";
      this.allowCreate = el.dataset.allowCreate === "true";
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

      // gobooks-picker-set-value: programmatic selection from outside the component
      // (e.g. after inline Quick Create). Accepts {id, label, payload?}.
      el.addEventListener("gobooks-picker-set-value", (e) => {
        const { id, label, payload } = e.detail || {};
        if (!id) return;
        this.selectedId    = String(id);
        this.selectedLabel = label || "";
        this.query         = label || "";
        this.open          = false;
        this.highlighted   = -1;
        // Dispatch the standard picker-select event so listeners (e.g. due-date
        // auto-fill and currency pre-fill) can react exactly as if the user had
        // picked from the dropdown. Forward the caller's payload (if any).
        this.$dispatch("gobooks-picker-select", {
          entity:  this.entity,
          context: this.context,
          id:      String(id),
          payload: payload || {},
          requiresBackendValidation: false,
        });
      });
    },

    // ── CSS helpers ──

    hasError() {
      return this.$el.dataset.hasError === "true";
    },

    // inputClass() returns only the state-conditional classes (border colour + ring colour).
    // The base layout/surface classes are on the static `class` attribute of the <input>
    // so they are applied before Alpine initialises — eliminating the white-box FOUC.
    inputClass() {
      return this.hasError()
        ? "border-danger focus:ring-danger-focus"
        : "border-border-input focus:ring-primary-focus";
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

      // Stale-response guard: increment sequence before each request.
      // After awaiting, check that our sequence is still the latest — if not,
      // a newer request has already written items and we must not overwrite it.
      this._fetchSeq++;
      const seq = this._fetchSeq;
      const requestId = this._newRequestId();

      this.loading = true;
      this.failed  = false;
      try {
        const params = new URLSearchParams({
          entity:  this.entity,
          context: this.context,
          q:       q,
          limit:   String(this.limit),
          request_id: requestId,
        });
        const fetchFn = window.gobooksFetch || fetch;
        const res = await fetchFn("/api/smart-picker/search?" + params.toString(), {
          method: "GET",
        });
        // Drop stale response — a newer fetch has taken over.
        if (seq !== this._fetchSeq) return;
        if (!res.ok) {
          this.failed = true;
          this.items  = [];
          return;
        }
        const data = await res.json();
        if (seq !== this._fetchSeq) return; // check again after second await
        // Backend renamed items → candidates in v3. Accept both for forward compat.
        this.items  = Array.isArray(data.candidates) ? data.candidates
                    : Array.isArray(data.items)       ? data.items
                    : [];
        this.requiresBackendValidation = data.requires_backend_validation !== false;
        // Capture request_id for usage ping correlation.
        if (data.request_id && data.request_id === requestId) {
          this._lastRequestId = data.request_id;
        }
        this.failed = false;
      } catch (_) {
        if (seq !== this._fetchSeq) return;
        this.failed = true;
        this.items  = [];
      } finally {
        if (seq === this._fetchSeq) this.loading = false;
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
        entity:                    this.entity,
        context:                   this.context,
        id:                        item.id,
        payload:                   item.payload || {},
        requiresBackendValidation: this.requiresBackendValidation,
      });
      // Fire-and-forget usage ping for future ranking signals.
      // Uses gobooksFetch so the X-CSRF-Token is injected automatically.
      // Errors are silently ignored — this must never break picker UX.
      const fetchFn = window.gobooksFetch || fetch;
      fetchFn("/api/smart-picker/usage", {
        method:  "POST",
        headers: {"Content-Type": "application/json"},
        body:    JSON.stringify({
          entity:     this.entity,
          context:    this.context,
          item_id:    item.id,
          request_id: this._lastRequestId || "",
        }),
      }).catch(() => {});
    },

    // triggerCreate — fired when user clicks/keyboards to the "+ Add new" row.
    // Closes the dropdown and dispatches gobooks-picker-create so the host page
    // can open an inline creation panel without navigating away.
    triggerCreate() {
      const q = this.query.trim();
      this.close();
      this.$dispatch("gobooks-picker-create", {
        entity:  this.entity,
        context: this.context,
        query:   q,
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
      this._fetchSeq       = 0;
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
          if (this.items.length > 0) {
            this.highlighted = Math.min(this.highlighted + 1, this.items.length - 1);
          }
          break;
        case "ArrowUp":
          event.preventDefault();
          // When allowCreate the create row lives at index -1; allow navigating back to it.
          this.highlighted = Math.max(this.highlighted - 1, this.allowCreate ? -1 : 0);
          break;
        case "Enter":
          event.preventDefault();
          if (this.highlighted === -1 && this.allowCreate) {
            this.triggerCreate();
          } else if (this.highlighted >= 0 && this.highlighted < this.items.length) {
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

    _newRequestId() {
      if (window.crypto && typeof window.crypto.randomUUID === "function") {
        return window.crypto.randomUUID();
      }
      return "sp-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
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
