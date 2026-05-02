import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    balancizFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type TBRow = {
  account_id: number;
  account_code: string;
  account_name: string;
  classification: string;
  root: string;
  detail: string;
  debit: string;
  credit: string;
  drill_url: string;
};

type TBSection = {
  title: string;
  root: string;
  debits: string;
  credits: string;
  rows: TBRow[];
};

type TBResponse = {
  from: string;
  to: string;
  row_count: number;
  totals: { debits: string; credits: string; difference: string; balanced: boolean };
  sections: TBSection[];
};

type FlatRow =
  | { kind: "section"; key: string; section: TBSection }
  | { kind: "row"; key: string; section: TBSection; row: TBRow }
  | { kind: "total"; key: string; section: TBSection };

const rowHeight = 34;
const overscan = 12;

function money(value: string): number {
  const n = Number(String(value || "").replace(/,/g, ""));
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: string): string {
  return money(value).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function rowMatches(row: TBRow, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [row.account_code, row.account_name, row.classification, row.root, row.detail, row.debit, row.credit]
    .some((value) => String(value || "").toLowerCase().includes(q));
}

function sectionMatches(section: TBSection, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if ([section.title, section.root, section.debits, section.credits].some((value) => String(value || "").toLowerCase().includes(q))) {
    return true;
  }
  return section.rows.some((row) => rowMatches(row, q));
}

function buildRows(sections: TBSection[], query: string): FlatRow[] {
  const rows: FlatRow[] = [];
  for (const section of sections) {
    if (!sectionMatches(section, query)) continue;
    rows.push({ kind: "section", key: `s:${section.root}`, section });
    for (const row of section.rows) {
      if (query.trim() && !rowMatches(row, query)) continue;
      rows.push({ kind: "row", key: `r:${row.account_id}`, section, row });
    }
    rows.push({ kind: "total", key: `t:${section.root}`, section });
  }
  return rows;
}

async function fetchJSON(url: string): Promise<TBResponse> {
  const fetchFn = window.balancizFetch || fetch;
  const response = await fetchFn(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Trial Balance request failed with ${response.status}`);
  return (await response.json()) as TBResponse;
}

function TrialBalanceExplorer({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<TBResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [scrollTop, setScrollTop] = useState(0);
  const viewportRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setData(await fetchJSON(apiURL));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Trial Balance could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const visibleSections = useMemo(() => (data?.sections || []).filter((section) => sectionMatches(section, query)), [data, query]);
  const rows = useMemo(() => buildRows(data?.sections || [], query), [data, query]);
  const totalHeight = rows.length * rowHeight;
  const viewportHeight = viewportRef.current?.clientHeight || 560;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(rows.length, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  const virtualRows = rows.slice(start, end);

  if (!data && loading) {
    return <Shell><div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2">Loading Trial Balance...</div></Shell>;
  }
  if (!data) {
    return <Shell><ErrorBox message={error || "Trial Balance could not be loaded."} /></Shell>;
  }

  return (
    <Shell>
      {error ? <ErrorBox message={error} /> : null}
      <section className="grid grid-cols-1 gap-3 lg:grid-cols-4">
        <Metric label="Period" value={`${data.from} to ${data.to}`} />
        <Metric label="Accounts" value={String(rows.filter((row) => row.kind === "row").length)} hint={`${data.row_count} total`} />
        <Metric label="Debits / Credits" value={`${fmt(data.totals.debits)} / ${fmt(data.totals.credits)}`} />
        <Metric label="Difference" value={fmt(data.totals.difference)} hint={data.totals.balanced ? "Balanced" : "Out of balance"} tone={data.totals.balanced ? "good" : "bad"} />
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
              placeholder="Account, classification, amount..."
              className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
            />
          </label>
          <button type="button" onClick={() => void load()} disabled={loading} className="rounded-md bg-primary px-3 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text">
            {loading ? "Refreshing..." : "Refresh"}
          </button>
        </div>

        <SectionStrip sections={visibleSections} />

        <div className="overflow-x-auto">
          <div className="min-w-[920px]">
            <div className="grid grid-cols-[120px_minmax(260px,1fr)_260px_150px_150px] border-b border-border bg-background px-3 py-2 text-[11px] font-bold uppercase tracking-wider text-text-muted">
              <div>Code</div>
              <div>Name</div>
              <div>Classification</div>
              <div className="text-right">Debit</div>
              <div className="text-right">Credit</div>
            </div>
            <div
              ref={viewportRef}
              onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
              className="relative h-[62vh] overflow-auto"
              data-react-trial-balance-ready="true"
            >
              <div style={{ height: totalHeight || rowHeight, position: "relative" }}>
                {virtualRows.length === 0 ? (
                  <div className="absolute inset-x-0 top-0 px-4 py-10 text-center text-small text-text-muted2">No accounts match this view.</div>
                ) : virtualRows.map((row, offset) => (
                  <RowView key={row.key} item={row} top={(start + offset) * rowHeight} />
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
  return <div className="mt-4 space-y-3" data-react-trial-balance-shell="true">{children}</div>;
}

function ErrorBox({ message }: { message: string }) {
  return <div className="rounded-lg border border-border-danger bg-danger-soft px-3 py-2 text-small text-danger-hover">{message}</div>;
}

function Metric({ label, value, hint, tone }: { label: string; value: string; hint?: string; tone?: "good" | "bad" }) {
  const hintClass = tone === "good" ? "text-success-hover" : tone === "bad" ? "text-danger-hover" : "text-text-muted3";
  return (
    <div className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{label}</div>
      <div className="mt-1 truncate text-body font-semibold tabular-nums text-text">{value}</div>
      {hint ? <div className={`mt-0.5 text-[11px] ${hintClass}`}>{hint}</div> : null}
    </div>
  );
}

function SectionStrip({ sections }: { sections: TBSection[] }) {
  if (!sections.length) return null;
  return (
    <div className="border-b border-border px-4 py-3">
      <div className="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-6">
        {sections.map((section) => (
          <div key={section.root} className="rounded-md border border-border-subtle px-2 py-1.5">
            <div className="truncate text-[11px] font-semibold text-text">{section.title}</div>
            <div className="mt-1 flex justify-between gap-2 text-[11px] tabular-nums text-text-muted2">
              <span>D {fmt(section.debits)}</span>
              <span>C {fmt(section.credits)}</span>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function RowView({ item, top }: { item: FlatRow; top: number }) {
  const style = { transform: `translateY(${top}px)`, height: rowHeight } as React.CSSProperties;
  if (item.kind === "section") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[1fr_150px_150px] items-center border-b border-border-subtle bg-surface-tableHeader px-3 text-small">
        <div className="font-semibold text-text">{item.section.title}</div>
        <div className="text-right font-mono tabular-nums text-text-muted2">{fmt(item.section.debits)}</div>
        <div className="text-right font-mono tabular-nums text-text-muted2">{fmt(item.section.credits)}</div>
      </div>
    );
  }
  if (item.kind === "total") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_minmax(260px,1fr)_260px_150px_150px] items-center border-b border-border bg-background px-3 text-small font-semibold text-text">
        <div />
        <div>{item.section.title} Total</div>
        <div />
        <div className="text-right font-mono tabular-nums">{fmt(item.section.debits)}</div>
        <div className="text-right font-mono tabular-nums">{fmt(item.section.credits)}</div>
      </div>
    );
  }
  const row = item.row;
  return (
    <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_minmax(260px,1fr)_260px_150px_150px] items-center border-b border-border-subtle/60 px-3 text-small text-text hover:bg-background">
      <div className="truncate font-mono text-[11px] font-semibold">{row.account_code}</div>
      <div className="truncate">
        {row.drill_url ? <a href={row.drill_url} className="hover:text-primary hover:underline">{row.account_name}</a> : row.account_name}
      </div>
      <div className="truncate text-text-muted2">{row.classification}</div>
      <div className="text-right font-mono tabular-nums">{money(row.debit) > 0 ? fmt(row.debit) : ""}</div>
      <div className="text-right font-mono tabular-nums">{money(row.credit) > 0 ? fmt(row.credit) : ""}</div>
    </div>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/reports/trial-balance";
  createRoot(root).render(<TrialBalanceExplorer apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="trial-balance"]').forEach((root) => {
  mount(root);
});
