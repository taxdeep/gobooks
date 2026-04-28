// doc_item_picker.js — shared per-row product/service picker for transaction-
// document line items (Invoice / Quote / SO / Bill / PO / Expense).
// v=1
//
// Used inside a parent Alpine x-for loop. Receives the row's `line` object
// + `idx` + an opts bag at construction so it can write through to
// line.product_service_id / .product_service_label without dataset plumbing
// (SmartPicker's global single-instance init-reads-dataset pattern doesn't
// work inside x-for).
//
// Usage in templ:
//   x-data="balancizItemPicker(line, idx, { context: 'invoice_line_item' })"
//
// On select: writes to line, then dispatches "balanciz-item-picker-select"
// up so the parent editor can auto-fill description / unit price / tax code
// via its existing onProductChange(idx, id, payload) logic.
function balancizItemPicker(line, idx, opts) {
  opts = opts || {};
  return {
    line:        line,
    idx:         idx,
    context:     opts.context || "doc_line_item",
    entity:      opts.entity  || "product_service",
    query:       line.product_service_label || "",
    open:        false,
    loading:     false,
    failed:      false,
    items:       [],       // [{id, primary, secondary, payload}]
    highlighted: -1,
    _fetchSeq:   0,

    init() {
      // Re-sync visible query when another code path writes to line.product_service_label
      // (e.g. row reindex after delete). Alpine $watch hooks into the reactive system.
      this.$watch(() => this.line.product_service_label, (v) => {
        this.query = v || "";
      });
    },

    onFocus() {
      this.open = true;
      // Empty query → load an initial page (top 20 by usage ranking).
      if (this.items.length === 0 && !this.loading) this._fetch();
    },

    // onInput opens the dropdown immediately (don't wait for the debounced
    // fetch — the dropdown should appear the moment the user starts typing,
    // showing "Searching…" while the request is in flight). Fetch itself
    // is debounced internally so we don't fire on every keystroke.
    onInput() {
      this.open = true;
      if (this._inputDebounce) clearTimeout(this._inputDebounce);
      this._inputDebounce = setTimeout(() => this._fetch(), 250);
    },

    async _fetch() {
      const seq = ++this._fetchSeq;
      this.loading = true;
      this.failed  = false;
      try {
        const q = encodeURIComponent(this.query);
        const url = "/api/smart-picker/search?entity=" + encodeURIComponent(this.entity) +
                    "&context=" + encodeURIComponent(this.context) + "&q=" + q + "&limit=20";
        const fetchFn = window.balancizFetch || fetch;
        const resp = await fetchFn(url);
        const data = await resp.json();
        if (seq !== this._fetchSeq) return;
        if (!resp.ok) {
          this.failed = true;
          this.items  = [];
        } else {
          this.items = Array.isArray(data.candidates) ? data.candidates : [];
        }
      } catch (_) {
        if (seq !== this._fetchSeq) return;
        this.failed = true;
        this.items  = [];
      } finally {
        if (seq === this._fetchSeq) this.loading = false;
      }
    },

    onKeydown(e) {
      if (e.key === "Escape") { this.close(); return; }
      if (e.key === "ArrowDown") { e.preventDefault(); this._move(1); return; }
      if (e.key === "ArrowUp")   { e.preventDefault(); this._move(-1); return; }
      if (e.key === "Enter" && this.highlighted >= 0 && this.items[this.highlighted]) {
        e.preventDefault();
        this.select(this.items[this.highlighted]);
      }
    },

    _move(delta) {
      this.open = true;
      if (this.items.length === 0) return;
      this.highlighted = Math.max(0, Math.min(this.items.length - 1, this.highlighted + delta));
    },

    select(item) {
      this.line.product_service_id    = String(item.id);
      this.line.product_service_label = item.primary || "";
      this.query = item.primary || "";
      this.open = false;
      this.highlighted = -1;
      // Bubble up so the parent editor can pre-fill description /
      // unit_price / tax_code from the chosen ProductService.
      this.$dispatch("balanciz-item-picker-select", {
        idx:     this.idx,
        id:      String(item.id),
        payload: item.payload || {},
      });
    },

    clear() {
      this.line.product_service_id    = "";
      this.line.product_service_label = "";
      this.query = "";
      this.open  = false;
      this.$dispatch("balanciz-item-picker-select", { idx: this.idx, id: "", payload: {} });
    },

    close() {
      this.open = false;
      this.highlighted = -1;
      // Reset visible input to the committed label so unfinished typing doesn't linger.
      this.query = this.line.product_service_label || "";
    },
  };
}
