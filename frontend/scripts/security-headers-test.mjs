import { readFileSync } from "node:fs";
import { join } from "node:path";

const root = process.cwd();
const nginx = readFileSync(join(root, "nginx.conf"), "utf8");
const index = readFileSync(join(root, "index.html"), "utf8");

const requiredHeaders = [
  "Content-Security-Policy",
  "X-Content-Type-Options",
  "X-Frame-Options",
  "Referrer-Policy",
  "Permissions-Policy",
];

const requiredCspDirectives = [
  "default-src 'self'",
  "base-uri 'none'",
  "object-src 'none'",
  "frame-ancestors 'none'",
  "script-src 'self'",
  "connect-src 'self' http://localhost:* http://127.0.0.1:* https:",
  "form-action 'none'",
];

const failures = [];

for (const header of requiredHeaders) {
  const pattern = new RegExp(`add_header\\s+${header}\\s+`, "g");
  const matches = nginx.match(pattern) || [];
  if (matches.length < 2) {
    failures.push(`${header} must be set on both app routes and /config.js`);
  }
}

for (const directive of requiredCspDirectives) {
  if (!nginx.includes(directive)) {
    failures.push(`CSP must include ${directive}`);
  }
}

if (!nginx.includes('add_header Cache-Control "no-store" always;')) {
  failures.push("/config.js must remain uncached");
}

if (/<script\b(?![^>]*\bsrc=)[^>]*>/i.test(index)) {
  failures.push("index.html must not include inline scripts");
}

if (!index.includes('<script src="/config.js"></script>')) {
  failures.push("index.html must load runtime config from /config.js");
}

if (failures.length) {
  console.error(`Frontend security checks failed:\n- ${failures.join("\n- ")}`);
  process.exit(1);
}

console.log(`Frontend security checks passed (${requiredHeaders.length} headers, ${requiredCspDirectives.length} CSP directives).`);
