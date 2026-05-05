// doc_transaction_editor.js — Alpine factory for "simple" line-item editors.
// v=7
//
// Shared by Quote, Sales Order, Purchase Order, Bill, Expense — every
// transaction-document editor whose totals are a plain
// subtotal + per-line-tax (no per-tax-code adjustments, no
// terms-driven due-date, no review-mode lock, no contact-override
// prompts). Invoice has its own dedicated factory because of those
// extra concerns.
//
// Composes balancizLineItems() for line-array management (addLine /
// removeLine / insertLineBelow / auto-grow) and adds:
//   • products / taxCodes catalogue (from data-* attrs)
//   • per-row recalc on qty / price / tax_code change
//   • cached subtotal / tax / grand-total strings
//   • onProductChange(idx, id) auto-fill from picker payload
//
// The form's underscore-suffix field naming (line_qty_0, line_price_0,
// line_tax_0, ...) is preserved so the existing parseDocumentLines()
// handler keeps working unchanged across all five editors.
function docTransactionEditor() {
  return Object.assign(
    balancizLineItems({
      defaults: {
        product_service_id:    "",
        product_service_label: "",
        product_service_code:  "",
        expense_account_id:    "",
        account_code:          "",
        account_name:          "",
        account_label:         "",
        description:           "",
        qty:                   "1",
        unit_price:            "0.00",
        tax_code_id:           "",
        line_net:              "0.00",
        line_tax:              "0.00",
        line_total:            "0.00",
      },
      isLineComplete: (line) =>
        ((line.product_service_id || "") !== "" || (line.expense_account_id || "") !== "")
        && (line.qty || "").trim()        !== ""
        && (line.unit_price || "").trim() !== "",
    }),
    {
      products:        [],
      taxCodes:        [],   // [{id, code, rate}]  rate is a fraction string e.g. "0.05"
      subtotalStr:     "0.00",
      totalTaxStr:     "0.00",
      grandTotalStr:   "0.00",
      baseCurrency:    "",
      currencyCode:    "",
      exchangeRate:    "",
      exchangeRateHint: "",
      exchangeRateManual: false,
      exchangeRateFetchSeq: 0,
      lockCounterpartyCurrency: false,
      counterpartyCurrencyLocked: false,
      counterpartyCurrencies: {},
      counterpartyLabel: "counterparty",
      exchangeRateDateOffsetDays: 0,

      _taxCodesById:   {},
      _productsById:   {},

      init() {
        const el = this.$el;
        this.products = JSON.parse(el.dataset.products || "[]");
        this.taxCodes = JSON.parse(el.dataset.taxCodes || "[]");
        this.baseCurrency = String(el.dataset.baseCurrency || "").trim().toUpperCase();
        this.counterpartyCurrencies = this._parseJSON(el.dataset.counterpartyCurrencies || "{}", {});
        this.lockCounterpartyCurrency = el.dataset.lockCounterpartyCurrency === "true";
        this.counterpartyLabel = String(el.dataset.counterpartyLabel || "counterparty").trim().toLowerCase() || "counterparty";
        this.exchangeRateDateOffsetDays = parseInt(el.dataset.exchangeRateDateOffsetDays || "0", 10) || 0;
        this._taxCodesById = Object.fromEntries(this.taxCodes.map(t => [String(t.id), t]));
        this._productsById = Object.fromEntries(this.products.map(p => [String(p.id), p]));
        const currencyField = this._currencyField();
        if (currencyField) {
          this.currencyCode = String(currencyField.value || el.dataset.initialCurrency || "").trim().toUpperCase();
          if (currencyField.value !== this.currencyCode) {
            currencyField.value = this.currencyCode;
          }
        }
        const exchangeRateField = this._exchangeRateField();
        if (exchangeRateField) {
          this.exchangeRate = String(exchangeRateField.value || "").trim();
          this.exchangeRateManual = this.exchangeRate !== "" && !this._isIdentityRate(this.exchangeRate);
        }

        const initial = JSON.parse(el.dataset.initialLines || "[]");
        if (initial.length > 0) {
          this.lines = initial.map(l => Object.assign({}, l));
        } else {
          this.addLine();
        }
        this.assignRowKeys();
        const counterpartyField = el.querySelector('[data-counterparty-currency-source]');
        if (counterpartyField) {
          if (typeof counterpartyField.addEventListener === "function") {
            counterpartyField.addEventListener("change", (event) => this.onCounterpartySelectChange(event));
          }
          if (this.lockCounterpartyCurrency) {
            this._syncCounterpartyCurrencyFromField(counterpartyField);
            if (!this.currencyCode) {
              if (this.$nextTick) {
                this.$nextTick(() => this._syncCounterpartyCurrencyFromField(counterpartyField));
              } else {
                setTimeout(() => this._syncCounterpartyCurrencyFromField(counterpartyField), 0);
              }
            }
          } else if (this.currencyCode) {
            this.onCurrencyFieldChange(this.currencyCode, { forceLookup: false });
          }
        } else if (this.currencyCode) {
          this.onCurrencyFieldChange(this.currencyCode, { forceLookup: false });
        }
        this._recalcAll();
      },

      onLinesChange() {
        this._recalcAll();
      },

      // Fired when the per-row Item picker emits balanciz-item-picker-select.
      // Auto-fills description / unit price / tax code from the chosen
      // ProductService, then recomputes totals.
      onProductChange(idx, psId) {
        const line = this.lines[idx];
        if (!line) return;
        if (!psId) {
          line.product_service_code = "";
          line.expense_account_id = "";
          line.account_code = "";
          line.account_name = "";
          line.account_label = "";
          this._recalcAll();
          return;
        }
        const ps = this._productsById[String(psId)];
        if (!ps) return;
        if (!line.description) line.description = ps.description || ps.name;
        line.unit_price = ps.default_price;
        line.product_service_code = ps.item_code || line.product_service_code || "";
        line.expense_account_id = ps.expense_account_id || "";
        line.account_code = ps.account_code || "";
        line.account_name = ps.account_name || "";
        line.account_label = this._accountLabel(line.account_code, line.account_name);
        if (ps.default_tax_code_id) {
          line.tax_code_id = String(ps.default_tax_code_id);
        }
        this._recalcAll();
      },

      onCounterpartySelectChange(event) {
        const option = event && event.target ? event.target.selectedOptions[0] : null;
        if (this.lockCounterpartyCurrency && event.target && !event.target.value) {
          this.counterpartyCurrencyLocked = false;
          this.currencyCode = "";
          this.exchangeRate = "";
          this.exchangeRateHint = "";
          this.exchangeRateManual = false;
          return;
        }
        this._applyCounterpartyCurrency(this._counterpartyCurrencyFromSelect(event ? event.target : null, option));
      },

      onCounterpartyPickerSelect(event) {
        const detail = event.detail || {};
        if (detail.entity !== "customer" && detail.entity !== "vendor") return;
        const payload = detail.payload || {};
        this._applyCounterpartyCurrency(payload.default_currency || payload.currency_code || "");
      },

      onCurrencyFieldChange(value, opts) {
        opts = opts || {};
        this.currencyCode = String(value || "").trim().toUpperCase();
        this.exchangeRateManual = false;
        if (!this.isForeignCurrency()) {
          this.exchangeRate = "1.000000";
          this.exchangeRateHint = "";
          return;
        }
        this.lookupExchangeRate({ force: opts.forceLookup === true });
      },

      onDocumentDateChange() {
        if (this.isForeignCurrency()) {
          this.exchangeRateManual = false;
          this.lookupExchangeRate({ force: true });
        }
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
        return this.currencyCode !== "" && this.baseCurrency !== "" && this.currencyCode !== this.baseCurrency;
      },

      currencyRateLeftLabel() {
        const currency = this.currencyCode || this.baseCurrency || "";
        return currency ? "1 " + currency : "Select " + this.counterpartyLabel;
      },

      async lookupExchangeRate(options) {
        const opts = options || {};
        if (!this.isForeignCurrency()) return;
        if (this.exchangeRateManual && !opts.force) return;
        const seq = ++this.exchangeRateFetchSeq;
        const params = new URLSearchParams({
          transaction_currency_code: this.currencyCode,
          date: this._exchangeRateLookupDateValue(),
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

      calcLine(idx) {
        const line = this.lines[idx];
        line.qty        = this._sanitizeDecimalInput(line.qty, 4);
        line.unit_price = this._sanitizeDecimalInput(line.unit_price, 4);
        this._recalcAll();
      },

      onQtyBlur(idx) {
        this.lines[idx].qty = this._format2dp(this.lines[idx].qty);
        this._recalcAll();
      },

      onPriceBlur(idx) {
        this.lines[idx].unit_price = this._format2dp(this.lines[idx].unit_price);
        this._recalcAll();
        this._autoGrowIfComplete(idx);
      },

      onTaxCodeChange(idx) {
        this._recalcAll();
      },

      _recalcLine(idx) {
        const line = this.lines[idx];
        const qty   = parseFloat(line.qty)        || 0;
        const price = parseFloat(line.unit_price) || 0;
        const net   = qty * price;
        line.line_net = net.toFixed(2);
        const rate = this._taxRate(line.tax_code_id);
        const tax  = net * rate;
        line.line_tax   = tax.toFixed(2);
        line.line_total = (net + tax).toFixed(2);
      },

      _recalcAll() {
        let subtotal = 0;
        let totalTax = 0;
        for (let i = 0; i < this.lines.length; i++) {
          this._recalcLine(i);
          subtotal += parseFloat(this.lines[i].line_net) || 0;
          totalTax += parseFloat(this.lines[i].line_tax) || 0;
        }
        this.subtotalStr   = subtotal.toFixed(2);
        this.totalTaxStr   = totalTax.toFixed(2);
        this.grandTotalStr = (subtotal + totalTax).toFixed(2);
      },

      _taxRate(taxCodeId) {
        if (!taxCodeId) return 0;
        const tc = this._taxCodesById[String(taxCodeId)];
        if (!tc) return 0;
        return parseFloat(tc.rate) || 0;
      },

      _applyCounterpartyCurrency(raw) {
        let currency = String(raw || "").trim().toUpperCase();
        if (!currency && this.lockCounterpartyCurrency) currency = this.baseCurrency;
        if (!currency) return;
        const field = this._currencyField();
        if (!field) return;
        if (field.tagName === "SELECT") {
          const option = Array.from(field.options).find(o => o.value.toUpperCase() === currency);
          if (!option) return;
          field.value = option.value;
          this.currencyCode = option.value;
        } else {
          field.value = currency;
          this.currencyCode = currency;
        }
        if (this.lockCounterpartyCurrency) {
          this.counterpartyCurrencyLocked = true;
        }
        this.exchangeRateManual = false;
        this.onCurrencyFieldChange(this.currencyCode, { forceLookup: true });
        field.dispatchEvent(new Event("change", { bubbles: true }));
      },

      _syncCounterpartyCurrencyFromField(field) {
        if (!field || !field.value) return;
        this._applyCounterpartyCurrency(this._counterpartyCurrencyFromSelect(field, field.selectedOptions ? field.selectedOptions[0] : null));
      },

      _counterpartyCurrencyFromSelect(field, option) {
        let currency = "";
        if (option && option.dataset) {
          currency = option.dataset.currency || option.dataset.defaultCurrency || "";
        }
        if (!currency && field && field.value) {
          currency = this.counterpartyCurrencies[String(field.value)] || "";
        }
        return currency;
      },

      _currencyField() {
        if (!this.$el) return null;
        if (typeof this.$el.querySelectorAll === "function") {
          const fields = Array.from(this.$el.querySelectorAll('[name="currency_code"]'));
          const visible = fields.find(f => String(f.type || "").toLowerCase() !== "hidden");
          if (visible) return visible;
          if (fields.length > 0) return fields[0];
        }
        return this.$el.querySelector ? this.$el.querySelector('[name="currency_code"]') : null;
      },

      _exchangeRateField() {
        if (!this.$el || !this.$el.querySelector) return null;
        return this.$el.querySelector('[name="exchange_rate"]');
      },

      _parseJSON(raw, fallback) {
        try {
          const parsed = JSON.parse(raw || "");
          return parsed && typeof parsed === "object" ? parsed : fallback;
        } catch (_) {
          return fallback;
        }
      },

      _documentDateValue() {
        if (!this.$el || !this.$el.querySelector) return "";
        const field = this.$el.querySelector('[name="po_date"], [name="bill_date"], [name="invoice_date"], [name="quote_date"], [name="order_date"]');
        return field ? String(field.value || "").trim() : "";
      },

      _exchangeRateLookupDateValue() {
        const raw = this._documentDateValue();
        if (!raw || !this.exchangeRateDateOffsetDays) return raw;
        const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw);
        if (!match) return raw;
        const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
        date.setUTCDate(date.getUTCDate() + this.exchangeRateDateOffsetDays);
        return date.toISOString().slice(0, 10);
      },

      _isIdentityRate(value) {
        const n = parseFloat(value);
        return !isNaN(n) && Math.abs(n - 1) < 0.00000001;
      },

      _accountLabel(code, name) {
        code = String(code || "").trim();
        name = String(name || "").trim();
        if (code && name) return code + " " + name;
        return code || name;
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
        return negative ? (s !== '' ? '-' + s : '-') : s;
      },

      _format2dp(val) {
        const n = parseFloat(val);
        return isNaN(n) ? '0.00' : n.toFixed(2);
      },
    },
  );
}
