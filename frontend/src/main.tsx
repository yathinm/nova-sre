import { StrictMode, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { RefreshCw } from "lucide-react";
import "./styles.css";

declare global {
  interface Window {
    NOVA_SRE_API_BASE?: string;
    NOVA_SRE_API_TOKEN?: string;
  }
}

const DEFAULT_API_BASE = "http://localhost:8080";
const ENDPOINTS = {
  events: ["/api/events", "/events"],
  jobs: ["/api/jobs", "/jobs"],
} as const;
const AUTO_REFRESH_MS = 30_000;

type Tone = "ok" | "warn" | "error";
type LoadStatus = "idle" | "loading" | "ready" | "empty" | "error";
type CardState = {
  label: string;
  value: string;
  detail: string;
  tone?: Tone;
};
type ListKind = keyof typeof ENDPOINTS;
type ListState<T> = {
  items: T[];
  source: string;
  message: string;
  status: LoadStatus;
  updatedAt: number | null;
};
type ListResult = Omit<ListState<ApiRecord>, "updatedAt">;
type ApiRecord = Record<string, unknown>;
type ApiError = Error & { status?: number };
type CommentAction = "created" | "updated" | "skipped" | "failed";
type ActivitySummaryState = {
  total: number;
  byStatus: Record<string, number>;
  byEvent: Record<string, number>;
  source: string;
  message: string;
  status: LoadStatus;
  updatedAt: number | null;
};
type RuntimeConfig = {
  activity_limit?: number;
  activity_store_enabled?: boolean;
  delivery_cache_ttl?: string;
  api_auth_enabled?: boolean;
  agent_auth_enabled?: boolean;
  api_cors_restricted?: boolean;
  runner_namespace?: string;
  runner_image?: string;
  runner_job_ttl_seconds?: number;
  github_comment_mode?: string;
};

function App() {
  const [apiBase, setApiBase] = useState(initialApiBase);
  const [apiToken, setApiToken] = useState(initialApiToken);
  const [notice, setNotice] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [paused, setPaused] = useState(false);
  const [lastRefreshAt, setLastRefreshAt] = useState<number | null>(null);
  const isRefreshingRef = useRef(false);
  const [healthCard, setHealthCard] = useState<CardState>({
    label: "API Health",
    value: "Checking",
    detail: "Waiting for /healthz",
  });
  const [metricsCard, setMetricsCard] = useState<CardState>({
    label: "Metrics",
    value: "Checking",
    detail: "Waiting for /metrics",
  });
  const [runtimeCard, setRuntimeCard] = useState<CardState>({
    label: "Runtime",
    value: "Checking",
    detail: "Waiting for /api/config",
  });
  const [events, setEvents] = useState<ListState<ApiRecord>>({
    items: [],
    source: "No endpoint",
    message: "",
    status: "idle",
    updatedAt: null,
  });
  const [jobs, setJobs] = useState<ListState<ApiRecord>>({
    items: [],
    source: "No endpoint",
    message: "",
    status: "idle",
    updatedAt: null,
  });
  const [activitySummary, setActivitySummary] = useState<ActivitySummaryState>({
    total: 0,
    byStatus: {},
    byEvent: {},
    source: "No endpoint",
    message: "",
    status: "idle",
    updatedAt: null,
  });
  const [runtimeConfig, setRuntimeConfig] = useState<RuntimeConfig | null>(null);

  const client = useMemo(() => createClient(apiBase, apiToken), [apiBase, apiToken]);

  const refresh = useCallback(async () => {
    if (isRefreshingRef.current) {
      return;
    }
    isRefreshingRef.current = true;
    const nextApiBase = trimTrailingSlash(apiBase || DEFAULT_API_BASE);
    setApiBase(nextApiBase);
    window.localStorage.setItem("novaSreApiBase", nextApiBase);
    window.localStorage.removeItem("novaSreApiToken");
    window.sessionStorage.setItem("novaSreApiToken", apiToken);
    setNotice("");
    setRefreshing(true);
    setEvents((current) => ({ ...current, message: "Loading recent webhook deliveries...", status: "loading" }));
    setJobs((current) => ({ ...current, message: "Loading runner job observations...", status: "loading" }));
    setActivitySummary((current) => ({ ...current, message: "Loading activity summary...", status: "loading" }));

    try {
      const [healthOk, , , summary, eventList, jobList] = await Promise.all([
        loadHealth(client, setHealthCard),
        loadMetrics(client, setMetricsCard),
        loadRuntimeConfig(client, setRuntimeCard, setRuntimeConfig),
        loadActivitySummary(client),
        loadList(client, "events", ENDPOINTS.events),
        loadList(client, "jobs", ENDPOINTS.jobs),
      ]);
      const refreshedAt = Date.now();

      setActivitySummary({
        total: summary.total,
        byStatus: summary.byStatus,
        byEvent: summary.byEvent,
        source: summary.source,
        message: summary.message,
        status: summary.status,
        updatedAt: summary.updatedAt || refreshedAt,
      });
      setEvents({
        items: eventList.items,
        source: eventList.source,
        message: eventList.message || (eventList.items.length ? "" : "No webhook deliveries have been observed yet."),
        status: eventList.status,
        updatedAt: refreshedAt,
      });
      setJobs({
        items: jobList.items,
        source: jobList.source,
        message: jobList.message || (jobList.items.length ? "" : "No runner job observations have been reported yet."),
        status: jobList.status,
        updatedAt: refreshedAt,
      });
      setLastRefreshAt(refreshedAt);
      if (!healthOk) {
        setNotice("The browser could not reach the Go API. Confirm the server is running, the API base is correct, and CORS allows this frontend origin.");
      }
    } finally {
      setRefreshing(false);
      isRefreshingRef.current = false;
    }
  }, [apiBase, apiToken, client]);

  useEffect(() => {
    void refresh();
    // Run the initial load once; auto-refresh is scheduled separately.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (paused) {
      return undefined;
    }
    const interval = window.setInterval(() => {
      void refresh();
    }, AUTO_REFRESH_MS);
    return () => window.clearInterval(interval);
  }, [paused, refresh]);

  return (
    <main className="shell">
      <header className="topbar">
        <div>
          <p className="eyebrow">Nova-SRE</p>
          <h1>Control Panel</h1>
          <p className="subtitle">Live signal from webhook ingestion and Kubernetes runner activity.</p>
        </div>
        <form
          className="api-control"
          onSubmit={(event) => {
            event.preventDefault();
            refresh();
          }}
        >
          <label htmlFor="api-base">API base</label>
          <div className="api-row">
            <input
              id="api-base"
              type="url"
              spellCheck={false}
              placeholder={DEFAULT_API_BASE}
              value={apiBase}
              onChange={(event) => setApiBase(event.target.value)}
            />
            <button type="submit" title="Refresh data" aria-label="Refresh data" disabled={refreshing}>
              <RefreshCw className={refreshing ? "spin-icon" : undefined} aria-hidden="true" size={20} strokeWidth={2.5} />
            </button>
          </div>
          <label htmlFor="api-token">API token</label>
          <input
            id="api-token"
            type="password"
            spellCheck={false}
            autoComplete="off"
            placeholder="Optional"
            value={apiToken}
            onChange={(event) => setApiToken(event.target.value)}
          />
          <div className="refresh-row">
            <span>{lastRefreshAt ? `Updated ${formatTime(lastRefreshAt, "time")}` : "Waiting for first refresh"}</span>
            <label className="pause-toggle">
              <input type="checkbox" checked={paused} onChange={(event) => setPaused(event.target.checked)} />
              Pause auto-refresh
            </label>
          </div>
        </form>
      </header>

      {notice ? <section className="notice">{notice}</section> : null}

      <section className="summary-grid" aria-label="Service summary">
        <SummaryCard card={healthCard} />
        <SummaryCard card={metricsCard} />
        <SummaryCard card={runtimeCard} />
        <SummaryCard
          card={{
            label: "Pipeline Total",
            value: String(activitySummary.total),
            detail: summaryCardDetail(activitySummary),
            tone: toneForSummary(activitySummary),
          }}
        />
        <SummaryCard
          card={{
            label: "Recent Events",
            value: String(events.items.length),
            detail: summaryDetail(events, "delivery", "deliveries"),
            tone: toneForList(events),
          }}
        />
        <SummaryCard
          card={{
            label: "Recent Jobs",
            value: String(jobs.items.length),
            detail: summaryDetail(jobs, "job", "jobs"),
            tone: toneForList(jobs),
          }}
        />
      </section>

      <section className="content-grid">
        <BreakdownPanel summary={activitySummary} />
        <RunnerIssuePanel jobs={jobs} />
        <CommentControlPanel config={runtimeConfig} jobs={jobs.items} status={jobs.status} />
        <DataPanel
          eyebrow="GitHub Webhooks"
          title="Recent Events"
          source={events.source}
          message={events.message}
          status={events.status}
          updatedAt={events.updatedAt}
          emptyTitle="No events yet"
          headers={["Delivery", "Event", "Repository", "Received", "Status"]}
          rows={events.items.slice(0, 20).map((item) => [
            <CodeValue key="delivery" value={textValue(item.delivery_id, item.deliveryID, item.id)} />,
            <strong key="event">{textValue(item.event, item.type)}</strong>,
            <span key="repo" className="truncate-value">{repositoryName(item)}</span>,
            <TimeValue key="received" value={textValue(item.received_at, item.receivedAt, item.created_at, item.createdAt, item.observed_time, "")} />,
            <StatusPill key="status" value={textValue(item.status, item.result, "accepted")} />,
          ])}
        />
        <DataPanel
          eyebrow="Kubernetes Runner"
          title="Recent Jobs"
          source={jobs.source}
          message={jobs.message}
          status={jobs.status}
          updatedAt={jobs.updatedAt}
          emptyTitle="No jobs yet"
          headers={["Job", "Namespace", "Event", "Observed", "Result", "Detail"]}
          rows={jobs.items.slice(0, 20).map((item) => [
            <CodeValue key="job" value={textValue(item.job_name, item.jobName, item.name)} />,
            <CodeValue key="namespace" value={textValue(item.namespace, "nova-sre")} />,
            <strong key="event">{textValue(item.event, item.type)}</strong>,
            <TimeValue key="observed" value={textValue(item.observed_time, item.observedTime, item.created_at, item.createdAt, item.completed_at, "")} />,
            <StatusPill key="status" value={textValue(item.status, item.result, resultFromBooleans(item))} />,
            <JobDetailValue key="detail" item={item} />,
          ])}
        />
      </section>
    </main>
  );
}

