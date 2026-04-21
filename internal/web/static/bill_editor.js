// bill_editor.js — Alpine component for the bill line-items editor.
// v=8  — IN.1: per-line Item picker + Qty/UnitPrice, Rule #4 (Item-Nature Invariant).
function billEditor() {
  return {
    lines: [],
    accounts: [],
    taxCodes: [],       // [{id, code, name, rate}]  rate is a fraction string e.g. "0.05"
    tasks: [],          // [{id, title, customer_name, status}]
    products: [],       // [{id, sku, name, is_stock_item, inventory_account_id, cogs_account_id}]
    paymentTerms: [],   // [{code, netDays}]
    contactTerms: {},   // {"vendorId": "termCode", ...}
    taxAdj: {},         // keyed by taxCodeId (string): { calc: "0.00", user: null }
    terms: "",
    billDate: "",
    dueDate: "",
    dueDateEditable: false,

    init() {
      const el = this.$el;
      this.accounts     = JSON.parse(el.dataset.accounts     || "[]");
      this.taxCodes     = JSON.parse(el.dataset.taxCodes     || "[]");
      this.tasks        = JSON.parse(el.dataset.tasks        || "[]");
      this.products     = JSON.parse(el.dataset.products     || "[]");
      this.paymentTerms = JSON.parse(el.dataset.paymentTerms || "[]");
      this.contactTerms = JSON.parse(el.dataset.contactTerms || "{}");
      this.terms        = el.dataset.initialTerms   || "";
      this.billDate     = el.dataset.initialDate    || "";
      this.dueDate      = el.dataset.initialDueDate || "";
      this.dueDateEditable = this._isEditable(this.terms);

      const initial = JSON.parse(el.dataset.initialLines || "[]");
      if (initial.length > 0) {
        this.lines = initial.map(l => Object.assign(this._defaultLine(), l, { line_tax: l.line_tax || "0.00", error: l.error || "" }));
      } else {
        this.addLine();
      }
      this._recalcAll();
    },

    // ── Line management ──────────────────────────────────────────────────────

    _defaultLine() {
      return {
        product_service_id: "",
        expense_account_id: "",
        description: "",
        qty: "1",
        unit: "",
        unit_price: "0.00",
        task_id: "",
        is_billable: false,
        amount: "0.00",
        tax_code_id: "",
        line_net: "0.00",
        line_tax: "0.00",
        error: "",
      };
    },

    addLine() {
      this.lines.push(this._defaultLine());
      this._recalcAll();
    },

    removeLine(idx) {
      if (this.lines.length > 1) {
        this.lines.splice(idx, 1);
        this._recalcAll();
      }
    },

    onExpenseAccountChange(idx, accountId) {
      const line = this.lines[idx];
      if (!line) return;
      if (!line.description) {
        line.description = this._accountName(accountId);
      }
      this._clearLineError(idx);
    },

    // Item picker change handler (IN.1 / Rule #4).
    // Side effects on item SELECT:
    //   - Pre-fill Description from product name (if empty)
    //   - Pre-fill Category (ExpenseAccountID) from product's
    //     InventoryAccountID → COGSAccountID chain (matches the
    //     derivePOLineExpenseAccountID priority used on the server
    //     for PO→Bill conversion)
    //   - Seed Qty=1 if empty and switch Amount to computed mode
    //     (qty × unit_price) on subsequent input
    // Side effects on item DESELECT:
    //   - Amount becomes editable again (legacy amount-only mode);
    //     Qty/UnitPrice inputs visually disabled per template
    onProductChange(idx, productId) {
      const line = this.lines[idx];
      if (!line) return;
      if (!productId) {
        // Deselect: keep entered Qty/UnitPrice as-is but stop driving
        // Amount from them. If Amount looks like a stale computed
        // value (rounded qty*price), leave it; operator can edit.
        this._clearLineError(idx);
        this._recalcAll();
        return;
      }
      const p = this.products.find(x => String(x.id) === String(productId));
      if (!p) return;
      if (!line.description) line.description = p.name || "";
      // Auto-fill Category if none set. Priority: Inventory → COGS.
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
      this._clearLineError(idx);
      this._recalcAll();
    },

    // Qty / Unit Price input handler. When an Item is picked, Amount
    // is authoritative = qty × unit_price; recompute and re-run line
    // calc so Tax / subtotal stay consistent.
    onQtyOrPriceInput(idx) {
      const line = this.lines[idx];
      if (!line || !line.product_service_id) return;
      line.qty = this._sanitizeDecimalInput(line.qty, 4);
      line.unit_price = this._sanitizeDecimalInput(line.unit_price, 4);
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
      line.unit_price = this._format2dp(line.unit_price);
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

    calcLine(idx) {
      const line = this.lines[idx];
      // When Item is picked, Amount is read-only; don't let the
      // sanitiser clobber the computed value on input events.
      if (!line.product_service_id) {
        line.amount = this._sanitizeDecimalInput(line.amount, 2);
      }
      this._clearLineError(idx);
      this._recalcLine(idx);
      this._recalcAll();
    },

    onAmountBlur(idx) {
      const line = this.lines[idx];
      if (line.product_service_id) return; // computed; no blur reformat
      line.amount = this._format2dp(line.amount);
      this._recalcLine(idx);
      this._recalcAll();
    },

    onTaxCodeChange(idx) {
      this._recalcLine(idx);
      this._recalcAll();
    },

    // Product label used in the <option>, mirrors poLineItemOptionLabel
    // on the server side: "SKU — Name · stock/service".
    productLabel(p) {
      const kind = p.is_stock_item ? "stock" : "service";
      const name = p.sku ? (p.sku + " — " + p.name) : p.name;
      return name + " · " + kind;
    },

    // ── Internal recalculation ───────────────────────────────────────────────

    _recalcLine(idx) {
      const line = this.lines[idx];
      const net = parseFloat(line.amount) || 0;
      line.line_net = net.toFixed(2);

      const rate = this._taxRate(line.tax_code_id);
      line.line_tax = (net * rate).toFixed(2);
    },

    _recalcAll() {
      for (let i = 0; i < this.lines.length; i++) {
        this._recalcLine(i);
      }
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

    _accountName(accountId) {
      if (!accountId) return "";
      const account = this.accounts.find(a => String(a.id) === String(accountId));
      return account ? (account.name || "") : "";
    },

    // Strip non-numeric chars; keep at most one '.'; truncate to maxDp decimal places.
    _sanitizeDecimalInput(val, maxDp) {
      let s = String(val).replace(/[^0-9.]/g, '');
      const firstDot = s.indexOf('.');
      if (firstDot !== -1) {
        s = s.slice(0, firstDot + 1) + s.slice(firstDot + 1).replace(/\./g, '');
        if (s.length - firstDot - 1 > maxDp) {
          s = s.slice(0, firstDot + maxDp + 1);
        }
      }
      return s;
    },

    // Format to exactly 2 decimal places on blur; negative → 0.
    _format2dp(val) {
      const n = parseFloat(val);
      return (isNaN(n) || n < 0) ? '0.00' : n.toFixed(2);
    },

    _clearLineError(idx) {
      const line = this.lines[idx];
      if (!line || !line.error) return;
      if ((line.description || "").trim() !== "") {
        line.error = "";
      }
    },

    // ── Tax adjustment API (called from template inputs) ─────────────────────

    taxAdjValue(cid) {
      const a = this.taxAdj[String(cid)];
      if (!a) return "0.00";
      return a.user !== null ? a.user : a.calc;
    },

    onTaxAdjInput(cid, val) {
      const a = this.taxAdj[String(cid)];
      if (!a) return;
      const trimmed = val.trim();
      if (trimmed === "" || trimmed === a.calc) {
        a.user = null;
      } else {
        a.user = trimmed;
      }
    },

    // ── Aggregates used by the template ─────────────────────────────────────

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

    // Called when the vendor dropdown changes; auto-fills terms from vendor default.
    onContactChange(vendorId) {
      if (!vendorId) return;
      const termCode = this.contactTerms[String(vendorId)];
      if (termCode) {
        this.onTermsChange(termCode);
      }
    },

    onTermsChange(val) {
      this.terms = val;
      this.dueDateEditable = this._isEditable(val);
      if (!this.dueDateEditable) {
        this.dueDate = this._computeDueDate(this.billDate, val);
      }
    },

    onDateChange(val) {
      this.billDate = val;
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
  };
}
