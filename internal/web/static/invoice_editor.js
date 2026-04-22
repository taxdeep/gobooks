// invoice_editor.js — Alpine component for the invoice line-items editor.
// v=16
//
// Composes gobooksLineItems() for line-array management (addLine / removeLine /
// auto-grow) and layers Invoice-specific state on top: tax-code breakdown,
// terms → due-date computation, AI memo assist, customer auto-fill via
// SmartPicker payload, contact-block (email / bill-to / ship-to) overrides.
//
// Caching: taxBreakdown, subtotalStr, totalTaxStr, grandTotalStr are properties
// recomputed once per _recalcAll() call (which fires on line / tax edits).
// Templates read the properties directly instead of invoking getter functions
// inside x-for / x-text, so Alpine re-evaluates O(1) instead of re-scanning
// lines on every keystroke.
function invoiceEditor() {
  return Object.assign(
    gobooksLineItems({
      defaults: {
        product_service_id:    "",
        product_service_label: "",
        description:           "",
        qty:                   "1",
        unit_price:            "0.00",
        tax_code_id:           "",
        line_net:              "0.00",
        line_tax:              "0.00",
        error:                 "",
        locked:                false,
      },
      // Auto-grow fires when item + qty + unit price are all filled. Description
      // alone is not enough because Invoice allows blank-item ad-hoc lines.
      isLineComplete: (line) =>
        (line.product_service_id || "") !== ""
        && (line.qty || "").trim()        !== ""
        && (line.unit_price || "").trim() !== "",
    }),
    {
      products:        [],
      taxCodes:        [],       // [{id, code, name, rate}]  rate is a fraction string e.g. "0.05"
      paymentTerms:    [],       // [{code, netDays}]
      contactTerms:    {},       // {"customerId": "termCode", ...}
      taxAdj:          {},       // keyed by taxCodeId (string): { calc: "0.00", user: null }
      terms:           "",
      invoiceDate:     "",
      dueDate:         "",
      dueDateEditable: false,
      // taskReadOnly: true when this is a task-generated draft. Lines loaded
      // from initial data are marked locked=true so the template can disable
      // their product/description/qty/price cells individually while still
      // allowing tax-code edits; user-added lines are always unlocked.
      taskReadOnly:    false,

      // Contact block — pre-filled from the Customer record via the
      // SmartPicker payload (or from existing snapshot fields on edit).
      // Operators can override per-invoice; values save through the
      // form's customer_email / bill_to / ship_to / ship_to_label inputs.
      customerEmail:   "",
      billTo:          "",
      shipTo:          "",
      shipToLabel:     "",
      shippingOptions: [],   // [{label, address, is_default}]

      // Cached reactive properties maintained by _recalcAll(). Templates read
      // these directly (no parens) so Alpine doesn't rescan lines per render.
      taxBreakdown:    [],
      subtotalStr:     "0.00",
      totalTaxStr:     "0.00",
      grandTotalStr:   "0.00",

      // AI Memo Assist
      invoiceId:       0,        // 0 on new drafts; set from data-invoice-id on edit pages
      memoAssist:      { loading: false, visible: false, suggestion: "", error: "", empty: false },
      _memoAssistSeq:  0,

      // Fast lookups: built once in init() so _recalcAll() doesn't Array.find()
      // per line per tax code.
      _taxCodesById:   {},
      _productsById:   {},
      _paymentTermsByCode: {},

      init() {
        const el = this.$el;
        this.products     = JSON.parse(el.dataset.products     || "[]");
        this.taxCodes     = JSON.parse(el.dataset.taxCodes     || "[]");
        this.paymentTerms = JSON.parse(el.dataset.paymentTerms || "[]");
        this.contactTerms = JSON.parse(el.dataset.contactTerms || "{}");
        this.terms        = el.dataset.initialTerms   || "";
        this.invoiceDate  = el.dataset.initialDate    || "";
        this.dueDate      = el.dataset.initialDueDate || "";
        this.dueDateEditable = this._isEditable(this.terms);
        this.taskReadOnly = el.dataset.taskReadonly === "true";
        this.invoiceId    = parseInt(el.dataset.invoiceId, 10) || 0;

        this._taxCodesById = Object.fromEntries(this.taxCodes.map(t => [String(t.id), t]));
        this._productsById = Object.fromEntries(this.products.map(p => [String(p.id), p]));
        this._paymentTermsByCode = Object.fromEntries(this.paymentTerms.map(p => [p.code, p]));

        this.customerEmail = el.dataset.initialCustomerEmail || "";
        this.billTo        = el.dataset.initialBillTo        || "";
        this.shipTo        = el.dataset.initialShipTo        || "";
        this.shipToLabel   = el.dataset.initialShipToLabel   || "";
        try {
          this.shippingOptions = JSON.parse(el.dataset.initialShippingAddresses || "[]");
        } catch (_) {
          this.shippingOptions = [];
        }

        const initial = JSON.parse(el.dataset.initialLines || "[]");
        if (initial.length > 0) {
          this.lines = initial.map(l => Object.assign(
            { line_tax: "0.00", error: "", locked: this.taskReadOnly },
            l,
          ));
        } else {
          this.addLine();
        }
        // Task-readonly mode always appends one blank unlocked row so the user
        // can enter ad-hoc line items without clicking "+ Add Line" first.
        // isInvoicePlaceholderLine() on the server skips this row if empty.
        if (this.taskReadOnly) {
          this.addLine();
        }
        // Stamp stable _rowKey on every initial line so the x-for :key binding
        // destroys/recreates rows cleanly on splice — required for per-row
        // Alpine components (e.g. the Items SmartPicker).
        this.assignRowKeys();
        this._recalcAll();

        // Restore saved tax overrides: aggregate saved_line_tax per tax code
        // and treat values that differ from the calculated total as user
        // overrides so the review page shows the right value.
        const savedByCode = {};
        for (const line of this.lines) {
          const cid = String(line.tax_code_id);
          if (!cid || !line.saved_line_tax) continue;
          const stored = parseFloat(line.saved_line_tax) || 0;
          savedByCode[cid] = (savedByCode[cid] || 0) + stored;
        }
        for (const [cid, storedTotal] of Object.entries(savedByCode)) {
          const a = this.taxAdj[cid];
          if (!a) continue;
          const stored = storedTotal.toFixed(2);
          if (stored !== a.calc) {
            a.user = stored;
          }
        }
        // Refresh totals after applying user overrides so grandTotalStr reflects them.
        this._recalcTotals();
      },

      // gobooksLineItems calls onLinesChange() after add/remove/clear;
      // recompute everything so cached totals and breakdowns stay in sync.
      onLinesChange() {
        this._recalcAll();
      },

      onProductChange(idx, psId) {
        if (!psId) return;
        const ps = this._productsById[String(psId)];
        if (!ps) return;
        const line = this.lines[idx];
        if (!line.description) line.description = ps.description || ps.name;
        line.unit_price = ps.default_price;
        if (ps.default_tax_code_id) {
          line.tax_code_id = String(ps.default_tax_code_id);
        }
        this._clearLineError(idx);
        this._recalcAll();
      },

      calcLine(idx) {
        const line = this.lines[idx];
        line.qty        = this._sanitizeDecimalInput(line.qty, 2);
        line.unit_price = this._sanitizeDecimalInput(line.unit_price, 2);
        this._clearLineError(idx);
        this._recalcAll();
      },

      onQtyBlur(idx) {
        this.lines[idx].qty = this._format2dp(this.lines[idx].qty);
        this._recalcAll();
      },

      onPriceBlur(idx) {
        this.lines[idx].unit_price = this._format2dp(this.lines[idx].unit_price);
        this._recalcAll();
        // Auto-grow: append a blank line when the last row becomes complete.
        this._autoGrowIfComplete(idx);
      },

      onTaxCodeChange(idx) {
        this._recalcAll();
      },

      // ── Internal recalculation ───────────────────────────────────────────

      _recalcLine(idx) {
        const line = this.lines[idx];
        const qty   = parseFloat(line.qty)        || 0;
        const price = parseFloat(line.unit_price) || 0;
        const net   = qty * price;
        line.line_net = net.toFixed(2);

        const rate = this._taxRate(line.tax_code_id);
        line.line_tax = (net * rate).toFixed(2);
      },

      // _recalcAll rebuilds line totals, the taxAdj map, the cached tax
      // breakdown, and the cached subtotal/tax/total strings.
      _recalcAll() {
        for (let i = 0; i < this.lines.length; i++) {
          this._recalcLine(i);
        }
        // Rebuild taxAdj: preserve any user overrides for codes that are still
        // in use; drop entries for codes no longer referenced.
        const calcByCode = {};
        for (const line of this.lines) {
          const cid = String(line.tax_code_id);
          if (!cid) continue;
          calcByCode[cid] = (calcByCode[cid] || 0) + (parseFloat(line.line_tax) || 0);
        }
        const next = {};
        for (const [cid, amt] of Object.entries(calcByCode)) {
          const prev = this.taxAdj[cid];
          next[cid] = {
            calc: amt.toFixed(2),
            user: prev ? prev.user : null,
          };
        }
        this.taxAdj = next;
        this._recalcTotals();
      },

      // _recalcTotals rebuilds taxBreakdown, subtotalStr, totalTaxStr, grandTotalStr
      // from the current lines + taxAdj state. Cheap pure-computation step
      // invoked by _recalcAll() and by onTaxAdjInput() when only the user
      // override changed.
      _recalcTotals() {
        // Tax breakdown: one entry per tax code in use, preserving insertion order.
        const byCode = {};
        let subtotal = 0;
        for (const line of this.lines) {
          subtotal += parseFloat(line.line_net) || 0;
          const cid = String(line.tax_code_id);
          if (!cid) continue;
          if (!byCode[cid]) {
            const tc = this._taxCodesById[cid];
            if (!tc) continue;
            byCode[cid] = { id: tc.id, code: tc.code, name: tc.name, rate: parseFloat(tc.rate) || 0, base: 0 };
          }
          byCode[cid].base += parseFloat(line.line_net) || 0;
        }
        this.taxBreakdown = Object.values(byCode);

        let totalTax = 0;
        for (const a of Object.values(this.taxAdj)) {
          const v = a.user !== null ? parseFloat(a.user) : parseFloat(a.calc);
          totalTax += isNaN(v) ? 0 : v;
        }

        this.subtotalStr    = subtotal.toFixed(2);
        this.totalTaxStr    = totalTax.toFixed(2);
        this.grandTotalStr  = (subtotal + totalTax).toFixed(2);
      },

      _sanitizeDecimalInput(val, maxDp) {
        let s = String(val);
        const negative = s.startsWith('-');
        s = s.replace(/[^0-9.]/g, '');
        const firstDot = s.indexOf('.');
        if (firstDot !== -1) {
          s = s.slice(0, firstDot + 1) + s.slice(firstDot + 1).replace(/\./g, '');
          if (s.length - firstDot - 1 > maxDp) {
            s = s.slice(0, firstDot + maxDp + 1);
          }
        }
        // Preserve a bare '-' so the user can finish typing a negative number;
        // _format2dp resolves '-' to '0.00' on blur.
        return negative ? (s !== '' ? '-' + s : '-') : s;
      },

      _format2dp(val) {
        const n = parseFloat(val);
        return isNaN(n) ? '0.00' : n.toFixed(2);
      },

      _clearLineError(idx) {
        const line = this.lines[idx];
        if (!line || !line.error) return;
        if ((line.description || "").trim() !== "") {
          line.error = "";
        }
      },

      _taxRate(taxCodeId) {
        if (!taxCodeId) return 0;
        const tc = this._taxCodesById[String(taxCodeId)];
        if (!tc) return 0;
        return parseFloat(tc.rate) || 0;
      },

      // ── Tax adjustment API (called from template inputs) ─────────────────

      taxAdjValue(cid) {
        const a = this.taxAdj[String(cid)];
        if (!a) return "0.00";
        return a.user !== null ? a.user : a.calc;
      },

      onTaxAdjInput(cid, val) {
        const a = this.taxAdj[String(cid)];
        if (!a) return;
        const trimmed = val.trim();
        // If user clears the field or matches the calculated value, reset to auto.
        if (trimmed === "" || trimmed === a.calc) {
          a.user = null;
        } else {
          a.user = trimmed;
        }
        this._recalcTotals();
      },

      // ── Terms / due-date auto-computation ────────────────────────────────

      // onContactChange auto-fills terms / currency / contact block from the
      // customer's defaults (carried in the SmartPicker payload).
      // Picking a different customer always replaces the contact-block
      // overrides — operators who want to keep manual overrides shouldn't
      // re-pick the customer.
      onContactChange(contactId, payload) {
        if (!contactId) return;
        const p = payload || {};
        const termCode = this.contactTerms[String(contactId)];
        if (termCode) {
          this.onTermsChange(termCode);
        }
        if (p.default_currency) {
          const sel = document.querySelector('select[name="currency_code"]');
          if (sel) sel.value = p.default_currency;
        }
        this.customerEmail = p.email    || "";
        this.billTo        = p.bill_to  || "";
        this.shippingOptions = this._parseShippingAddresses(p.shipping_addresses);
        // Pick the default shipping address (first entry — payload sorts is_default
        // DESC). If none, leave ship-to blank — operator can type freeform.
        if (this.shippingOptions.length > 0) {
          this.shipToLabel = this.shippingOptions[0].label;
          this.shipTo      = this.shippingOptions[0].address;
        } else {
          this.shipToLabel = "";
          this.shipTo      = "";
        }
      },

      // onShipToLabelChange syncs the ship-to textarea when the named-address
      // dropdown changes. "" means "Custom" — leave the textarea content alone
      // so the operator can keep typing.
      onShipToLabelChange(label) {
        if (!label) return;
        const opt = this.shippingOptions.find(o => o.label === label);
        if (opt) {
          this.shipTo = opt.address;
        }
      },

      _parseShippingAddresses(raw) {
        if (!raw) return [];
        try {
          const arr = JSON.parse(raw);
          return Array.isArray(arr) ? arr : [];
        } catch (_) {
          return [];
        }
      },

      onTermsChange(val) {
        this.terms = val;
        this.dueDateEditable = this._isEditable(val);
        if (!this.dueDateEditable) {
          this.dueDate = this._computeDueDate(this.invoiceDate, val);
        }
      },

      onDateChange(val) {
        this.invoiceDate = val;
        if (!this.dueDateEditable) {
          this.dueDate = this._computeDueDate(val, this.terms);
        }
      },

      // Due date is manually editable only when no payment term is selected.
      _isEditable(termCode) {
        return termCode === "";
      },

      _netDays(termCode) {
        const pt = this._paymentTermsByCode[termCode];
        return pt ? pt.netDays : null;
      },

      _computeDueDate(dateStr, termCode) {
        if (!termCode) return "";
        const netDays = this._netDays(termCode);
        if (netDays === null || netDays === 0) return "";
        const d = new Date(dateStr);
        if (isNaN(d.getTime())) return "";
        d.setDate(d.getDate() + netDays);
        return d.toISOString().slice(0, 10);
      },

      // ── AI Memo Assist ───────────────────────────────────────────────────

      // aiMemoAssist calls the memo-assist API and surfaces the suggestion.
      // Only available when invoiceId > 0 (editing a saved draft).
      async aiMemoAssist() {
        if (!this.invoiceId || this.memoAssist.loading || this.memoAssist.visible) return;
        const seq = ++this._memoAssistSeq;
        this.memoAssist.loading    = true;
        this.memoAssist.visible    = true;
        this.memoAssist.suggestion = "";
        this.memoAssist.error      = "";
        this.memoAssist.empty      = false;

        const fetchFn = window.gobooksFetch || fetch;
        try {
          const resp = await fetchFn("/api/ai/invoice-memo-assist", {
            method:  "POST",
            headers: { "Content-Type": "application/json" },
            body:    JSON.stringify({ invoice_id: this.invoiceId }),
          });
          const data = await resp.json();
          if (seq !== this._memoAssistSeq) return;
          if (!resp.ok) {
            this.memoAssist.error = data.error || "AI assist unavailable.";
          } else {
            this.memoAssist.suggestion = (data.suggestion || "").trim();
            this.memoAssist.empty = this.memoAssist.suggestion === "";
          }
        } catch (_) {
          if (seq !== this._memoAssistSeq) return;
          this.memoAssist.error = "Request failed. Please try again.";
        } finally {
          if (seq === this._memoAssistSeq) this.memoAssist.loading = false;
        }
      },

      applyMemoSuggestion() {
        const input = this.$refs.memoInput;
        if (input) input.value = this.memoAssist.suggestion;
        this.memoAssist.visible = false;
        this.memoAssist.empty = false;
      },

      dismissMemoAssist() {
        this.memoAssist.visible    = false;
        this.memoAssist.suggestion = "";
        this.memoAssist.error      = "";
        this.memoAssist.empty      = false;
      },
    },
  );
}

