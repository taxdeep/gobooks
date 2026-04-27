import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    gobooksFetch?: (url: string, options?: RequestInit) => Promise<Response>;
    gbUnsavedWork?: {
      set: (key: string, state: "saving" | "unsaved" | "error", message?: string) => void;
      clear: (key: string) => void;
    };
  }
}

type Candidate = {
  id: string;
  line_id: number;
  journal_entry_id: number;
  date: string;
  type: string;
  source_type: string;
  source_id: number;
  reference: string;
  payee: string;
  memo: string;
  amount: string;
  payment: string;
  deposit: string;
  is_payment: boolean;
  is_deposit: boolean;
  detail_url: string;
  is_reversal: boolean;
  is_reversal_pair: boolean;
  reversal_pair_key?: string;
};

type SuggestionSignal = {
  name: string;
  detail: string;
  stars: string;
};

type Suggestion = {
  id: number;
  status: string;
  type_label: string;
  confidence_pct: string;
  summary: string;
  net_amount: string;
  line_ids: number[];
  journal_nos: string[];
  signals: SuggestionSignal[];
};

type WorkspacePayload = {
  account_id: string;
  account_name: string;
  statement_date: string;
  ending_balance: string;
  beginning_balance: string;
  candidates: Candidate[];
  selected_line_ids: string[];
  suggestions: Suggestion[];
  save_draft_url: string;
  save_progress_url: string;
  finish_url: string;
  auto_match_url: string;
  accept_suggestion_url: string;
  reject_suggestion_url: string;
};

type ViewMode = "all" | "payments" | "deposits" | "split";
type SortKey = "date" | "type" | "reference" | "payee" | "memo" | "payment" | "deposit";

function classNames(...parts: Array<string | false | null | undefined>) {
  return parts.filter(Boolean).join(" ");
}

