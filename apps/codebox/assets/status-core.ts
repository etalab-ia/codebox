/**
 * Shared status bar logic for OpenCode and Pi inside codebox (#318).
 *
 * Pure TypeScript (strip-types compatible: runs natively under Node 22+, Bun
 * in OpenCode v2.2.5, and jiti in Pi 1.5.2 with no build step).
 */

export const ALBERT_HOST = "albert.api.etalab.gouv.fr";
export const ALBERT_BASE_URL = "https://albert.api.etalab.gouv.fr/v1";
export const GAUGE_WIDTH = 10;
export const WINDOW_MS = 60_000;
export const FETCH_TIMEOUT_MS = 5_000;

export type SegmentTone = "base" | "accent" | "muted" | "mutedBar" | "success" | "warning" | "error";

export interface StatusSegment {
  readonly text: string;
  readonly tone: SegmentTone;
}

export type StatusLine = readonly StatusSegment[];

export interface StatusBarConfig {
  readonly projectName?: string;
  readonly providerNames?: Readonly<Record<string, string>>;
}

export interface RequestSample {
  readonly t: number;
  readonly tokens: number;
  readonly model?: string;
}

export interface LimitWindow {
  /** null means unlimited (OpenGateLLM 0.9.3 value: null). */
  readonly limit: number | null;
  /** Used count reported by /v1/limits (value - remaining), or null when unlimited. */
  readonly used: number | null;
  /** Remaining count reported by /v1/limits, or null when unlimited. */
  readonly remaining: number | null;
}

export interface AccountModelLimits {
  readonly model: string;
  readonly rpm?: LimitWindow;
  readonly tpm?: LimitWindow;
  readonly rpd?: LimitWindow;
  readonly tpd?: LimitWindow;
  readonly fetchedAt: number;
}

export interface PublicModelLimits {
  readonly rpm: number;
  readonly tpm: number;
  readonly rpd: number | null;
}

export interface DailyUsage {
  readonly requests: number;
  readonly kgCO2eq: number;
  readonly completionTokens: number;
}

export interface StatusRenderInput {
  readonly providerID?: string;
  readonly providerName?: string;
  readonly baseURL?: string;
  readonly modelDisplayID?: string;
  readonly apiModelID?: string;
  readonly thinkingLevel?: string;
  readonly cwd: string;
  readonly gitBranch?: string | null;
  readonly running?: boolean;
  readonly contextWindow: number;
  readonly lastContextTokens: number;
  readonly sessionInputTokens: number;
  readonly sessionOutputTokens: number;
  readonly requests: readonly RequestSample[];
  readonly now: number;
  readonly accountLimits?: AccountModelLimits;
  readonly limitsChecked?: boolean;
  readonly dailyUsage?: DailyUsage;
  readonly usageUnavailable?: boolean;
  readonly config?: StatusBarConfig;
  readonly env?: Readonly<Record<string, string | undefined>>;
}

export interface GaugeView {
  readonly pct: number;
  readonly barFilled: string;
  readonly barEmpty: string;
  readonly severity: "normal" | "warning" | "error";
}

/**
 * Official Albert API public plan limits (Expérimentation tier):
 * https://ia.numerique.gouv.fr/outils-ia/albert-api/tarifs-et-limites/
 *
 * Used only as a fallback when GET /v1/limits (OpenGateLLM 0.9.3+) is not
 * available yet; the status bar marks this fallback with "(grille publique)".
 */
const PUBLIC_ALBERT_LIMITS_TABLE: Readonly<Record<string, PublicModelLimits>> = {
  "openai/gpt-oss-120b": { rpm: 10, tpm: 128_000, rpd: 1_000 },
  "gpt-oss-120b": { rpm: 10, tpm: 128_000, rpd: 1_000 },
  "deepseek-ai/deepseek-v4-flash-0731": { rpm: 50, tpm: 246_000, rpd: null },
  "deepseek-ai/deepseek-v4-flash": { rpm: 50, tpm: 246_000, rpd: null },
  "deepseek-v4-flash": { rpm: 50, tpm: 246_000, rpd: null },
  "qwen/qwen3-coder-30b-a3b-instruct": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "qwen3-coder-30b-a3b-instruct": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "qwen3-coder": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "mistralai/mistral-small-3.2-24b-instruct-2506": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "mistral-small-3.2-24b-instruct-2506": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "mistralai/ministral-3-8b-instruct-2512": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "ministral-3-8b-instruct-2512": { rpm: 50, tpm: 128_000, rpd: 1_000 },
  "ministral-3-8b": { rpm: 50, tpm: 128_000, rpd: 1_000 },
};

