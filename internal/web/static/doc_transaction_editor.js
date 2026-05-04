// doc_transaction_editor.js — Alpine factory for "simple" line-item editors.
// v=2
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
        description:           "",
        qty:                   "1",
        unit_price:            "0.00",
        tax_code_id:           "",
        line_net:              "0.00",
        line_tax:              "0.00",
        line_total:            "0.00",
      },
      isLineComplete: (line) =>
        (line.product_service_id || "") !== ""
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
      lockCounterpartyCurrency: false,
      counterpartyCurrencyLocked: false,

      _taxCodesById:   {},
      _productsById:   {},

      init() {
        const el = this.$el;
        this.products = JSON.parse(el.dataset.products || "[]");
        this.taxCodes = JSON.parse(el.dataset.taxCodes || "[]");
        this.baseCurrency = String(el.dataset.baseCurrency || "").trim().toUpperCase();
        this.lockCounterpartyCurrency = el.dataset.lockCounterpartyCurrency === "true";
        this._taxCodesById = Object.fromEntries(this.taxCodes.map(t => [String(t.id), t]));
        this._productsById = Object.fromEntries(this.products.map(p => [String(p.id), p]));
        const currencyField = this._currencyField();
        if (currencyField) {
          this.currencyCode = String(currencyField.value || "").trim().toUpperCase();
        }

        const initial = JSON.parse(el.dataset.initialLines || "[]");
        if (initial.length > 0) {
          this.lines = initial.map(l => Object.assign({}, l));
        } else {
          this.addLine();
        }
        this.assignRowKeys();
        const counterpartyField = el.querySelector('[data-counterparty-currency-source]');
        if (
          this.lockCounterpartyCurrency
          && counterpartyField
          && counterpartyField.value
          && counterpartyField.selectedOptions
          && counterpartyField.selectedOptions[0]
        ) {
          this._applyCounterpartyCurrency(
            counterpartyField.selectedOptions[0].dataset.currency
              || counterpartyField.selectedOptions[0].dataset.defaultCurrency
              || "",
          );
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
        if (!psId) return;
        const ps = this._productsById[String(psId)];
        if (!ps) return;
        const line = this.lines[idx];
        if (!line.description) line.description = ps.description || ps.name;
        line.unit_price = ps.default_price;
        if (ps.default_tax_code_id) {
          line.tax_code_id = String(ps.default_tax_code_id);
        }
        this._recalcAll();
      },

      onCounterpartySelectChange(event) {
        const option = event && event.target ? event.target.selectedOptions[0] : null;
        if (!option) return;
        if (this.lockCounterpartyCurrency && event.target && !event.target.value) {
          this.counterpartyCurrencyLocked = false;
          return;
        }
        this._applyCounterpartyCurrency(option.dataset.currency || option.dataset.defaultCurrency || "");
      },

      onCounterpartyPickerSelect(event) {
        const detail = event.detail || {};
        if (detail.entity !== "customer" && detail.entity !== "vendor") return;
        const payload = detail.payload || {};
        this._applyCounterpartyCurrency(payload.default_currency || payload.currency_code || "");
      },

      onCurrencyFieldChange(value) {
        this.currencyCode = String(value || "").trim().toUpperCase();
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
        field.dispatchEvent(new Event("change", { bubbles: true }));
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
