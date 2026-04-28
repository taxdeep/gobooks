// expense_form.js — Alpine component for the multi-line expense entry form.
// v=4
//
// State read from data-* attributes on the form element:
//   data-base-currency      — company base currency code (e.g. "CAD")
//   data-multi-currency     — "true" | "false"
//   data-expense-accounts   — JSON [{id, code, name}]
//   data-tax-codes          — JSON [{id, code, name, rate}]  rate is fraction e.g. "0.05"
//   data-tasks              — JSON [{id, title, customer_name}]
//   data-products           — JSON [{id, sku, name, kind}]  kind is "stock"|"service"
//   data-initial-lines      — JSON [{expense_account_id, product_service_id, description,
//                                    amount, tax_code_id, line_tax, line_total, task_id,
//                                    is_billable, error}]
//
function balancizExpenseForm() {
  return {
    // ── Config ───────────────────────────────────────────────────────────────
    baseCurrency:  "",
    multiCurrency: false,
    accounts:      [],   // [{id, code, name}]
    taxCodes:      [],   // [{id, code, name, rate}]  rate = fraction string
    tasks:         [],   // [{id, title, customer_name}]
    products:      [],   // [{id, sku, name, kind}]   kind: "stock"|"service"

    // ── State ─────────────────────────────────────────────────────────────────
    currency: "",
    showFX:   false,
    taxAdj:   {},  // keyed by taxCodeId string: { calc: "0.00", user: null }

    // lines: [{expense_account_id, description, amount, tax_code_id, line_tax,
    //          line_total, task_id, is_billable, error}]
    lines: [],

    // ── Init ─────────────────────────────────────────────────────────────────
    init() {
      const el = this.$el;
      this.baseCurrency  = el.dataset.baseCurrency  || "";
      this.multiCurrency = el.dataset.multiCurrency === "true";
      this.accounts      = JSON.parse(el.dataset.expenseAccounts || "[]");
      this.taxCodes      = JSON.parse(el.dataset.taxCodes        || "[]");
      this.tasks         = JSON.parse(el.dataset.tasks           || "[]");
      this.products      = JSON.parse(el.dataset.products        || "[]");

      const initial = JSON.parse(el.dataset.initialLines || "[]");
      if (initial.length > 0) {
        this.lines = initial.map(l => ({
          expense_account_id: String(l.expense_account_id || ""),
          product_service_id: String(l.product_service_id || ""),
          description:        String(l.description || ""),
          // IN.2 Rule #4 fields: authoritative when product_service_id
          // is set; otherwise cosmetic (server falls back to Qty=1,
          // UnitPrice=Amount).
          qty:                String(l.qty || "1"),
          unit_price:         String(l.unit_price || "0.00"),
          amount:             String(l.amount || "0.00"),
          tax_code_id:        String(l.tax_code_id || ""),
          line_tax:           String(l.line_tax || "0.00"),
          line_total:         String(l.line_total || l.amount || "0.00"),
          task_id:            String(l.task_id || ""),
          is_billable:        Boolean(l.is_billable),
          error:              String(l.error || ""),
        }));
      } else {
        this.addLine();
        this.addLine();
      }
      this._recalcAll();

      // Detect initial currency.
      const sel = el.querySelector('select[name="currency_code"]');
      this.currency = sel ? (sel.value || this.baseCurrency) : this.baseCurrency;
      this._syncFX();
    },

    // ── Line management ───────────────────────────────────────────────────────

    addLine() {
      this.lines.push({
        expense_account_id: "",
        product_service_id: "",
        description:        "",
        qty:                "1",
        unit_price:         "0.00",
        amount:             "0.00",
        tax_code_id:        "",
        line_tax:           "0.00",
        line_total:         "0.00",
        task_id:            "",
        is_billable:        false,
        error:              "",
      });
      this._recalcAll();
    },

    removeLine(idx) {
      if (this.lines.length > 1) {
        this.lines.splice(idx, 1);
        this._recalcAll();
      }
    },

    onAccountChange(idx) {
      const line = this.lines[idx];
      if (!line) return;
      if (!line.description.trim()) {
        const acc = this.accounts.find(a => String(a.id) === String(line.expense_account_id));
        if (acc) line.description = acc.name;
      }
      line.error = "";
    },

    // IN.2: when operator picks an item, auto-fill category from the
    // product's inventory_account_id → cogs_account_id chain (same
    // priority as server-side derivePOLineExpenseAccountID) and seed
    // description. Amount becomes qty × unit_price thereafter.
    onProductChange(idx) {
      const line = this.lines[idx];
      if (!line) return;
      if (!line.product_service_id) {
        // Deselect: stop computing amount; let operator type it again.
        line.error = "";
        this._recalcAll();
        return;
      }
      const p = this.products.find(x => String(x.id) === String(line.product_service_id));
      if (!p) return;
      if (!line.description.trim()) line.description = p.name || "";
      if (!line.expense_account_id) {
        if (p.inventory_account_id) {
          line.expense_account_id = String(p.inventory_account_id);
        } else if (p.cogs_account_id) {
          line.expense_account_id = String(p.cogs_account_id);
        }
      }
      if (!line.qty || line.qty === "0") line.qty = "1";
      if (!line.unit_price) line.unit_price = "0.00";
      this._recomputeAmountFromQtyPrice(idx);
      line.error = "";
      this._recalcLine(idx);
      this._recalcAll();
    },

    onQtyOrPriceInput(idx) {
      const line = this.lines[idx];
      if (!line || !line.product_service_id) return;
      this._recomputeAmountFromQtyPrice(idx);
      this._recalcLine(idx);
      this._recalcAll();
    },

    onQtyBlur(idx) {
      const line = this.lines[idx];
      if (!line) return;
      const n = parseFloat(line.qty);
      line.qty = (isNaN(n) || n <= 0) ? "1" : String(n);
      if (line.product_service_id) {
        this._recomputeAmountFromQtyPrice(idx);
        this._recalcLine(idx);
        this._recalcAll();
      }
    },

    onUnitPriceBlur(idx) {
      const line = this.lines[idx];
      if (!line) return;
      const n = parseFloat(line.unit_price);
      line.unit_price = (isNaN(n) || n < 0) ? "0.00" : n.toFixed(2);
      if (line.product_service_id) {
        this._recomputeAmountFromQtyPrice(idx);
        this._recalcLine(idx);
        this._recalcAll();
      }
    },

    _recomputeAmountFromQtyPrice(idx) {
      const line = this.lines[idx];
      if (!line || !line.product_service_id) return;
      const q = parseFloat(line.qty) || 0;
      const u = parseFloat(line.unit_price) || 0;
      line.amount = (q * u).toFixed(2);
    },

    onAmountInput(idx) {
      // When an item is picked, Amount is computed from qty × unit_price;
      // skip the recalc pass so the typed input doesn't fight the
      // computed value.
      const line = this.lines[idx];
      if (line && line.product_service_id) return;
      this._recalcLine(idx);
      this._recalcAll();
    },

    onAmountBlur(idx) {
      const line = this.lines[idx];
      if (!line) return;
      if (line.product_service_id) return; // computed; don't reformat
      const n = parseFloat(line.amount);
      line.amount = (isNaN(n) || n < 0) ? "0.00" : n.toFixed(2);
      this._recalcLine(idx);
      this._recalcAll();
    },

    onTaxCodeChange(idx) {
      this._recalcLine(idx);
      this._recalcAll();
    },

    onLineTaskChange(idx, val) {
      const line = this.lines[idx];
      if (!line) return;
      line.task_id = val;
      if (!val) line.is_billable = false;
    },

    // ── Internal recalculation ────────────────────────────────────────────────

    _recalcLine(idx) {
      const line = this.lines[idx];
      if (!line) return;
      const net = parseFloat(line.amount) || 0;
      const rate = this._taxRate(line.tax_code_id);
      line.line_tax   = (net * rate).toFixed(2);
      line.line_total = (net + net * rate).toFixed(2);
    },

    _recalcAll() {
      for (let i = 0; i < this.lines.length; i++) {
        this._recalcLine(i);
      }
      // Aggregate line taxes by tax code for the adjustment section.
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
        next[cid] = { calc, user: prev ? prev.user : null };
      }
      this.taxAdj = next;
    },

    _taxRate(taxCodeId) {
      if (!taxCodeId) return 0;
      const tc = this.taxCodes.find(t => String(t.id) === String(taxCodeId));
      return tc ? (parseFloat(tc.rate) || 0) : 0;
    },

    // ── Tax adjustment API ────────────────────────────────────────────────────

    taxAdjValue(cid) {
      const a = this.taxAdj[String(cid)];
      if (!a) return "0.00";
      return a.user !== null ? a.user : a.calc;
    },

    onTaxAdjInput(cid, val) {
      const a = this.taxAdj[String(cid)];
      if (!a) return;
      const trimmed = val.trim();
      a.user = (trimmed === "" || trimmed === a.calc) ? null : trimmed;
    },

    // ── Aggregates ────────────────────────────────────────────────────────────

    taxBreakdown() {
      const byCode = {};
      for (const line of this.lines) {
        const cid = String(line.tax_code_id);
        if (!cid) continue;
        if (!byCode[cid]) {
          const tc = this.taxCodes.find(t => String(t.id) === cid);
          if (!tc) continue;
          byCode[cid] = { id: tc.id, code: tc.code, name: tc.name, rate: parseFloat(tc.rate) || 0 };
        }
      }
      return Object.values(byCode);
    },

    subtotal() {
      return this.lines.reduce((acc, l) => acc + (parseFloat(l.amount) || 0), 0).toFixed(2);
    },

    totalTax() {
      let t = 0;
      for (const [, a] of Object.entries(this.taxAdj)) {
        const v = a.user !== null ? parseFloat(a.user) : parseFloat(a.calc);
        t += isNaN(v) ? 0 : v;
      }
      return t.toFixed(2);
    },

    grandTotal() {
      return (parseFloat(this.subtotal()) + parseFloat(this.totalTax())).toFixed(2);
    },

    // ── Header field handlers ─────────────────────────────────────────────────

    onDateChange(_val) {},

    onCurrencyChange(val) {
      this.currency = val;
      this._syncFX();
    },

    // ── SmartPicker event handler ─────────────────────────────────────────────

    onPickerSelect(event) {
      const { context, payload } = event.detail || {};
      if (!payload) return;
      if (context === "expense_form_vendor" && this.multiCurrency) {
        const vendorCurrency = (payload.currency_code || "").trim().toUpperCase();
        if (vendorCurrency) {
          const sel = this.$el.querySelector('select[name="currency_code"]');
          if (sel) {
            const opt = Array.from(sel.options).find(o => o.value === vendorCurrency);
            if (opt) {
              sel.value = vendorCurrency;
              this.currency = vendorCurrency;
              this._syncFX();
            }
          }
        }
      }
    },

    // ── Internal ─────────────────────────────────────────────────────────────

    _syncFX() {
      this.showFX = this.multiCurrency && this.currency !== "" && this.currency !== this.baseCurrency;
    },
  };
}