const DEFAULT_PUBLIC_ALBERT_LIMITS: PublicModelLimits = {
  rpm: 50,
  tpm: 246_000,
  rpd: 1_000,
};

const GUEST_WORKSPACE_ROOTS = new Set(["/workspace", "/Volumes/My Shared Files/workspace"]);

export function shortModelName(modelID?: string): string {
  const raw = (modelID ?? "").trim();
  if (!raw) return "aucun modèle";
  const slash = raw.lastIndexOf("/");
  if (slash >= 0 && slash < raw.length - 1) {
    return raw.slice(slash + 1);
  }
  return raw;
}

export function isAlbertProvider(providerID?: string, baseURL?: string): boolean {
  if ((providerID ?? "").trim().toLowerCase() === "albert") return true;
  if (!baseURL) return false;
  try {
    const parsed = new URL(baseURL);
    return parsed.hostname.toLowerCase() === ALBERT_HOST;
  } catch {
    return false;
  }
}

/**
 * Validates the Albert API base URL before attaching Authorization headers.
 * In production, only https://albert.api.etalab.gouv.fr is permitted so a
 * misconfigured or untrusted endpoint can never receive the Albert API key.
 */
export function resolveAlbertBaseURL(
  baseURL?: string,
  options?: { readonly allowCustomBaseURL?: boolean },
): string | undefined {
  const raw = (baseURL ?? "").trim() || ALBERT_BASE_URL;
  try {
    const parsed = new URL(raw);
    if (!options?.allowCustomBaseURL) {
      if (parsed.protocol !== "https:" || parsed.hostname.toLowerCase() !== ALBERT_HOST) {
        return undefined;
      }
      if (parsed.port && parsed.port !== "443") {
        return undefined;
      }
    } else if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      return undefined;
    }
    if (parsed.username || parsed.password || parsed.search || parsed.hash) {
      return undefined;
    }
    return raw.replace(/\/+$/, "");
  } catch {
    return undefined;
  }
}

export function resolveProviderDisplayName(
  providerID?: string,
  configuredName?: string,
  providerNames?: Readonly<Record<string, string>>,
): string {
  const id = (providerID ?? "").trim();
  const lower = id.toLowerCase();
  if (lower === "albert") return "Albert API";
  const fromConfig = id ? providerNames?.[id] ?? providerNames?.[lower] : undefined;
  if (fromConfig && fromConfig.trim()) {
    return normalizeProviderName(fromConfig);
  }
  if (configuredName && configuredName.trim()) {
    return normalizeProviderName(configuredName);
  }
  if (lower === "mistral") return "Mistral AI";
  if (lower === "local") return "Serveur local";
  return id || "Fournisseur";
}

function normalizeProviderName(name: string): string {
  const trimmed = name.trim();
  if (trimmed === "Albert API (État)") return "Albert API";
  return trimmed;
}

export function resolveFolderName(cwd: string, configuredProjectName?: string): string {
  const clean = (cwd || "").replace(/\/+$/, "") || "/";
  const project = (configuredProjectName ?? "").trim();
  if (project && GUEST_WORKSPACE_ROOTS.has(clean)) {
    return project;
  }
  const slash = clean.lastIndexOf("/");
  if (slash >= 0 && slash < clean.length - 1) {
    return clean.slice(slash + 1);
  }
  return project || clean;
}

export function normalizeThinkingLevel(level?: string): string | undefined {
  const clean = (level ?? "").trim();
  if (!clean) return undefined;
  const lower = clean.toLowerCase();
  if (lower === "off" || lower === "none" || lower === "default") return undefined;
  return clean;
}

