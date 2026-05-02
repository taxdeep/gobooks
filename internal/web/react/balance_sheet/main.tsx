import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    balancizFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type BSLine = {
  account_id: number;
  account_code: string;
  account_name: string;
  detail: string;
  amount: string;
  drill_url: string;
};

type BSSection = {
  title: string;
  root: string;
  total: string;
  rows: BSLine[];
};

type BSResponse = {
  as_of: string;
  totals: {
    assets: string;
    liabilities: string;
    equity: string;
    liabilities_and_equity: string;
    difference: string;
    balanced: boolean;
  };
  sections: BSSection[];
};

type FlatRow =
  | { kind: "section"; key: string; section: BSSection }
  | { kind: "line"; key: string; section: BSSection; line: BSLine }
  | { kind: "total"; key: string; section: BSSection }
  | { kind: "summary"; key: string; label: string; amount: string; balanced?: boolean };

const rowHeight = 34;
const overscan = 12;

function money(value: string): number {
  const n = Number(String(value || "").replace(/,/g, ""));
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: string): string {
  return money(value).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function detailLabel(value: string): string {
  return String(value || "").replaceAll("_", " ");
}

function lineMatches(line: BSLine, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [line.account_code, line.account_name, line.detail, line.amount].some((value) => String(value || "").toLowerCase().includes(q));
}

function sectionMatches(section: BSSection, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if ([section.title, section.root, section.total].some((value) => String(value || "").toLowerCase().includes(q))) return true;
  return section.rows.some((line) => lineMatches(line, q));
}

function appendSection(rows: FlatRow[], section: BSSection | undefined, query: string) {
  if (!section || !sectionMatches(section, query)) return;
  rows.push({ kind: "section", key: `s:${section.root}`, section });
  for (const line of section.rows) {
    if (query.trim() && !lineMatches(line, query)) continue;
    rows.push({ kind: "line", key: `l:${line.account_id}`, section, line });
  }
  rows.push({ kind: "total", key: `t:${section.root}`, section });
}

function buildRows(data: BSResponse, query: string): FlatRow[] {
  const rows: FlatRow[] = [];
  appendSection(rows, data.sections.find((section) => section.root === "asset"), query);
  appendSection(rows, data.sections.find((section) => section.root === "liability"), query);
  appendSection(rows, data.sections.find((section) => section.root === "equity"), query);
  rows.push({
    kind: "summary",
    key: "liabilities_and_equity",
    label: "Total Liabilities + Equity",
    amount: data.totals.liabilities_and_equity,
    balanced: data.totals.balanced,
  });
  return rows;
}

async function fetchJSON(url: string): Promise<BSResponse> {
  const fetchFn = window.balancizFetch || fetch;
  const response = await fetchFn(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Balance Sheet request failed with ${response.status}`);
  return (await response.json()) as BSResponse;
}

function BalanceSheetExplorer({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<BSResponse | null>(null);
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
      setError(err instanceof Error ? err.message : "Balance Sheet could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const rows = useMemo(() => (data ? buildRows(data, query) : []), [data, query]);
  const totalHeight = rows.length * rowHeight;
  const viewportHeight = viewportRef.current?.clientHeight || 560;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(rows.length, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  const virtualRows = rows.slice(start, end);

  if (!data && loading) {
    return <Shell><div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2">Loading Balance Sheet...</div></Shell>;
  }
  if (!data) {
    return <Shell><ErrorBox message={error || "Balance Sheet could not be loaded."} /></Shell>;
  }

  return (
    <Shell>
      {error ? <ErrorBox message={error} /> : null}
      <section className="grid grid-cols-1 gap-3 lg:grid-cols-5">
        <Metric label="Assets" value={fmt(data.totals.assets)} />
        <Metric label="Liabilities" value={fmt(data.totals.liabilities)} />
        <Metric label="Equity" value={fmt(data.totals.equity)} />
        <Metric label="Liabilities + Equity" value={fmt(data.totals.liabilities_and_equity)} />
        <Metric label="Difference" value={fmt(data.totals.difference)} tone={data.totals.balanced ? "good" : "bad"} />
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
              placeholder="Account, detail, amount..."
              className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
            />
          </label>
          <span className="text-small text-text-muted2">As of {data.as_of}</span>
          <button type="button" onClick={() => void load()} disabled={loading} className="rounded-md bg-primary px-3 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text">
            {loading ? "Refreshing..." : "Refresh"}
          </button>
        </div>

        <div className="overflow-x-auto">
          <div className="min-w-[860px]">
            <div className="grid grid-cols-[120px_minmax(280px,1fr)_260px_170px] border-b border-border bg-background px-3 py-2 text-[11px] font-bold uppercase tracking-wider text-text-muted">
              <div>Code</div>
              <div>Name</div>
              <div>Detail</div>
              <div className="text-right">Amount</div>
            </div>
            <div
              ref={viewportRef}
              onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
              className="relative h-[62vh] overflow-auto"
              data-react-balance-sheet-ready="true"
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
  return <div className="mt-4 space-y-3" data-react-balance-sheet-shell="true">{children}</div>;
}

function ErrorBox({ message }: { message: string }) {
  return <div className="rounded-lg border border-border-danger bg-danger-soft px-3 py-2 text-small text-danger-hover">{message}</div>;
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: "good" | "bad" }) {
  const valueClass = tone === "good" ? "text-success-hover" : tone === "bad" ? "text-danger-hover" : "text-text";
  return (
    <div className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{label}</div>
      <div className={`mt-1 truncate text-body font-semibold tabular-nums ${valueClass}`}>{value}</div>
    </div>
  );
}

function RowView({ item, top }: { item: FlatRow; top: number }) {
  const style = { transform: `translateY(${top}px)`, height: rowHeight } as React.CSSProperties;
  if (item.kind === "section") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[1fr_170px] items-center border-b border-border-subtle bg-surface-tableHeader px-3 text-small">
        <div className="font-semibold text-text">{item.section.title}</div>
        <div className="text-right font-mono tabular-nums text-text-muted2">{fmt(item.section.total)}</div>
      </div>
    );
  }
  if (item.kind === "total") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_minmax(280px,1fr)_260px_170px] items-center border-b border-border bg-background px-3 text-small font-semibold text-text">
        <div />
        <div>Total {item.section.title}</div>
        <div />
        <div className="text-right font-mono tabular-nums">{fmt(item.section.total)}</div>
      </div>
    );
  }
  if (item.kind === "summary") {
    const balanceClass = item.balanced ? "text-success-hover" : "text-danger-hover";
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_minmax(280px,1fr)_260px_170px] items-center border-b border-border bg-surface-tableHeader px-3 text-small font-bold text-text">
        <div />
        <div>{item.label}</div>
        <div className={`text-right text-[11px] uppercase tracking-wider ${balanceClass}`}>{item.balanced ? "Balanced" : "Review"}</div>
        <div className="text-right font-mono tabular-nums">{fmt(item.amount)}</div>
      </div>
    );
  }
  const line = item.line;
  return (
    <div style={style} className="absolute inset-x-0 grid grid-cols-[120px_minmax(280px,1fr)_260px_170px] items-center border-b border-border-subtle/60 px-3 text-small text-text hover:bg-background">
      <div className="truncate font-mono text-[11px] font-semibold">{line.account_code}</div>
      <div className="truncate">
        {line.drill_url ? <a href={line.drill_url} className="hover:text-primary hover:underline">{line.account_name}</a> : line.account_name}
      </div>
      <div className="truncate text-text-muted2">{detailLabel(line.detail)}</div>
      <div className="text-right font-mono tabular-nums">{fmt(line.amount)}</div>
    </div>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/reports/balance-sheet";
  createRoot(root).render(<BalanceSheetExplorer apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="balance-sheet"]').forEach((root) => {
  mount(root);
});
