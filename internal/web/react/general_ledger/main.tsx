import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    balancizFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type GLRow = {
  key: string;
  date: string;
  type: string;
  document_number: string;
  document_url: string;
  counterparty_name: string;
  description: string;
  debit: string;
  credit: string;
  balance: string;
  journal_no: string;
};

type GLSection = {
  account_id: number;
  account_code: string;
  account_name: string;
  account_root_type: string;
  detail_type: string;
  starting_balance: string;
  total_debits: string;
  total_credits: string;
  ending_balance: string;
  rows: GLRow[];
};

type GLResponse = {
  from: string;
  to: string;
  section_count: number;
  row_count: number;
  totals: { debits: string; credits: string };
  sections: GLSection[];
};

type FlatRow =
  | { kind: "section"; key: string; section: GLSection }
  | { kind: "starting"; key: string; section: GLSection }
  | { kind: "row"; key: string; section: GLSection; row: GLRow }
  | { kind: "total"; key: string; section: GLSection };

const rowHeight = 34;
const overscan = 10;

function parseMoney(value: string): number {
  const n = Number(String(value || "").replace(/,/g, ""));
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: string): string {
  const n = parseMoney(value);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function compactDetail(section: GLSection): string {
  return [section.account_root_type, section.detail_type].filter(Boolean).join(" / ").replaceAll("_", " ");
}

function rowMatches(row: GLRow, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    row.date,
    row.type,
    row.document_number,
    row.counterparty_name,
    row.description,
    row.journal_no,
    row.debit,
    row.credit,
    row.balance,
  ].some((value) => String(value || "").toLowerCase().includes(q));
}

function sectionMatches(section: GLSection, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if (`${section.account_code} ${section.account_name} ${compactDetail(section)}`.toLowerCase().includes(q)) {
    return true;
  }
  return section.rows.some((row) => rowMatches(row, q));
}

function buildFlatRows(sections: GLSection[], expanded: Set<string>, query: string): FlatRow[] {
  const out: FlatRow[] = [];
  for (const section of sections) {
    if (!sectionMatches(section, query)) continue;
    const sectionKey = String(section.account_id);
    out.push({ kind: "section", key: `s:${sectionKey}`, section });
    if (!expanded.has(sectionKey)) continue;
    out.push({ kind: "starting", key: `b:${sectionKey}`, section });
    for (const row of section.rows) {
      if (!rowMatches(row, query) && query.trim()) continue;
      out.push({ kind: "row", key: `r:${sectionKey}:${row.key}`, section, row });
    }
    out.push({ kind: "total", key: `t:${sectionKey}`, section });
  }
  return out;
}

async function fetchJSON(url: string): Promise<GLResponse> {
  const fetchFn = window.balancizFetch || fetch;
  const response = await fetchFn(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`General Ledger request failed with ${response.status}`);
  return (await response.json()) as GLResponse;
}