export function estimateTokens(value: unknown): number {
  if (value === undefined || value === null) return 0;
  try {
    const serialized = typeof value === "string" ? value : JSON.stringify(value);
    if (!serialized) return 0;
    return Math.round(serialized.length / 4);
  } catch {
    return 0;
  }
}

export interface SessionTokenSummary {
  readonly lastContextTokens: number;
  readonly sessionInputTokens: number;
  readonly sessionOutputTokens: number;
}

/**
 * Reconstructs session input/output totals and latest context size from Pi's
 * active session branch entries. Keeps resumed and newly switched sessions
 * accurate without carrying stale counters across sessions.
 */
export function summarizePiBranch(entries: readonly unknown[]): SessionTokenSummary {
  let lastContextTokens = 0;
  let sessionInputTokens = 0;
  let sessionOutputTokens = 0;
  let runningEstimate = 0;

  for (const raw of entries) {
    if (!raw || typeof raw !== "object") continue;
    const entry = raw as {
      readonly type?: unknown;
      readonly message?: {
        readonly role?: unknown;
        readonly content?: unknown;
        readonly usage?: {
          readonly input?: unknown;
          readonly output?: unknown;
          readonly cacheRead?: unknown;
          readonly cacheWrite?: unknown;
        } | null;
      } | null;
    };
    if (entry.type !== "message" || !entry.message) continue;
    const msg = entry.message;
    if (msg.role === "assistant") {
      const usage = msg.usage;
      const reportedInput =
        (typeof usage?.input === "number" && Number.isFinite(usage.input) ? Math.max(0, usage.input) : 0) +
        (typeof usage?.cacheRead === "number" && Number.isFinite(usage.cacheRead) ? Math.max(0, usage.cacheRead) : 0) +
        (typeof usage?.cacheWrite === "number" && Number.isFinite(usage.cacheWrite) ? Math.max(0, usage.cacheWrite) : 0);
      const reportedOutput =
        typeof usage?.output === "number" && Number.isFinite(usage.output) ? Math.max(0, usage.output) : 0;

      const inputForTurn = reportedInput > 0 ? reportedInput : runningEstimate;
      const outputForTurn = reportedOutput > 0 ? reportedOutput : estimateTokens(msg.content);

      if (inputForTurn > 0) {
        lastContextTokens = inputForTurn;
        sessionInputTokens += inputForTurn;
      }
      sessionOutputTokens += outputForTurn;
      runningEstimate += outputForTurn;
    } else {
      runningEstimate += estimateTokens(msg.content ?? msg);
    }
  }

  return { lastContextTokens, sessionInputTokens, sessionOutputTokens };
}

export function formatTokens(n: number): string {
  const safe = Math.max(0, Math.round(n));
  if (safe >= 1_000_000) {
    return `${(safe / 1_000_000).toFixed(1).replace(".", ",")}M`;
  }
  if (safe >= 1_000) {
    return `${Math.round(safe / 1_000)}k`;
  }
  return String(safe);
}

export function formatGramsCO2(kg: number): string {
  const grams = Math.max(0, kg * 1_000);
  const raw = grams >= 10 ? grams.toFixed(0) : grams.toFixed(1);
  return raw.replace(".", ",");
}

export function buildGauge(used: number, max: number, width = GAUGE_WIDTH): GaugeView {
  let pct: number;
  if (max <= 0) {
    // OpenGateLLM#1334: a limit of 0 blocks before any request, so show 100% used.
    pct = 1;
  } else {
    pct = Math.max(0, Math.min(1, used / max));
  }
  const filled = Math.round(pct * width);
  const severity: GaugeView["severity"] = pct >= 0.9 ? "error" : pct >= 0.7 ? "warning" : "normal";
  return {
    pct,
    barFilled: "█".repeat(filled),
    barEmpty: "░".repeat(Math.max(0, width - filled)),
    severity,
  };
}

function positiveEnvInt(env: Readonly<Record<string, string | undefined>> | undefined, key: string): number | undefined {
  const raw = env?.[key];
  if (!raw) return undefined;
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : undefined;
}

