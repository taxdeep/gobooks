// invoice_editor.js — Alpine component for the invoice line-items editor.
// v=9
function invoiceEditor() {
  return {
    lines: [],
    products: [],
    taxCodes: [],       // [{id, code, name, rate}]  rate is a fraction string e.g. "0.05"
    paymentTerms: [],   // [{code, netDays}]
    contactTerms: {},   // {"customerId": "termCode", ...}
    taxAdj: {},         // keyed by taxCodeId (string): { calc: "0.00", user: null }
    terms: "",
    invoiceDate: "",
    dueDate: "",
    dueDateEditable: false,
    // taskReadOnly: true when this is a task-generated draft (set from data-task-readonly).
    // Lines loaded from initial data are marked locked=true in this mode; lines the user
    // adds via addLine() are always locked=false.
    taskReadOnly: false,

    // ── AI Memo Assist ──
    invoiceId: 0,       // 0 on new drafts; set from data-invoice-id on edit pages
    memoAssist: { loading: false, visible: false, suggestion: "", error: "", empty: false },
    _memoAssistSeq: 0,

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

      const initial = JSON.parse(el.dataset.initialLines || "[]");
      if (initial.length > 0) {
        // Mark initial lines as locked when in task-readonly mode so the template
        // can disable their product/description/qty/price cells individually while
        // still allowing tax-code and GST edits.
        this.lines = initial.map(l => Object.assign(
          { line_tax: "0.00", error: "", locked: this.taskReadOnly },
          l,
        ));
      } else {
        this.addLine();
      }
      // In task-readonly mode, always append one blank unlocked row so the user
      // can enter ad-hoc line items without clicking "+ Add Line" first.
      // isInvoicePlaceholderLine() on the server skips this row if left empty.
      if (this.taskReadOnly) {
        this.addLine();
      }
      this._recalcAll();
    },

    // ── Line management ──────────────────────────────────────────────────────

    addLine() {
      this.lines.push({
        product_service_id: "",
        description: "",
        qty: "1",
        unit_price: "0.00",
        tax_code_id: "",
        line_net: "0.00",
        line_tax: "0.00",
        error: "",
        locked: false,  // user-added lines are always fully editable
      });
      this._recalcAll();
    },

    removeLine(idx) {
      if (this.lines.length > 1) {
        this.lines.splice(idx, 1);
        this._recalcAll();
      }
    },

    onProductChange(idx, psId) {
      if (!psId) return;
      const ps = this.products.find(p => String(p.id) === String(psId));
      if (!ps) return;
      const line = this.lines[idx];
      // Prefer the item's own description field; fall back to the item name when blank.
      if (!line.description) line.description = ps.description || ps.name;
      line.unit_price = ps.default_price;
      if (ps.default_tax_code_id) {
        line.tax_code_id = String(ps.default_tax_code_id);
      }
      this._clearLineError(idx);
      this._recalcLine(idx);
      this._recalcAll();
    },

    calcLine(idx) {
      const line = this.lines[idx];
      line.qty        = this._sanitizeDecimalInput(line.qty, 2);
      line.unit_price = this._sanitizeDecimalInput(line.unit_price, 2);
      this._clearLineError(idx);
      this._recalcLine(idx);
      this._recalcAll();
    },

    onQtyBlur(idx) {
      const line = this.lines[idx];
      line.qty = this._format2dp(line.qty);
      this._recalcLine(idx);
      this._recalcAll();
    },

    onPriceBlur(idx) {
      const line = this.lines[idx];
      line.unit_price = this._format2dp(line.unit_price);
      this._recalcLine(idx);
      this._recalcAll();
    },

    // When user changes the tax code dropdown on a line, reset that code's adjustment.
    onTaxCodeChange(idx) {
      this._recalcLine(idx);
      this._recalcAll();
    },

    // ── Internal recalculation ───────────────────────────────────────────────

    _recalcLine(idx) {
      const line = this.lines[idx];
      const qty   = parseFloat(line.qty)        || 0;
      const price = parseFloat(line.unit_price) || 0;
      const net   = qty * price;
      line.line_net = net.toFixed(2);

      const rate = this._taxRate(line.tax_code_id);
      line.line_tax = (net * rate).toFixed(2);
    },

    _recalcAll() {
      for (let i = 0; i < this.lines.length; i++) {
        this._recalcLine(i);
      }
      // Rebuild taxAdj: for each code used, recompute calculated total;
      // preserve user overrides.
      const newAdj = {};
      for (const line of this.lines) {
        const cid = String(line.tax_code_id);
        if (!cid) continue;
        if (!newAdj[cid]) newAdj[cid] = 0;
        newAdj[cid] += parseFloat(line.line_tax) || 0;
      }
      const next = {};
      for (const [cid, calcAmt] of Object.entries(newAdj)) {
        const calc = calcAmt.toFixed(2);
        const prev = this.taxAdj[cid];
        next[cid] = {
          calc,
          // Keep existing user override only if this code was already tracked.
          user: prev ? prev.user : null,
        };
      }
      this.taxAdj = next;
    },

    // Strip non-numeric chars; keep at most one '.'; truncate to maxDp decimal places.
    _sanitizeDecimalInput(val, maxDp) {
      let s = String(val);
      // Allow a leading minus sign.
      const negative = s.startsWith('-');
      s = s.replace(/[^0-9.]/g, '');
      const firstDot = s.indexOf('.');
      if (firstDot !== -1) {
        s = s.slice(0, firstDot + 1) + s.slice(firstDot + 1).replace(/\./g, '');
        if (s.length - firstDot - 1 > maxDp) {
          s = s.slice(0, firstDot + maxDp + 1);
        }
      }
      return negative && s !== '' ? '-' + s : s;
    },

    // Format to exactly 2 decimal places on blur.
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
      const tc = this.taxCodes.find(t => String(t.id) === String(taxCodeId));
      if (!tc) return 0;
      return parseFloat(tc.rate) || 0;
    },

    // ── Tax adjustment API (called from template inputs) ─────────────────────

    // Returns the display value for a tax code's adjustment input.
    taxAdjValue(cid) {
      const a = this.taxAdj[String(cid)];
      if (!a) return "0.00";
      return a.user !== null ? a.user : a.calc;
    },

    // Called when the user edits a tax adjustment input.
    onTaxAdjInput(cid, val) {
      const a = this.taxAdj[String(cid)];
      if (!a) return;
      const trimmed = val.trim();
      // If user clears the field or matches calculated value, reset to auto.
      if (trimmed === "" || trimmed === a.calc) {
        a.user = null;
      } else {
        a.user = trimmed;
      }
    },

    // ── Aggregates used by the template ─────────────────────────────────────

    // Breakdown of active tax codes: [{id, code, name, rate, base, amount}]
    taxBreakdown() {
      const byCode = {};
      for (const line of this.lines) {
        const cid = String(line.tax_code_id);
        if (!cid) continue;
        if (!byCode[cid]) {
          const tc = this.taxCodes.find(t => String(t.id) === cid);
          if (!tc) continue;
          byCode[cid] = { id: tc.id, code: tc.code, name: tc.name, rate: parseFloat(tc.rate) || 0, base: 0 };
        }
        byCode[cid].base += parseFloat(line.line_net) || 0;
      }
      return Object.values(byCode);
    },

    subtotal() {
      return this.lines.reduce((acc, l) => acc + (parseFloat(l.line_net) || 0), 0).toFixed(2);
    },

    totalTax() {
      let t = 0;
      for (const [cid, a] of Object.entries(this.taxAdj)) {
        const v = a.user !== null ? parseFloat(a.user) : parseFloat(a.calc);
        t += isNaN(v) ? 0 : v;
      }
      return t.toFixed(2);
    },

    grandTotal() {
      return (parseFloat(this.subtotal()) + parseFloat(this.totalTax())).toFixed(2);
    },

    // ── Terms / due-date auto-computation ────────────────────────────────────

    // Called when the customer dropdown changes; auto-fills terms and currency
    // from the customer's defaults (carried in the SmartPicker payload).
    onContactChange(contactId, payload) {
      if (!contactId) return;
      const termCode = this.contactTerms[String(contactId)];
      if (termCode) {
        this.onTermsChange(termCode);
      }
      // Pre-fill invoice currency when the customer has a default currency set.
      const defaultCurrency = (payload || {}).default_currency || "";
      if (defaultCurrency) {
        const sel = document.querySelector('select[name="currency_code"]');
        if (sel) sel.value = defaultCurrency;
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

    // Look up netDays for a term code from the DB-driven paymentTerms list.
    _netDays(termCode) {
      const pt = this.paymentTerms.find(p => p.code === termCode);
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

    // ── AI Memo Assist ───────────────────────────────────────────────────────

    // aiMemoAssist calls the memo-assist API and surfaces the suggestion.
    // Only available when invoiceId > 0 (i.e. editing a saved draft).
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

    // applyMemoSuggestion writes the AI suggestion into the memo input.
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
  };
}

// gobooksCustomerQuickCreate — Alpine component for the inline customer creation
// slide-over panel on the invoice editor page.
//
// Lifecycle:
//   1. Listens for gobooks-picker-create (window-level) emitted by the Customer SmartPicker.
//   2. Opens a slide-over with the typed query pre-filled as the customer name.
//      When multi-currency is enabled (data-currencies has >1 entry), shows a Currency
//      dropdown so the user can set the customer's default invoice currency.
//   3. On save, POSTs to /api/customers/quick-create (name + currency_code) and dispatches
//      gobooks-picker-set-value to the SmartPicker's root element so it auto-selects
//      the newly created customer without reloading the page.
//      The chosen currency is forwarded in the picker payload so onContactChange can
//      pre-fill the invoice's currency select.
function gobooksCustomerQuickCreate() {
  return {
    open:          false,
    name:          "",
    currency:      "",
    currencies:    [],
    nameError:     "",
    currencyError: "",
    formError:     "",
    saving:        false,

    init() {
      // Read available currencies from data-currencies attribute (JSON string array).
      // Set by the templ template when multi-currency is enabled; empty otherwise.
      try {
        const raw = this.$el.dataset.currencies;
        if (raw) {
          this.currencies = JSON.parse(raw);
          if (this.currencies.length > 0) this.currency = this.currencies[0];
        }
      } catch (_) {
        this.currencies = [];
      }
    },

    onPickerCreate(event) {
      const { context, query } = (event.detail || event) || {};
      if (context !== "invoice_editor_customer") return;
      this.name          = (query || "").trim();
      this.nameError     = "";
      this.currencyError = "";
      this.formError     = "";
      this.saving        = false;
      this.open          = true;
      // Reset currency to empty so user must actively choose when multi-currency.
      this.currency = "";
      this.$nextTick(() => {
        if (this.$refs.nameInput) this.$refs.nameInput.focus();
      });
    },

    cancel() {
      this.open = false;
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
      this.saving    = true;
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
        // Programmatically select the new customer in the SmartPicker.
        // Include the chosen currency in the payload so onContactChange can
        // pre-fill the invoice's currency select.
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
        this.open = false;
      } catch (_) {
        this.formError = "Could not create customer. Please try again.";
      } finally {
        this.saving = false;
      }
    },
  };
}