function parseMoney(value: string): number {
  const cleaned = String(value || "").replace(/[^\d.-]/g, "");
  const n = Number(cleaned);
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: number): string {
  return value.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function compactDate(value: string): string {
  const parts = value.split("-");
  if (parts.length !== 3) return value;
  return `${Number(parts[2])}/${Number(parts[1])}/${parts[0].slice(-2)}`;
}

async function saveDraft(payload: WorkspacePayload, selected: string[]) {
  const fetchFn = window.gobooksFetch || fetch;
  const response = await fetchFn(payload.save_draft_url, {
    method: "POST",
    credentials: "same-origin",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({
      account_id: payload.account_id,
      statement_date: payload.statement_date,
      ending_balance: payload.ending_balance,
      selected_line_ids: selected,
    }),
  });
  if (!response.ok) throw new Error(`Save failed with ${response.status}`);
}

function postForm(action: string, fields: Record<string, string | number | Array<string | number>>) {
  const form = document.createElement("form");
  form.method = "post";
  form.action = action;
  form.style.display = "none";
  Object.entries(fields).forEach(([name, value]) => {
    const values = Array.isArray(value) ? value : [value];
    values.forEach((v) => {
      const input = document.createElement("input");
      input.type = "hidden";
      input.name = name;
      input.value = String(v);
      form.appendChild(input);
    });
  });
  document.body.appendChild(form);
  form.requestSubmit();
}

function useSortedCandidates(candidates: Candidate[], sortKey: SortKey, sortAsc: boolean) {
  return useMemo(() => {
    const sorted = [...candidates];
    sorted.sort((a, b) => {
      const av = sortValue(a, sortKey);
      const bv = sortValue(b, sortKey);
      const cmp = typeof av === "number" && typeof bv === "number" ? av - bv : String(av).localeCompare(String(bv));
      return sortAsc ? cmp : -cmp;
    });
    return sorted;
  }, [candidates, sortKey, sortAsc]);
}

function sortValue(candidate: Candidate, key: SortKey): string | number {
  switch (key) {
    case "payment":
      return parseMoney(candidate.payment);
    case "deposit":
      return parseMoney(candidate.deposit);
    case "type":
      return candidate.type;
    case "reference":
      return candidate.reference;
    case "payee":
      return candidate.payee;
    case "memo":
      return candidate.memo;
    default:
      return candidate.date;
  }
}

function BankReconcileWorkspace({ payload }: { payload: WorkspacePayload }) {
  const [selected, setSelected] = useState<Set<string>>(() => new Set(payload.selected_line_ids || []));
  const [viewMode, setViewMode] = useState<ViewMode>("all");
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [showReversals, setShowReversals] = useState(false);
  const [sortKey, setSortKey] = useState<SortKey>("date");
  const [sortAsc, setSortAsc] = useState(true);
  const [active, setActive] = useState<Candidate | null>(null);
  const [saveState, setSaveState] = useState<"saved" | "saving" | "unsaved" | "error">("saved");
  const firstSave = useRef(true);

  const candidates = payload.candidates || [];
  const baseCandidates = useMemo(() => filterCandidates(candidates, query, typeFilter, showReversals), [candidates, query, typeFilter, showReversals]);
  const visibleCandidates = useMemo(() => {
    if (viewMode === "payments") return baseCandidates.filter((candidate) => candidate.is_payment);
    if (viewMode === "deposits") return baseCandidates.filter((candidate) => candidate.is_deposit);
    return baseCandidates;
  }, [baseCandidates, viewMode]);
  const sortedVisible = useSortedCandidates(visibleCandidates, sortKey, sortAsc);
  const sortedPayments = useSortedCandidates(baseCandidates.filter((candidate) => candidate.is_payment), sortKey, sortAsc);
  const sortedDeposits = useSortedCandidates(baseCandidates.filter((candidate) => candidate.is_deposit), sortKey, sortAsc);

  const selectedIDs = useMemo(() => Array.from(selected), [selected]);
  const selectedCandidates = useMemo(() => candidates.filter((candidate) => selected.has(candidate.id)), [candidates, selected]);
  const selectedNet = selectedCandidates.reduce((sum, candidate) => sum + parseMoney(candidate.amount), 0);
  const selectedPayments = selectedCandidates.reduce((sum, candidate) => sum + parseMoney(candidate.payment), 0);
  const selectedDeposits = selectedCandidates.reduce((sum, candidate) => sum + parseMoney(candidate.deposit), 0);
  const beginning = parseMoney(payload.beginning_balance);
  const ending = parseMoney(payload.ending_balance);
  const cleared = beginning + selectedNet;
  const difference = ending - cleared;
  const diffOk = Math.abs(difference) < 0.005;

  useEffect(() => {
    if (firstSave.current) {
      firstSave.current = false;
      return;
    }
    setSaveState("unsaved");
    const t = window.setTimeout(() => {
      void runSave("auto");
    }, 800);
    return () => window.clearTimeout(t);
  }, [selectedIDs.join(",")]);

  useEffect(() => {
    if (!window.gbUnsavedWork) return;
    const key = "bank-reconcile";
    if (saveState === "saved") {
      window.gbUnsavedWork.clear(key);
      return;
    }
    const message = saveState === "saving"
      ? "Your bank reconciliation is still saving. Please wait before switching company."
      : "Your bank reconciliation has unsaved changes. Please save before switching company.";
    window.gbUnsavedWork.set(key, saveState, message);
    return () => window.gbUnsavedWork?.clear(key);
  }, [saveState]);

  const runSave = async (_reason: "auto" | "manual" | "navigate") => {
    setSaveState("saving");
    try {
      await saveDraft(payload, selectedIDs);
      setSaveState("saved");
      return true;
    } catch {
      setSaveState("error");
      return false;
    }
  };

  const toggleCandidate = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const markRows = (rows: Candidate[], checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      rows.forEach((row) => {
        if (checked) next.add(row.id);
        else next.delete(row.id);
      });
      return next;
    });
  };

  const openRecord = async (candidate: Candidate) => {
    if (!candidate.detail_url) return;
    const ok = await runSave("navigate");
    if (ok) window.location.assign(candidate.detail_url);
  };

  const submitFinish = async () => {
    if (!diffOk || selectedIDs.length === 0) return;
    const ok = await runSave("manual");
    if (!ok) return;
    postForm(payload.finish_url, {
      account_id: payload.account_id,
      statement_date: payload.statement_date,
      ending_balance: payload.ending_balance,
      line_ids: selectedIDs,
    });
  };

  const submitAutoMatch = async () => {
    const ok = await runSave("manual");
    if (!ok) return;
    postForm(payload.auto_match_url, {
      account_id: payload.account_id,
      statement_date: payload.statement_date,
      ending_balance: payload.ending_balance,
    });
  };

  const submitSuggestion = async (suggestionID: number, action: "accept" | "reject") => {
    const ok = await runSave("manual");
    if (!ok) return;
    postForm(action === "accept" ? payload.accept_suggestion_url : payload.reject_suggestion_url, {
      suggestion_id: suggestionID,
      account_id: payload.account_id,
      statement_date: payload.statement_date,
      ending_balance: payload.ending_balance,
    });
  };

  const typeOptions = Array.from(new Set(candidates.map((candidate) => candidate.type).filter(Boolean))).sort();

  return (
    <div className="mt-6 space-y-4" data-react-bank-reconcile-ready="true">
      <section className="sticky top-16 z-20 rounded-lg border border-border bg-surface/95 px-4 py-3 shadow-sm backdrop-blur">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="text-section font-semibold text-text">{payload.account_name}</h2>
              <span className="rounded-md bg-background px-2 py-0.5 text-[11px] font-semibold text-text-muted2">Statement {payload.statement_date}</span>
              <a href={`/banking/reconcile?account_id=${payload.account_id}`} className="text-small text-primary hover:underline">Edit info</a>
            </div>
            <div className="mt-1 text-small text-text-muted2">Draft autosaves before you open a source record.</div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <StatusPill state={saveState} />
            <button type="button" onClick={() => void runSave("manual")} className="rounded-md border border-border-input px-3 py-1.5 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">Save</button>
            <button type="button" onClick={submitAutoMatch} className="rounded-md border border-border-input px-3 py-1.5 text-small font-semibold text-primary hover:bg-background">Auto Match</button>
            <button
              type="button"
              onClick={submitFinish}
              disabled={!diffOk || selectedIDs.length === 0 || saveState === "saving"}
              className={classNames(
                "rounded-md px-3 py-1.5 text-small font-semibold",
                diffOk && selectedIDs.length > 0 ? "bg-primary text-onPrimary hover:bg-primary-hover" : "cursor-not-allowed bg-disabled-bg text-disabled-text",
              )}
            >
              Finish Now
            </button>
          </div>
        </div>
        <SummaryGrid
          ending={ending}
          beginning={beginning}
          payments={selectedPayments}
          deposits={selectedDeposits}
          cleared={cleared}
          difference={difference}
        />
      </section>

      <Toolbar
        viewMode={viewMode}
        setViewMode={setViewMode}
        query={query}
        setQuery={setQuery}
        typeFilter={typeFilter}
        setTypeFilter={setTypeFilter}
        typeOptions={typeOptions}
        showReversals={showReversals}
        setShowReversals={setShowReversals}
        markAll={() => markRows(viewMode === "split" ? baseCandidates : sortedVisible, true)}
        unmarkAll={() => markRows(viewMode === "split" ? baseCandidates : sortedVisible, false)}
        visibleCount={viewMode === "split" ? baseCandidates.length : sortedVisible.length}
      />

      <SuggestionsPanel suggestions={payload.suggestions || []} onAction={submitSuggestion} />

      {viewMode === "split" ? (
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <TransactionPane
            title="Cheques and Payments"
            rows={sortedPayments}
            selected={selected}
            sortKey={sortKey}
            sortAsc={sortAsc}
            setSort={setSortKeyWithDirection(setSortKey, setSortAsc, sortKey, sortAsc)}
            onToggle={toggleCandidate}
            onOpen={openRecord}
            onInspect={setActive}
            compact
          />
          <TransactionPane
            title="Deposits and Other Credits"
            rows={sortedDeposits}
            selected={selected}
            sortKey={sortKey}
            sortAsc={sortAsc}
            setSort={setSortKeyWithDirection(setSortKey, setSortAsc, sortKey, sortAsc)}
            onToggle={toggleCandidate}
            onOpen={openRecord}
            onInspect={setActive}
            compact
          />
        </div>
      ) : (
        <TransactionPane
          title="Unreconciled Transactions"
          rows={sortedVisible}
          selected={selected}
          sortKey={sortKey}
          sortAsc={sortAsc}
          setSort={setSortKeyWithDirection(setSortKey, setSortAsc, sortKey, sortAsc)}
          onToggle={toggleCandidate}
          onOpen={openRecord}
          onInspect={setActive}
        />
      )}

      <DetailDrawer candidate={active} onClose={() => setActive(null)} onOpen={openRecord} />
    </div>
  );
}

