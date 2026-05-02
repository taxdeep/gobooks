import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    balancizFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type AccountTransactionRow = {
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

type AccountTransactionsResponse = {
  from: string;
  to: string;
  account_id: number;
  account_code: string;
  account_name: string;
  root_type: string;
  detail_type: string;
  starting_balance: string;
  total_debits: string;
  total_credits: string;
  ending_balance: string;
  balance_change: string;
  row_count: number;
  rows: AccountTransactionRow[];
};

type FlatRow =
  | { kind: "starting"; key: string }
  | { kind: "row"; key: string; row: AccountTransactionRow }
  | { kind: "total"; key: string }
  | { kind: "change"; key: string };

const rowHeight = 34;
const overscan = 12;

function money(value: string): number {
  const n = Number(String(value || "").replace(/,/g, ""));
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: string): string {
  return money(value).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function detailLabel(data: AccountTransactionsResponse): string {
  return [data.root_type, data.detail_type].filter(Boolean).join(" / ").replaceAll("_", " ");
}

function rowMatches(row: AccountTransactionRow, query: string): boolean {
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

function buildRows(data: AccountTransactionsResponse, query: string): FlatRow[] {
  const rows: FlatRow[] = [{ kind: "starting", key: "starting" }];
  for (const row of data.rows) {
    if (rowMatches(row, query)) rows.push({ kind: "row", key: row.key, row });
  }
  rows.push({ kind: "total", key: "total" }, { kind: "change", key: "change" });
  return rows;
}

async function fetchJSON(url: string): Promise<AccountTransactionsResponse> {
  const fetchFn = window.balancizFetch || fetch;
  const response = await fetchFn(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Account transactions request failed with ${response.status}`);
  return (await response.json()) as AccountTransactionsResponse;
}

function AccountTransactionsExplorer({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<AccountTransactionsResponse | null>(null);
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
      setError(err instanceof Error ? err.message : "Account transactions could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const rows = useMemo(() => (data ? buildRows(data, query) : []), [data, query]);
  const visibleTransactions = rows.filter((row) => row.kind === "row").length;
  const totalHeight = rows.length * rowHeight;
  const viewportHeight = viewportRef.current?.clientHeight || 560;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(rows.length, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  const virtualRows = rows.slice(start, end);

  if (!data && loading) {
    return <Shell><div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2">Loading account transactions...</div></Shell>;
  }
  if (!data) {
    return <Shell><ErrorBox message={error || "Account transactions could not be loaded."} /></Shell>;
  }

  return (
    <Shell>
      {error ? <ErrorBox message={error} /> : null}
      <section className="grid grid-cols-1 gap-3 lg:grid-cols-4">
        <Metric label="Account" value={`${data.account_code} ${data.account_name}`} hint={detailLabel(data)} />
        <Metric label="Period" value={`${data.from} to ${data.to}`} />
        <Metric label="Rows" value={String(visibleTransactions)} hint={`${data.row_count} total`} />
        <Metric label="Debits / Credits" value={`${fmt(data.total_debits)} / ${fmt(data.total_credits)}`} />
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
              placeholder="Date, type, no, name, memo, amount..."
              className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
            />
          </label>
          <button type="button" onClick={() => void load()} disabled={loading} className="rounded-md bg-primary px-3 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text">
            {loading ? "Refreshing..." : "Refresh"}
          </button>
        </div>

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
              data-react-account-transactions-ready="true"
            >
              <div style={{ height: totalHeight || rowHeight, position: "relative" }}>
                {virtualRows.length === 0 ? (
                  <div className="absolute inset-x-0 top-0 px-4 py-10 text-center text-small text-text-muted2">No transactions match this view.</div>
                ) : virtualRows.map((row, offset) => (
                  <RowView key={row.key} item={row} data={data} top={(start + offset) * rowHeight} />
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
  return <div className="mt-4 space-y-3" data-react-account-transactions-shell="true">{children}</div>;
}

function ErrorBox({ message }: { message: string }) {
  return <div className="rounded-lg border border-border-danger bg-danger-soft px-3 py-2 text-small text-danger-hover">{message}</div>;
}

function Metric({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{label}</div>
      <div className="mt-1 truncate text-body font-semibold tabular-nums text-text">{value}</div>
      {hint ? <div className="mt-0.5 truncate text-[11px] text-text-muted3">{hint}</div> : null}
    </div>
  );
}

function RowView({ item, data, top }: { item: FlatRow; data: AccountTransactionsResponse; top: number }) {
  const style = { transform: `translateY(${top}px)`, height: rowHeight } as React.CSSProperties;
  if (item.kind === "starting") {
    return <SummaryRow style={style} label="Starting Balance" balance={data.starting_balance} />;
  }
  if (item.kind === "total") {
    return (
      <div style={style} className="absolute inset-x-0 grid grid-cols-[110px_120px_150px_190px_minmax(260px,1fr)_130px_130px_130px] items-center border-b border-border bg-background px-3 text-small font-semibold text-text">
        <div />
        <div />
        <div />
        <div />
        <div>Totals and Ending Balance</div>
        <div className="text-right font-mono tabular-nums">{fmt(data.total_debits)}</div>
        <div className="text-right font-mono tabular-nums">{fmt(data.total_credits)}</div>
        <div className="text-right font-mono tabular-nums">{fmt(data.ending_balance)}</div>
      </div>
    );
  }
  if (item.kind === "change") {
    return <SummaryRow style={style} label="Balance Change" balance={data.balance_change} muted />;
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
      <div className="text-right font-mono tabular-nums">{money(row.debit) > 0 ? fmt(row.debit) : ""}</div>
      <div className="text-right font-mono tabular-nums">{money(row.credit) > 0 ? fmt(row.credit) : ""}</div>
      <div className="text-right font-mono tabular-nums">{fmt(row.balance)}</div>
    </div>
  );
}

function SummaryRow({ style, label, balance, muted }: { style: React.CSSProperties; label: string; balance: string; muted?: boolean }) {
  return (
    <div style={style} className="absolute inset-x-0 grid grid-cols-[110px_120px_150px_190px_minmax(260px,1fr)_130px_130px_130px] items-center border-b border-border-subtle bg-surface-tableHeader px-3 text-small">
      <div />
      <div />
      <div />
      <div />
      <div className={muted ? "text-text-muted2" : "font-semibold text-text"}>{label}</div>
      <div />
      <div />
      <div className="text-right font-mono tabular-nums font-semibold text-text">{fmt(balance)}</div>
    </div>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/reports/account-transactions";
  createRoot(root).render(<AccountTransactionsExplorer apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="account-transactions"]').forEach((root) => {
  mount(root);
});