// gobooksCustomerQuickCreate — Alpine component for the inline customer
// creation slide-over panel on the invoice editor page.
//
// Uses `drawerOpen` (the state name ui.RightDrawerBackdrop + RightDrawerPanel
// expect) so the backdrop + panel's own transitions and escape-key handling
// drive visibility.
//
// Lifecycle:
//   1. Listens for gobooks-picker-create (window-level) emitted by the Customer SmartPicker.
//   2. Opens a slide-over with the typed query pre-filled as the customer name.
//      When multi-currency is enabled (data-currencies has >1 entry), shows a Currency
//      dropdown so the user can set the customer's default invoice currency.
//   3. On save, POSTs to /api/customers/quick-create (name + currency_code) and dispatches
//      gobooks-picker-set-value to the SmartPicker's root element so it auto-selects
//      the newly created customer without reloading the page.
function gobooksCustomerQuickCreate() {
  return {
    drawerOpen:    false,
    name:          "",
    currency:      "",
    currencies:    [],
    nameError:     "",
    currencyError: "",
    formError:     "",
    saving:        false,

    init() {
      try {
        const raw = this.$el.dataset.currencies;
        if (raw) {
          this.currencies = JSON.parse(raw);
        }
      } catch (_) {
        this.currencies = [];
      }
    },

    onPickerCreate(event) {
      const { context, query } = (event.detail || event) || {};
      if (context !== "invoice_editor_customer") return;
      this.name          = (query || "").trim();
      this.currency      = "";
      this.nameError     = "";
      this.currencyError = "";
      this.formError     = "";
      this.saving        = false;
      this.drawerOpen    = true;
      this.$nextTick(() => {
        if (this.$refs.nameInput) this.$refs.nameInput.focus();
      });
    },

    cancel() {
      this.drawerOpen = false;
    },

    async save() {
      const name = this.name.trim();
      this.nameError     = "";
      this.currencyError = "";
      this.formError     = "";
      let hasErr = false;
      if (!name) {
        this.nameError = "Customer name is required.";
        hasErr = true;
      }
      if (this.currencies.length > 1 && !this.currency) {
        this.currencyError = "Currency is required.";
        hasErr = true;
      }
      if (hasErr) return;
      this.saving = true;
      try {
        const fetchFn = window.gobooksFetch || fetch;
        const body = { name };
        if (this.currency) {
          body.currency_code = this.currency;
        }
        const resp = await fetchFn("/api/customers/quick-create", {
          method:  "POST",
          headers: { "Content-Type": "application/json" },
          body:    JSON.stringify(body),
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          this.formError = data.error || "Could not create customer.";
          return;
        }
        // Programmatically select the new customer in the SmartPicker. Include
        // the chosen currency in the payload so onContactChange can pre-fill
        // the invoice's currency select.
        const pickerEl = document.querySelector('[data-context="invoice_editor_customer"]');
        if (pickerEl) {
          pickerEl.dispatchEvent(new CustomEvent("gobooks-picker-set-value", {
            detail: {
              id:      String(data.id),
              label:   data.name,
              payload: data.currency_code ? { default_currency: data.currency_code } : {},
            },
            bubbles: false,
          }));
        }
        this.drawerOpen = false;
      } catch (_) {
        this.formError = "Could not create customer. Please try again.";
      } finally {
        this.saving = false;
      }
    },
  };
}

// gobooksItemPicker — per-row product/service picker used inside the invoice
// line-items table's x-for loop. Receives the Alpine-scope `line` object and
// row `idx` at construction so it can write through to line.product_service_id
// / product_service_label without dataset plumbing (SmartPicker's global
// single-instance init-reads-dataset pattern doesn't work inside x-for).
//
// On select: writes to line, then dispatches "item-picker-select" up so the
// parent invoiceEditor() can auto-fill description / unit price / tax code
// via its existing onProductChange(idx, id) logic.
function gobooksItemPicker(line, idx) {
  return {
    line:        line,
    idx:         idx,
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

    async onInput() {
      this.open = true;
      await this._fetch();
    },

    async _fetch() {
      const seq = ++this._fetchSeq;
      this.loading = true;
      this.failed  = false;
      try {
        const q = encodeURIComponent(this.query);
        const url = "/api/smart-picker/search?entity=product_service&context=invoice_line_item&q=" + q + "&limit=20";
        const fetchFn = window.gobooksFetch || fetch;
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
      // Bubble up so invoiceEditor.onProductChange can pre-fill description /
      // unit_price / tax_code from the chosen ProductService.
      this.$dispatch("gobooks-item-picker-select", {
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
      this.$dispatch("gobooks-item-picker-select", { idx: this.idx, id: "", payload: {} });
    },

    close() {
      this.open = false;
      this.highlighted = -1;
      // Reset visible input to the committed label so unfinished typing doesn't linger.
      this.query = this.line.product_service_label || "";
    },

    inputClass() {
      return "border-border-input focus:ring-primary-focus";
    },
  };
}