function RunnerIssuePanel({ jobs }: { jobs: ListState<ApiRecord> }) {
  const issue = latestRunnerIssue(jobs.items);
  const hasLoadedJobs = jobs.status === "ready" || jobs.status === "empty";

  return (
    <section className="panel issue-panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">Runner Attention</p>
          <h2>Latest Issue</h2>
        </div>
        <div className="panel-meta">
          <span className="source-label">{jobs.source}</span>
          {jobs.updatedAt ? <time dateTime={new Date(jobs.updatedAt).toISOString()}>{formatTime(jobs.updatedAt, "time")}</time> : null}
        </div>
      </div>
      {issue ? <RunnerIssueCard item={issue} /> : <RunnerIssueEmptyState status={jobs.status} message={jobs.message} hasLoadedJobs={hasLoadedJobs} />}
    </section>
  );
}

function RunnerIssueCard({ item }: { item: ApiRecord }) {
  const status = recordStatus(item);
  const commentUrl = githubCommentURL(item);
  const observedAt = textValue(item.observed_time, item.observedTime, item.updated_at, item.updatedAt, item.completed_at, "");

  return (
    <div className="issue-card">
      <div className="issue-summary">
        <StatusPill value={status} />
        <div>
          <h3>{jobTitle(item)}</h3>
          <p>{jobDetail(item)}</p>
        </div>
      </div>
      <dl className="issue-meta-grid">
        <div>
          <dt>Job</dt>
          <dd>
            <CodeValue value={textValue(item.job_name, item.jobName, item.name)} />
          </dd>
        </div>
        <div>
          <dt>Repository</dt>
          <dd className="truncate-value">{repositoryName(item)}</dd>
        </div>
        <div>
          <dt>Event</dt>
          <dd>{textValue(item.event, item.type)}</dd>
        </div>
        <div>
          <dt>Observed</dt>
          <dd>
            <TimeValue value={observedAt} />
          </dd>
        </div>
      </dl>
      {commentUrl ? (
        <a href={commentUrl} target="_blank" rel="noreferrer" className="detail-link">
          Open related PR comment
        </a>
      ) : null}
    </div>
  );
}

