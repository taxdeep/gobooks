// journal_entry_fx.js — Balanciz Journal Entry Alpine component.
// v=4
function balancizJournalEntryDraft() {
  let accounts = [];
  try {
    const el = document.getElementById("balanciz-journal-accounts-data");
    if (el && el.textContent) {
      accounts = JSON.parse(el.textContent);
    }
  } catch (e) {
    accounts = [];
  }

  let currencyOptions = [];
  try {
    const el = document.getElementById("balanciz-journal-currency-options");
    if (el && el.textContent) {
      currencyOptions = JSON.parse(el.textContent);
    }
  } catch (e) {
    currencyOptions = [];
  }

  const RECENT_MAX = 8;
  const MAX_LINES = 50;
  const RECENT_LS_PREFIX = "balanciz:journalRecentAccountIds:v1:";

  function recentStorageKey(companyId) {
    const c = companyId && String(companyId).trim() !== "" ? String(companyId) : "0";
    return RECENT_LS_PREFIX + c;
  }

  function primaryTier(acc, qRaw) {
    const q = (qRaw || "").trim().toLowerCase();
    if (!q) {
      return null;
    }
    const code = (acc.code || "").toLowerCase();
    const name = (acc.name || "").toLowerCase();
    if (code === q) {
      return 1;
    }
    if (code.startsWith(q)) {
      return 2;
    }
    if (code.includes(q)) {
      return 3;
    }
    if (name.startsWith(q)) {
      return 4;
    }
    if (name.includes(q)) {
      return 5;
    }
    return null;
  }

  function rankSearchResults(source, qRaw, recentIds) {
    const q = (qRaw || "").trim();
    if (!q) {
      return source.slice();
    }
    const recentIndex = new Map();
    recentIds.forEach((id, i) => {
      const key = String(id);
      if (!recentIndex.has(key)) {
        recentIndex.set(key, i);
      }
    });
    const rows = [];
    for (const acc of source) {
      const tier = primaryTier(acc, q);
      if (tier == null) {
        continue;
      }
      rows.push({
        acc,
        tier,
        recentRank: recentIndex.has(String(acc.id)) ? recentIndex.get(String(acc.id)) : 999,
      });
    }
    const qLower = q.toLowerCase();
    const numericOnly = /^[0-9]+$/.test(qLower);
    rows.sort((left, right) => {
      if (left.tier !== right.tier) {
        return left.tier - right.tier;
      }
      if (left.recentRank !== right.recentRank) {
        return left.recentRank - right.recentRank;
      }
      if (numericOnly) {
        return String(left.acc.code).localeCompare(String(right.acc.code));
      }
      const byName = String(left.acc.name || "").localeCompare(String(right.acc.name || ""));
      if (byName !== 0) {
        return byName;
      }
      return String(left.acc.code).localeCompare(String(right.acc.code));
    });
    return rows.map((row) => row.acc);
  }

  function highlightSegments(text, qRaw) {
    const value = text || "";
    const query = (qRaw || "").trim();
    if (!query) {
      return [{ text: value, em: false }];
    }
    const lower = value.toLowerCase();
    const qLower = query.toLowerCase();
    const idx = lower.indexOf(qLower);
    if (idx < 0) {
      return [{ text: value, em: false }];
    }
    const segments = [];
    if (idx > 0) {
      segments.push({ text: value.slice(0, idx), em: false });
    }
    segments.push({ text: value.slice(idx, idx + query.length), em: true });
    if (idx + query.length < value.length) {
      segments.push({ text: value.slice(idx + query.length), em: false });
    }
    return segments;
  }

  function roundBank(value, decimals = 2) {
    const factor = Math.pow(10, decimals);
    const scaled = value * factor;
    const sign = scaled < 0 ? -1 : 1;
    const abs = Math.abs(scaled);
    const floor = Math.floor(abs);
    const diff = abs - floor;
    let rounded = 0;
    if (Math.abs(diff - 0.5) < 1e-9) {
      rounded = floor % 2 === 0 ? floor : floor + 1;
    } else {
      rounded = Math.round(abs);
    }
    return (rounded * sign) / factor;
  }

  return {
    accounts,
    currencyOptions,
    companyId: "0",
    baseCurrencyCode: "CAD",
    header: { entry_date: "", journal_no: "", transaction_currency_code: "" },
    fx: {
      snapshot_id: "",
      rate: "1",
      date: "",
      source: "identity",
      sourceLabel: "Identity",
      manual: false,
      loading: false,
    },
    showFXBlock: false,
    lines: [],
    totals: { txDebits: 0, txCredits: 0, baseDebits: 0, baseCredits: 0 },
    difference: 0,
    baseDifference: 0,
    diffOk: false,
    baseDiffOk: false,
    canSave: false,
    primaryError: "",
    recalcRunning: false,
    lastTransactionCurrencyCode: "",
    draftSuffix: "",

    formatAccountLabel(account) {
      if (!account) {
        return "";
      }
      return `${account.code} - ${account.name}`;
    },

    accountLabelForId(id) {
      if (!id) {
        return "";
      }
      const account = this.accounts.find((row) => String(row.id) === String(id));
      return account ? this.formatAccountLabel(account) : "";
    },

    loadRecentIds() {
      try {
        const raw = localStorage.getItem(recentStorageKey(this.companyId));
        if (!raw) {
          return [];
        }
        const parsed = JSON.parse(raw);
        return Array.isArray(parsed) ? parsed.map((id) => String(id)) : [];
      } catch (e) {
        return [];
      }
    },

    saveRecentIds(ids) {
      try {
        localStorage.setItem(recentStorageKey(this.companyId), JSON.stringify(ids.slice(0, RECENT_MAX)));
      } catch (e) {}
    },

    accountsEmptyQueryOrder() {
      const recent = this.loadRecentIds();
      if (!recent.length) {
        return this.accounts.slice();
      }
      const byId = new Map(this.accounts.map((acc) => [String(acc.id), acc]));
      const seen = new Set();
      const ordered = [];
      for (const recentID of recent) {
        const acc = byId.get(String(recentID));
        if (acc && !seen.has(String(acc.id))) {
          ordered.push(acc);
          seen.add(String(acc.id));
        }
      }
      for (const acc of this.accounts) {
        if (!seen.has(String(acc.id))) {
          ordered.push(acc);
        }
      }
      return ordered;
    },

    recordRecentAccountId(accountId) {
      const id = String(accountId);
      if (!id) {
        return;
      }
      const ids = this.loadRecentIds().filter((row) => String(row) !== id);
      ids.unshift(id);
      this.saveRecentIds(ids);
    },

    filteredAccounts(line) {
      const query = line.acctQuery || "";
      if (!query.trim()) {
        return this.accountsEmptyQueryOrder();
      }
      return rankSearchResults(this.accounts, query, this.loadRecentIds());
    },

    highlightSegments,

    openAcctPicker(line) {
      line.acctOpen = true;
      const list = this.filteredAccounts(line);
      line.acctHi = list.length > 0 ? 0 : -1;
    },

    closeAcctPicker(line) {
      line.acctOpen = false;
      line.acctHi = -1;
      line.acctQuery = this.accountLabelForId(line.account_id);
    },

    onAcctBlur(line) {
      setTimeout(() => {
        if (line.acctOpen) {
          line.acctOpen = false;
          line.acctHi = -1;
        }
        line.acctQuery = this.accountLabelForId(line.account_id);
      }, 180);
    },

    onAcctQueryInput(line) {
      if (line.account_id) {
        const current = this.accountLabelForId(line.account_id);
        if (current !== line.acctQuery) {
          line.account_id = "";
        }
      }
      line.acctOpen = true;
      const list = this.filteredAccounts(line);
      line.acctHi = list.length > 0 ? 0 : -1;
      this.recalc();
      this.persist();
    },

    selectAccount(line, account, idx) {
      line.account_id = String(account.id);
      line.acctQuery = this.formatAccountLabel(account);
      line.acctOpen = false;
      line.acctHi = -1;
      this.recordRecentAccountId(account.id);
      this.recalc();
      this.autoGrowIfNeeded(line, idx);
      this.persist();
    },

    onAcctKeydown(line, event, idx) {
      if (!line.acctOpen && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
        line.acctOpen = true;
      }
      const list = this.filteredAccounts(line);
      if (!line.acctOpen) {
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        this.closeAcctPicker(line);
        return;
      }
      if (event.key === "ArrowDown") {
        event.preventDefault();
        if (list.length === 0) {
          return;
        }
        line.acctHi = Math.min(line.acctHi + 1, list.length - 1);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        if (list.length === 0) {
          return;
        }
        line.acctHi = Math.max(line.acctHi - 1, 0);
        return;
      }
      if (event.key === "Enter") {
        event.preventDefault();
        if (list.length === 1) {
          this.selectAccount(line, list[0], idx);
          return;
        }
        if (line.acctHi >= 0 && line.acctHi < list.length) {
          this.selectAccount(line, list[line.acctHi], idx);
        }
      }
    },

    init() {
      const el = this.$el;
      this.companyId =
        el && el.dataset && el.dataset.companyId != null && String(el.dataset.companyId).trim() !== ""
          ? String(el.dataset.companyId).trim()
          : "0";
      this.baseCurrencyCode =
        el && el.dataset && el.dataset.baseCurrency != null && String(el.dataset.baseCurrency).trim() !== ""
          ? String(el.dataset.baseCurrency).trim().toUpperCase()
          : "CAD";
      const defaultCurrency =
        el && el.dataset && el.dataset.defaultCurrency != null && String(el.dataset.defaultCurrency).trim() !== ""
          ? String(el.dataset.defaultCurrency).trim().toUpperCase()
          : this.baseCurrencyCode;
      this.draftSuffix =
        el && el.dataset && el.dataset.draftSuffix != null && String(el.dataset.draftSuffix).trim() !== ""
          ? String(el.dataset.draftSuffix).trim()
          : "";

      const today = new Date().toISOString().slice(0, 10);
      this.header.entry_date = today;
      this.header.transaction_currency_code = defaultCurrency;
      this.fx.date = today;

      if (new URLSearchParams(window.location.search).get("saved") === "1") {
        localStorage.removeItem(this.draftStorageKey());
      }

      const initial = this.loadInitialDraft();
      const usingInitialDraft = !!(initial && initial.header && Array.isArray(initial.lines));
      if (usingInitialDraft) {
        this.header = { ...this.header, ...initial.header };
        this.fx = { ...this.fx, ...(initial.fx || {}) };
        this.lines = initial.lines.map((line) => this.normalizeLine(line));
      } else {
        const raw = localStorage.getItem(this.draftStorageKey());
        if (raw) {
          try {
            const draft = JSON.parse(raw);
            if (draft && draft.header && Array.isArray(draft.lines)) {
              this.header = { ...this.header, ...draft.header };
              this.fx = { ...this.fx, ...(draft.fx || {}) };
              this.lines = draft.lines.map((line) => this.normalizeLine(line));
              if (this.lines.length > MAX_LINES) {
                this.lines = this.lines.slice(0, MAX_LINES);
              }
            }
          } catch (e) {}
        }
      }

      if (this.lines.length > MAX_LINES) {
        this.lines = this.lines.slice(0, MAX_LINES);
      }

      if (this.lines.length < 2) {
        this.lines = [this.newLine(), this.newLine()];
      }
      if (!this.header.transaction_currency_code) {
        this.header.transaction_currency_code = defaultCurrency;
      }
      this.lastTransactionCurrencyCode = this.header.transaction_currency_code;
      this.syncFXMode();
      this.recalc();
      this.persist();
      if (this.showFXBlock && !this.fx.manual && !usingInitialDraft) {
        this.fetchFX(true);
      }
    },

    draftStorageKey() {
      const suffix = this.draftSuffix ? `:${this.draftSuffix}` : "";
      return `balanciz:journalDraft:v2:${this.companyId}${suffix}`;
    },

    loadInitialDraft() {
      try {
        const el = document.getElementById("balanciz-journal-initial-draft");
        if (!el || !el.textContent) {
          return null;
        }
        return JSON.parse(el.textContent);
      } catch (e) {
        return null;
      }
    },

    persist() {
      const slim = this.lines.map((line) => ({
        key: line.key,
        account_id: line.account_id,
        debit: line.debit,
        credit: line.credit,
        memo: line.memo,
        party: line.party,
        errors: {},
      }));
      localStorage.setItem(this.draftStorageKey(), JSON.stringify({ header: this.header, fx: this.fx, lines: slim }));
    },

    newLine() {
      return this.normalizeLine({
        key: crypto.randomUUID(),
        account_id: "",
        debit: "",
        credit: "",
        memo: "",
        party: "",
        errors: {},
      });
    },

    normalizeLine(line) {
      const next = {
        key: line.key || crypto.randomUUID(),
        account_id: line.account_id || "",
        debit: line.debit || "",
        credit: line.credit || "",
        memo: line.memo || "",
        party: line.party || "",
        errors: line.errors || {},
        acctOpen: false,
        acctHi: -1,
        acctQuery: "",
      };
      next.acctQuery = this.accountLabelForId(next.account_id);
      return next;
    },

    addLine() {
      if (this.lines.length >= MAX_LINES) {
        return;
      }
      this.lines.push(this.newLine());
      this.recalc();
      this.persist();
    },

    insertLineBelow(idx) {
      if (this.lines.length >= MAX_LINES) {
        return;
      }
      const pos = Math.max(0, Math.min(this.lines.length, idx + 1));
      this.lines.splice(pos, 0, this.newLine());
      this.recalc();
      this.persist();
    },

    removeLine(idx) {
      if (this.lines.length <= 2) {
        return;
      }
      this.lines.splice(idx, 1);
      this.recalc();
      this.persist();
    },

    lineIndex(line) {
      if (!line || !line.key) {
        return -1;
      }
      return this.lines.findIndex((row) => row.key === line.key);
    },

    lineIsComplete(line) {
      if (!line || line.account_id === "") {
        return false;
      }
      const debit = this.parseMoney(line.debit);
      const credit = this.parseMoney(line.credit);
      return (debit && debit > 0) || (credit && credit > 0);
    },

    autoGrowIfNeeded(line, idx) {
      const pos = Number.isInteger(idx) ? idx : this.lineIndex(line);
      if (pos < 0 || pos !== this.lines.length - 1 || this.lines.length >= MAX_LINES) {
        return;
      }
      if (this.lineIsComplete(line)) {
        this.lines.push(this.newLine());
      }
    },

    onLineTouched(line, idx) {
      this.autoGrowIfNeeded(line, idx);
      this.persist();
    },

    onDebitInput(line, idx) {
      if (line.debit && line.debit.trim() !== "") {
        line.credit = "";
      }
      this.recalc();
      this.autoGrowIfNeeded(line, idx);
      this.persist();
    },

    onCreditInput(line, idx) {
      if (line.credit && line.credit.trim() !== "") {
        line.debit = "";
      }
      this.recalc();
      this.autoGrowIfNeeded(line, idx);
      this.persist();
    },

    onCurrencyChange() {
      const next = String(this.header.transaction_currency_code || "").trim().toUpperCase();
      const prev = String(this.lastTransactionCurrencyCode || this.baseCurrencyCode).trim().toUpperCase();
      if (next === prev) {
        this.syncFXMode();
        return;
      }
      if (this.hasEnteredAmounts()) {
        const confirmed = window.confirm(
          `Changing the journal currency from ${prev} to ${next} will clear all entered debit and credit amounts. Continue?`,
        );
        if (!confirmed) {
          this.header.transaction_currency_code = prev;
          this.syncFXMode();
          this.persist();
          return;
        }
        this.clearAmounts();
      }
      this.lastTransactionCurrencyCode = next;
      this.syncFXMode();
      if (this.showFXBlock && !this.fx.manual) {
        this.fetchFX(true);
      }
      this.recalc();
      this.persist();
    },

    // onDateChange is called when the JE Date field changes.
    // In non-manual FX mode the effective date tracks the entry date, so when
    // the date changes we re-sync fx.date and re-fetch the stored rate for the
    // new date (local-first; provider fetch on miss, same as currency change).
    onDateChange() {
      if (!this.showFXBlock || this.fx.manual) {
        this.persist();
        return;
      }
      // Keep FX effective date in sync with the JE date.
      const newDate = String(this.header.entry_date || "").trim();
      if (newDate) {
        this.fx.date = newDate;
      }
      // Clear the stale snapshot so canSave is false while the fetch is in flight.
      // This mirrors onCurrencyChange() → syncFXMode() which also clears snapshot_id.
      this.fx.snapshot_id = "";
      this.recalc();
      // Re-fetch: local-first with provider fallback on miss (allow_provider_fetch=1).
      this.fetchFX(true);
      this.persist();
    },

    syncFXMode() {
      this.showFXBlock = this.header.transaction_currency_code !== this.baseCurrencyCode;
      if (!this.showFXBlock) {
        this.fx = {
          snapshot_id: "",
          rate: "1",
          date: this.header.entry_date || new Date().toISOString().slice(0, 10),
          source: "identity",
          sourceLabel: "Identity",
          manual: false,
          loading: false,
        };
      } else {
        this.fx.date = this.fx.date || this.header.entry_date || new Date().toISOString().slice(0, 10);
        this.fx.source = this.fx.source || "system_stored";
        this.fx.sourceLabel = this.fx.sourceLabel || "Stored";
      }
    },

    hasEnteredAmounts() {
      return this.lines.some((line) => {
        const debit = this.parseMoney(line.debit);
        const credit = this.parseMoney(line.credit);
        return (debit && debit > 0) || (credit && credit > 0);
      });
    },

    clearAmounts() {
      for (const line of this.lines) {
        line.debit = "";
        line.credit = "";
      }
    },

    fxSummary() {
      if (!this.showFXBlock) {
        return "";
      }
      const rate = this.fx.rate && String(this.fx.rate).trim() !== "" ? String(this.fx.rate) : "0.00000000";
      return `1 ${this.header.transaction_currency_code} = ${rate} ${this.baseCurrencyCode}`;
    },

    // onRateInput — called when the user edits the inline rate field directly.
    // Marks the rate as manual and suppresses future auto-fetches until Refresh.
    onRateInput() {
      this.fx.manual = true;
      this.fx.source = "manual";
      this.fx.sourceLabel = "Manual";
      this.fx.snapshot_id = "";
      this.recalc();
      this.persist();
    },

    toggleManualFX() {
      if (!this.showFXBlock) {
        return;
      }
      if (this.fx.manual) {
        this.fx.manual = false;
        this.fetchFX(true);
        return;
      }
      this.fx.manual = true;
      this.fx.snapshot_id = "";
      this.fx.source = "manual";
      this.fx.sourceLabel = "Manual";
      this.fx.date = this.fx.date || this.header.entry_date || new Date().toISOString().slice(0, 10);
      if (!this.fx.rate || String(this.fx.rate).trim() === "" || Number(this.fx.rate) <= 0) {
        this.fx.rate = "1.00000000";
      }
      this.recalc();
      this.persist();
    },

    async refreshFX() {
      if (!this.showFXBlock) {
        return;
      }
      this.fx.manual = false;
      await this.fetchFX(true);
    },

    async fetchFX(allowProviderFetch) {
      if (!this.showFXBlock) {
        return;
      }
      this.fx.loading = true;
      this.primaryError = "";
      try {
        const params = new URLSearchParams({
          transaction_currency_code: this.header.transaction_currency_code,
          date: this.header.entry_date || new Date().toISOString().slice(0, 10),
          allow_provider_fetch: allowProviderFetch ? "1" : "0",
        });
        const resp = await fetch(`/api/exchange-rate?${params.toString()}`, {
          headers: { Accept: "application/json" },
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          this.primaryError = data.error || "Could not load exchange rate.";
          return;
        }
        this.fx.snapshot_id = data.snapshot_id ? String(data.snapshot_id) : "";
        this.fx.rate = String(data.exchange_rate || "");
        this.fx.date = String(data.exchange_rate_date || this.header.entry_date || "");
        this.fx.source = String(data.exchange_rate_source || "system_stored");
        this.fx.sourceLabel = String(data.source_label || "Stored");
        this.fx.manual = false;
      } catch (e) {
        this.primaryError = "Could not load exchange rate.";
      } finally {
        this.fx.loading = false;
        this.recalc();
        this.persist();
      }
    },

    parseMoney(value) {
      if (!value) {
        return 0;
      }
      const parsed = Number(String(value).replace(/,/g, ""));
      if (Number.isNaN(parsed) || parsed < 0) {
        return null;
      }
      return parsed;
    },

    formatMoney(value) {
      return (Math.round(value * 100) / 100).toFixed(2);
    },

    effectiveRate() {
      if (!this.showFXBlock) {
        return 1;
      }
      const rate = Number(String(this.fx.rate || "").replace(/,/g, ""));
      if (Number.isNaN(rate) || rate <= 0) {
        return null;
      }
      return rate;
    },

    recalc() {
      if (this.recalcRunning) {
        return;
      }
      this.recalcRunning = true;
      try {
        let txDebits = 0;
        let txCredits = 0;
        let baseDebits = 0;
        let baseCredits = 0;
        let validLines = 0;
        this.primaryError = "";
        this.difference = 0;
        this.baseDifference = 0;
        this.diffOk = false;
        this.baseDiffOk = false;
        const rate = this.effectiveRate();
        const fxReady =
          !this.showFXBlock ||
          (rate !== null &&
            !!this.fx.date &&
            (this.fx.manual || (this.fx.snapshot_id && String(this.fx.snapshot_id).trim() !== "")));

        for (const line of this.lines) {
          line.errors = {};
          const debit = this.parseMoney(line.debit);
          const credit = this.parseMoney(line.credit);
          const hasAmount = (debit && debit > 0) || (credit && credit > 0);

          if (line.account_id === "" && hasAmount) {
            line.errors.account = "Select an account.";
          }
          if (debit === null || credit === null) {
            line.errors.amount = "Amounts must be non-negative numbers.";
            continue;
          }
          if (debit > 0 && credit > 0) {
            line.errors.amount = "Debit and credit cannot both be set.";
            continue;
          }
          if (debit > 0) {
            txDebits += debit;
            if (rate !== null) {
              baseDebits += roundBank(debit * rate, 2);
            }
          }
          if (credit > 0) {
            txCredits += credit;
            if (rate !== null) {
              baseCredits += roundBank(credit * rate, 2);
            }
          }
          if (line.account_id !== "" && ((debit && debit > 0) || (credit && credit > 0))) {
            validLines++;
          }
        }

        this.totals.txDebits = txDebits;
        this.totals.txCredits = txCredits;
        this.totals.baseDebits = baseDebits;
        this.totals.baseCredits = baseCredits;
        this.difference = txDebits - txCredits;
        this.baseDifference = baseDebits - baseCredits;
        this.diffOk = Math.abs(this.difference) < 0.0001;
        this.baseDiffOk = Math.abs(this.baseDifference) < 0.0001;
        this.canSave = fxReady && this.diffOk && this.baseDiffOk && validLines >= 2 && txDebits > 0;

        if (!this.canSave) {
          if (validLines < 2) {
            this.primaryError = "At least 2 valid lines are required.";
          } else if (!fxReady) {
            this.primaryError = "Load a stored exchange-rate snapshot or enter a manual override before saving.";
          } else if (!this.diffOk) {
            this.primaryError = "Total debits must equal total credits.";
          } else if (!this.baseDiffOk) {
            this.primaryError =
              "Converted base totals do not balance exactly under the current exchange rate. Phase 1 blocks save instead of adding an auto-rounding line.";
          }
        }
      } finally {
        this.recalcRunning = false;
      }
    },

    beforeSubmit(event) {
      this.recalc();
      if (!this.canSave) {
        event.preventDefault();
      }
    },
  };
}