export function getPublicAlbertLimits(
  candidates: readonly (string | undefined)[],
  env?: Readonly<Record<string, string | undefined>>,
): PublicModelLimits {
  let base: PublicModelLimits = DEFAULT_PUBLIC_ALBERT_LIMITS;
  for (const candidate of candidates) {
    const raw = (candidate ?? "").trim().toLowerCase();
    if (!raw) continue;
    if (PUBLIC_ALBERT_LIMITS_TABLE[raw]) {
      base = PUBLIC_ALBERT_LIMITS_TABLE[raw];
      break;
    }
    const short = shortModelName(raw).toLowerCase();
    if (PUBLIC_ALBERT_LIMITS_TABLE[short]) {
      base = PUBLIC_ALBERT_LIMITS_TABLE[short];
      break;
    }
  }
  return {
    rpm: positiveEnvInt(env, "ALBERT_RPM") ?? base.rpm,
    tpm: positiveEnvInt(env, "ALBERT_TPM") ?? base.tpm,
    rpd: positiveEnvInt(env, "ALBERT_RPD") ?? base.rpd,
  };
}

interface RawLimitItem {
  readonly type?: unknown;
  readonly value?: unknown;
  readonly remaining?: unknown;
}

interface RawModelLimitsEntry {
  readonly model?: unknown;
  readonly limits?: unknown;
}

export function parseLimitsResponse(
  payload: unknown,
  candidates: readonly (string | undefined)[],
  fetchedAt: number,
): AccountModelLimits | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const data = (payload as { readonly data?: unknown }).data;
  if (!Array.isArray(data)) return undefined;

  const normalizedCandidates = candidates
    .map((c) => (c ?? "").trim())
    .filter((c): c is string => c.length > 0);
  if (normalizedCandidates.length === 0) return undefined;

  const entries = data.filter(
    (item): item is RawModelLimitsEntry =>
      Boolean(item) && typeof item === "object" && typeof (item as RawModelLimitsEntry).model === "string",
  );

  let match: RawModelLimitsEntry | undefined;
  // 1. Exact case-sensitive match
  for (const cand of normalizedCandidates) {
    match = entries.find((e) => e.model === cand);
    if (match) break;
  }
  // 2. Case-insensitive exact match
  if (!match) {
    for (const cand of normalizedCandidates) {
      const lower = cand.toLowerCase();
      match = entries.find((e) => String(e.model).toLowerCase() === lower);
      if (match) break;
    }
  }
  // 3. Short model name fallback (after last '/')
  if (!match) {
    for (const cand of normalizedCandidates) {
      const short = shortModelName(cand).toLowerCase();
      match = entries.find((e) => shortModelName(String(e.model)).toLowerCase() === short);
      if (match) break;
    }
  }
  if (!match || !Array.isArray(match.limits)) return undefined;

  const parsed: {
    model: string;
    rpm?: LimitWindow;
    tpm?: LimitWindow;
    rpd?: LimitWindow;
    tpd?: LimitWindow;
    fetchedAt: number;
  } = { model: String(match.model), fetchedAt };

  for (const raw of match.limits as RawLimitItem[]) {
    if (!raw || typeof raw !== "object" || typeof raw.type !== "string") continue;
    const type = raw.type.toLowerCase();
    if (type !== "rpm" && type !== "tpm" && type !== "rpd" && type !== "tpd") continue;

    if (raw.value === null) {
      parsed[type] = { limit: null, used: null, remaining: null };
      continue;
    }
    if (typeof raw.value !== "number" || !Number.isFinite(raw.value) || raw.value < 0) continue;
    const limit = Math.floor(raw.value);
    const remainingRaw =
      typeof raw.remaining === "number" && Number.isFinite(raw.remaining) ? Math.floor(raw.remaining) : limit;
    const remaining = Math.max(0, remainingRaw);
    const used = Math.max(0, limit - remaining);
    parsed[type] = { limit, used, remaining };
  }

  return parsed;
}

