import { StrictMode, type ReactNode, useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

declare global {
  interface Window {
    NOVA_SRE_API_BASE?: string;
  }
}

const DEFAULT_API_BASE = "http://localhost:8080";
const ENDPOINTS = {
  events: ["/api/events", "/events"],
  jobs: ["/api/jobs", "/jobs"],
} as const;

type Tone = "ok" | "warn" | "error";
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
};
type ApiRecord = Record<string, unknown>;
type ApiError = Error & { status?: number };

function App() {
  const [apiBase, setApiBase] = useState(initialApiBase);
  const [notice, setNotice] = useState("");
  const [refreshing, setRefreshing] = useState(false);
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
  const [events, setEvents] = useState<ListState<ApiRecord>>({
    items: [],
    source: "No endpoint",
    message: "",
  });
  const [jobs, setJobs] = useState<ListState<ApiRecord>>({
    items: [],
    source: "No endpoint",
    message: "",
  });

  const client = useMemo(() => createClient(apiBase), [apiBase]);

  const refresh = useCallback(async () => {
    const nextApiBase = trimTrailingSlash(apiBase || DEFAULT_API_BASE);
    setApiBase(nextApiBase);
    window.localStorage.setItem("novaSreApiBase", nextApiBase);
    setNotice("");
    setRefreshing(true);
    setEvents((current) => ({ ...current, message: "Loading events..." }));
    setJobs((current) => ({ ...current, message: "Loading jobs..." }));

    const [healthOk, , eventList, jobList] = await Promise.all([
      loadHealth(client, setHealthCard),
      loadMetrics(client, setMetricsCard),
      loadList(client, "events", ENDPOINTS.events),
      loadList(client, "jobs", ENDPOINTS.jobs),
    ]);

    setEvents({
      items: eventList.items,
      source: eventList.source,
      message: eventList.message || (eventList.items.length ? "" : "No webhook events to show."),
    });
    setJobs({
      items: jobList.items,
      source: jobList.source,
      message: jobList.message || (jobList.items.length ? "" : "No runner jobs to show."),
    });
    if (!healthOk) {
      setNotice("The browser could not reach the Go API. Confirm the server is running, the API base is correct, and CORS allows this frontend origin.");
    }
    setRefreshing(false);
  }, [apiBase, client]);

  useEffect(() => {
    void refresh();
    // Run the initial load once; later refreshes are user driven.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <main className="shell">
      <header className="topbar">
        <div>
          <p className="eyebrow">Nova-SRE</p>
          <h1>Control Panel</h1>
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
              {refreshing ? "..." : "↻"}
            </button>
          </div>
        </form>
      </header>

      {notice ? <section className="notice">{notice}</section> : null}

      <section className="summary-grid" aria-label="Service summary">
        <SummaryCard card={healthCard} />
        <SummaryCard card={metricsCard} />
        <SummaryCard
          card={{
            label: "Recent Events",
            value: String(events.items.length),
            detail: events.items.length ? "Loaded from API" : "No recent events",
            tone: events.items.length ? "ok" : "warn",
          }}
        />
        <SummaryCard
          card={{
            label: "Recent Jobs",
            value: String(jobs.items.length),
            detail: jobs.items.length ? "Loaded from API" : "No recent jobs",
            tone: jobs.items.length ? "ok" : "warn",
          }}
        />
      </section>

      <section className="content-grid">
        <DataPanel
          eyebrow="GitHub Webhooks"
          title="Recent Events"
          source={events.source}
          message={events.message}
          headers={["Delivery", "Event", "Repository", "Received", "Status"]}
          rows={events.items.slice(0, 20).map((item) => [
            textValue(item.delivery_id, item.deliveryID, item.id),
            textValue(item.event, item.type),
            repositoryName(item),
            formatTime(textValue(item.received_at, item.receivedAt, item.created_at, item.createdAt, item.observed_time, "")),
            <StatusPill key="status" value={textValue(item.status, item.result, "accepted")} />,
          ])}
        />
        <DataPanel
          eyebrow="Kubernetes Runner"
          title="Recent Jobs"
          source={jobs.source}
          message={jobs.message}
          headers={["Job", "Namespace", "Event", "Observed", "Result"]}
          rows={jobs.items.slice(0, 20).map((item) => [
            textValue(item.job_name, item.jobName, item.name),
            textValue(item.namespace, "nova-sre"),
            textValue(item.event, item.type),
            formatTime(textValue(item.observed_time, item.observedTime, item.created_at, item.createdAt, item.completed_at, "")),
            <StatusPill key="status" value={textValue(item.status, item.result, resultFromBooleans(item))} />,
          ])}
        />
      </section>
    </main>
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
  headers,
  rows,
}: {
  eyebrow: string;
  title: string;
  source: string;
  message: string;
  headers: string[];
  rows: Array<Array<string | ReactNode>>;
}) {
  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">{eyebrow}</p>
          <h2>{title}</h2>
        </div>
        <span className="source-label">{source}</span>
      </div>
      {message ? <div className="state">{message}</div> : null}
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
                  <td key={`${title}-${rowIndex}-${cellIndex}`}>{cell}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function StatusPill({ value }: { value: string }) {
  const normalized = value.toLowerCase();
  return <span className={`pill ${toneForStatus(normalized)}`}>{normalized}</span>;
}

function initialApiBase() {
  const configured = String(window.NOVA_SRE_API_BASE || "").trim();
  const saved = window.localStorage.getItem("novaSreApiBase");
  return trimTrailingSlash(configured || saved || DEFAULT_API_BASE);
}

function trimTrailingSlash(value: string) {
  return value.replace(/\/+$/, "");
}

function createClient(apiBase: string) {
  const base = trimTrailingSlash(apiBase || DEFAULT_API_BASE);
  return {
    async text(path: string) {
      const response = await fetch(`${base}${path}`, {
        cache: "no-store",
        headers: { Accept: "text/plain, */*" },
      });
      if (!response.ok) {
        throw new Error(`${response.status} ${response.statusText}`.trim());
      }
      return response.text();
    },
    async json(path: string) {
      const response = await fetch(`${base}${path}`, {
        cache: "no-store",
        headers: { Accept: "application/json" },
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
    const novaFamilies = families.filter((name) => name.startsWith("nova_"));
    setCard({
      label: "Metrics",
      value: families.length ? String(families.length) : "Ready",
      detail: novaFamilies.length
        ? `${novaFamilies.length} Nova metric families exposed`
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

async function loadList(client: ReturnType<typeof createClient>, kind: ListKind, paths: readonly string[]) {
  for (const path of paths) {
    try {
      const payload = await client.json(path);
      return { items: normalizeList(payload), source: path, message: "" };
    } catch (error) {
      const apiError = error as ApiError;
      if (apiError.status && apiError.status !== 404) {
        return {
          items: [],
          source: path,
          message: `Could not load ${kind}: ${describeFetchError(error)}`,
        };
      }
    }
  }

  return {
    items: [],
    source: "Not implemented",
    message: `${titleCase(kind)} are not exposed by the Go API yet.`,
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

function textValue(...values: unknown[]) {
  const value = values.find((candidate) => candidate !== undefined && candidate !== null && String(candidate).trim() !== "");
  return value === undefined ? "Unknown" : String(value);
}

function toneForStatus(status: string) {
  if (["ok", "accepted", "success", "succeeded", "complete", "completed"].includes(status)) {
    return "ok";
  }
  if (["failed", "error", "errored"].includes(status)) {
    return "error";
  }
  return "warn";
}

function formatTime(value: string) {
  if (!value) {
    return "Unknown";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
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