function RunnerIssueEmptyState({ status, message, hasLoadedJobs }: { status: LoadStatus; message: string; hasLoadedJobs: boolean }) {
  const stateTone = status === "error" ? "error" : status === "loading" ? "loading" : "empty";
  const title = status === "error" ? "Issue scan unavailable" : hasLoadedJobs ? "No runner issues in recent jobs" : "Waiting for runner jobs";
  const detail =
    message ||
    (hasLoadedJobs
      ? "Recent runner jobs do not include failed, cancelled, rejected, or diagnosis error statuses."
      : "Failed runner jobs will be highlighted here after the first refresh.");

  return (
    <div className={`state ${stateTone}`}>
      <strong>{title}</strong>
      <span>{detail}</span>
    </div>
  );
}

function CommentControlPanel({ config, jobs, status }: { config: RuntimeConfig | null; jobs: ApiRecord[]; status: LoadStatus }) {
  const mode = optionalText(config?.github_comment_mode) || "upsert";
  const commentJobs = jobs.filter(hasCommentSignal);
  const counts = commentActionCounts(commentJobs);
  const latest = commentJobs[0];
  const latestUrl = latest ? githubCommentURL(latest) : "";
  const latestAction = latest ? githubCommentAction(latest) || "observed" : "";
  const latestError = latest ? optionalText(latest.github_comment_error) : "";
  const failureCount = commentJobs.filter(isCommentFailure).length;
  const hasLoadedJobs = status === "ready" || status === "empty";

  return (
    <section className="panel comment-controls">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">GitHub PR Comments</p>
          <h2>Comment Controls</h2>
        </div>
        <div className="panel-meta">
          <span className="source-label">/api/config + /api/jobs</span>
        </div>
      </div>
      <div className="comment-control-grid">
        <CommentControlCard label="Mode" value={statusLabel(mode)} detail={commentModeDetail(mode)} tone={mode === "create" ? "warn" : "ok"} />
        <CommentControlCard label="Posted" value={String(counts.created + counts.updated)} detail="Created or updated comments" tone="ok" />
        <CommentControlCard label="Skipped" value={String(counts.skipped)} detail="Missing token, metadata, or disabled path" tone={counts.skipped ? "warn" : undefined} />
        <CommentControlCard label="Failed" value={String(failureCount)} detail="Permission, lookup, or API failures" tone={failureCount ? "error" : "ok"} />
      </div>
      {latest ? (
        <div className={latestError || isCommentFailure(latest) ? "comment-latest error" : "comment-latest"}>
          <div>
            <strong>{latestAction ? `${statusLabel(latestAction)} most recent PR comment action` : "Most recent PR comment action"}</strong>
            <span>{latestError || jobDetail(latest)}</span>
          </div>
          {latestUrl ? (
            <a href={latestUrl} target="_blank" rel="noreferrer" className="detail-link">
              Open latest comment
            </a>
          ) : null}
        </div>
      ) : (
        <div className="comment-latest empty">
          <div>
            <strong>{hasLoadedJobs ? "No PR comment activity yet" : "Waiting for runner jobs"}</strong>
            <span>Failed PR diagnoses will report created, updated, skipped, or failed comment actions here.</span>
          </div>
        </div>
      )}
      <p className="control-hint">
        Use <code>NOVA_SRE_GITHUB_COMMENT_MODE=upsert</code> to keep one marked diagnosis comment current, or <code>create</code> when each
        failed diagnosis should leave a separate PR comment.
      </p>
    </section>
  );
}