function filterCandidates(candidates: Candidate[], query: string, typeFilter: string, showReversals: boolean) {
  const q = query.trim().toLowerCase();
  return candidates.filter((candidate) => {
    if (!showReversals && candidate.is_reversal_pair) return false;
    if (typeFilter && candidate.type !== typeFilter) return false;
    if (!q) return true;
    return [candidate.date, candidate.type, candidate.reference, candidate.payee, candidate.memo, candidate.payment, candidate.deposit]
      .join(" ")
      .toLowerCase()
      .includes(q);
  });
}

function setSortKeyWithDirection(
  setSortKey: (key: SortKey) => void,
  setSortAsc: (value: boolean | ((old: boolean) => boolean)) => void,
  currentKey: SortKey,
  currentAsc: boolean,
) {
  return (key: SortKey) => {
    if (currentKey === key) setSortAsc(!currentAsc);
    else {
      setSortKey(key);
      setSortAsc(true);
    }
  };
}

function StatusPill({ state }: { state: "saved" | "saving" | "unsaved" | "error" }) {
  const label = state === "saving" ? "Saving" : state === "unsaved" ? "Unsaved" : state === "error" ? "Save failed" : "Saved";
  const cls = state === "error" ? "bg-danger-soft text-danger-hover" : state === "unsaved" ? "bg-warning-soft text-warning-hover" : "bg-background text-text-muted2";
  return <span className={classNames("rounded-full px-2.5 py-1 text-[11px] font-semibold", cls)}>{label}</span>;
}

