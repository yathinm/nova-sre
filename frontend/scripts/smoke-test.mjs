import { readFileSync } from "node:fs";
import { join } from "node:path";

const source = readFileSync(join(process.cwd(), "src", "main.tsx"), "utf8");

const checks = [
  ["events endpoint", 'events: ["/api/events", "/events"]'],
  ["jobs endpoint", 'jobs: ["/api/jobs", "/jobs"]'],
  ["runtime config endpoint", 'client.json("/api/config")'],
  ["authorization header", "Authorization: `Bearer ${token}`"],
  ["api token storage", 'window.localStorage.setItem("novaSreApiToken", apiToken)'],
  ["runtime summary card", 'label: "Runtime"'],
  ["pipeline metrics filter", 'name.startsWith("pipeline_")'],
  ["delivery plural copy", 'summaryDetail(events, "delivery", "deliveries")'],
  ["runtime cors status", "api_cors_restricted"],
];

const failures = checks
  .filter(([, needle]) => !source.includes(needle))
  .map(([label]) => label);

if (failures.length) {
  console.error(`Frontend smoke checks failed: ${failures.join(", ")}`);
  process.exit(1);
}

console.log(`Frontend smoke checks passed (${checks.length}).`);