function CommentControlCard({ label, value, detail, tone }: { label: string; value: string; detail: string; tone?: Tone }) {
  return (
    <article className={["comment-control-card", tone].filter(Boolean).join(" ")}>
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </article>
  );
}

function BreakdownPanel({ summary }: { summary: ActivitySummaryState }) {
  return (
    <section className="panel breakdown-panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">Activity Summary</p>
          <h2>Status Breakdown</h2>
        </div>
        <div className="panel-meta">
          <span className="source-label">{summary.source}</span>
          {summary.updatedAt ? (
            <time dateTime={new Date(summary.updatedAt).toISOString()}>{formatTime(summary.updatedAt, "time")}</time>
          ) : null}
        </div>
      </div>
      {summary.message ? (
        <div className={`state ${summary.status === "error" ? "error" : summary.status === "loading" ? "loading" : "empty"}`}>
          <strong>{summary.status === "error" ? "Summary unavailable" : titleCase(summary.status)}</strong>
          <span>{summary.message}</span>
        </div>
      ) : null}
      <div className="breakdown-grid">
        <BreakdownList title="By Status" entries={summary.byStatus} />
        <BreakdownList title="By Event" entries={summary.byEvent} />
      </div>
    </section>
  );
}

function BreakdownList({ title, entries }: { title: string; entries: Record<string, number> }) {
  const rows = sortedCountEntries(entries);
  return (
    <div className="breakdown-list">
      <h3>{title}</h3>
      {rows.length ? (
        <ul>
          {rows.map(([label, count]) => (
            <li key={label}>
              <span>{statusLabel(label)}</span>
              <strong>{count}</strong>
            </li>
          ))}
        </ul>
      ) : (
        <p>No activity yet</p>
      )}
    </div>
  );
}