function SummaryGrid({
  ending,
  beginning,
  payments,
  deposits,
  cleared,
  difference,
}: {
  ending: number;
  beginning: number;
  payments: number;
  deposits: number;
  cleared: number;
  difference: number;
}) {
  const items = [
    ["Ending", ending, "text-text"],
    ["Beginning", beginning, "text-text"],
    ["Payments", payments, "text-danger-hover"],
    ["Deposits", deposits, "text-success-hover"],
    ["Cleared", cleared, "text-text"],
    ["Difference", difference, Math.abs(difference) < 0.005 ? "text-success-hover" : "text-danger-hover"],
  ];
  return (
    <div className="mt-3 grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-6">
      {items.map(([label, value, cls]) => (
        <div key={String(label)} className="rounded-md border border-border-subtle bg-background px-3 py-2">
          <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{label}</div>
          <div className={classNames("mt-1 font-mono text-small font-semibold tabular-nums", String(cls))}>{fmt(Number(value))}</div>
        </div>
      ))}
    </div>
  );
}

function Toolbar({
  viewMode,
  setViewMode,
  query,
  setQuery,
  typeFilter,
  setTypeFilter,
  typeOptions,
  showReversals,
  setShowReversals,
  markAll,
  unmarkAll,
  visibleCount,
}: {
  viewMode: ViewMode;
  setViewMode: (mode: ViewMode) => void;
  query: string;
  setQuery: (value: string) => void;
  typeFilter: string;
  setTypeFilter: (value: string) => void;
  typeOptions: string[];
  showReversals: boolean;
  setShowReversals: (value: boolean) => void;
  markAll: () => void;
  unmarkAll: () => void;
  visibleCount: number;
}) {
  return (
    <section className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">View</div>
          <div className="mt-1 inline-flex rounded-md border border-border-input bg-background p-0.5">
            {(["all", "payments", "deposits", "split"] as ViewMode[]).map((mode) => (
              <button
                key={mode}
                type="button"
                onClick={() => setViewMode(mode)}
                className={classNames(
                  "rounded px-3 py-1.5 text-body font-semibold capitalize",
                  viewMode === mode ? "bg-primary text-onPrimary" : "text-text-muted2 hover:text-text",
                )}
              >
                {mode}
              </button>
            ))}
          </div>
        </div>
        <label className="min-w-[260px] flex-1">
          <span className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Search</span>
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Ref, payee, memo, amount..."
            className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
          />
        </label>
        <label className="w-44">
          <span className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Type</span>
          <select
            value={typeFilter}
            onChange={(event) => setTypeFilter(event.target.value)}
            className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
          >
            <option value="">All types</option>
            {typeOptions.map((type) => <option key={type} value={type}>{type}</option>)}
          </select>
        </label>
        <label className="flex items-center gap-2 rounded-md border border-border-subtle px-3 py-2 text-small text-text-muted2">
          <input type="checkbox" checked={showReversals} onChange={(event) => setShowReversals(event.target.checked)} className="h-4 w-4 rounded border-border-input text-primary" />
          Show reversals
        </label>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <span className="text-body font-medium text-text-muted2">{visibleCount} visible</span>
          <button type="button" onClick={markAll} className="rounded-md border border-border-input px-3 py-2 text-body font-semibold text-text-muted3 hover:bg-background hover:text-text">Mark visible</button>
          <button type="button" onClick={unmarkAll} className="rounded-md border border-border-input px-3 py-2 text-body font-semibold text-text-muted3 hover:bg-background hover:text-text">Unmark visible</button>
        </div>
      </div>
    </section>
  );
}

