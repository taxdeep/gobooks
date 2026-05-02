import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    balancizFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type JournalLine = {
  key: string;
  account: string;
  account_code: string;
  account_name: string;
  memo: string;
  debit: string;
  credit: string;
};

type JournalEntry = {
  id: number;
  entry_date: string;
  journal_no: string;
  document_url: string;
  reversal_note: string;
  line_count: number;
  debits: string;
  credits: string;
  lines: JournalLine[];
};

type JournalReport = {
  from: string;
  to: string;
  entry_count: number;
  line_count: number;
  totals: { debits: string; credits: string };
  entries: JournalEntry[];
};

type FlatRow =
  | { kind: "entry"; key: string; entry: JournalEntry }
  | { kind: "line"; key: string; entry: JournalEntry; line: JournalLine };

const rowHeight = 34;
const overscan = 12;

function money(value: string): number {
  const n = Number(String(value || "").replace(/,/g, ""));
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: string): string {
  return money(value).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function matchesEntry(entry: JournalEntry, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if ([entry.entry_date, entry.journal_no, entry.reversal_note, entry.debits, entry.credits].some((v) => String(v || "").toLowerCase().includes(q))) {
    return true;
  }
  return entry.lines.some((line) => matchesLine(line, q));
}

function matchesLine(line: JournalLine, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [line.account, line.account_code, line.account_name, line.memo, line.debit, line.credit].some((v) => String(v || "").toLowerCase().includes(q));
}

function buildRows(entries: JournalEntry[], expanded: Set<number>, query: string): FlatRow[] {
  const q = query.trim();
  const rows: FlatRow[] = [];
  for (const entry of entries) {
    if (!matchesEntry(entry, q)) continue;
    rows.push({ kind: "entry", key: `e:${entry.id}`, entry });
    if (!expanded.has(entry.id)) continue;
    for (const line of entry.lines) {
      if (q && !matchesLine(line, q)) continue;
      rows.push({ kind: "line", key: `l:${entry.id}:${line.key}`, entry, line });
    }
  }
  return rows;
}

async function fetchJSON(url: string): Promise<JournalReport> {
  const fetchFn = window.balancizFetch || fetch;
  const response = await fetchFn(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Journal entries request failed with ${response.status}`);
  return (await response.json()) as JournalReport;
}

function JournalEntryReportExplorer({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<JournalReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set());
  const [scrollTop, setScrollTop] = useState(0);
  const viewportRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const next = await fetchJSON(apiURL);
      setData(next);
      setExpanded(new Set(next.entries.map((entry) => entry.id)));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Journal entries could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const visibleEntries = useMemo(() => (data?.entries || []).filter((entry) => matchesEntry(entry, query)), [data, query]);
  const rows = useMemo(() => buildRows(data?.entries || [], expanded, query), [data, expanded, query]);
  const totalHeight = rows.length * rowHeight;
  const viewportHeight = viewportRef.current?.clientHeight || 560;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(rows.length, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  const virtualRows = rows.slice(start, end);

  const expandAll = () => setExpanded(new Set((data?.entries || []).map((entry) => entry.id)));
  const collapseAll = () => setExpanded(new Set());
  const toggle = (entry: JournalEntry) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(entry.id)) next.delete(entry.id);
      else next.add(entry.id);
      return next;
    });
  };

  if (!data && loading) {
    return <Shell><div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2">Loading journal entries...</div></Shell>;
  }

  if (!data) {
    return <Shell><ErrorBox message={error || "Journal entries could not be loaded."} /></Shell>;
  }

  return (
    <Shell>
      {error ? <ErrorBox message={error} /> : null}
      <section className="grid grid-cols-1 gap-3 lg:grid-cols-4">
        <Metric label="Period" value={`${data.from} to ${data.to}`} />
        <Metric label="Entries" value={String(visibleEntries.length)} hint={`${data.entry_count} total`} />
        <Metric label="Lines" value={String(rows.filter((row) => row.kind === "line").length)} hint={`${data.line_count} total`} />
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
              placeholder="Journal no, account, memo, amount..."
              className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
            />
          </label>
          <button type="button" onClick={expandAll} className="rounded-md border border-border-input px-3 py-2 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">Expand all</button>
          <button type="button" onClick={collapseAll} className="rounded-md border border-border-input px-3 py-2 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">Collapse all</button>
          <button type="button" onClick={() => void load()} disabled={loading} className="rounded-md bg-primary px-3 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text">
            {loading ? "Refreshing..." : "Refresh"}
          </button>
        </div>

        <div className="overflow-x-auto">
          <div className="min-w-[980px]">
            <div className="grid grid-cols-[120px_170px_minmax(300px,1fr)_140px_140px] border-b border-border bg-background px-3 py-2 text-[11px] font-bold uppercase tracking-wider text-text-muted">
              <div>Date</div>
              <div>Journal</div>
              <div>Account / Memo</div>
              <div className="text-right">Debit</div>
              <div className="text-right">Credit</div>
            </div>
            <div
              ref={viewportRef}
              onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
              className="relative h-[62vh] overflow-auto"
              data-react-journal-entry-report-ready="true"
            >
              <div style={{ height: totalHeight || rowHeight, position: "relative" }}>
                {virtualRows.length === 0 ? (
                  <div className="absolute inset-x-0 top-0 px-4 py-10 text-center text-small text-text-muted2">No journal entries match this view.</div>
                ) : virtualRows.map((row, offset) => (
                  <RowView key={row.key} item={row} top={(start + offset) * rowHeight} expanded={expanded} onToggle={toggle} />
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
  return <div className="mt-4 space-y-3" data-react-journal-entry-report-shell="true">{children}</div>;
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

function RowView({ item, top, expanded, onToggle }: { item: FlatRow; top: number; expanded: Set<number>; onToggle: (entry: JournalEntry) => void }) {
  const style = { transform: `translateY(${top}px)`, height: rowHeight } as React.CSSProperties;
  if (item.kind === "entry") {
    const open = expanded.has(item.entry.id);
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_170px_minmax(300px,1fr)_140px_140px] items-center border-b border-border-subtle bg-surface-tableHeader px-3 text-small">
        <div className="font-semibold text-text">{item.entry.entry_date}</div>
        <div className="min-w-0">
          <button type="button" onClick={() => onToggle(item.entry)} className="max-w-full truncate text-left font-mono text-[11px] font-semibold text-primary hover:underline">
            <span className="mr-2 text-text-muted2">{open ? "v" : ">"}</span>
            {item.entry.journal_no || `#${item.entry.id}`}
          </button>
        </div>
        <div className="truncate font-semibold text-text">
          <a href={item.entry.document_url} className="hover:text-primary">Entry #{item.entry.id}</a>
          {item.entry.reversal_note ? <span className="ml-2 font-normal text-text-muted2">{item.entry.reversal_note}</span> : null}
        </div>
        <div className="text-right font-mono tabular-nums font-semibold text-text">{fmt(item.entry.debits)}</div>
        <div className="text-right font-mono tabular-nums font-semibold text-text">{fmt(item.entry.credits)}</div>
      </div>
    );
  }
  const line = item.line;
  return (
    <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_170px_minmax(300px,1fr)_140px_140px] items-center border-b border-border-subtle/60 px-3 text-small text-text hover:bg-background">
      <div />
      <div />
      <div className="min-w-0 truncate">
        <span className="font-mono text-[11px] font-semibold">{line.account_code}</span>
        <span className="text-text-muted2"> {line.account_name}</span>
        {line.memo ? <span className="ml-3 text-text-muted2">{line.memo}</span> : null}
      </div>
      <div className="text-right font-mono tabular-nums">{money(line.debit) > 0 ? fmt(line.debit) : ""}</div>
      <div className="text-right font-mono tabular-nums">{money(line.credit) > 0 ? fmt(line.credit) : ""}</div>
    </div>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/reports/journal-entries";
  createRoot(root).render(<JournalEntryReportExplorer apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="journal-entry-report"]').forEach((root) => {
  mount(root);
});
