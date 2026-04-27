import React, { useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";

type SortField = "date" | "type" | "number" | "customer" | "amount" | "status";
type SortDir = "asc" | "desc";

type SalesTransactionRow = {
  key: string;
  id: number;
  type: string;
  date: string;
  number: string;
  customer_id: number;
  customer_name: string;
  customer_url: string;
  memo: string;
  amount: string;
  currency: string;
  status: string;
  due_date?: string;
  detail_url: string;
};

type SalesTransactionsResponse = {
  rows: SalesTransactionRow[];
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
  page_start: number;
  page_end: number;
  rows_total: string;
  sort_by: SortField;
  sort_dir: SortDir;
};

const sortFields: SortField[] = ["date", "type", "number", "customer", "amount", "status"];

function isSortField(value: string): value is SortField {
  return sortFields.includes(value as SortField);
}

function pageURLFromAPI(apiURL: string): string {
  const url = new URL(apiURL, window.location.origin);
  return `/sales-transactions${url.search}`;
}

function apiURLFromPage(): string {
  return `/api/sales-transactions${window.location.search}`;
}

function nextSortDir(currentField: SortField, currentDir: SortDir, target: SortField): SortDir {
  if (currentField === target) {
    return currentDir === "asc" ? "desc" : "asc";
  }
  if (target === "type" || target === "number" || target === "customer" || target === "status") {
    return "asc";
  }
  return "desc";
}

function updateHiddenSortInputs(sortBy: SortField, sortDir: SortDir) {
  const sortInput = document.querySelector<HTMLInputElement>('form[action="/sales-transactions"] input[name="sort"]');
  const dirInput = document.querySelector<HTMLInputElement>('form[action="/sales-transactions"] input[name="dir"]');
  if (sortInput) sortInput.value = sortBy;
  if (dirInput) dirInput.value = sortDir;
}

function formatDate(date: string): string {
  const parts = date.split("-");
  if (parts.length !== 3) return date;
  const year = parts[0].slice(-2);
  const month = String(Number(parts[1]));
  const day = String(Number(parts[2]));
  return `${day}/${month}/${year}`;
}

function formatMoney(value: string): string {
  const num = Number(value);
  if (!Number.isFinite(num)) return value;
  return num.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2
  });
}

function typeLabel(type: string): string {
  switch (type) {
    case "invoice":
      return "Invoice";
    case "quote":
      return "Quote";
    case "sales_order":
      return "Sales order";
    case "payment":
      return "Payment";
    case "credit_note":
      return "Credit memo";
    case "return":
      return "Return";
    default:
      return type;
  }
}

function statusKind(row: SalesTransactionRow): "success" | "danger" | "warning" | "info" | "neutral" {
  const status = row.status.toLowerCase();
  switch (row.type) {
    case "invoice":
      if (status === "paid") return "success";
      if (status === "overdue") return "danger";
      if (status === "partially_paid") return "info";
      return "neutral";
    case "quote":
      if (status === "accepted") return "success";
      if (status === "rejected" || status === "cancelled") return "danger";
      if (status === "sent") return "info";
      return "neutral";
    case "sales_order":
      if (status === "invoiced" || status === "closed") return "success";
      if (status === "cancelled") return "danger";
      if (status === "confirmed" || status === "partially_invoiced") return "info";
      return "neutral";
    case "payment":
      if (status === "confirmed") return "success";
      if (status === "voided") return "danger";
      return "neutral";
    case "credit_note":
      if (status === "applied" || status === "fully_applied") return "success";
      if (status === "voided") return "danger";
      if (status === "issued") return "info";
      return "neutral";
    case "return":
      if (status === "approved" || status === "processed") return "success";
      if (status === "rejected" || status === "cancelled") return "danger";
      if (status === "submitted") return "info";
      return "neutral";
    default:
      return "neutral";
  }
}

function statusLabel(row: SalesTransactionRow): string {
  const status = row.status.toLowerCase();
  if (!status) return "-";
  if (row.type === "invoice" && status === "overdue" && row.due_date) {
    const due = new Date(`${row.due_date}T00:00:00`);
    const now = new Date();
    const days = Math.floor((now.getTime() - due.getTime()) / 86400000);
    if (days > 0) return `Overdue ${days}d`;
  }
  if (status === "partially_paid") return "Partially paid";
  if (status === "partially_invoiced") return "Partially invoiced";
  if (status === "fully_applied") return "Fully applied";
  return status.slice(0, 1).toUpperCase() + status.slice(1).replaceAll("_", " ");
}

function badgeClass(row: SalesTransactionRow): string {
  const base = "inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium";
  switch (statusKind(row)) {
    case "success":
      return `${base} bg-success-soft text-success-hover`;
    case "danger":
      return `${base} bg-danger-soft text-danger-hover`;
    case "warning":
      return `${base} bg-warning-soft text-warning-hover`;
    case "info":
      return `${base} bg-primary-soft text-primary`;
    default:
      return `${base} bg-background text-text-muted2`;
  }
}

