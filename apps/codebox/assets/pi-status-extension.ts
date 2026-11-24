/**
 * Barre d'état Albert API pour Pi (remplace le footer), alignée avec le plugin
 * TUI OpenCode de codebox (#318). Inspirée de benoitvx/pi-albert-status.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { truncateToWidth } from "@earendil-works/pi-tui";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  buildStatusLines,
  estimateTokens,
  fetchAlbertLimits,
  fetchAlbertUsage,
  isAlbertProvider,
  pruneWindow,
  type AccountModelLimits,
  type DailyUsage,
  type RequestSample,
  type SegmentTone,
  type StatusBarConfig,
} from "./status-core.ts";

const POLL_INTERVAL_MS = 10_000;
const WINDOW_TICK_MS = 5_000;

function loadConfig(): StatusBarConfig {
  try {
    const dir = dirname(fileURLToPath(import.meta.url));
    const raw = readFileSync(join(dir, "config.json"), "utf8");
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object") {
      return parsed as StatusBarConfig;
    }
  } catch {
    // Missing or unreadable config.json falls back to runtime metadata.
  }
  return {};
}

function sessionOutputTokens(ctx: any): number {
  let total = 0;
  try {
    for (const entry of ctx.sessionManager.getBranch()) {
      if (entry?.type === "message" && entry.message?.role === "assistant") {
        total += entry.message.usage?.output || estimateTokens(entry.message.content);
      }
    }
  } catch {
    return total;
  }
  return total;
}

export default function (pi: ExtensionAPI) {
  const config = loadConfig();

  let requests: RequestSample[] = [];
  let sessionInputTokens = 0;
  let lastContextTokens = 0;
  let accountLimits: AccountModelLimits | undefined;
  let limitsChecked = false;
  let dailyUsage: DailyUsage | undefined;
  let usageUnavailable = false;
  let pollTimer: ReturnType<typeof setInterval> | undefined;
  let windowTimer: ReturnType<typeof setInterval> | undefined;
  let pollSeq = 0;
  let render = () => {};

  async function poll(ctx: any) {
    const seq = ++pollSeq;
    const model = ctx?.model;
    if (!model) {
      accountLimits = undefined;
      limitsChecked = false;
      dailyUsage = undefined;
      usageUnavailable = false;
      render();
      return;
    }

    let baseURL: string | undefined = model.baseUrl;
    let apiKey: string | undefined;
    try {
      const auth = await ctx.modelRegistry.getApiKeyAndHeaders(model);
      if (auth?.ok) {
        baseURL = auth.baseUrl ?? model.baseUrl;
        apiKey = auth.apiKey;
      }
    } catch {
      // Handled below if provider is Albert.
    }

    if (!isAlbertProvider(model.provider, baseURL)) {
      if (seq !== pollSeq) return;
      accountLimits = undefined;
      limitsChecked = false;
      dailyUsage = undefined;
      usageUnavailable = false;
      render();
      return;
    }

    const effectiveBase = baseURL || "https://albert.api.etalab.gouv.fr/v1";
    const keyId = process.env.ALBERT_KEY_ID || undefined;
    const nowMs = Date.now();

    const [limitsRes, usageRes] = await Promise.allSettled([
      fetchAlbertLimits(effectiveBase, apiKey, [model.id, model.name], nowMs),
      fetchAlbertUsage(effectiveBase, apiKey, nowMs, { keyId }),
    ]);

    if (seq !== pollSeq) return;

    accountLimits = limitsRes.status === "fulfilled" ? limitsRes.value : undefined;
    limitsChecked = true;

    if (usageRes.status === "fulfilled") {
      dailyUsage = usageRes.value;
      usageUnavailable = false;
    } else {
      usageUnavailable = true;
    }
    render();
  }

  pi.on("before_provider_request", async (e: any, ctx: any) => {
    const n = estimateTokens(e?.payload);
    const ts = Date.now();
    const modelKey = ctx?.model?.id ?? e?.payload?.model;
    requests = [...pruneWindow(requests, ts), { t: ts, tokens: n, model: modelKey }];
    sessionInputTokens += n;
    lastContextTokens = n;
    render();
  });

  pi.on("session_start", async (_e: any, ctx: any) => {
    ctx.ui.setFooter((tui: any, theme: any, footerData: any) => {
      render = () => tui.requestRender();
      const unsubBranch = footerData.onBranchChange(render);

      const paint = (text: string, tone: SegmentTone): string => {
        switch (tone) {
          case "accent":
            return theme.fg("accent", text);
          case "muted":
            return theme.fg("dim", text);
          case "mutedBar":
            return theme.fg("borderMuted", text);
          case "success":
            return theme.fg("success", text);
          case "warning":
            return theme.fg("warning", text);
          case "error":
            return theme.fg("error", text);
          default:
            return text;
        }
      };

      return {
        dispose: unsubBranch,
        invalidate() {},
        render(width: number): string[] {
          const now = Date.now();
          requests = pruneWindow(requests, now);
          const model = ctx.model;
          const thinking = model?.reasoning ? pi.getThinkingLevel() : undefined;

          const lines = buildStatusLines({
            providerID: model?.provider,
            baseURL: model?.baseUrl,
            modelDisplayID: model?.id,
            apiModelID: model?.id,
            thinkingLevel: thinking,
            cwd: ctx.cwd,
            gitBranch: footerData.getGitBranch(),
            contextWindow: model?.contextWindow ?? 0,
            lastContextTokens,
            sessionInputTokens,
            sessionOutputTokens: sessionOutputTokens(ctx),
            requests,
            now,
            accountLimits,
            limitsChecked,
            dailyUsage,
            usageUnavailable,
            config,
            env: process.env,
          });

          return lines.map((line) =>
            truncateToWidth(line.map((seg) => paint(seg.text, seg.tone)).join(""), width),
          );
        },
      };
    });

    await poll(ctx);
    clearInterval(pollTimer);
    clearInterval(windowTimer);
    pollTimer = setInterval(() => {
      void poll(ctx);
    }, POLL_INTERVAL_MS);
    windowTimer = setInterval(() => {
      if (requests.length > 0) {
        requests = pruneWindow(requests, Date.now());
        render();
      }
    }, WINDOW_TICK_MS);
  });

  pi.on("model_select", async (_e: any, ctx: any) => {
    accountLimits = undefined;
    limitsChecked = false;
    usageUnavailable = false;
    render();
    await poll(ctx);
  });

  pi.on("turn_end", async (_e: any, ctx: any) => {
    await poll(ctx);
  });

  pi.on("session_shutdown", async () => {
    clearInterval(pollTimer);
    clearInterval(windowTimer);
  });
}
