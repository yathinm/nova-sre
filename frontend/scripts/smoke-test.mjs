import { readFileSync } from "node:fs";
import { join } from "node:path";

const source = readFileSync(join(process.cwd(), "src", "main.tsx"), "utf8");

const checks = [
  ["events endpoint", 'events: ["/api/events", "/events"]'],
  ["jobs endpoint", 'jobs: ["/api/jobs", "/jobs"]'],
  ["runtime config endpoint", 'client.json("/api/config")'],
  ["activity summary endpoint", 'client.json("/api/summary")'],
  ["authorization header", "Authorization: `Bearer ${token}`"],
  ["api token session storage", 'window.sessionStorage.setItem("novaSreApiToken", apiToken)'],
  ["legacy token cleanup", 'window.localStorage.removeItem("novaSreApiToken")'],
  ["runtime summary card", 'label: "Runtime"'],
  ["pipeline total card", 'label: "Pipeline Total"'],
  ["status breakdown panel", "function BreakdownPanel"],
  ["pipeline metrics filter", 'name.startsWith("pipeline_")'],
  ["delivery plural copy", 'summaryDetail(events, "delivery", "deliveries")'],
  ["runtime cors status", "api_cors_restricted"],
  ["runtime agent auth status", "agent_auth_enabled"],
  ["runtime comment mode status", "github_comment_mode"],
  ["job detail comment link", "function JobDetailValue"],
  ["github comment url guard", "function githubCommentURL"],
  ["comment controls panel", "function CommentControlPanel"],
  ["comment action counts", "function commentActionCounts"],
  ["comment mode guidance", "NOVA_SRE_GITHUB_COMMENT_MODE=upsert"],
  ["runner issue panel", "function RunnerIssuePanel"],
  ["runner issue selector", "function latestRunnerIssue"],
  ["runner attention copy", "Runner Attention"],
  ["workflow guide", "function WorkflowGuide"],
  ["operator guide panel", "function OperatorGuidePanel"],
  ["webhook accepted explanation", "Verified and queued; check runner status next."],
  ["runner status explanation", "function runnerStatusHelp"],
  ["backoff guidance", "BackoffLimitExceeded"],
  ["issue next step guidance", "function runnerNextStep"],
];

const failures = checks
  .filter(([, needle]) => !source.includes(needle))
  .map(([label]) => label);

if (failures.length) {
  console.error(`Frontend smoke checks failed: ${failures.join(", ")}`);
  process.exit(1);
}

console.log(`Frontend smoke checks passed (${checks.length}).`);