export function parseUsageResponse(payload: unknown): DailyUsage {
  if (!payload || typeof payload !== "object") {
    return { requests: 0, kgCO2eq: 0, completionTokens: 0 };
  }
  const data = (payload as { readonly data?: unknown }).data;
  if (!Array.isArray(data)) {
    return { requests: 0, kgCO2eq: 0, completionTokens: 0 };
  }
  let requests = 0;
  let kgCO2eq = 0;
  let completionTokens = 0;
  for (const item of data) {
    if (!item || typeof item !== "object") continue;
    const u = item as {
      readonly requests?: unknown;
      readonly completion_tokens?: unknown;
      readonly impacts?: { readonly kgCO2eq?: unknown } | null;
    };
    if (typeof u.requests === "number" && Number.isFinite(u.requests)) {
      requests += Math.max(0, u.requests);
    }
    if (typeof u.completion_tokens === "number" && Number.isFinite(u.completion_tokens)) {
      completionTokens += Math.max(0, u.completion_tokens);
    }
    const kg = u.impacts?.kgCO2eq;
    if (typeof kg === "number" && Number.isFinite(kg)) {
      kgCO2eq += Math.max(0, kg);
    }
  }
  return { requests, kgCO2eq, completionTokens };
}

export interface FetchOptions {
  readonly fetchFn?: typeof fetch;
  readonly allowCustomBaseURL?: boolean;
  readonly timeoutMs?: number;
  readonly keyId?: string;
}

async function fetchAlbertJSON(
  baseURL: string,
  apiKey: string | undefined,
  path: string,
  options?: FetchOptions,
): Promise<{ readonly status: number; readonly ok: boolean; readonly body?: unknown }> {
  const validatedBase = resolveAlbertBaseURL(baseURL, { allowCustomBaseURL: options?.allowCustomBaseURL });
  if (!validatedBase) {
    throw new Error("unapproved Albert base URL");
  }
  const fetchImpl = options?.fetchFn ?? fetch;
  const headers: Record<string, string> = {};
  if (apiKey) {
    headers.Authorization = `Bearer ${apiKey}`;
  }
  const response = await fetchImpl(`${validatedBase}${path}`, {
    method: "GET",
    headers,
    redirect: "manual",
    signal: AbortSignal.timeout(options?.timeoutMs ?? FETCH_TIMEOUT_MS),
  });
  if (!response.ok) {
    return { status: response.status, ok: false };
  }
  const body = await response.json();
  return { status: response.status, ok: true, body };
}

export async function fetchAlbertLimits(
  baseURL: string,
  apiKey: string | undefined,
  modelCandidates: readonly (string | undefined)[],
  nowMs: number,
  options?: FetchOptions,
): Promise<AccountModelLimits | undefined> {
  try {
    const res = await fetchAlbertJSON(baseURL, apiKey, "/limits", options);
    if (!res.ok) return undefined;
    return parseLimitsResponse(res.body, modelCandidates, nowMs);
  } catch {
    return undefined;
  }
}