function SummaryCard({ card }: { card: CardState }) {
  return (
    <article className={["summary-card", card.tone].filter(Boolean).join(" ")}>
      <span className="card-label">{card.label}</span>
      <strong>{card.value}</strong>
      <small>{card.detail}</small>
    </article>
  );
}

function DataPanel({
  eyebrow,
  title,
  source,
  message,
  status,
  updatedAt,
  emptyTitle,
  headers,
  rows,
}: {
  eyebrow: string;
  title: string;
  source: string;
  message: string;
  status: LoadStatus;
  updatedAt: number | null;
  emptyTitle: string;
  headers: string[];
  rows: Array<Array<string | ReactNode>>;
}) {
  const showTable = rows.length > 0;
  const stateTone = status === "error" ? "error" : status === "loading" ? "loading" : "empty";

  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">{eyebrow}</p>
          <h2>{title}</h2>
        </div>
        <div className="panel-meta">
          <span className="source-label">{source}</span>
          {updatedAt ? <time dateTime={new Date(updatedAt).toISOString()}>{formatTime(updatedAt, "time")}</time> : null}
        </div>
      </div>
      {message ? (
        <div className={`state ${stateTone}`}>
          <strong>{status === "empty" ? emptyTitle : titleCase(status)}</strong>
          <span>{message}</span>
        </div>
      ) : null}
      {showTable ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                {headers.map((header) => (
                  <th key={header}>{header}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, rowIndex) => (
                <tr key={`${title}-${rowIndex}`}>
                  {row.map((cell, cellIndex) => (
                    <td key={`${title}-${rowIndex}-${cellIndex}`} data-label={headers[cellIndex]}>
                      {cell}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </section>
  );
}

function StatusPill({ value }: { value: string }) {
  const normalized = value.trim().toLowerCase();
  return <span className={`pill ${toneForStatus(normalized)}`}>{statusLabel(normalized)}</span>;
}

function CodeValue({ value }: { value: string }) {
  return <code className="code-value">{value}</code>;
}

function JobDetailValue({ item }: { item: ApiRecord }) {
  const detail = jobDetail(item);
  const url = githubCommentURL(item);
  const action = githubCommentAction(item);
  if (url) {
    return (
      <span className="detail-stack">
        <a href={url} target="_blank" rel="noreferrer" className="detail-link">
          Open comment
        </a>
        {action ? <span className="detail-meta">{statusLabel(action)}</span> : null}
      </span>
    );
  }
  return <span className="truncate-value" title={detail}>{detail}</span>;
}

function TimeValue({ value }: { value: string }) {
  const date = parseDate(value);
  if (!date) {
    return <span className="muted-value">{value || "Unknown"}</span>;
  }
  return (
    <time className="time-value" dateTime={date.toISOString()} title={formatTime(date.getTime(), "full")}>
      <span>{formatTime(date.getTime(), "compact")}</span>
      <small>{relativeTime(date.getTime())}</small>
    </time>
  );
}

function initialApiBase() {
  const configured = String(window.NOVA_SRE_API_BASE || "").trim();
  const saved = window.localStorage.getItem("novaSreApiBase");
  return trimTrailingSlash(configured || saved || DEFAULT_API_BASE);
}

function initialApiToken() {
  const configured = String(window.NOVA_SRE_API_TOKEN || "").trim();
  window.localStorage.removeItem("novaSreApiToken");
  const saved = window.sessionStorage.getItem("novaSreApiToken");
  return configured || saved || "";
}

function trimTrailingSlash(value: string) {
  return value.replace(/\/+$/, "");
}

function createClient(apiBase: string, apiToken: string) {
  const base = trimTrailingSlash(apiBase || DEFAULT_API_BASE);
  const token = apiToken.trim();
  const headers = (accept: string) => ({
    Accept: accept,
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  });
  return {
    async text(path: string) {
      const response = await fetch(`${base}${path}`, {
        cache: "no-store",
        headers: headers("text/plain, */*"),
      });
      if (!response.ok) {
        throw new Error(`${response.status} ${response.statusText}`.trim());
      }
      return response.text();
    },
    async json(path: string) {
      const response = await fetch(`${base}${path}`, {
        cache: "no-store",
        headers: headers("application/json"),
      });
      if (!response.ok) {
        const error = new Error(`${response.status} ${response.statusText}`.trim()) as ApiError;
        error.status = response.status;
        throw error;
      }
      return response.json() as Promise<unknown>;
    },
  };
}

async function loadHealth(client: ReturnType<typeof createClient>, setCard: (card: CardState) => void) {
  try {
    const body = await client.text("/healthz");
    const healthy = body.trim().toLowerCase() === "ok";
    setCard({
      label: "API Health",
      value: healthy ? "Online" : "Unexpected",
      detail: healthy ? "/healthz returned ok" : body.trim() || "No response body",
      tone: healthy ? "ok" : "warn",
    });
    return true;
  } catch (error) {
    setCard({
      label: "API Health",
      value: "Offline",
      detail: describeFetchError(error),
      tone: "error",
    });
    return false;
  }
}

async function loadMetrics(client: ReturnType<typeof createClient>, setCard: (card: CardState) => void) {
  try {
    const body = await client.text("/metrics");
    const families = parseMetricFamilies(body);
    const pipelineFamilies = families.filter((name) => name.startsWith("pipeline_"));
    setCard({
      label: "Metrics",
      value: families.length ? String(families.length) : "Ready",
      detail: pipelineFamilies.length
        ? `${pipelineFamilies.length} pipeline metric families exposed`
        : `${families.length} metric families exposed`,
      tone: "ok",
    });
  } catch (error) {
    setCard({
      label: "Metrics",
      value: "Unavailable",
      detail: describeFetchError(error),
      tone: "error",
    });
  }
}

async function loadRuntimeConfig(client: ReturnType<typeof createClient>, setCard: (card: CardState) => void, setConfig: (config: RuntimeConfig | null) => void) {
  try {
    const payload = await client.json("/api/config");
    const config = isRecord(payload) ? (payload as RuntimeConfig) : {};
    setConfig(config);
    const authLabel = config.api_auth_enabled ? "Token" : "Open";
    const agentLabel = config.agent_auth_enabled ? "agent auth" : "agent open";
    const corsLabel = config.api_cors_restricted ? "restricted CORS" : "wildcard CORS";
    const storeLabel = config.activity_store_enabled ? "durable activity" : "memory activity";
    const commentLabel = `comment ${config.github_comment_mode || "upsert"}`;
    const limit = typeof config.activity_limit === "number" ? config.activity_limit : 0;
    const ttl = textValue(config.runner_job_ttl_seconds ? `${config.runner_job_ttl_seconds}s jobs` : "", config.delivery_cache_ttl);
    setCard({
      label: "Runtime",
      value: authLabel,
      detail: limit
        ? `${limit} records, ${storeLabel}, ${ttl}, ${corsLabel}, ${agentLabel}, ${commentLabel}`
        : `Runtime config loaded, ${storeLabel}, ${corsLabel}, ${agentLabel}, ${commentLabel}`,
      tone: config.api_auth_enabled ? "ok" : "warn",
    });
  } catch (error) {
    const apiError = error as ApiError;
    setConfig(null);
    setCard({
      label: "Runtime",
      value: apiError.status === 404 ? "Legacy" : "Unavailable",
      detail: apiError.status === 404 ? "/api/config not exposed" : describeFetchError(error),
      tone: apiError.status === 404 ? "warn" : "error",
    });
  }
}

async function loadActivitySummary(client: ReturnType<typeof createClient>): Promise<ActivitySummaryState> {
  try {
    const payload = await client.json("/api/summary");
    const summary = normalizeActivitySummary(payload);
    return {
      total: summary.total,
      byStatus: summary.byStatus,
      byEvent: summary.byEvent,
      source: "/api/summary",
      message: summary.total ? "" : "No webhook activity has been summarized yet.",
      status: summary.total ? "ready" : "empty",
      updatedAt: summary.updatedAt,
    };
  } catch (error) {
    return {
      total: 0,
      byStatus: {},
      byEvent: {},
      source: "/api/summary",
      message: `Could not load summary: ${describeFetchError(error)}`,
      status: "error",
      updatedAt: null,
    };
  }
}

function normalizeActivitySummary(payload: unknown) {
  if (!isRecord(payload)) {
    return { total: 0, byStatus: {}, byEvent: {}, updatedAt: null };
  }
  return {
    total: numericValue(payload.total),
    byStatus: normalizeCountMap(payload.by_status),
    byEvent: normalizeCountMap(payload.by_event),
    updatedAt: parseDate(textValue(payload.updated_at, ""))?.getTime() || null,
  };
}

function parseMetricFamilies(metricsText: string) {
  const names = new Set<string>();
  metricsText.split("\n").forEach((line) => {
    const match = line.match(/^# HELP\s+([a-zA-Z_:][a-zA-Z0-9_:]*)\s+/);
    if (match) {
      names.add(match[1]);
    }
  });
  return Array.from(names).sort();
}

async function loadList(client: ReturnType<typeof createClient>, kind: ListKind, paths: readonly string[]): Promise<ListResult> {
  for (const path of paths) {
    try {
      const payload = await client.json(path);
      const items = normalizeList(payload);
      return { items, source: path, message: "", status: items.length ? "ready" : "empty" };
    } catch (error) {
      const apiError = error as ApiError;
      if (apiError.status && apiError.status !== 404) {
        return {
          items: [],
          source: path,
          message: `Could not load ${kind}: ${describeFetchError(error)}`,
          status: "error" as const,
        };
      }
    }
  }

  return {
    items: [],
    source: "Not implemented",
    message: `${titleCase(kind)} are not exposed by the Go API yet.`,
    status: "error" as const,
  };
}

function normalizeList(payload: unknown): ApiRecord[] {
  if (Array.isArray(payload)) {
    return payload.filter(isRecord);
  }
  if (!isRecord(payload)) {
    return [];
  }
  const nested = payload.items || payload.events || payload.jobs || payload.data;
  return Array.isArray(nested) ? nested.filter(isRecord) : [];
}

function isRecord(value: unknown): value is ApiRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function repositoryName(item: ApiRecord) {
  if (typeof item.repository === "string") {
    return item.repository;
  }
  if (isRecord(item.repository)) {
    return textValue(item.repository.full_name, item.repository.name);
  }
  return textValue(item.repo);
}

function resultFromBooleans(item: ApiRecord) {
  if (item.failed) {
    return "failed";
  }
  if (item.succeeded) {
    return "succeeded";
  }
  return "pending";
}

function recordStatus(item: ApiRecord) {
  return textValue(item.status, item.result, resultFromBooleans(item)).trim().toLowerCase();
}

function textValue(...values: unknown[]) {
  const value = values.find((candidate) => candidate !== undefined && candidate !== null && String(candidate).trim() !== "");
  return value === undefined ? "Unknown" : String(value);
}

function optionalText(...values: unknown[]) {
  const value = values.find((candidate) => candidate !== undefined && candidate !== null && String(candidate).trim() !== "");
  return value === undefined ? "" : String(value);
}

function toneForStatus(status: string) {
  if (["ok", "accepted", "success", "succeeded", "complete", "completed", "running", "active", "diagnosed"].includes(status)) {
    return "ok";
  }
  if (["failed", "error", "errored", "cancelled", "canceled", "rejected", "diagnosis_error", "diagnosis_comment_error"].includes(status)) {
    return "error";
  }
  return "warn";
}

function jobDetail(item: ApiRecord) {
  return textValue(item.message, item.reason, item.github_comment_error, item.github_comment_action, "No detail");
}

function jobTitle(item: ApiRecord) {
  return `${statusLabel(recordStatus(item))} runner job`;
}

function latestRunnerIssue(items: ApiRecord[]) {
  return items.find((item) => toneForStatus(recordStatus(item)) === "error") || null;
}

function githubCommentURL(item: ApiRecord) {
  const explicit = optionalText(item.github_comment_url);
  if (isGitHubURL(explicit)) {
    return explicit;
  }
  const message = optionalText(item.message);
  return isGitHubURL(message) ? message : "";
}

function githubCommentAction(item: ApiRecord) {
  const action = optionalText(item.github_comment_action, item.reason);
  return ["created", "updated", "skipped", "failed"].includes(action.trim().toLowerCase()) ? action : "";
}

function hasCommentSignal(item: ApiRecord) {
  return Boolean(
    optionalText(item.github_comment_action, item.github_comment_url, item.github_comment_error) ||
      typeof item.github_comment_posted === "boolean" ||
      optionalText(item.status).includes("diagnosis_comment"),
  );
}

function commentActionCounts(items: ApiRecord[]): Record<CommentAction, number> {
  return items.reduce<Record<CommentAction, number>>(
    (counts, item) => {
      const action = githubCommentAction(item).trim().toLowerCase();
      if (isCommentAction(action)) {
        counts[action] += 1;
      }
      return counts;
    },
    { created: 0, updated: 0, skipped: 0, failed: 0 },
  );
}

function isCommentAction(action: string): action is CommentAction {
  return action === "created" || action === "updated" || action === "skipped" || action === "failed";
}

function isCommentFailure(item: ApiRecord) {
  return githubCommentAction(item).trim().toLowerCase() === "failed" || optionalText(item.status).trim().toLowerCase() === "diagnosis_comment_error";
}

function commentModeDetail(mode: string) {
  return mode.trim().toLowerCase() === "create" ? "Create a fresh comment for every diagnosis" : "Update the marked Nova-SRE comment when present";
}

function isGitHubURL(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.hostname === "github.com";
  } catch {
    return false;
  }
}

function statusLabel(status: string) {
  return status
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map(titleCase)
    .join(" ") || "Unknown";
}

function toneForList(list: ListState<ApiRecord>): Tone {
  if (list.status === "error") {
    return "error";
  }
  if (list.status === "ready") {
    return "ok";
  }
  return "warn";
}

function toneForSummary(summary: ActivitySummaryState): Tone {
  if (summary.status === "error") {
    return "error";
  }
  if (summary.total > 0) {
    return "ok";
  }
  return "warn";
}

function summaryCardDetail(summary: ActivitySummaryState) {
  if (summary.status === "loading") {
    return "Refreshing from /api/summary";
  }
  if (summary.status === "error") {
    return summary.message || "Could not load summary";
  }
  if (!summary.total) {
    return "No summarized pipeline activity yet";
  }
  const topStatus = sortedCountEntries(summary.byStatus)[0];
  return topStatus ? `${topStatus[1]} ${statusLabel(topStatus[0]).toLowerCase()} in recent activity` : `${summary.total} summarized records`;
}

function summaryDetail(list: ListState<ApiRecord>, singular: string, plural: string) {
  if (list.status === "loading") {
    return "Refreshing from API";
  }
  if (list.status === "error") {
    return list.message || "Could not load data";
  }
  if (list.items.length) {
    return `${list.items.length} recent ${list.items.length === 1 ? singular : plural} loaded`;
  }
  return `No recent ${plural}`;
}

function normalizeCountMap(value: unknown) {
  if (!isRecord(value)) {
    return {};
  }
  return Object.fromEntries(
    Object.entries(value)
      .map(([key, count]) => [key, numericValue(count)] as const)
      .filter(([, count]) => count > 0),
  );
}

function sortedCountEntries(entries: Record<string, number>) {
  return Object.entries(entries).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

function numericValue(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function parseDate(value: string) {
  if (!value) {
    return null;
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function formatTime(value: number, style: "compact" | "time" | "full") {
  const options: Intl.DateTimeFormatOptions =
    style === "time"
      ? { hour: "numeric", minute: "2-digit", second: "2-digit" }
      : {
          month: "short",
          day: "numeric",
          hour: "numeric",
          minute: "2-digit",
          ...(style === "full" ? { year: "numeric", second: "2-digit", timeZoneName: "short" } : {}),
        };
  return new Intl.DateTimeFormat(undefined, options).format(new Date(value));
}

function relativeTime(value: number) {
  const seconds = Math.round((value - Date.now()) / 1000);
  const absSeconds = Math.abs(seconds);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (absSeconds < 60) {
    return formatter.format(seconds, "second");
  }
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) {
    return formatter.format(minutes, "minute");
  }
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) {
    return formatter.format(hours, "hour");
  }
  return formatter.format(Math.round(hours / 24), "day");
}

function describeFetchError(error: unknown) {
  if (error instanceof TypeError) {
    return "Network or CORS error";
  }
  if (error instanceof Error) {
    return error.message || "Request failed";
  }
  return "Request failed";
}

function titleCase(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

createRoot(document.getElementById("root") as HTMLElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
