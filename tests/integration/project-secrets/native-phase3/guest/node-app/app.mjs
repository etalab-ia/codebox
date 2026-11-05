// Phase 3 qualification guest application (TEST FIXTURE ONLY).
//
// Reads a just-code-managed dotenv file with the REAL loaders
// (dotenv@16.6.1 + dotenv-expand@13.0.0) and issues one HTTPS request to the
// test lab carrying the parsed value in a header. The file holds an opaque
// marker; the sandbox proxy — not this app — substitutes the real value at
// the network boundary, and only toward the approved destination.
//
// usage: node app.mjs <dotenv-path> <url>
import { readFileSync } from "node:fs";
import https from "node:https";
import dotenv from "dotenv";
import { expand } from "dotenv-expand";

const [, , dotenvPath, url] = process.argv;
if (!dotenvPath || !url) {
  console.error("usage: node app.mjs <dotenv-path> <url>");
  process.exit(2);
}

const parsed = dotenv.parse(readFileSync(dotenvPath));
// processEnv:{} keeps the value provably file-sourced: interpolation cannot
// pull the same marker from the process environment, which also holds it
// under the binding's guest variable.
const result = expand({ parsed, processEnv: {} });
const value = result.parsed.API_KEY;
if (typeof value !== "string" || value.length === 0) {
  console.error("API_KEY missing from " + dotenvPath);
  process.exit(2);
}
// The value here is the marker, not a credential: printing it lets the probe
// prove the app parsed the file before any network substitution happens.
console.log("API_KEY=" + value);

const request = https.get(
  url,
  // The proxy intercepts TLS with the run's temporary CA; like the Phase 0
  // guest, this fixture disables client verification instead of integrating
  // that CA into the guest store. Upstream verification stays with the proxy.
  { headers: { "X-Api-Key": value }, rejectUnauthorized: false, timeout: 12000 },
  (res) => {
    res.resume();
    res.on("end", () => {
      console.log("HTTP_STATUS=" + res.statusCode);
      process.exit(res.statusCode === 200 ? 0 : 1);
    });
  },
);
request.on("timeout", () => request.destroy(new Error("request timeout")));
request.on("error", (err) => {
  console.error("REQUEST_FAILED=" + err.message);
  process.exit(1);
});
