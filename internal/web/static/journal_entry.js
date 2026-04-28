// Journal Entry — Phase 1 account picker (QuickBooks-style combobox, client-side filter only).
//
// Per-line state (independent for each row):
//   account_id   — submitted value; hidden input lines[idx][account_id]; never the visible label string.
//   acctQuery    — visible text: search while editing; after blur/close, synced to selected label or cleared.
//   acctOpen     — dropdown open/closed.
//   acctHi       — keyboard highlight index within filteredAccounts(line), or -1.
// Global: accounts[] from #balanciz-journal-accounts-data (active accounts only; server-built JSON).
// Filtered list via filteredAccounts(line) — two modes only:
//   • Empty query: MRU recent ids (localStorage, per company) resolved against current accounts first, then
//     the rest in server order; missing/inactive ids skipped; duplicates deduped (seen set).
//   • Non-empty query: relevance first (primaryTier 1–5), then MRU index as tie-break only (first occurrence
//     if an id appears more than once in storage), then name/code. Non-matching accounts omitted.
//
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

  const RECENT_MAX = 8;
  const RECENT_LS_PREFIX = "balanciz:journalRecentAccountIds:v1:";

  function recentStorageKey(companyId) {
    const c = companyId && String(companyId).trim() !== "" ? String(companyId) : "0";
    return RECENT_LS_PREFIX + c;
  }

  const RECENT_RANK_MISSING = 999;

  /** @returns {1|2|3|4|5|null} — lower is better; null = no match */
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

  /**
   * Deterministic search ordering: tier → recent MRU index (smaller = more recent; first list slot wins if
   * duplicate ids in storage) → name/code tie-break. Recent never outranks a better tier.
   * @param {Array<{id:number}>} accounts
   * @param {string} qRaw
   * @param {string[]} recentIds
   */
  function rankSearchResults(accounts, qRaw, recentIds) {
    const q = (qRaw || "").trim();
    if (!q) {
      return accounts.slice();
    }
    const recentIndex = new Map();
    recentIds.forEach((id, i) => {
      const k = String(id);
      if (!recentIndex.has(k)) {
        recentIndex.set(k, i);
      }
    });

    const rows = [];
    for (const a of accounts) {
      const tier = primaryTier(a, q);
      if (tier == null) {
        continue;
      }
      const rr = recentIndex.has(String(a.id)) ? recentIndex.get(String(a.id)) : RECENT_RANK_MISSING;
      rows.push({ a, tier, rr });
    }
    const qLower = q.toLowerCase();
    const numericOnly = /^[0-9]+$/.test(qLower);
    rows.sort((x, y) => {
      if (x.tier !== y.tier) {
        return x.tier - y.tier;
      }
      if (x.rr !== y.rr) {
        return x.rr - y.rr;
      }
      if (numericOnly) {
        return String(x.a.code).localeCompare(String(y.a.code));
      }
      const n = String(x.a.name || "").localeCompare(String(y.a.name || ""));
      if (n !== 0) {
        return n;
      }
      return String(x.a.code).localeCompare(String(y.a.code));
    });
    return rows.map((r) => r.a);
  }

  function highlightSegments(text, qRaw) {
    const t = text || "";
    const qq = (qRaw || "").trim();
    if (!qq) {
      return [{ text: t, em: false }];
    }
    const lower = t.toLowerCase();
    const qLower = qq.toLowerCase();
    const i = lower.indexOf(qLower);
    if (i < 0) {
      return [{ text: t, em: false }];
    }
    const out = [];
    if (i > 0) {
      out.push({ text: t.slice(0, i), em: false });
    }
    out.push({ text: t.slice(i, i + qq.length), em: true });
    if (i + qq.length < t.length) {
      out.push({ text: t.slice(i + qq.length), em: false });
    }
    return out;
  }

  return {
    accounts,
    companyId: "0",
    header: { entry_date: "", journal_no: "" },
    lines: [],
    totals: { debits: 0, credits: 0 },
    difference: 0,
    diffOk: false,
    canSave: false,
    primaryError: "",
    recalcRunning: false,

    formatAccountLabel(a) {
      if (!a) {
        return "";
      }
      return `${a.code} - ${a.name}`;
    },

    accountLabelForId(id) {
      if (!id) {
        return "";
      }
      const a = this.accounts.find((x) => String(x.id) === String(id));
      return a ? this.formatAccountLabel(a) : "";
    },

    loadRecentIds() {
      try {
        const raw = localStorage.getItem(recentStorageKey(this.companyId));
        if (!raw) {
          return [];
        }
        const parsed = JSON.parse(raw);
        if (!Array.isArray(parsed)) {
          return [];
        }
        return parsed.map((id) => String(id));
      } catch (e) {
        return [];
      }
    },

    saveRecentIds(ids) {
      try {
        localStorage.setItem(recentStorageKey(this.companyId), JSON.stringify(ids.slice(0, RECENT_MAX)));
      } catch (e) {}
    },

    /** Empty query: recent MRU first (resolved against current accounts), then remaining in server order. */
    accountsEmptyQueryOrder() {
      const recent = this.loadRecentIds();
      if (!recent.length) {
        return this.accounts.slice();
      }
      const byId = new Map(this.accounts.map((a) => [String(a.id), a]));
      const seen = new Set();
      const out = [];
      for (const rid of recent) {
        const a = byId.get(String(rid));
        if (a && !seen.has(String(a.id))) {
          out.push(a);
          seen.add(String(a.id));
        }
      }
      for (const a of this.accounts) {
        if (!seen.has(String(a.id))) {
          out.push(a);
        }
      }
      return out;
    },

    recordRecentAccountId(accountId) {
      const id = String(accountId);
      if (!id) {
        return;
      }
      const cur = this.loadRecentIds().filter((x) => String(x) !== id);
      cur.unshift(id);
      this.saveRecentIds(cur);
    },

    filteredAccounts(line) {
      const q = line.acctQuery || "";
      if (!q.trim()) {
        return this.accountsEmptyQueryOrder();
      }
      return rankSearchResults(this.accounts, q, this.loadRecentIds());
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
      const id = line.account_id;
      if (id) {
        const cur = this.accountLabelForId(id);
        if (cur !== line.acctQuery) {
          line.account_id = "";
        }
      }
      line.acctOpen = true;
      const list = this.filteredAccounts(line);
      line.acctHi = list.length > 0 ? 0 : -1;
      this.recalc();
      this.persist();
    },

    selectAccount(line, acc) {
      line.account_id = String(acc.id);
      line.acctQuery = this.formatAccountLabel(acc);
      line.acctOpen = false;
      line.acctHi = -1;
      this.recordRecentAccountId(acc.id);
      this.recalc();
      this.persist();
    },

    onAcctKeydown(line, ev) {
      if (!line.acctOpen && (ev.key === "ArrowDown" || ev.key === "ArrowUp")) {
        line.acctOpen = true;
      }
      const list = this.filteredAccounts(line);
      if (!line.acctOpen) {
        return;
      }
      if (ev.key === "Escape") {
        ev.preventDefault();
        this.closeAcctPicker(line);
        return;
      }
      if (ev.key === "ArrowDown") {
        ev.preventDefault();
        if (list.length === 0) {
          return;
        }
        line.acctHi = Math.min(line.acctHi + 1, list.length - 1);
        return;
      }
      if (ev.key === "ArrowUp") {
        ev.preventDefault();
        if (list.length === 0) {
          return;
        }
        line.acctHi = Math.max(line.acctHi - 1, 0);
        return;
      }
      if (ev.key === "Enter") {
        ev.preventDefault();
        if (list.length === 1) {
          this.selectAccount(line, list[0]);
          return;
        }
        if (line.acctHi >= 0 && line.acctHi < list.length) {
          this.selectAccount(line, list[line.acctHi]);
        }
      }
    },

    init() {
      const el = this.$el;
      this.companyId =
        el && el.dataset && el.dataset.companyId != null && String(el.dataset.companyId).trim() !== ""
          ? String(el.dataset.companyId).trim()
          : "0";

      const MAX_LINES = 50;
      const today = new Date().toISOString().slice(0, 10);
      this.header.entry_date = today;

      if (new URLSearchParams(window.location.search).get("saved") === "1") {
        localStorage.removeItem("balanciz:journalDraft:v1");
      }

      const raw = localStorage.getItem("balanciz:journalDraft:v1");
      if (raw) {
        try {
          const d = JSON.parse(raw);
          if (d && d.header && Array.isArray(d.lines)) {
            this.header = { ...this.header, ...d.header };
            this.lines = d.lines.map((l) => this.normalizeLine(l));
            if (this.lines.length > MAX_LINES) {
              this.lines = this.lines.slice(0, MAX_LINES);
            }
          }
        } catch (e) {}
      }

      if (this.lines.length < 2) {
        this.lines = [this.newLine(), this.newLine()];
      }

      this.recalc();
      this.persist();
      this.$watch("header", () => this.persist(), { deep: true });
    },

    persist() {
      const slim = this.lines.map((l) => ({
        key: l.key,
        account_id: l.account_id,
        debit: l.debit,
        credit: l.credit,
        memo: l.memo,
        party: l.party,
        errors: {},
      }));
      localStorage.setItem("balanciz:journalDraft:v1", JSON.stringify({ header: this.header, lines: slim }));
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

    normalizeLine(l) {
      const line = {
        key: l.key || crypto.randomUUID(),
        account_id: l.account_id || "",
        debit: l.debit || "",
        credit: l.credit || "",
        memo: l.memo || "",
        party: l.party || "",
        errors: l.errors || {},
        acctOpen: false,
        acctHi: -1,
        acctQuery: "",
      };
      line.acctQuery = this.accountLabelForId(line.account_id);
      return line;
    },

    addLine() {
      this.lines.push(this.newLine());
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

    onDebitInput(line) {
      if (line.debit && line.debit.trim() !== "") {
        line.credit = "";
      }
      this.recalc();
      this.persist();
    },

    onCreditInput(line) {
      if (line.credit && line.credit.trim() !== "") {
        line.debit = "";
      }
      this.recalc();
      this.persist();
    },

    parseMoney(s) {
      if (!s) {
        return 0;
      }
      const n = Number(String(s).replace(/,/g, ""));
      if (Number.isNaN(n) || n < 0) {
        return null;
      }
      return n;
    },

    formatMoney(n) {
      return (Math.round(n * 100) / 100).toFixed(2);
    },

    recalc() {
      if (this.recalcRunning) {
        return;
      }
      this.recalcRunning = true;
      try {
        let deb = 0;
        let cred = 0;
        let validLines = 0;
        this.primaryError = "";
        this.difference = 0;
        this.diffOk = false;

        for (const line of this.lines) {
          line.errors = {};

          const d = this.parseMoney(line.debit);
          const c = this.parseMoney(line.credit);
          const hasAmount = (d && d > 0) || (c && c > 0);

          if (line.account_id === "" && hasAmount) {
            line.errors.account = "Select an account.";
          }

          if (d === null || c === null) {
            line.errors.amount = "Amounts must be non-negative numbers.";
            continue;
          }

          if (d > 0 && c > 0) {
            line.errors.amount = "Debit and credit cannot both be set.";
            continue;
          }

          if (d > 0) {
            deb += d;
          }
          if (c > 0) {
            cred += c;
          }

          if (line.account_id !== "" && ((d && d > 0) || (c && c > 0))) {
            validLines++;
          }
        }

        this.totals.debits = deb;
        this.totals.credits = cred;

        this.difference = deb - cred;
        this.diffOk = Math.abs(this.difference) < 0.0001;
        this.canSave = this.diffOk && validLines >= 2 && deb > 0;

        if (!this.canSave) {
          if (validLines < 2) {
            this.primaryError = "At least 2 valid lines are required.";
          } else if (!this.diffOk) {
            this.primaryError = "Total debits must equal total credits.";
          }
        }
      } finally {
        this.recalcRunning = false;
      }
    },

    beforeSubmit(e) {
      this.recalc();
      if (!this.canSave) {
        e.preventDefault();
      }
    },
  };
}
