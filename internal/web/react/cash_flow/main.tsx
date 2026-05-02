import React, { useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    balancizFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type CashAccount = {
  account_id: number;
  account_code: string;
  account_name: string;
  opening_balance: string;
  total_inflow: string;
  total_outflow: string;
  closing_balance: string;
  drill_url: string;
};

type SourceRow = {
  source_type: string;
  source_label: string;
  inflow: string;
  outflow: string;
  net: string;
};

type CashFlowResponse = {
  from: string;
  to: string;
  totals: {
    opening_cash: string;
    total_inflow: string;
    total_outflow: string;
    net_change: string;
    closing_cash: string;
  };
  accounts: CashAccount[];
  sources: {
    inflows: SourceRow[];
    outflows: SourceRow[];
  };
};

function money(value: string): number {
  const n = Number(String(value || "").replace(/,/g, ""));
  return Number.isFinite(n) ? n : 0;
}

function fmt(value: string): string {
  return money(value).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function accountMatches(account: CashAccount, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [account.account_code, account.account_name, account.opening_balance, account.total_inflow, account.total_outflow, account.closing_balance]
    .some((value) => String(value || "").toLowerCase().includes(q));
}

function sourceMatches(row: SourceRow, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [row.source_type, row.source_label, row.inflow, row.outflow, row.net].some((value) => String(value || "").toLowerCase().includes(q));
}

async function fetchJSON(url: string): Promise<CashFlowResponse> {
  const fetchFn = window.balancizFetch || fetch;
  const response = await fetchFn(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Cash Flow request failed with ${response.status}`);
  return (await response.json()) as CashFlowResponse;
}

function CashFlowExplorer({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<CashFlowResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setData(await fetchJSON(apiURL));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Cash Flow could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const accounts = useMemo(() => (data ? data.accounts.filter((account) => accountMatches(account, query)) : []), [data, query]);
  const inflows = useMemo(() => (data ? data.sources.inflows.filter((row) => sourceMatches(row, query)) : []), [data, query]);
  const outflows = useMemo(() => (data ? data.sources.outflows.filter((row) => sourceMatches(row, query)) : []), [data, query]);

  if (!data && loading) {
    return <Shell><div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2">Loading Cash Flow...</div></Shell>;
  }
  if (!data) {
    return <Shell><ErrorBox message={error || "Cash Flow could not be loaded."} /></Shell>;
  }

  if (data.accounts.length === 0) {
    return (
      <Shell>
        {error ? <ErrorBox message={error} /> : null}
        <div className="rounded-lg border border-border bg-surface p-4 text-text-muted2" data-react-cash-flow-ready="true">
          No bank-type accounts found. Mark at least one account as "Bank" in the chart of accounts to see cash flow data here.
        </div>
      </Shell>
    );
  }

  return (
    <Shell>
      {error ? <ErrorBox message={error} /> : null}
      <section className="grid grid-cols-1 gap-3 lg:grid-cols-5">
        <Metric label="Opening Cash" value={fmt(data.totals.opening_cash)} />
        <Metric label="Inflows" value={fmt(data.totals.total_inflow)} tone="good" />
        <Metric label="Outflows" value={fmt(data.totals.total_outflow)} tone="bad" />
        <Metric label="Net Change" value={fmt(data.totals.net_change)} tone={money(data.totals.net_change) >= 0 ? "good" : "bad"} />
        <Metric label="Closing Cash" value={fmt(data.totals.closing_cash)} />
      </section>

      <section className="rounded-lg border border-border bg-surface shadow-sm" data-react-cash-flow-ready="true">
        <div className="flex flex-wrap items-end gap-3 border-b border-border px-4 py-3">
          <label className="min-w-[280px] flex-1">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Search</span>
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Account, source, amount..."
              className="mt-1 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
            />
          </label>
          <span className="text-small text-text-muted2">{data.from} to {data.to}</span>
          <button type="button" onClick={() => void load()} disabled={loading} className="rounded-md bg-primary px-3 py-2 text-small font-semibold text-onPrimary hover:bg-primary-hover disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text">
            {loading ? "Refreshing..." : "Refresh"}
          </button>
        </div>
        <AccountTable accounts={accounts} />
      </section>

      <section className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <SourceTable title="Where Cash Came From" rows={inflows} positive />
        <SourceTable title="Where Cash Went" rows={outflows} />
      </section>
    </Shell>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return <div className="mt-4 space-y-3" data-react-cash-flow-shell="true">{children}</div>;
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

function AccountTable({ accounts }: { accounts: CashAccount[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[900px] text-left text-body">
        <thead className="border-b border-border-subtle text-small uppercase tracking-wider text-text-muted">
          <tr>
            <th className="px-4 py-2 font-medium">Account</th>
            <th className="px-4 py-2 text-right font-medium">Opening</th>
            <th className="px-4 py-2 text-right font-medium">Inflows</th>
            <th className="px-4 py-2 text-right font-medium">Outflows</th>
            <th className="px-4 py-2 text-right font-medium">Closing</th>
          </tr>
        </thead>
        <tbody className="text-text">
          {accounts.length === 0 ? (
            <tr><td colSpan={5} className="px-4 py-8 text-center text-small text-text-muted2">No accounts match this view.</td></tr>
          ) : accounts.map((account) => (
            <tr key={account.account_id} className="border-b border-border-subtle/60 hover:bg-background">
              <td className="px-4 py-2">
                {account.drill_url ? (
                  <a href={account.drill_url} className="text-primary hover:underline">{account.account_code} - {account.account_name}</a>
                ) : `${account.account_code} - ${account.account_name}`}
              </td>
              <td className="px-4 py-2 text-right font-mono tabular-nums">{fmt(account.opening_balance)}</td>
              <td className="px-4 py-2 text-right font-mono tabular-nums text-success-hover">{fmt(account.total_inflow)}</td>
              <td className="px-4 py-2 text-right font-mono tabular-nums text-danger-hover">{fmt(account.total_outflow)}</td>
              <td className="px-4 py-2 text-right font-mono font-semibold tabular-nums">{fmt(account.closing_balance)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SourceTable({ title, rows, positive }: { title: string; rows: SourceRow[]; positive?: boolean }) {
  const max = Math.max(1, ...rows.map((row) => Math.abs(money(row.net))));
  return (
    <div className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="border-b border-border-subtle bg-surface-tableHeader px-4 py-2.5">
        <div className="font-semibold text-text">{title}</div>
      </div>
      {rows.length === 0 ? (
        <div className="px-4 py-8 text-center text-small text-text-muted2">No activity in this category for the period.</div>
      ) : (
        <div className="divide-y divide-border-subtle/60">
          {rows.map((row) => {
            const amount = Math.abs(money(row.net));
            const width = `${Math.max(5, (amount / max) * 100)}%`;
            return (
              <div key={`${row.source_type}:${row.source_label}`} className="px-4 py-2.5">
                <div className="flex items-center justify-between gap-3 text-small">
                  <span className="truncate font-medium text-text">{row.source_label}</span>
                  <span className={`font-mono tabular-nums ${positive ? "text-success-hover" : "text-danger-hover"}`}>{fmt(String(amount))}</span>
                </div>
                <div className="mt-2 h-1.5 rounded-full bg-background">
                  <div className={`h-1.5 rounded-full ${positive ? "bg-success-hover" : "bg-danger-hover"}`} style={{ width }} />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/reports/cash-flow";
  createRoot(root).render(<CashFlowExplorer apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="cash-flow"]').forEach((root) => {
  mount(root);
});
