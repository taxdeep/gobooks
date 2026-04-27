import React, { useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";

declare global {
  interface Window {
    gobooksFetch?: (url: string, options?: RequestInit) => Promise<Response>;
  }
}

type KPI = {
  key: string;
  label: string;
  value: string;
  is_positive: boolean;
  hint: string;
  tone: "success" | "danger" | "primary" | "warning" | string;
  href?: string;
};

type TrendPoint = {
  label: string;
  revenue: string;
  is_positive: boolean;
};

type MoneyValue = {
  value: string;
  is_positive: boolean;
};

type ExpenseLine = {
  account: string;
  amount: MoneyValue;
};

type BankAccount = {
  code: string;
  name: string;
};

type DashboardTask = {
  id: string;
  task_type: string;
  title: string;
  reason: string;
  priority: "low" | "medium" | "high" | "urgent" | string;
  status: string;
  action_url: string;
  due_date?: string;
  evidence?: Record<string, unknown>;
  ai_generated: boolean;
};

type DashboardSuggestion = {
  id: string;
  widget_key: string;
  title: string;
  reason: string;
  status: string;
  source: string;
  confidence: string;
  evidence?: Record<string, unknown>;
};

type DashboardWidget = {
  id: string;
  widget_key: string;
  title: string;
  source: string;
};

type DashboardOverview = {
  range_label: string;
  generated_at: string;
  kpis: KPI[];
  revenue_trend: TrendPoint[];
  expenses: {
    total: MoneyValue;
    top_lines: ExpenseLine[];
  };
  bank_accounts: BankAccount[];
  tasks: DashboardTask[];
  suggestions: DashboardSuggestion[];
  widgets: DashboardWidget[];
};

function fetchJSON(url: string): Promise<DashboardOverview> {
  return fetch(url, { credentials: "same-origin", headers: { Accept: "application/json" } }).then(async (response) => {
    if (!response.ok) throw new Error(`Request failed with ${response.status}`);
    return (await response.json()) as DashboardOverview;
  });
}

async function postJSON(url: string) {
  const fetchFn = window.gobooksFetch || fetch;
  const response = await fetchFn(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) throw new Error(`Request failed with ${response.status}`);
}

function classNames(...parts: Array<string | false | null | undefined>) {
  return parts.filter(Boolean).join(" ");
}

function numberFromMoney(value: string): number {
  const cleaned = value.replace(/[^\d.-]/g, "");
  const n = Number(cleaned);
  return Number.isFinite(n) ? Math.abs(n) : 0;
}

function compactDate(value?: string): string {
  if (!value) return "";
  const parts = value.split("-");
  if (parts.length !== 3) return value;
  return `${Number(parts[2])}/${Number(parts[1])}/${parts[0].slice(-2)}`;
}

function priorityClass(priority: string) {
  switch (priority) {
    case "urgent":
      return "bg-danger-soft text-danger-hover";
    case "high":
      return "bg-warning-soft text-warning-hover";
    case "medium":
      return "bg-primary-soft text-primary";
    default:
      return "bg-background text-text-muted2";
  }
}

function kpiClass(tone: string, isPositive: boolean) {
  if (tone === "danger") return "text-danger-hover";
  if (tone === "success") return "text-success-hover";
  if (tone === "warning") return isPositive ? "text-text" : "text-warning-hover";
  return "text-text";
}

function maxTrendValue(points: TrendPoint[]) {
  return points.reduce((max, point) => Math.max(max, numberFromMoney(point.revenue)), 0);
}

function evidenceRows(evidence?: Record<string, unknown>): Array<[string, string]> {
  if (!evidence) return [];
  return Object.entries(evidence)
    .slice(0, 5)
    .map(([key, value]) => [key.replaceAll("_", " "), typeof value === "object" ? JSON.stringify(value) : String(value)]);
}

function DashboardIsland({ apiURL }: { apiURL: string }) {
  const [data, setData] = useState<DashboardOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setData(await fetchJSON(apiURL));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Dashboard could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, [apiURL]);

  useEffect(() => {
    void load();
  }, [load]);

  const runRefresh = async () => {
    setBusy("refresh");
    setError("");
    try {
      await postJSON("/api/action-center/tasks/run");
      await postJSON("/api/dashboard/suggestions/run");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Refresh failed.");
    } finally {
      setBusy("");
    }
  };

  const transitionTask = async (taskID: string, action: "done" | "dismiss" | "snooze") => {
    setBusy(taskID + action);
    setError("");
    try {
      const suffix = action === "snooze" ? "snooze?days=7" : action;
      await postJSON(`/api/action-center/tasks/${taskID}/${suffix}`);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Task update failed.");
    } finally {
      setBusy("");
    }
  };

  const transitionSuggestion = async (suggestionID: string, action: "accept" | "dismiss" | "snooze") => {
    setBusy(suggestionID + action);
    setError("");
    try {
      const suffix = action === "snooze" ? "snooze?days=7" : action;
      await postJSON(`/api/dashboard/suggestions/${suggestionID}/${suffix}`);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Suggestion update failed.");
    } finally {
      setBusy("");
    }
  };

  const maxRevenue = useMemo(() => maxTrendValue(data?.revenue_trend || []), [data]);
  const attentionCount = (data?.tasks.length || 0) + (data?.suggestions.length || 0);
  const revenueReportHref = data?.kpis.find((kpi) => kpi.key === "revenue")?.href || "/reports/income-statement";
  const expensesReportHref = data?.kpis.find((kpi) => kpi.key === "expenses")?.href || "/reports/income-statement#expenses";

  if (!data && loading) {
    return (
      <div className="max-w-[95%] space-y-4">
        <DashboardHeader loading={loading} onRefresh={runRefresh} busy={busy === "refresh"} />
        <div className="rounded-lg border border-border bg-surface p-8 text-center text-small text-text-muted2 shadow-sm">Loading dashboard...</div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="max-w-[95%] space-y-4">
        <DashboardHeader loading={loading} onRefresh={runRefresh} busy={busy === "refresh"} />
        <ErrorBox message={error || "Dashboard could not be loaded."} />
      </div>
    );
  }

  return (
    <div className="max-w-[95%] space-y-4" data-react-dashboard-ready="true">
      <DashboardHeader loading={loading} onRefresh={runRefresh} busy={busy === "refresh"} generatedAt={data.generated_at} />
      {error ? <ErrorBox message={error} /> : null}

      <section className="grid grid-cols-1 gap-3 md:grid-cols-3 xl:grid-cols-6">
        {data.kpis.map((kpi) => (
          <KPICard key={kpi.key} kpi={kpi} />
        ))}
      </section>

      <section className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1.25fr)_minmax(380px,0.75fr)]">
        <div className="space-y-4">
          <div className="rounded-lg border border-border bg-surface shadow-sm">
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <div>
                <div className="text-section font-semibold text-text">Revenue trend</div>
                <div className="text-small text-text-muted2">Last 3 calendar months</div>
              </div>
              <a href={revenueReportHref} className="text-small text-primary hover:underline">Income Statement</a>
            </div>
            <div className="px-4 py-4">
              {data.revenue_trend.length > 0 ? (
                <div className="flex h-44 items-end gap-4">
                  {data.revenue_trend.map((point) => {
                    const pct = maxRevenue <= 0 ? 4 : Math.max(4, Math.min(100, (numberFromMoney(point.revenue) / maxRevenue) * 100));
                    return (
                      <div key={point.label} className="flex h-full flex-1 flex-col justify-end gap-2">
                        <div className="text-center text-[11px] font-semibold tabular-nums text-text-muted2">{point.revenue}</div>
                        <div className="rounded-t bg-primary/70 transition-colors hover:bg-primary" style={{ height: `${pct}%` }} />
                        <div className="text-center text-small text-text-muted">{point.label}</div>
                      </div>
                    );
                  })}
                </div>
              ) : (
                <EmptyState text="No revenue recorded yet." />
              )}
            </div>
          </div>

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <TopExpenses expenses={data.expenses} reportHref={expensesReportHref} />
            <BankAccounts accounts={data.bank_accounts} />
          </div>
        </div>

        <div className="space-y-4">
          <ActionCenter tasks={data.tasks} busy={busy} onTaskAction={transitionTask} onRefresh={runRefresh} attentionCount={attentionCount} />
          <WidgetSuggestions suggestions={data.suggestions} widgets={data.widgets} busy={busy} onSuggestionAction={transitionSuggestion} />
        </div>
      </section>

      <QuickActions />
    </div>
  );
}

