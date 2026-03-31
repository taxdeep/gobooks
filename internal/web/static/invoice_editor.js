// invoice_editor.js — Alpine component for the invoice line-items editor.
// v=3
function invoiceEditor() {
  return {
    lines: [],
    products: [],
    taxCodes: [],   // [{id, code, name, rate}]  rate is a fraction string e.g. "0.05"
    taxAdj: {},     // keyed by taxCodeId (string): { calc: "0.00", user: null }
    terms: "net_30",
    invoiceDate: "",
    dueDate: "",
    dueDateEditable: false,

    init() {
      const el = this.$el;
      this.products  = JSON.parse(el.dataset.products   || "[]");
      this.taxCodes  = JSON.parse(el.dataset.taxCodes   || "[]");
      this.terms     = el.dataset.initialTerms  || "net_30";
      this.invoiceDate = el.dataset.initialDate || "";
      this.dueDate   = el.dataset.initialDueDate || "";
      this.dueDateEditable = this.terms === "custom";

      const initial = JSON.parse(el.dataset.initialLines || "[]");
      if (initial.length > 0) {
        this.lines = initial.map(l => Object.assign({ line_tax: "0.00" }, l));
      } else {
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
      if (!line.description) line.description = ps.name;
      line.unit_price = ps.default_price;
      if (ps.default_tax_code_id) {
        line.tax_code_id = String(ps.default_tax_code_id);
      }
      this._recalcLine(idx);
      this._recalcAll();
    },

    calcLine(idx) {
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

    onTermsChange(val) {
      this.terms = val;
      this.dueDateEditable = val === "custom";
      if (val !== "custom") {
        this.dueDate = this._computeDueDate(this.invoiceDate, val);
      }
    },

    onDateChange(val) {
      this.invoiceDate = val;
      if (this.terms !== "custom") {
        this.dueDate = this._computeDueDate(val, this.terms);
      }
    },

    _computeDueDate(dateStr, terms) {
      const days = { net_15: 15, net_30: 30, net_60: 60, due_on_receipt: 0 }[terms];
      if (days === undefined) return "";
      const d = new Date(dateStr);
      if (isNaN(d.getTime())) return "";
      d.setDate(d.getDate() + days);
      return d.toISOString().slice(0, 10);
    },
  };
}