function GeneralLedgerExplorer({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<GLResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [scrollTop, setScrollTop] = useState(0);
  const viewportRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const next = await fetchJSON(apiURL);
      setData(next);
      setExpanded(new Set(next.sections.map((section) => String(section.account_id))));
    } catch (err) {
      setError(err instanceof Error ? err.message : "General Ledger could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const rows = useMemo(() => buildFlatRows(data?.sections || [], expanded, query), [data, expanded, query]);
  const visibleSections = useMemo(() => (data?.sections || []).filter((section) => sectionMatches(section, query)), [data, query]);
  const maxActivity = Math.max(1, ...visibleSections.map((section) => parseMoney(section.total_debits) + parseMoney(section.total_credits)));
  const totalHeight = rows.length * rowHeight;
  const viewportHeight = viewportRef.current?.clientHeight || 560;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(rows.length, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  const virtualRows = rows.slice(start, end);

  const toggle = (section: GLSection) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      const key = String(section.account_id);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const expandAll = () => setExpanded(new Set((data?.sections || []).map((section) => String(section.account_id))));
  const collapseAll = () => setExpanded(new Set());

  if (!data && loading) {
    return <Shell><div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2">Loading General Ledger...</div></Shell>;
  }

  if (!data) {
    return <Shell><ErrorBox message={error || "General Ledger could not be loaded."} /></Shell>;
  }

  return (
    <Shell>
      {error ? <ErrorBox message={error} /> : null}
      <section className="grid grid-cols-1 gap-3 lg:grid-cols-4">
        <Metric label="Period" value={`${data.from} to ${data.to}`} />
        <Metric label="Accounts" value={String(visibleSections.length)} hint={`${data.section_count} total`} />
        <Metric label="Rows" value={String(rows.length)} hint={`${data.row_count} transactions`} />
        <Metric label="Debits / Credits" value={`${fmt(data.totals.debits)} / ${fmt(data.totals.credits)}`} />
      </section>

      <section className="rounded-lg border border-border bg-surface shadow-sm">
        <div className="flex flex-wrap items-end gap-3 border-b border-border px-4 py-3">
          <label className="min-w-[280px] flex-1">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Search</span>
            <input
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setScrollTop(0);
                if (viewportRef.current) viewportRef.current.scrollTop = 0;
              }}
              placeholder="Account, ref, counterparty, memo, amount..."
              className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
            />
          </label>
          <button type="button" onClick={expandAll} className="rounded-md border border-border-input px-3 py-2 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">Expand all</button>
          <button type="button" onClick={collapseAll} className="rounded-md border border-border-input px-3 py-2 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">Collapse all</button>
          <button type="button" onClick={() => void load()} disabled={loading} className="rounded-md bg-primary px-3 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text">
            {loading ? "Refreshing..." : "Refresh"}
          </button>
        </div>

        <ActivityStrip sections={visibleSections.slice(0, 12)} maxActivity={maxActivity} />

        <div className="overflow-x-auto">
          <div className="min-w-[1160px]">
            <div className="grid grid-cols-[110px_120px_150px_190px_minmax(260px,1fr)_130px_130px_130px] border-b border-border bg-background px-3 py-2 text-[11px] font-bold uppercase tracking-wider text-text-muted">
              <div>Date</div>
              <div>Type</div>
              <div>No.</div>
              <div>Name</div>
              <div>Description</div>
              <div className="text-right">Debit</div>
              <div className="text-right">Credit</div>
              <div className="text-right">Balance</div>
            </div>
            <div
              ref={viewportRef}
              onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
              className="relative h-[62vh] overflow-auto"
              data-react-general-ledger-ready="true"
            >
              <div style={{ height: totalHeight || rowHeight, position: "relative" }}>
                {virtualRows.length === 0 ? (
                  <div className="absolute inset-x-0 top-0 px-4 py-10 text-center text-small text-text-muted2">No rows match this view.</div>
                ) : virtualRows.map((row, offset) => (
                  <FlatRowView key={row.key} item={row} top={(start + offset) * rowHeight} expanded={expanded} onToggle={toggle} />
                ))}
              </div>
            </div>
          </div>
        </div>
      </section>
    </Shell>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return <div className="mt-4 space-y-3" data-react-general-ledger-shell="true">{children}</div>;
}

function ErrorBox({ message }: { message: string }) {
  return <div className="rounded-lg border border-border-danger bg-danger-soft px-3 py-2 text-small text-danger-hover">{message}</div>;
}

function Metric({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{label}</div>
      <div className="mt-1 truncate text-body font-semibold tabular-nums text-text">{value}</div>
      {hint ? <div className="mt-0.5 text-[11px] text-text-muted3">{hint}</div> : null}
    </div>
  );
}

function ActivityStrip({ sections, maxActivity }: { sections: GLSection[]; maxActivity: number }) {
  if (!sections.length) return null;
  return (
    <div className="border-b border-border px-4 py-3">
      <div className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Most active accounts</div>
      <div className="grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-6">
        {sections.map((section) => {
          const activity = parseMoney(section.total_debits) + parseMoney(section.total_credits);
          const pct = Math.max(3, Math.min(100, (activity / maxActivity) * 100));
          return (
            <div key={section.account_id} className="min-w-0 rounded-md border border-border-subtle px-2 py-1.5">
              <div className="truncate text-[11px] font-semibold text-text">{section.account_code} {section.account_name}</div>
              <div className="mt-1 h-1.5 rounded bg-background">
                <div className="h-1.5 rounded bg-primary" style={{ width: `${pct}%` }} />
              </div>
              <div className="mt-1 text-[11px] tabular-nums text-text-muted2">{activity.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function FlatRowView({ item, top, expanded, onToggle }: { item: FlatRow; top: number; expanded: Set<string>; onToggle: (section: GLSection) => void }) {
  const style = { transform: `translateY(${top}px)`, height: rowHeight } as React.CSSProperties;
  const sectionKey = String(item.section.account_id);
  if (item.kind === "section") {
    const open = expanded.has(sectionKey);
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[1fr_130px_130px_130px] items-center border-b border-border-subtle bg-surface-tableHeader px-3 text-small">
        <button type="button" onClick={() => onToggle(item.section)} className="min-w-0 truncate text-left font-semibold text-text hover:text-primary">
          <span className="mr-2 text-text-muted2">{open ? "v" : ">"}</span>
          {item.section.account_code} - {item.section.account_name}
          <span className="ml-2 font-normal text-text-muted2">{compactDetail(item.section)}</span>
        </button>
        <div className="text-right font-mono tabular-nums text-text-muted2">{fmt(item.section.total_debits)}</div>
        <div className="text-right font-mono tabular-nums text-text-muted2">{fmt(item.section.total_credits)}</div>
        <div className="text-right font-mono tabular-nums font-semibold text-text">{fmt(item.section.ending_balance)}</div>
      </div>
    );
  }
  if (item.kind === "starting") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[110px_120px_150px_190px_minmax(260px,1fr)_130px_130px_130px] items-center border-b border-border-subtle/60 px-3 text-small text-text-muted2">
        <div />
        <div />
        <div />
        <div />
        <div className="font-semibold text-text">Starting Balance</div>
        <div />
        <div />
        <div className="text-right font-mono tabular-nums font-semibold text-text">{fmt(item.section.starting_balance)}</div>
      </div>
    );
  }
  if (item.kind === "total") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[110px_120px_150px_190px_minmax(260px,1fr)_130px_130px_130px] items-center border-b border-border bg-background px-3 text-small font-semibold text-text">
        <div />
        <div />
        <div />
        <div />
        <div>Totals and Ending Balance</div>
        <div className="text-right font-mono tabular-nums">{fmt(item.section.total_debits)}</div>
        <div className="text-right font-mono tabular-nums">{fmt(item.section.total_credits)}</div>
        <div className="text-right font-mono tabular-nums">{fmt(item.section.ending_balance)}</div>
      </div>
    );
  }
  const row = item.row;
  return (
    <div style={style} className="absolute inset-x-0 grid grid-cols-[110px_120px_150px_190px_minmax(260px,1fr)_130px_130px_130px] items-center border-b border-border-subtle/60 px-3 text-small text-text hover:bg-background">
      <div className="truncate text-text-muted2">{row.date}</div>
      <div className="truncate">{row.type}</div>
      <div className="truncate font-mono text-[11px]">
        {row.document_url ? <a href={row.document_url} className="text-primary hover:underline">{row.document_number}</a> : row.document_number}
      </div>
      <div className="truncate">{row.counterparty_name || "-"}</div>
      <div className="truncate text-text-muted2" title={row.description}>{row.description || "-"}</div>
      <div className="text-right font-mono tabular-nums">{parseMoney(row.debit) > 0 ? fmt(row.debit) : ""}</div>
      <div className="text-right font-mono tabular-nums">{parseMoney(row.credit) > 0 ? fmt(row.credit) : ""}</div>
      <div className="text-right font-mono tabular-nums">{fmt(row.balance)}</div>
    </div>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/reports/general-ledger";
  createRoot(root).render(<GeneralLedgerExplorer apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="general-ledger"]').forEach((root) => {
  mount(root);
});
