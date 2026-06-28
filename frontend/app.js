(function () {
  const DEFAULT_API_BASE = "http://localhost:8080";
  const ENDPOINTS = {
    events: ["/api/events", "/events"],
    jobs: ["/api/jobs", "/jobs"],
  };

  const elements = {
    apiBase: document.querySelector("#api-base"),
    refresh: document.querySelector("#refresh"),
    notice: document.querySelector("#notice"),
    cards: {
      health: document.querySelector("#card-health"),
      metrics: document.querySelector("#card-metrics"),
      events: document.querySelector("#card-events"),
      jobs: document.querySelector("#card-jobs"),
    },
    eventsBody: document.querySelector("#events-body"),
    jobsBody: document.querySelector("#jobs-body"),
    eventsState: document.querySelector("#events-state"),
    jobsState: document.querySelector("#jobs-state"),
    eventsSource: document.querySelector("#events-source"),
    jobsSource: document.querySelector("#jobs-source"),
  };

  function initialApiBase() {
    const configured = String(window.NOVA_SRE_API_BASE || "").trim();
    const saved = window.localStorage.getItem("novaSreApiBase");
    return trimTrailingSlash(configured || saved || DEFAULT_API_BASE);
  }

  function trimTrailingSlash(value) {
    return String(value || "").replace(/\/+$/, "");
  }

  function apiURL(path) {
    return `${trimTrailingSlash(elements.apiBase.value || DEFAULT_API_BASE)}${path}`;
  }

  async function requestText(path) {
    const response = await fetch(apiURL(path), {
      cache: "no-store",
      headers: { Accept: "text/plain, */*" },
    });
    if (!response.ok) {
      throw new Error(`${response.status} ${response.statusText}`.trim());
    }
    return response.text();
  }

  async function requestJSON(path) {
    const response = await fetch(apiURL(path), {
      cache: "no-store",
      headers: { Accept: "application/json" },
    });
    if (!response.ok) {
      const error = new Error(`${response.status} ${response.statusText}`.trim());
      error.status = response.status;
      throw error;
    }
    return response.json();
  }

  function setCard(card, value, detail, tone) {
    card.classList.remove("ok", "warn", "error");
    if (tone) {
      card.classList.add(tone);
    }
    card.querySelector("strong").textContent = value;
    card.querySelector("small").textContent = detail;
  }

  function setNotice(message) {
    elements.notice.hidden = !message;
    elements.notice.textContent = message || "";
  }

  async function loadHealth() {
    try {
      const body = await requestText("/healthz");
      const healthy = body.trim().toLowerCase() === "ok";
      setCard(elements.cards.health, healthy ? "Online" : "Unexpected", healthy ? "/healthz returned ok" : body.trim() || "No response body", healthy ? "ok" : "warn");
      return true;
    } catch (error) {
      setCard(elements.cards.health, "Offline", describeFetchError(error), "error");
      return false;
    }
  }

  async function loadMetrics() {
    try {
      const body = await requestText("/metrics");
      const families = parseMetricFamilies(body);
      const novaFamilies = families.filter((name) => name.startsWith("nova_"));
      const detail = novaFamilies.length
        ? `${novaFamilies.length} Nova metric families exposed`
        : `${families.length} metric families exposed`;
      setCard(elements.cards.metrics, families.length ? String(families.length) : "Ready", detail, "ok");
    } catch (error) {
      setCard(elements.cards.metrics, "Unavailable", describeFetchError(error), "error");
    }
  }

  function parseMetricFamilies(metricsText) {
    const names = new Set();
    metricsText.split("\n").forEach((line) => {
      const match = line.match(/^# HELP\s+([a-zA-Z_:][a-zA-Z0-9_:]*)\s+/);
      if (match) {
        names.add(match[1]);
      }
    });
    return Array.from(names).sort();
  }

  async function loadList(kind, paths) {
    const stateEl = kind === "events" ? elements.eventsState : elements.jobsState;
    const sourceEl = kind === "events" ? elements.eventsSource : elements.jobsSource;

    for (const path of paths) {
      try {
        const payload = await requestJSON(path);
        sourceEl.textContent = path;
        return { items: normalizeList(payload), source: path };
      } catch (error) {
        if (error.status && error.status !== 404) {
          stateEl.textContent = `Could not load ${kind}: ${describeFetchError(error)}`;
          sourceEl.textContent = path;
          return { items: [], source: path, error };
        }
      }
    }

    sourceEl.textContent = "Not implemented";
    stateEl.textContent = `${titleCase(kind)} are not exposed by the Go API yet.`;
    return { items: [], source: null };
  }

  function normalizeList(payload) {
    if (Array.isArray(payload)) {
      return payload;
    }
    if (!payload || typeof payload !== "object") {
      return [];
    }
    return payload.items || payload.events || payload.jobs || payload.data || [];
  }

  function renderEvents(items) {
    elements.eventsBody.replaceChildren();
    elements.eventsState.textContent = items.length ? "" : elements.eventsState.textContent || "No webhook events to show.";
    setCard(elements.cards.events, String(items.length), items.length ? "Loaded from API" : "No recent events", items.length ? "ok" : "warn");

    items.slice(0, 20).forEach((item) => {
      const row = document.createElement("tr");
      row.append(
        cell(item.delivery_id || item.deliveryID || item.id || "Unknown"),
        cell(item.event || item.type || "Unknown"),
        cell(repositoryName(item)),
        cell(formatTime(item.received_at || item.receivedAt || item.created_at || item.createdAt || item.observed_time)),
        statusCell(item.status || item.result || "accepted")
      );
      elements.eventsBody.append(row);
    });
  }

  function renderJobs(items) {
    elements.jobsBody.replaceChildren();
    elements.jobsState.textContent = items.length ? "" : elements.jobsState.textContent || "No runner jobs to show.";
    setCard(elements.cards.jobs, String(items.length), items.length ? "Loaded from API" : "No recent jobs", items.length ? "ok" : "warn");

    items.slice(0, 20).forEach((item) => {
      const row = document.createElement("tr");
      row.append(
        cell(item.job_name || item.jobName || item.name || "Unknown"),
        cell(item.namespace || "nova-sre"),
        cell(item.event || item.type || "Unknown"),
        cell(formatTime(item.observed_time || item.observedTime || item.created_at || item.createdAt || item.completed_at)),
        statusCell(item.status || item.result || resultFromBooleans(item))
      );
      elements.jobsBody.append(row);
    });
  }

  function repositoryName(item) {
    if (typeof item.repository === "string") {
      return item.repository;
    }
    if (item.repository && typeof item.repository === "object") {
      return item.repository.full_name || item.repository.name || "Unknown";
    }
    return item.repo || "Unknown";
  }

  function resultFromBooleans(item) {
    if (item.failed) {
      return "failed";
    }
    if (item.succeeded) {
      return "succeeded";
    }
    return "pending";
  }

  function cell(value) {
    const td = document.createElement("td");
    td.textContent = String(value || "Unknown");
    return td;
  }

  function statusCell(value) {
    const td = document.createElement("td");
    const pill = document.createElement("span");
    const normalized = String(value || "unknown").toLowerCase();
    pill.className = `pill ${toneForStatus(normalized)}`;
    pill.textContent = normalized;
    td.append(pill);
    return td;
  }

  function toneForStatus(status) {
    if (["ok", "accepted", "success", "succeeded", "complete", "completed"].includes(status)) {
      return "ok";
    }
    if (["failed", "error", "errored"].includes(status)) {
      return "error";
    }
    return "warn";
  }

  function formatTime(value) {
    if (!value) {
      return "Unknown";
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    return new Intl.DateTimeFormat(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    }).format(date);
  }

  function describeFetchError(error) {
    if (error instanceof TypeError) {
      return "Network or CORS error";
    }
    return error.message || "Request failed";
  }

  function titleCase(value) {
    return value.charAt(0).toUpperCase() + value.slice(1);
  }

  async function refresh() {
    const base = trimTrailingSlash(elements.apiBase.value || DEFAULT_API_BASE);
    elements.apiBase.value = base;
    window.localStorage.setItem("novaSreApiBase", base);
    setNotice("");
    elements.refresh.disabled = true;
    elements.refresh.textContent = "…";
    elements.eventsState.textContent = "Loading events...";
    elements.jobsState.textContent = "Loading jobs...";

    const [healthOk, , events, jobs] = await Promise.all([
      loadHealth(),
      loadMetrics(),
      loadList("events", ENDPOINTS.events),
      loadList("jobs", ENDPOINTS.jobs),
    ]);

    renderEvents(events.items);
    renderJobs(jobs.items);
    if (!healthOk) {
      setNotice("The browser could not reach the Go API. Confirm the server is running, the API base is correct, and CORS allows this frontend origin.");
    }
    elements.refresh.disabled = false;
    elements.refresh.textContent = "↻";
  }

  elements.apiBase.value = initialApiBase();
  elements.refresh.addEventListener("click", refresh);
  elements.apiBase.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      refresh();
    }
  });

  refresh();
})();
