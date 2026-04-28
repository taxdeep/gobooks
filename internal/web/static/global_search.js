// global_search.js — Alpine factory for the topbar global search dropdown.
// v=1
//
// Drives /api/global-search and renders QuickBooks-style grouped results
// (Transactions / Contacts / Products / Other). Keyboard nav across
// groups; Enter navigates; Esc closes; Cmd/Ctrl-K focuses from anywhere.
//
// Wiring:
//   <input x-data="balancizGlobalSearch()" ... />
// (See layout.templ for the canonical markup.)
//
// Backend contract (see internal/web/global_search_handler.go):
//   GET /api/global-search?q=<q>&limit=20
//   { candidates: [{id, primary, secondary, group_key, group_label,
//                   action_kind, url, entity_type, payload}, ...],
//     source: "ranked" | "recent" | "legacy_empty",
//     mode:   "ent" | "legacy" | "dual" }
function balancizGlobalSearch() {
  return {
    query: "",
    open: false,
    loading: false,
    failed: false,
    items: [],          // flat list, group_key in each candidate drives the headers
    groups: [],         // [{key, label, rows: [...]}], rebuilt from items on each fetch
    highlighted: -1,    // index into the FLAT items list
    mode: "",           // engine mode echoed back from server, for debug overlay
    source: "",
    _fetchSeq: 0,
    _inputDebounce: null,

    init() {
      // Cmd/Ctrl-K from anywhere focuses the input. Bound on the
      // enclosing form so the listener is removed on Alpine teardown.
      // ignore presses while a modal/drawer has captured focus to avoid
      // hijacking modal-internal text fields.
      this._keyHandler = (e) => {
        if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
          e.preventDefault();
          this.$refs.input && this.$refs.input.focus();
        }
      };
      window.addEventListener('keydown', this._keyHandler);
    },

    destroy() {
      window.removeEventListener('keydown', this._keyHandler);
    },

    onFocus() {
      this.open = true;
      // Empty query → load recent transactions (the empty-state panel).
      if (this.items.length === 0 && !this.loading) this._fetch();
    },

    onInput() {
      this.open = true;
      if (this._inputDebounce) clearTimeout(this._inputDebounce);
      this._inputDebounce = setTimeout(() => this._fetch(), 200);
    },

    onKeydown(e) {
      // Skip composition-active key events so IME input (Chinese,
      // Japanese, Korean) doesn't trigger Enter / arrow handling
      // mid-word. e.isComposing is the standard check.
      if (e.isComposing) return;

      if (e.key === "Escape") { this.close(); return; }
      if (e.key === "ArrowDown") { e.preventDefault(); this._move(1); return; }
      if (e.key === "ArrowUp")   { e.preventDefault(); this._move(-1); return; }
      if (e.key === "Enter") {
        e.preventDefault();
        if (this.highlighted >= 0 && this.items[this.highlighted]) {
          this.select(this.items[this.highlighted]);
          return;
        }
        if (this.query.trim() !== "") {
          this.openAdvanced();
        }
      }
    },

    async _fetch() {
      const seq = ++this._fetchSeq;
      this.loading = true;
      this.failed = false;
      try {
        const q = encodeURIComponent(this.query);
        const url = "/api/global-search?q=" + q + "&limit=20";
        const fetchFn = window.balancizFetch || fetch;
        const resp = await fetchFn(url);
        const data = await resp.json();
        // Last-write-wins guard: drop stale responses.
        if (seq !== this._fetchSeq) return;
        if (!resp.ok) {
          this.failed = true;
          this.items = [];
          this.groups = [];
        } else {
          this.items = Array.isArray(data.candidates) ? data.candidates : [];
          this.groups = this._buildGroups(this.items);
          this.mode = data.mode || "";
          this.source = data.source || "";
        }
        this.highlighted = this.items.length > 0 ? 0 : -1;
      } catch (_) {
        if (seq !== this._fetchSeq) return;
        this.failed = true;
        this.items = [];
        this.groups = [];
      } finally {
        if (seq === this._fetchSeq) this.loading = false;
      }
    },

    // _buildGroups walks the flat candidate list and clusters consecutive
    // rows sharing the same group_key. The backend already orders by
    // family (transactions → contacts → products → other) so a single
    // pass produces the right structure for the template.
    _buildGroups(items) {
      const out = [];
      let cur = null;
      for (const item of items) {
        const key = item.group_key || "";
        const label = item.group_label || "";
        if (!cur || cur.key !== key) {
          cur = { key, label, rows: [] };
          out.push(cur);
        }
        cur.rows.push(item);
      }
      return out;
    },

    _move(delta) {
      this.open = true;
      if (this.items.length === 0) return;
      this.highlighted = Math.max(0, Math.min(this.items.length - 1, this.highlighted + delta));
      // Scroll the highlighted row into view if it's outside the panel.
      this.$nextTick(() => {
        const el = document.querySelector('[data-global-search-row="' + this.highlighted + '"]');
        if (el && el.scrollIntoView) {
          el.scrollIntoView({ block: "nearest" });
        }
      });
    },

    // select handles row activation: navigate vs select-for-form.
    // For the topbar dropdown every row is a navigate (action_kind defaults
    // to "navigate" server-side). The select branch is reserved for a
    // future hybrid mode where this component is also embedded inside a
    // form; today it's a no-op fallback.
    select(item) {
      this.open = false;
      this.highlighted = -1;
      if (item.action_kind === "select") {
        // Reserved for future inline-form usage.
        return;
      }
      if (item.url) {
        window.location.href = item.url;
      }
    },

    // openAdvanced jumps to the dedicated /advanced-search page (Phase
    // 5 design). Query string preserves the current input so the user
    // doesn't re-type. Falls back to "/" if the page isn't deployed yet.
    openAdvanced() {
      const target = "/advanced-search";
      const q = this.query ? "?q=" + encodeURIComponent(this.query) : "";
      this.open = false;
      window.location.href = target + q;
    },

    close() {
      this.open = false;
      this.highlighted = -1;
    },

    // Visible-row ARIA helpers — exposed because Alpine x-bind doesn't
    // see methods inside object-literal x-data shorthand.
    isHighlighted(idx) { return this.highlighted === idx; },
    flatIndex(groupIdx, rowIdx) {
      // Compute the flat-list index for a (group, row) pair so keyboard
      // nav and click highlight align. Done in JS rather than templ to
      // avoid ugly nested counter state.
      let n = 0;
      for (let g = 0; g < groupIdx; g++) n += this.groups[g].rows.length;
      return n + rowIdx;
    },

    // groupIcon returns a single emoji-ish glyph per group. Pure visual
    // shorthand; intentionally text so it inherits text colour and works
    // without an icon font.
    groupIcon(key) {
      switch (key) {
        case "transactions": return "↹";
        case "contacts":     return "◌";
        case "products":     return "◇";
        case "jump_to":      return "→";
        default:             return "•";
      }
    },
  };
}