function sumSelected(rows: SalesTransactionRow[], selected: Set<string>): string {
  const total = rows.reduce((acc, row) => {
    if (!selected.has(row.key)) return acc;
    const amount = Number(row.amount);
    return Number.isFinite(amount) ? acc + amount : acc;
  }, 0);
  return total.toFixed(2);
}

type HeaderProps = {
  field: SortField;
  label: string;
  align?: "left" | "right";
  data: SalesTransactionsResponse;
  onSort: (field: SortField) => void;
};

function Header({ field, label, align = "left", data, onSort }: HeaderProps) {
  const active = data.sort_by === field;
  const icon = active ? (data.sort_dir === "asc" ? "^" : "v") : "";
  const alignClass = align === "right" ? "justify-end text-right" : "";
  return (
    <th className={`px-3 py-2 ${align === "right" ? "text-right" : ""}`}>
      <button
        type="button"
        className={`inline-flex items-center gap-1 hover:text-text ${alignClass}`}
        onClick={() => onSort(field)}
      >
        <span>{label}</span>
        <span className={active ? "text-primary" : "text-text-muted3"}>{icon}</span>
      </button>
    </th>
  );
}

type AppProps = {
  initialAPIURL: string;
};

function SalesTransactionsApp({ initialAPIURL }: AppProps) {
  const [apiURL, setAPIURL] = useState(initialAPIURL);
  const [data, setData] = useState<SalesTransactionsResponse | null>(null);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async (targetURL: string, pushHistory: boolean) => {
    setLoading(true);
    setError("");
    try {
      const response = await fetch(targetURL, {
        credentials: "same-origin",
        headers: { Accept: "application/json" }
      });
      if (!response.ok) {
        throw new Error(`Request failed with ${response.status}`);
      }
      const payload = (await response.json()) as SalesTransactionsResponse;
      setData(payload);
      setSelected(new Set());
      setAPIURL(targetURL);
      updateHiddenSortInputs(payload.sort_by, payload.sort_dir);
      if (pushHistory) {
        window.history.pushState({ salesTransactionsAPIURL: targetURL }, "", pageURLFromAPI(targetURL));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load sales transactions.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(initialAPIURL, false);
  }, [initialAPIURL, load]);

  useEffect(() => {
    const onPopState = () => {
      void load(apiURLFromPage(), false);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [load]);

  const selectedAmount = useMemo(() => {
    if (!data) return "0.00";
    return sumSelected(data.rows, selected);
  }, [data, selected]);

  const allPageSelected = data != null && data.rows.length > 0 && data.rows.every((row) => selected.has(row.key));

  const changeSort = (field: SortField) => {
    if (!data) return;
    const url = new URL(apiURL, window.location.origin);
    url.searchParams.set("sort", field);
    url.searchParams.set("dir", nextSortDir(data.sort_by, data.sort_dir, field));
    url.searchParams.set("page", "1");
    void load(`${url.pathname}${url.search}`, true);
  };

  const goToPage = (page: number) => {
    const url = new URL(apiURL, window.location.origin);
    url.searchParams.set("page", String(page));
    void load(`${url.pathname}${url.search}`, true);
  };

  const toggleAllPage = () => {
    if (!data) return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (data.rows.every((row) => next.has(row.key))) {
        data.rows.forEach((row) => next.delete(row.key));
      } else {
        data.rows.forEach((row) => next.add(row.key));
      }
      return next;
    });
  };

  const toggleRow = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  return (
    <div className="space-y-2" data-react-sales-transactions-ready="true">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-surface px-3 py-2 text-small shadow-sm">
        <div className="text-text-muted2">
          {data && data.total > 0 ? (
            <span>
              Showing <span className="tabular-nums text-text">{data.page_start}-{data.page_end}</span> of{" "}
              <span className="tabular-nums text-text">{data.total}</span>
            </span>
          ) : (
            <span>No matching transactions</span>
          )}
          {loading ? <span className="ml-2 text-text-muted3">Loading...</span> : null}
        </div>
        <div className="flex items-center gap-3">
          {selected.size > 0 ? (
            <div className="rounded-md border border-border bg-background px-2 py-1 text-text-muted2">
              <span className="font-medium text-text">{selected.size}</span> selected,{" "}
              <span className="font-mono tabular-nums text-text">{formatMoney(selectedAmount)}</span>
            </div>
          ) : null}
        </div>
      </div>

      {error ? (
        <div className="rounded-lg border border-border-danger bg-danger-soft px-3 py-2 text-small text-danger-hover">
          {error}
        </div>
      ) : null}

      <div className="rounded-lg border border-border bg-surface shadow-sm">
        <div className="max-h-[68vh] overflow-auto">
          <table className="w-full min-w-[1100px] text-left text-body">
            <thead className="sticky top-0 z-10 border-b border-border bg-surface text-[11px] uppercase tracking-wider text-text-muted">
              <tr>
                <th className="w-10 px-3 py-2">
                  <input
                    type="checkbox"
                    checked={allPageSelected}
                    onChange={toggleAllPage}
                    aria-label="Select all visible transactions"
                    className="h-4 w-4 rounded border-border-input text-primary focus:ring-primary-focus"
                  />
                </th>
                {data ? (
                  <>
                    <Header field="date" label="Date" data={data} onSort={changeSort} />
                    <Header field="type" label="Type" data={data} onSort={changeSort} />
                    <Header field="number" label="No." data={data} onSort={changeSort} />
                    <Header field="customer" label="Customer" data={data} onSort={changeSort} />
                    <th className="px-3 py-2">Memo</th>
                    <Header field="amount" label="Amount" align="right" data={data} onSort={changeSort} />
                    <Header field="status" label="Status" data={data} onSort={changeSort} />
                  </>
                ) : (
                  <th className="px-3 py-2" colSpan={7}>Transactions</th>
                )}
                <th className="w-28 px-3 py-2 text-right">Action</th>
              </tr>
            </thead>
            <tbody className="text-text">
              {data && data.rows.length === 0 ? (
                <tr>
                  <td colSpan={9} className="px-3 py-10 text-center text-small text-text-muted2">
                    No transactions match your filters.
                  </td>
                </tr>
              ) : null}
              {data?.rows.map((row) => (
                <tr key={row.key} className="border-b border-border-subtle hover:bg-background">
                  <td className="w-10 px-3 py-2">
                    <input
                      type="checkbox"
                      checked={selected.has(row.key)}
                      onChange={() => toggleRow(row.key)}
                      aria-label={`Select ${row.number}`}
                      className="h-4 w-4 rounded border-border-input text-primary focus:ring-primary-focus"
                    />
                  </td>
                  <td className="px-3 py-2 text-small tabular-nums text-text-muted2">{formatDate(row.date)}</td>
                  <td className="px-3 py-2 text-small">{typeLabel(row.type)}</td>
                  <td className="px-3 py-2 text-small font-mono">
                    {row.detail_url ? (
                      <a href={row.detail_url} className="text-primary hover:underline">{row.number}</a>
                    ) : (
                      row.number
                    )}
                  </td>
                  <td className="px-3 py-2 text-small">
                    {row.customer_url ? (
                      <a href={row.customer_url} className="hover:text-primary hover:underline">{row.customer_name}</a>
                    ) : (
                      <span className="text-text-muted2">-</span>
                    )}
                  </td>
                  <td className="max-w-[340px] px-3 py-2 text-small text-text-muted2">
                    <span className="block truncate" title={row.memo || undefined}>{row.memo || "-"}</span>
                  </td>
                  <td className="px-3 py-2 text-right text-small font-mono tabular-nums">
                    <span className={Number(row.amount) < 0 ? "text-danger-hover" : ""}>{formatMoney(row.amount)}</span>
                  </td>
                  <td className="px-3 py-2 text-small">
                    <span className={badgeClass(row)}>{statusLabel(row)}</span>
                  </td>
                  <td className="w-28 px-3 py-2 text-right text-small">
                    {row.detail_url ? <a href={row.detail_url} className="text-primary hover:underline">View/Edit</a> : null}
                  </td>
                </tr>
              ))}
            </tbody>
            {data && data.rows.length > 0 ? (
              <tfoot className="sticky bottom-0 border-t border-border bg-background">
                <tr>
                  <td colSpan={6} className="px-3 py-2 text-small font-semibold text-text-muted2">Page total</td>
                  <td className="px-3 py-2 text-right text-small font-semibold tabular-nums text-text">
                    {formatMoney(data.rows_total)}
                  </td>
                  <td colSpan={2}></td>
                </tr>
              </tfoot>
            ) : null}
          </table>
        </div>
      </div>

      {data && data.total > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-small text-text-muted2">
          <div>
            Page <span className="tabular-nums text-text">{data.page}</span> /{" "}
            <span className="tabular-nums text-text">{data.total_pages}</span>
          </div>
          <div className="flex items-center gap-3">
            <button type="button" disabled={data.page <= 1} onClick={() => goToPage(1)} className="hover:text-text disabled:text-text-muted3">First</button>
            <button type="button" disabled={data.page <= 1} onClick={() => goToPage(data.page - 1)} className="hover:text-text disabled:text-text-muted3">Previous</button>
            <button type="button" disabled={data.page >= data.total_pages} onClick={() => goToPage(data.page + 1)} className="hover:text-text disabled:text-text-muted3">Next</button>
            <button type="button" disabled={data.page >= data.total_pages} onClick={() => goToPage(data.total_pages)} className="hover:text-text disabled:text-text-muted3">Last</button>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function mountSalesTransactions(root: HTMLElement) {
  const initialAPIURL = root.dataset.apiUrl || apiURLFromPage();
  createRoot(root).render(<SalesTransactionsApp initialAPIURL={initialAPIURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="sales-transactions"]').forEach((root) => {
  mountSalesTransactions(root);
});

export {};
