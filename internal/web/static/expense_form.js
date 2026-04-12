// expense_form.js — Alpine component for the multi-line expense entry form.
// v=1
//
// State read from data-* attributes on the form element:
//   data-base-currency      — company base currency code (e.g. "CAD")
//   data-multi-currency     — "true" | "false"
//   data-expense-accounts   — JSON [{id, code, name}] for category <select>
//   data-initial-lines      — JSON [{expense_account_id, description, amount, error}]
//
// Events listened to:
//   @gobooks-picker-select  — SmartPicker selection; handles vendor currency coupling.
//
function gobooksExpenseForm() {
  return {
    // ── Config ───────────────────────────────────────────────────────────────
    baseCurrency:   "",
    multiCurrency:  false,
    accounts:       [],  // [{id, code, name}]

    // ── State ─────────────────────────────────────────────────────────────────
    currency:  "",   // currently selected currency_code
    showFX:    false,
    hasTask:   false,

    // lines: [{expense_account_id, description, amount, error}]
    lines: [],

    // ── Init ─────────────────────────────────────────────────────────────────
    init() {
      const el = this.$el;
      this.baseCurrency  = el.dataset.baseCurrency  || "";
      this.multiCurrency = el.dataset.multiCurrency === "true";
      this.accounts      = JSON.parse(el.dataset.expenseAccounts || "[]");

      const initial = JSON.parse(el.dataset.initialLines || "[]");
      if (initial.length > 0) {
        this.lines = initial.map(l => ({
          expense_account_id: String(l.expense_account_id || ""),
          description:        String(l.description || ""),
          amount:             String(l.amount || "0.00"),
          error:              String(l.error || ""),
        }));
      } else {
        this.addLine();
        this.addLine();
      }

      // Detect initial currency from the currency select if present.
      const sel = el.querySelector('select[name="currency_code"]');
      if (sel) {
        this.currency = sel.value || this.baseCurrency;
      } else {
        this.currency = this.baseCurrency;
      }
      this._syncFX();

      // Detect initial task selection.
      const taskSel = el.querySelector('select[name="task_id"]');
      if (taskSel) this.hasTask = taskSel.value !== "";
    },

    // ── Line management ───────────────────────────────────────────────────────

    addLine() {
      this.lines.push({
        expense_account_id: "",
        description:        "",
        amount:             "0.00",
        error:              "",
      });
    },

    removeLine(idx) {
      if (this.lines.length > 1) {
        this.lines.splice(idx, 1);
      }
    },

    onAccountChange(idx) {
      const line = this.lines[idx];
      if (!line) return;
      // Auto-fill description from account name if description is blank.
      if (!line.description.trim()) {
        const acc = this.accounts.find(a => String(a.id) === String(line.expense_account_id));
        if (acc) line.description = acc.name;
      }
      line.error = "";
    },

    onAmountBlur(idx) {
      const line = this.lines[idx];
      if (!line) return;
      const n = parseFloat(line.amount);
      line.amount = (isNaN(n) || n < 0) ? "0.00" : n.toFixed(2);
    },

    recalc() {
      // Triggers totalFormatted() reactivity; no extra work needed.
    },

    // ── Aggregates ────────────────────────────────────────────────────────────

    total() {
      return this.lines.reduce((sum, l) => {
        const n = parseFloat(l.amount);
        return sum + (isNaN(n) ? 0 : n);
      }, 0);
    },

    totalFormatted() {
      return this.total().toFixed(2);
    },

    // ── Header field handlers ─────────────────────────────────────────────────

    onDateChange(_val) {
      // Reserved for future FX date coupling.
    },

    onCurrencyChange(val) {
      this.currency = val;
      this._syncFX();
    },

    onTaskChange(val) {
      this.hasTask = val !== "";
      if (!this.hasTask) {
        const cb = this.$el.querySelector('input[name="is_billable"]');
        if (cb) cb.checked = false;
      }
    },

    // ── SmartPicker event handler ─────────────────────────────────────────────

    onPickerSelect(event) {
      const { context, payload } = event.detail || {};
      if (!payload) return;

      // Vendor selection: auto-set currency if the vendor has a currency_code
      // and multi-currency is enabled.
      if (context === "expense_form_vendor" && this.multiCurrency) {
        const vendorCurrency = (payload.currency_code || "").trim().toUpperCase();
        if (vendorCurrency) {
          const sel = this.$el.querySelector('select[name="currency_code"]');
          if (sel) {
            // Only set if the currency is in the select options.
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

    // ── Internal ──────────────────────────────────────────────────────────────

    _syncFX() {
      this.showFX = this.multiCurrency && this.currency !== "" && this.currency !== this.baseCurrency;
    },
  };
}