export async function fetchAlbertUsage(
  baseURL: string,
  apiKey: string | undefined,
  nowMs: number,
  options?: FetchOptions,
): Promise<DailyUsage> {
  const end = Math.floor(nowMs / 1_000);
  const start = end - (end % 86_400);
  const keyFilter = options?.keyId ? `&key_id=${encodeURIComponent(options.keyId)}` : "";
  const query = `?start_time=${start}&end_time=${end}${keyFilter}`;
  let res = await fetchAlbertJSON(baseURL, apiKey, `/me/usage${query}`, options);
  if (!res.ok && res.status === 404) {
    // OpenGateLLM also serves /v1/usage (the non-deprecated alias of /v1/me/usage).
    res = await fetchAlbertJSON(baseURL, apiKey, `/usage${query}`, options);
  }
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}`);
  }
  return parseUsageResponse(res.body);
}

export function pruneWindow(requests: readonly RequestSample[], now: number): RequestSample[] {
  return requests.filter((r) => now - r.t >= 0 && now - r.t < WINDOW_MS);
}

function gaugeSegments(
  label: string,
  used: number,
  max: number,
  normalTone: "success" | "accent",
  valueText: string,
): StatusSegment[] {
  const gauge = buildGauge(used, max);
  const tone: SegmentTone = gauge.severity === "normal" ? normalTone : gauge.severity;
  const out: StatusSegment[] = [];
  if (label) {
    out.push({ text: `${label} `, tone: "muted" });
  }
  if (gauge.barFilled) {
    out.push({ text: gauge.barFilled, tone });
  }
  if (gauge.barEmpty) {
    out.push({ text: gauge.barEmpty, tone: "mutedBar" });
  }
  out.push({ text: ` ${valueText}`, tone });
  return out;
}

function joinParts(parts: readonly StatusSegment[][]): StatusSegment[] {
  const out: StatusSegment[] = [];
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i];
    if (!part || part.length === 0) continue;
    if (out.length > 0) {
      out.push({ text: " │ ", tone: "muted" });
    }
    out.push(...part);
  }
  return out;
}

/**
 * Renders the 2-line (non-Albert) or 3-line (Albert) status bar into
 * harness-agnostic styled segments.
 */
export function buildStatusLines(input: StatusRenderInput): StatusLine[] {
  const albert = isAlbertProvider(input.providerID, input.baseURL);
  const provider = resolveProviderDisplayName(
    input.providerID,
    input.providerName,
    input.config?.providerNames,
  );
  const model = shortModelName(input.modelDisplayID ?? input.apiModelID);
  const thinking = normalizeThinkingLevel(input.thinkingLevel);
  const folder = resolveFolderName(input.cwd, input.config?.projectName);
  const branch = (input.gitBranch ?? "").trim();

  // Line 1: Provider · model [· thinking] │ folder [(branch)]
  const line1Left: StatusSegment[] = [];
  if (input.running) {
    line1Left.push({ text: "⋯ ", tone: "accent" });
  }
  const headText = thinking ? `${provider} · ${model} · ${thinking}` : `${provider} · ${model}`;
  line1Left.push({ text: headText, tone: "accent" });

  const line1Right: StatusSegment[] = [{ text: folder, tone: "warning" }];
  if (branch) {
    line1Right.push({ text: ` (${branch})`, tone: "muted" });
  }
  const line1 = joinParts([line1Left, line1Right]);

  const contextPart: StatusSegment[] =
    input.contextWindow > 0
      ? gaugeSegments(
          "Contexte",
          input.lastContextTokens,
          input.contextWindow,
          "success",
          `${Math.min(100, Math.round((Math.max(0, input.lastContextTokens) / input.contextWindow) * 100))}%`,
        )
      : [];

  const sessionTotal = Math.max(0, input.sessionInputTokens) + Math.max(0, input.sessionOutputTokens);

  // Non-Albert providers: 2 lines only.
  if (!albert) {
    const sessionPart: StatusSegment[] = [
      { text: "Session ", tone: "muted" },
      { text: `${formatTokens(sessionTotal)} tok`, tone: "base" },
    ];
    return [line1, joinParts([contextPart, sessionPart])];
  }

  // Albert API: 3 lines.
  const activeModelKey = (input.apiModelID ?? input.modelDisplayID ?? "").trim().toLowerCase();
  const recent = pruneWindow(input.requests, input.now).filter((r) => {
    if (!r.model || !activeModelKey) return true;
    const sampleKey = r.model.trim().toLowerCase();
    return sampleKey === activeModelKey || shortModelName(sampleKey) === shortModelName(activeModelKey);
  });
  const localRpm = recent.length;
  const localTpm = recent.reduce((sum, r) => sum + Math.max(0, r.tokens), 0);

  const account = input.accountLimits;
  const usingPublicFallback = !account;
  const pub = getPublicAlbertLimits([input.apiModelID, input.modelDisplayID], input.env);

  // Optimistic local delta for requests dispatched strictly after the last
  // /v1/limits snapshot so the RPM/TPM gauges react immediately while working.
  const pendingSinceLimits = account
    ? recent.filter((r) => r.t > account.fetchedAt)
    : [];
  const pendingReq = pendingSinceLimits.length;
  const pendingTok = pendingSinceLimits.reduce((sum, r) => sum + Math.max(0, r.tokens), 0);

  let rpmPart: StatusSegment[];
  if (account?.rpm) {
    if (account.rpm.limit === null) {
      rpmPart = [
        { text: "RPM ", tone: "muted" },
        { text: `${localRpm}/∞`, tone: "accent" },
      ];
    } else {
      const used = (account.rpm.used ?? 0) + pendingReq;
      rpmPart = gaugeSegments("RPM", used, account.rpm.limit, "accent", `${used}/${account.rpm.limit}`);
    }
  } else {
    rpmPart = gaugeSegments("RPM", localRpm, pub.rpm, "accent", `${localRpm}/${pub.rpm}`);
  }

  let tpmPart: StatusSegment[];
  if (account?.tpm) {
    if (account.tpm.limit === null) {
      tpmPart = [
        { text: "TPM ", tone: "muted" },
        { text: `${formatTokens(localTpm)}/∞`, tone: "accent" },
      ];
    } else {
      const used = (account.tpm.used ?? 0) + pendingTok;
      tpmPart = gaugeSegments(
        "TPM",
        used,
        account.tpm.limit,
        "accent",
        `${formatTokens(used)}/${formatTokens(account.tpm.limit)}`,
      );
    }
  } else {
    tpmPart = gaugeSegments(
      "TPM",
      localTpm,
      pub.tpm,
      "accent",
      `${formatTokens(localTpm)}/${formatTokens(pub.tpm)}`,
    );
  }

  if (usingPublicFallback && input.limitsChecked !== false) {
    tpmPart = [...tpmPart, { text: " (grille publique)", tone: "muted" }];
  }

  const line2 = joinParts([contextPart, rpmPart, tpmPart]);

  // Line 3: Session tokens & CO2 │ Jour requests/RPD & CO2 (or usage Albert indisponible)
  const sessionPart: StatusSegment[] = [
    { text: "Session ", tone: "muted" },
    { text: `${formatTokens(sessionTotal)} tok`, tone: "base" },
  ];
  const usage = input.dailyUsage;
  if (!input.usageUnavailable && usage && usage.completionTokens > 0 && usage.kgCO2eq > 0 && input.sessionOutputTokens > 0) {
    const sessionKg = (usage.kgCO2eq / usage.completionTokens) * input.sessionOutputTokens;
    sessionPart.push({ text: ` · ≈ ${formatGramsCO2(sessionKg)} gCO2e`, tone: "base" });
  }

  let jourPart: StatusSegment[] = [];
  if (input.usageUnavailable) {
    jourPart = [{ text: "usage Albert indisponible", tone: "error" }];
  } else if (usage) {
    const co2Suffix: StatusSegment = { text: ` · ${formatGramsCO2(usage.kgCO2eq)} gCO2e`, tone: "base" };
    if (account?.rpd) {
      if (account.rpd.limit === null) {
        jourPart = [
          { text: "Jour ", tone: "muted" },
          { text: `${usage.requests} req`, tone: "base" },
          co2Suffix,
        ];
      } else {
        const used = (account.rpd.used ?? 0) + pendingReq;
        jourPart = [
          ...gaugeSegments("Jour", used, account.rpd.limit, "accent", `${used}/${account.rpd.limit} req`),
          co2Suffix,
        ];
      }
    } else if (pub.rpd !== null) {
      jourPart = [
        ...gaugeSegments("Jour", usage.requests, pub.rpd, "accent", `${usage.requests}/${pub.rpd} req`),
        co2Suffix,
      ];
    } else {
      jourPart = [
        { text: "Jour ", tone: "muted" },
        { text: `${usage.requests} req`, tone: "base" },
        co2Suffix,
      ];
    }
  } else if (account?.rpd && account.rpd.limit !== null) {
    // /v1/limits succeeded before /v1/me/usage completed: show RPD progress right away.
    const used = (account.rpd.used ?? 0) + pendingReq;
    jourPart = gaugeSegments("Jour", used, account.rpd.limit, "accent", `${used}/${account.rpd.limit} req`);
  }

  const line3 = joinParts([sessionPart, jourPart]);
  return [line1, line2, line3];
}

export function renderPlainLines(lines: readonly StatusLine[]): string[] {
  return lines.map((line) => line.map((seg) => seg.text).join(""));
}