function SuggestionsPanel({ suggestions, onAction }: { suggestions: Suggestion[]; onAction: (id: number, action: "accept" | "reject") => void }) {
  if (!suggestions.length) return null;
  return (
    <section className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <div>
          <div className="text-section font-semibold text-text">Auto-match suggestions</div>
          <div className="text-small text-text-muted2">Accepting a suggestion selects its proposed lines; finishing still requires backend validation.</div>
        </div>
        <span className="rounded-full bg-primary-soft px-2.5 py-1 text-small font-semibold text-primary">{suggestions.length}</span>
      </div>
      <div className="divide-y divide-border-subtle">
        {suggestions.map((suggestion) => (
          <details key={suggestion.id} className="px-4 py-3">
            <summary className="cursor-pointer list-none">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="rounded-full bg-background px-2 py-0.5 text-[11px] font-semibold text-text-muted2">{suggestion.type_label}</span>
                    <span className="rounded-full bg-primary-soft px-2 py-0.5 text-[11px] font-semibold text-primary">{suggestion.confidence_pct}</span>
                    <span className="font-semibold text-text">{suggestion.summary}</span>
                  </div>
                  <div className="mt-1 text-small text-text-muted2">Refs {suggestion.journal_nos.join(", ") || "-"} - Net {suggestion.net_amount}</div>
                </div>
                <div className="flex items-center gap-2">
                  {suggestion.status === "pending" ? (
                    <>
                      <button type="button" onClick={(event) => { event.preventDefault(); onAction(suggestion.id, "accept"); }} className="rounded-md bg-primary px-3 py-1.5 text-small font-semibold text-onPrimary hover:bg-primary-hover">Accept</button>
                      <button type="button" onClick={(event) => { event.preventDefault(); onAction(suggestion.id, "reject"); }} className="rounded-md border border-border-input px-3 py-1.5 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">Reject</button>
                    </>
                  ) : (
                    <span className="rounded-full bg-background px-2.5 py-1 text-small font-semibold capitalize text-text-muted2">{suggestion.status}</span>
                  )}
                </div>
              </div>
            </summary>
            {suggestion.signals.length > 0 ? (
              <div className="mt-3 grid gap-2 rounded-md border border-border-subtle bg-background p-3 text-small">
                {suggestion.signals.map((signal) => (
                  <div key={`${suggestion.id}-${signal.name}`} className="flex min-w-0 gap-3">
                    <span className="w-32 shrink-0 text-text-muted">{signal.name}</span>
                    <span className="shrink-0 text-primary">{signal.stars}</span>
                    <span className="min-w-0 text-text-muted2">{signal.detail}</span>
                  </div>
                ))}
              </div>
            ) : null}
          </details>
        ))}
      </div>
    </section>
  );
}

