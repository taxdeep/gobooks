// bill_editor.js — Alpine component for the bill line-items editor.
// v=9  - IN.1 item lines plus SmartPicker category combobox.
function billEditor() {
  return {
    lines: [],
    accounts: [],
    taxCodes: [],       // [{id, code, name, rate}]  rate is a fraction string e.g. "0.05"
    tasks: [],          // [{id, title, customer_name, status}]
    products: [],       // [{id, sku, name, is_stock_item, inventory_account_id, cogs_account_id}]
    paymentTerms: [],   // [{code, netDays}]
    contactTerms: {},   // {"vendorId": "termCode", ...}
    baseCurrency: "",
    currency: "",
    exchangeRate: "",
    exchangeRateHint: "",
    exchangeRateManual: false,
    exchangeRateFetchSeq: 0,
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
      this.baseCurrency = String(el.dataset.baseCurrency || "").trim().toUpperCase();
      this.currency     = String(el.dataset.initialCurrency || "").trim().toUpperCase();
      this.exchangeRate = String(el.dataset.initialExchangeRate || "").trim();
      this.exchangeRateManual = this.exchangeRate !== "";
      this.dueDateEditable = this._isEditable(this.terms);

      const initial = JSON.parse(el.dataset.initialLines || "[]");
      if (initial.length > 0) {
        this.lines = initial.map(l => this._normalizeLine(l));
      } else {
        this.addLine();
      }
      for (let i = 0; i < this.lines.length; i++) {
        this._syncItemCategory(i, { initializing: true });
      }
      this._recalcAll();
      this.lookupExchangeRate();
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
        category_query: "",
        category_source: "",
        category_open: false,
        category_loading: false,
        category_failed: false,
        category_results: [],
        category_highlighted: -1,
        category_fetch_seq: 0,
      };
    },

    _normalizeLine(raw) {
      const line = Object.assign(this._defaultLine(), raw || {}, {
        line_tax: raw && raw.line_tax ? raw.line_tax : "0.00",
        error: raw && raw.error ? raw.error : "",
      });
      line.product_service_id = line.product_service_id ? String(line.product_service_id) : "";
      line.expense_account_id = line.expense_account_id ? String(line.expense_account_id) : "";
      line.category_query = line.category_query || this._accountLabel(line.expense_account_id);
      line.category_source = line.expense_account_id ? "manual" : "";
      return line;
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

    isCategoryLocked(idx) {
      const line = this.lines[idx];
      return Boolean(line && this._productLinkedAccountID(line.product_service_id));
    },

    onCategoryFocus(idx) {
      const line = this.lines[idx];
      if (!line || this.isCategoryLocked(idx)) return;
      line.category_open = true;
      if (!line.category_results || line.category_results.length === 0) {
        this._fetchCategoryResults(idx);
      }
    },

    onCategoryInput(idx) {
      const line = this.lines[idx];
      if (!line || this.isCategoryLocked(idx)) {
        this._syncCategoryQuery(idx);
        return;
      }
      const committedLabel = this._accountLabel(line.expense_account_id);
      if ((line.category_query || "") !== committedLabel) {
        line.expense_account_id = "";
        line.category_source = "";
      }
      line.category_open = true;
      line.category_highlighted = -1;
      if (line._categoryDebounce) clearTimeout(line._categoryDebounce);
      line._categoryDebounce = setTimeout(() => this._fetchCategoryResults(idx), 250);
    },

    onCategoryKeydown(idx, event) {
      if (this.isCategoryLocked(idx)) return;
      switch (event.key) {
        case "Escape":
          this.closeCategoryPicker(idx);
          break;
        case "ArrowDown":
          event.preventDefault();
          this.moveCategoryHighlight(idx, 1);
          break;
        case "ArrowUp":
          event.preventDefault();
          this.moveCategoryHighlight(idx, -1);
          break;
        case "Enter": {
          const line = this.lines[idx];
          if (line && line.category_highlighted >= 0 && line.category_results[line.category_highlighted]) {
            event.preventDefault();
            this.selectCategory(idx, line.category_results[line.category_highlighted], line.category_highlighted);
          }
          break;
        }
      }
    },

    moveCategoryHighlight(idx, delta) {
      const line = this.lines[idx];
      if (!line || this.isCategoryLocked(idx)) return;
      line.category_open = true;
      if (!line.category_results || line.category_results.length === 0) return;
      const current = line.category_highlighted < 0 ? (delta > 0 ? -1 : 0) : line.category_highlighted;
      const next = current + delta;
      line.category_highlighted = Math.max(0, Math.min(line.category_results.length - 1, next));
    },

    selectCategory(idx, item, resultIndex) {
      const line = this.lines[idx];
      if (!line || this.isCategoryLocked(idx) || !item) return;
      const selectedQuery = line.category_query || "";
      line.expense_account_id = String(item.id);
      line.category_query = this.categoryCandidateLabel(item);
      line.category_source = "manual";
      line.category_open = false;
      line.category_highlighted = -1;
      this.onExpenseAccountChange(idx, item.id);
      this._sendCategoryUsage("select", {
        query: selectedQuery,
        selected_entity_id: String(item.id),
        item_id: String(item.id),
        rank_position: typeof resultIndex === "number" ? resultIndex + 1 : null,
        result_count: line.category_results ? line.category_results.length : null,
        request_id: line.category_request_id || "",
      });
    },

    closeCategoryPicker(idx) {
      const line = this.lines[idx];
      if (!line) return;
      line.category_open = false;
      line.category_highlighted = -1;
      this._syncCategoryQuery(idx);
    },

    categoryCandidateLabel(item) {
      if (!item) return "";
      const code = String(item.secondary || "").trim();
      const name = String(item.primary || "").trim();
      return code && name ? code + " " + name : (name || code);
    },

    async _fetchCategoryResults(idx) {
      const line = this.lines[idx];
      if (!line || this.isCategoryLocked(idx)) return;
      const seq = (line.category_fetch_seq || 0) + 1;
      line.category_fetch_seq = seq;
      line.category_loading = true;
      line.category_failed = false;
      const requestID = this._newPickerRequestId();
      line.category_request_id = requestID;
      try {
        const params = new URLSearchParams({
          entity: "account",
          context: "expense_form_category",
          q: line.category_query || "",
          limit: "20",
          request_id: requestID,
        });
        const fetchFn = window.balancizFetch || fetch;
        const resp = await fetchFn("/api/smart-picker/search?" + params.toString());
        const data = await resp.json();
        if (seq !== line.category_fetch_seq) return;
        if (!resp.ok) {
          line.category_failed = true;
          line.category_results = [];
          return;
        }
        line.category_results = Array.isArray(data.candidates) ? data.candidates : [];
        line.category_highlighted = -1;
        this._sendCategorySearchUsage(line.category_query || "", line.category_results.length, requestID);
      } catch (_) {
        if (seq !== line.category_fetch_seq) return;
        line.category_failed = true;
        line.category_results = [];
      } finally {
        if (seq === line.category_fetch_seq) line.category_loading = false;
      }
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
        if (line.category_source === "item") {
          this._setCategory(line, "", "");
        }
        this._clearLineError(idx);
        this._recalcAll();
        return;
      }
      const p = this.products.find(x => String(x.id) === String(productId));
      if (!p) return;
      if (!line.description) line.description = p.name || "";
      // Lock Category to the selected item's configured account.
      this._syncItemCategory(idx);
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

    _accountLabel(accountId) {
      if (!accountId) return "";
      const account = this.accounts.find(a => String(a.id) === String(accountId));
      if (!account) return "";
      const code = String(account.code || "").trim();
      const name = String(account.name || "").trim();
      return code && name ? code + " " + name : (name || code);
    },

    _productLinkedAccountID(productId) {
      if (!productId) return "";
      const product = this.products.find(p => String(p.id) === String(productId));
      if (!product) return "";
      if (product.inventory_account_id) return String(product.inventory_account_id);
      if (product.cogs_account_id) return String(product.cogs_account_id);
      return "";
    },

    _syncItemCategory(idx, opts) {
      const line = this.lines[idx];
      if (!line) return;
      opts = opts || {};
      const linkedAccountID = this._productLinkedAccountID(line.product_service_id);
      if (linkedAccountID) {
        this._setCategory(line, linkedAccountID, "item");
        return;
      }
      if (!opts.initializing && line.category_source === "item") {
        this._setCategory(line, "", "");
        return;
      }
      this._syncCategoryQuery(idx);
    },

    _setCategory(line, accountId, source) {
      line.expense_account_id = accountId ? String(accountId) : "";
      line.category_source = source || "";
      line.category_query = this._accountLabel(line.expense_account_id);
      line.category_open = false;
      line.category_loading = false;
      line.category_failed = false;
      line.category_results = [];
      line.category_highlighted = -1;
    },

    _syncCategoryQuery(idx) {
      const line = this.lines[idx];
      if (!line) return;
      const label = this._accountLabel(line.expense_account_id);
      if (line.expense_account_id && label) {
        line.category_query = label;
      } else if (line.expense_account_id && !label) {
        line.category_query = "";
      } else if (!line.expense_account_id) {
        line.category_query = "";
      }
    },

    _sendCategorySearchUsage(query, resultCount, requestID) {
      const now = Date.now();
      const key = ["account", "expense_form_category", query, resultCount].join("|");
      if (key !== this._lastCategorySearchUsageKey || now - (this._lastCategorySearchUsageAt || 0) > 1000) {
        this._lastCategorySearchUsageKey = key;
        this._lastCategorySearchUsageAt = now;
        this._sendCategoryUsage("search", { query: query, result_count: resultCount, request_id: requestID || "" });
      }
      if (query && resultCount === 0) {
        this._sendCategoryUsage("no_match", { query: query, result_count: 0, request_id: requestID || "" });
      }
    },

    _sendCategoryUsage(eventType, extra) {
      const fetchFn = window.balancizFetch || fetch;
      const payload = Object.assign({
        entity: "account",
        entity_type: "account",
        context: "expense_form_category",
        event_type: eventType,
        source_route: window.location ? window.location.pathname : "",
      }, extra || {});
      fetchFn("/api/smart-picker/usage", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(payload),
      }).catch(() => {});
    },

    _newPickerRequestId() {
      if (window.crypto && typeof window.crypto.randomUUID === "function") {
        return window.crypto.randomUUID();
      }
      return "sp-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
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

    onPickerSelect(event) {
      const detail = event.detail || {};
      if (detail.context !== "bill.vendor_picker") return;
      this.onContactChange(detail.id, detail.payload || {});
    },

    // Called when the vendor picker changes; auto-fills terms / currency from vendor defaults.
    onContactChange(vendorId, payload) {
      if (!vendorId) return;
      const p = payload || {};
      const termCode = p.payment_term || this.contactTerms[String(vendorId)];
      if (termCode) {
        this.contactTerms[String(vendorId)] = termCode;
        this.onTermsChange(termCode);
      }
      const vendorCurrency = String(p.currency_code || "").trim().toUpperCase();
      if (vendorCurrency) {
        const sel = this.$el.querySelector('select[name="currency_code"]');
        if (sel && Array.from(sel.options).some(o => o.value === vendorCurrency)) {
          sel.value = vendorCurrency;
          this.onCurrencyChange(vendorCurrency);
        }
      }
    },

    onCurrencyChange(value) {
      this.currency = String(value || "").trim().toUpperCase();
      this.exchangeRateManual = false;
      if (!this.isForeignCurrency()) {
        this.exchangeRate = "";
        this.exchangeRateHint = "";
        return;
      }
      this.lookupExchangeRate({ force: true });
    },

    onExchangeRateInput() {
      this.exchangeRateManual = String(this.exchangeRate || "").trim() !== "";
      if (!this.exchangeRateManual && this.isForeignCurrency()) {
        this.lookupExchangeRate({ force: true });
      } else {
        this.exchangeRateHint = "";
      }
    },

    isForeignCurrency() {
      const selected = this.effectiveCurrency();
      return selected !== "" && this.baseCurrency !== "" && selected !== this.baseCurrency;
    },

    effectiveCurrency() {
      return String(this.currency || this.baseCurrency || "").trim().toUpperCase();
    },

    async lookupExchangeRate(options) {
      const opts = options || {};
      if (!this.isForeignCurrency()) return;
      if (this.exchangeRateManual && !opts.force) return;
      const seq = ++this.exchangeRateFetchSeq;
      const params = new URLSearchParams({
        transaction_currency_code: this.effectiveCurrency(),
        date: this.billDate || "",
        allow_provider_fetch: "1",
      });
      this.exchangeRateHint = "Looking up rate...";
      try {
        const fetchFn = window.balancizFetch || fetch;
        const resp = await fetchFn("/api/exchange-rate?" + params.toString(), {
          credentials: "same-origin",
          headers: { Accept: "application/json" },
        });
        const data = await resp.json();
        if (seq !== this.exchangeRateFetchSeq) return;
        if (!resp.ok) {
          this.exchangeRate = "";
          this.exchangeRateHint = data && data.error ? data.error : "No exchange rate found.";
          return;
        }
        this.exchangeRate = String(data.exchange_rate || "");
        const rateDate = data.exchange_rate_date ? " for " + data.exchange_rate_date : "";
        const label = data.source_label || data.exchange_rate_source || "rate table";
        this.exchangeRateHint = "Auto-filled from " + label + rateDate + ".";
      } catch (_) {
        if (seq !== this.exchangeRateFetchSeq) return;
        this.exchangeRate = "";
        this.exchangeRateHint = "Could not look up exchange rate.";
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
      this.lookupExchangeRate();
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

function balancizVendorQuickCreate() {
  return {
    drawerOpen: false,
    name: "",
    email: "",
    phone: "",
    address: "",
    paymentTerm: "",
    currency: "",
    currencies: [],
    notes: "",
    nameError: "",
    currencyError: "",
    formError: "",
    saving: false,

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
      const detail = event.detail || {};
      if (detail.context !== "bill.vendor_picker") return;
      this.name = (detail.query || "").trim();
      this.email = "";
      this.phone = "";
      this.address = "";
      this.paymentTerm = "";
      this.currency = "";
      this.notes = "";
      this.nameError = "";
      this.currencyError = "";
      this.formError = "";
      this.saving = false;
      this.drawerOpen = true;
      this.$nextTick(() => {
        if (this.$refs.nameInput) this.$refs.nameInput.focus();
      });
    },

    cancel() {
      this.drawerOpen = false;
    },

    async save() {
      const name = this.name.trim();
      this.nameError = "";
      this.currencyError = "";
      this.formError = "";
      let hasErr = false;
      if (!name) {
        this.nameError = "Vendor name is required.";
        hasErr = true;
      }
      if (this.currencies.length > 1 && !this.currency) {
        this.currencyError = "Currency is required.";
        hasErr = true;
      }
      if (hasErr) return;
      this.saving = true;
      try {
        const fetchFn = window.balancizFetch || fetch;
        const body = {
          name,
          email: this.email.trim(),
          phone: this.phone.trim(),
          address: this.address.trim(),
          payment_term: this.paymentTerm,
          notes: this.notes.trim(),
        };
        if (this.currency) {
          body.currency_code = this.currency;
        }
        const resp = await fetchFn("/api/vendors/quick-create", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          this.formError = data.error || "Could not create vendor.";
          return;
        }
        const pickerEl = document.querySelector('[data-context="bill.vendor_picker"]');
        if (pickerEl) {
          pickerEl.dispatchEvent(new CustomEvent("balanciz-picker-set-value", {
            detail: {
              id: String(data.id),
              label: data.name,
              payload: {
                currency_code: data.currency_code || "",
                payment_term: data.payment_term || "",
              },
            },
            bubbles: false,
          }));
        }
        this.drawerOpen = false;
      } catch (_) {
        this.formError = "Could not create vendor. Please try again.";
      } finally {
        this.saving = false;
      }
    },
  };
}