function KPICard({ kpi }: { kpi: KPI }) {
  const inner = (
    <>
      <div className="text-[11px] font-semibold uppercase tracking-wider text-text-muted">{kpi.label}</div>
      <div className={classNames("mt-2 text-section font-semibold tabular-nums", kpiClass(kpi.tone, kpi.is_positive))}>{kpi.value}</div>
      <div className="mt-0.5 text-[11px] text-text-muted3">{kpi.hint}</div>
    </>
  );

  const cardClass = "rounded-lg border border-border bg-surface px-4 py-3 shadow-sm transition-colors";
  if (kpi.href) {
    return (
      <a href={kpi.href} className={classNames(cardClass, "block hover:border-primary/60 hover:bg-primary-soft/10")} title={`Open ${kpi.label} report`}>
        {inner}
      </a>
    );
  }

  return <div className={cardClass}>{inner}</div>;
}

function DashboardHeader({ loading, busy, generatedAt, onRefresh }: { loading: boolean; busy: boolean; generatedAt?: string; onRefresh: () => void }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-title font-semibold">Dashboard</h1>
        <p className="mt-1 text-small text-text-muted2">
          Last 30 days - core indicators, tasks, and suggestions
          {generatedAt ? <span className="ml-2 text-text-muted3">Updated {new Date(generatedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span> : null}
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          onClick={onRefresh}
          disabled={loading || busy}
          className="rounded-md border border-border-input px-3 py-1.5 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text disabled:cursor-not-allowed disabled:text-text-muted3"
        >
          {busy ? "Refreshing..." : "Refresh tasks"}
        </button>
        <a href="/invoices/new" className="rounded-md bg-primary px-3 py-1.5 text-small font-semibold text-onPrimary hover:bg-primary-hover">+ New Invoice</a>
        <a href="/bills/new" className="rounded-md border border-border-input px-3 py-1.5 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">+ New Bill</a>
        <a href="/expenses/new" className="rounded-md border border-border-input px-3 py-1.5 text-small font-semibold text-text-muted3 hover:bg-background hover:text-text">+ New Expense</a>
      </div>
    </div>
  );
}

function ErrorBox({ message }: { message: string }) {
  return <div className="rounded-lg border border-border-danger bg-danger-soft px-3 py-2 text-small text-danger-hover">{message}</div>;
}

function EmptyState({ text }: { text: string }) {
  return <div className="rounded-md border border-dashed border-border p-6 text-center text-small text-text-muted2">{text}</div>;
}

function TopExpenses({ expenses, reportHref }: { expenses: DashboardOverview["expenses"]; reportHref: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="flex items-start justify-between border-b border-border px-4 py-3">
        <div>
          <div className="text-section font-semibold text-text">Top expenses</div>
          <div className="text-small text-text-muted2">Largest expense accounts</div>
        </div>
        <a href={reportHref} className="text-section font-semibold tabular-nums text-danger-hover hover:underline">{expenses.total.value}</a>
      </div>
      <div className="p-4">
        {expenses.top_lines.length > 0 ? (
          <div className="space-y-2">
            {expenses.top_lines.map((line) => (
              <div key={line.account} className="flex items-center justify-between gap-3 rounded-md border border-border-subtle px-3 py-2">
                <div className="min-w-0 truncate text-body text-text">{line.account}</div>
                <div className="shrink-0 font-mono text-small tabular-nums text-danger-hover">{line.amount.value}</div>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState text="No expenses in this period." />
        )}
      </div>
    </div>
  );
}

function BankAccounts({ accounts }: { accounts: BankAccount[] }) {
  return (
    <div className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="flex items-start justify-between border-b border-border px-4 py-3">
        <div>
          <div className="text-section font-semibold text-text">Bank accounts</div>
          <div className="text-small text-text-muted2">Asset accounts</div>
        </div>
        <div className="rounded-full bg-background px-2 py-0.5 text-[11px] font-semibold text-text-muted3">{accounts.length}</div>
      </div>
      <div className="p-4">
        {accounts.length > 0 ? (
          <div className="space-y-2">
            {accounts.map((account) => (
              <div key={`${account.code}-${account.name}`} className="flex items-center justify-between gap-3 rounded-md border border-border-subtle px-3 py-2">
                <span className="font-mono text-small text-text-muted2">{account.code}</span>
                <span className="min-w-0 flex-1 truncate text-body text-text">{account.name}</span>
              </div>
            ))}
            <a href="/accounts" className="block text-center text-small text-primary hover:underline">View all accounts</a>
          </div>
        ) : (
          <EmptyState text="Add a bank account in Chart of Accounts." />
        )}
      </div>
    </div>
  );
}

function ActionCenter({
  tasks,
  busy,
  attentionCount,
  onTaskAction,
  onRefresh,
}: {
  tasks: DashboardTask[];
  busy: string;
  attentionCount: number;
  onTaskAction: (taskID: string, action: "done" | "dismiss" | "snooze") => void;
  onRefresh: () => void;
}) {
  return (
    <div className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div>
          <div className="text-section font-semibold text-text">Action Center</div>
          <div className="text-small text-text-muted2">{attentionCount} item(s) need review</div>
        </div>
        <button type="button" onClick={onRefresh} className="text-small text-primary hover:underline">Run</button>
      </div>
      <div className="divide-y divide-border-subtle">
        {tasks.length > 0 ? (
          tasks.map((task) => (
            <div key={task.id} className="space-y-2 px-4 py-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-semibold text-text">{task.title}</span>
                    <span className={classNames("rounded-full px-2 py-0.5 text-[11px] font-semibold", priorityClass(task.priority))}>{task.priority}</span>
                    {task.ai_generated ? <span className="rounded-full bg-primary-soft px-2 py-0.5 text-[11px] font-semibold text-primary">AI</span> : null}
                  </div>
                  <div className="mt-1 text-small text-text-muted2">{task.reason}</div>
                  {task.due_date ? <div className="mt-1 text-[11px] text-text-muted3">Due {compactDate(task.due_date)}</div> : null}
                </div>
              </div>
              <Evidence evidence={task.evidence} />
              <div className="flex flex-wrap items-center gap-3 text-small">
                {task.action_url ? <a href={task.action_url} className="font-medium text-primary hover:underline">Open</a> : null}
                <button type="button" disabled={busy === task.id + "done"} onClick={() => onTaskAction(task.id, "done")} className="text-success-hover hover:underline disabled:text-text-muted3">Done</button>
                <button type="button" disabled={busy === task.id + "snooze"} onClick={() => onTaskAction(task.id, "snooze")} className="text-text-muted2 hover:text-text disabled:text-text-muted3">Snooze</button>
                <button type="button" disabled={busy === task.id + "dismiss"} onClick={() => onTaskAction(task.id, "dismiss")} className="text-text-muted2 hover:text-text disabled:text-text-muted3">Dismiss</button>
              </div>
            </div>
          ))
        ) : (
          <div className="p-4">
            <EmptyState text="No open action tasks." />
          </div>
        )}
      </div>
    </div>
  );
}

function WidgetSuggestions({
  suggestions,
  widgets,
  busy,
  onSuggestionAction,
}: {
  suggestions: DashboardSuggestion[];
  widgets: DashboardWidget[];
  busy: string;
  onSuggestionAction: (suggestionID: string, action: "accept" | "dismiss" | "snooze") => void;
}) {
  return (
    <div className="rounded-lg border border-border bg-surface shadow-sm">
      <div className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div>
          <div className="text-section font-semibold text-text">Suggested widgets</div>
          <div className="text-small text-text-muted2">{widgets.length} active - {suggestions.length} pending</div>
        </div>
      </div>
      <div className="divide-y divide-border-subtle">
        {suggestions.length > 0 ? (
          suggestions.map((suggestion) => (
            <div key={suggestion.id} className="space-y-2 px-4 py-3">
              <div>
                <div className="font-semibold text-text">{suggestion.title}</div>
                <div className="mt-1 text-small text-text-muted2">{suggestion.reason}</div>
                <div className="mt-1 text-[11px] text-text-muted3">Confidence {suggestion.confidence} - {suggestion.source}</div>
              </div>
              <Evidence evidence={suggestion.evidence} />
              <div className="flex flex-wrap items-center gap-3 text-small">
                <button type="button" disabled={busy === suggestion.id + "accept"} onClick={() => onSuggestionAction(suggestion.id, "accept")} className="font-medium text-primary hover:underline disabled:text-text-muted3">Accept</button>
                <button type="button" disabled={busy === suggestion.id + "snooze"} onClick={() => onSuggestionAction(suggestion.id, "snooze")} className="text-text-muted2 hover:text-text disabled:text-text-muted3">Snooze</button>
                <button type="button" disabled={busy === suggestion.id + "dismiss"} onClick={() => onSuggestionAction(suggestion.id, "dismiss")} className="text-text-muted2 hover:text-text disabled:text-text-muted3">Dismiss</button>
              </div>
            </div>
          ))
        ) : (
          <div className="p-4">
            <EmptyState text="No pending widget suggestions." />
          </div>
        )}
      </div>
    </div>
  );
}

function Evidence({ evidence }: { evidence?: Record<string, unknown> }) {
  const rows = evidenceRows(evidence);
  if (rows.length === 0) return null;
  return (
    <details className="rounded-md border border-border-subtle bg-background px-3 py-2 text-small">
      <summary className="cursor-pointer text-text-muted2">Evidence</summary>
      <dl className="mt-2 grid grid-cols-1 gap-1">
        {rows.map(([key, value]) => (
          <div key={key} className="flex min-w-0 justify-between gap-3">
            <dt className="capitalize text-text-muted3">{key}</dt>
            <dd className="min-w-0 truncate text-right text-text" title={value}>{value}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

function QuickActions() {
  const actions = [
    { label: "Receive Payment", href: "/banking/receive-payment" },
    { label: "Pay Bills", href: "/banking/pay-bills" },
    { label: "Reconcile Bank", href: "/banking/reconcile" },
    { label: "AR Aging", href: "/reports/ar-aging" },
    { label: "AP Aging", href: "/ap-aging" },
    { label: "Balance Sheet", href: "/reports/balance-sheet" },
  ];
  return (
    <section className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="mb-3 text-section font-semibold text-text">Quick actions</div>
      <div className="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-6">
        {actions.map((action) => (
          <a key={action.href} href={action.href} className="rounded-md border border-border-input px-3 py-2 text-center text-small font-medium text-text hover:bg-background hover:text-primary">
            {action.label}
          </a>
        ))}
      </div>
    </section>
  );
}

function mount(root: HTMLElement) {
  const apiURL = root.dataset.apiUrl || "/api/dashboard/overview";
  createRoot(root).render(<DashboardIsland apiURL={apiURL} />);
}

document.querySelectorAll<HTMLElement>('[data-gb-react="dashboard"]').forEach((root) => {
  mount(root);
});