function TransactionPane({
  title,
  rows,
  selected,
  sortKey,
  sortAsc,
  setSort,
  onToggle,
  onOpen,
  onInspect,
  compact,
}: {
  title: string;
  rows: Candidate[];
  selected: Set<string>;
  sortKey: SortKey;
  sortAsc: boolean;
  setSort: (key: SortKey) => void;
  onToggle: (id: string) => void;
  onOpen: (candidate: Candidate) => void;
  onInspect: (candidate: Candidate) => void;
  compact?: boolean;
}) {
  return (
    <section className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <div>
          <div className="text-title font-semibold text-text">{title}</div>
          <div className="text-body text-text-muted2">{rows.length} transaction(s)</div>
        </div>
      </div>
      <div className="max-h-[62vh] overflow-auto">
        <table className="min-w-full table-fixed text-left text-[11px]">
          <thead className="sticky top-0 z-10 bg-background text-body font-bold uppercase tracking-wider text-text-muted2">
            <tr className="border-b border-border">
              <Header label="Date" sortKey="date" current={sortKey} asc={sortAsc} onSort={setSort} className="w-24" />
              {!compact ? <Header label="Type" sortKey="type" current={sortKey} asc={sortAsc} onSort={setSort} className="w-28" /> : null}
              <Header label="Ref No." sortKey="reference" current={sortKey} asc={sortAsc} onSort={setSort} className="w-40" />
              <Header label="Payee" sortKey="payee" current={sortKey} asc={sortAsc} onSort={setSort} className="w-44" />
              {!compact ? <Header label="Memo" sortKey="memo" current={sortKey} asc={sortAsc} onSort={setSort} className="w-[32rem]" /> : null}
              <Header label="Payment" sortKey="payment" current={sortKey} asc={sortAsc} onSort={setSort} className="w-40 text-right" />
              <Header label="Deposit" sortKey="deposit" current={sortKey} asc={sortAsc} onSort={setSort} className="w-40 text-right" />
              <th className="w-16 px-2 py-2 text-center">Cleared</th>
            </tr>
          </thead>
          <tbody className="text-text">
            {rows.length === 0 ? (
              <tr><td colSpan={compact ? 6 : 8} className="px-4 py-8 text-center text-small text-text-muted2">No transactions match this view.</td></tr>
            ) : rows.map((row) => {
              const checked = selected.has(row.id);
              return (
                <tr
                  key={row.id}
                  onClick={() => onInspect(row)}
                  className={classNames(
                    "cursor-pointer border-b border-border-subtle transition-colors",
                    checked ? "bg-primary-soft/35 hover:bg-primary-soft/45" : "bg-surface hover:bg-background/80",
                    row.is_reversal_pair && "text-text-muted2",
                  )}
                >
                  <td className="truncate px-2 py-1 text-text-muted2">{compactDate(row.date)}</td>
                  {!compact ? <td className="truncate px-2 py-1">{row.type}{row.is_reversal_pair ? <span className="ml-1 rounded bg-warning-soft px-1 text-[10px] text-warning-hover">Pair</span> : null}</td> : null}
                  <td className="truncate px-2 py-1">
                    <button type="button" onClick={(event) => { event.stopPropagation(); void onOpen(row); }} className="truncate text-primary hover:underline">{row.reference}</button>
                  </td>
                  <td className="truncate px-2 py-1">{row.payee || "-"}</td>
                  {!compact ? <td className="truncate px-2 py-1 text-text-muted2" title={row.memo}>{row.memo || "-"}</td> : null}
                  <td className="px-2 py-1 text-right font-mono tabular-nums">
                    {row.is_payment ? <button type="button" onClick={(event) => { event.stopPropagation(); void onOpen(row); }} className="text-danger-hover hover:underline">{fmt(parseMoney(row.payment))}</button> : ""}
                  </td>
                  <td className="px-2 py-1 text-right font-mono tabular-nums">
                    {row.is_deposit ? <button type="button" onClick={(event) => { event.stopPropagation(); void onOpen(row); }} className="text-success-hover hover:underline">{fmt(parseMoney(row.deposit))}</button> : ""}
                  </td>
                  <td className="px-2 py-1 text-center" onClick={(event) => event.stopPropagation()}>
                    <label className="inline-flex cursor-pointer items-center justify-center">
                      <input type="checkbox" checked={checked} onChange={() => onToggle(row.id)} className="peer sr-only" aria-label={`Mark ${row.reference} cleared`} />
                      <span className={classNames(
                        "inline-flex h-4 w-4 items-center justify-center rounded-full border text-[10px] font-bold transition-colors",
                        checked ? "border-primary bg-primary text-onPrimary" : "border-border-input bg-surface text-transparent hover:border-primary",
                      )}>
                        ✓
                      </span>
                    </label>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Header({ label, sortKey, current, asc, onSort, className }: { label: string; sortKey: SortKey; current: SortKey; asc: boolean; onSort: (key: SortKey) => void; className?: string }) {
  const active = current === sortKey;
  return (
    <th className={classNames("px-2 py-2", className)}>
      <button type="button" onClick={() => onSort(sortKey)} className="inline-flex items-center gap-1 hover:text-text">
        {label}
        <span className={active ? "text-primary" : "text-text-muted3"}>{active ? (asc ? "^" : "v") : "sort"}</span>
      </button>
    </th>
  );
}

function DetailDrawer({ candidate, onClose, onOpen }: { candidate: Candidate | null; onClose: () => void; onOpen: (candidate: Candidate) => void }) {
  if (!candidate) return null;
  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/30" onClick={onClose}>
      <aside className="h-full w-full max-w-xl border-l border-border bg-surface p-5 shadow-xl" onClick={(event) => event.stopPropagation()}>
        <div className="flex items-start justify-between gap-3">
          <div>
            <div className="text-small uppercase tracking-wider text-text-muted">{candidate.type}</div>
            <h3 className="mt-1 text-section font-semibold text-text">{candidate.reference}</h3>
          </div>
          <button type="button" onClick={onClose} className="rounded-md border border-border-input px-2 py-1 text-small text-text-muted2 hover:bg-background">Close</button>
        </div>
        <dl className="mt-5 grid grid-cols-2 gap-3 text-small">
          <Detail label="Date" value={candidate.date} />
          <Detail label="Journal Entry" value={`JE ${candidate.journal_entry_id}`} />
          <Detail label="Payee" value={candidate.payee || "-"} />
          <Detail label="Source" value={candidate.source_type || "manual"} />
          <Detail label="Payment" value={candidate.is_payment ? fmt(parseMoney(candidate.payment)) : "-"} danger />
          <Detail label="Deposit" value={candidate.is_deposit ? fmt(parseMoney(candidate.deposit)) : "-"} success />
        </dl>
        <div className="mt-5 rounded-md border border-border-subtle bg-background p-3">
          <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Memo</div>
          <div className="mt-1 whitespace-pre-wrap text-small text-text-muted2">{candidate.memo || "-"}</div>
        </div>
        {candidate.is_reversal_pair ? (
          <div className="mt-3 rounded-md border border-warning-border bg-warning-soft px-3 py-2 text-small text-warning-hover">
            This row is part of a reversal pair and is hidden by default.
          </div>
        ) : null}
        <button
          type="button"
          onClick={() => void onOpen(candidate)}
          disabled={!candidate.detail_url}
          className="mt-5 rounded-md bg-primary px-4 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text"
        >
          Open full record
        </button>
      </aside>
    </div>
  );
}

function Detail({ label, value, danger, success }: { label: string; value: string; danger?: boolean; success?: boolean }) {
  return (
    <div className="rounded-md border border-border-subtle bg-background px-3 py-2">
      <dt className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{label}</dt>
      <dd className={classNames("mt-1 truncate font-mono text-small", danger && "text-danger-hover", success && "text-success-hover", !danger && !success && "text-text")}>{value}</dd>
    </div>
  );
}

function mount(root: HTMLElement) {
  let payload: WorkspacePayload;
  try {
    payload = JSON.parse(root.dataset.workspace || "{}") as WorkspacePayload;
  } catch {
    root.innerHTML = `<div class="mt-6 rounded-lg border border-border-danger bg-danger-soft p-4 text-small text-danger-hover">Could not load reconciliation workspace.</div>`;
    return;
  }
  createRoot(root).render(<BankReconcileWorkspace payload={payload} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="bank-reconcile"]').forEach((root) => {
  mount(root);
});

