import { Plugin } from "@opencode/plugin/tui";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { For, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
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

interface SessionTokenTotals {
  readonly lastContextTokens: number;
  readonly sessionInputTokens: number;
  readonly sessionOutputTokens: number;
}

function summarizeSessionMessages(messages: readonly any[]): SessionTokenTotals {
  let lastContextTokens = 0;
  let sessionInputTokens = 0;
  let sessionOutputTokens = 0;
  let runningEstimate = 0;

  for (const msg of messages) {
    if (!msg || typeof msg !== "object") continue;
    if (msg.type === "assistant") {
      const tokens = msg.tokens;
      const reportedInput =
        (typeof tokens?.input === "number" ? tokens.input : 0) +
        (typeof tokens?.cache?.read === "number" ? tokens.cache.read : 0) +
        (typeof tokens?.cache?.write === "number" ? tokens.cache.write : 0);
      const reportedOutput =
        (typeof tokens?.output === "number" ? tokens.output : 0) +
        (typeof tokens?.reasoning === "number" ? tokens.reasoning : 0);

      const inputForStep = reportedInput > 0 ? reportedInput : runningEstimate;
      const outputForStep = reportedOutput > 0 ? reportedOutput : estimateTokens(msg.content);

      if (inputForStep > 0) {
        lastContextTokens = inputForStep;
        sessionInputTokens += inputForStep;
      }
      sessionOutputTokens += outputForStep;
      runningEstimate += outputForStep;
    } else {
      runningEstimate += estimateTokens(msg);
    }
  }

  return { lastContextTokens, sessionInputTokens, sessionOutputTokens };
}

export default Plugin.define({
  id: "codebox.status",
  setup(context) {
    const config = loadConfig();

    const [requests, setRequests] = createSignal<RequestSample[]>([]);
    const [now, setNow] = createSignal(Date.now());
    const [lastDispatchContext, setLastDispatchContext] = createSignal(0);
    const [accountLimits, setAccountLimits] = createSignal<AccountModelLimits | undefined>(undefined);
    const [limitsChecked, setLimitsChecked] = createSignal(false);
    const [dailyUsage, setDailyUsage] = createSignal<DailyUsage | undefined>(undefined);
    const [usageUnavailable, setUsageUnavailable] = createSignal(false);

    let pollSeq = 0;
    let lastTargetKey = "";
    let currentTarget: {
      providerID?: string;
      baseURL?: string;
      apiKey?: string;
      apiModelID?: string;
      aliasModelID?: string;
    } = {};

    async function pollAlbert() {
      const target = currentTarget;
      const seq = ++pollSeq;
      if (!isAlbertProvider(target.providerID, target.baseURL)) {
        setAccountLimits(undefined);
        setLimitsChecked(false);
        setDailyUsage(undefined);
        setUsageUnavailable(false);
        return;
      }

      const baseURL = target.baseURL || "https://albert.api.etalab.gouv.fr/v1";
      const apiKey = target.apiKey || process.env.ALBERT_API_KEY;
      const keyId = process.env.ALBERT_KEY_ID || undefined;
      const nowMs = Date.now();

      const [limitsRes, usageRes] = await Promise.allSettled([
        fetchAlbertLimits(baseURL, apiKey, [target.apiModelID, target.aliasModelID], nowMs),
        fetchAlbertUsage(baseURL, apiKey, nowMs, { keyId }),
      ]);

      if (seq !== pollSeq) return;

      if (limitsRes.status === "fulfilled") {
        setAccountLimits(limitsRes.value);
      } else {
        setAccountLimits(undefined);
      }
      setLimitsChecked(true);

      if (usageRes.status === "fulfilled") {
        setDailyUsage(usageRes.value);
        setUsageUnavailable(false);
      } else {
        setUsageUnavailable(true);
      }
      setNow(Date.now());
    }

    const unsubStepStarted = context.data.on("session.step.started", (event: any) => {
      const sessionID: string | undefined = event?.data?.sessionID;
      const messages = sessionID ? context.data.session.message.list(sessionID) : [];
      const est = Math.max(1, estimateTokens(messages));
      const ts = Date.now();
      const modelKey = event?.data?.model?.modelID ?? currentTarget.apiModelID ?? currentTarget.aliasModelID;
      setLastDispatchContext(est);
      setRequests((prev) => [...pruneWindow(prev, ts), { t: ts, tokens: est, model: modelKey }]);
      setNow(ts);
    });

    const unsubStepEnded = context.data.on("session.step.ended", (event: any) => {
      const tokens = event?.data?.tokens;
      const reportedInput =
        (typeof tokens?.input === "number" ? tokens.input : 0) +
        (typeof tokens?.cache?.read === "number" ? tokens.cache.read : 0) +
        (typeof tokens?.cache?.write === "number" ? tokens.cache.write : 0);
      if (reportedInput > 0) {
        setLastDispatchContext(reportedInput);
      }
      void pollAlbert();
    });

    const pollTimer = setInterval(() => {
      void pollAlbert();
    }, POLL_INTERVAL_MS);

    const windowTimer = setInterval(() => {
      if (requests().length > 0) {
        const ts = Date.now();
        setRequests((prev) => pruneWindow(prev, ts));
        setNow(ts);
      }
    }, WINDOW_TICK_MS);

    const unslot = context.ui.slot({
      replace: "prompt.footer",
      render: (slotInput) => {
        const currentSelection = createMemo(() => context.ui.model.current());
        const providers = createMemo(() => context.data.location.provider.list(context.location) ?? []);
        const models = createMemo(() => context.data.location.model.list(context.location) ?? []);
        const vcs = createMemo(() => context.data.location.vcs.info(context.location));

        const activeProvider = createMemo(() => {
          const sel = currentSelection();
          if (!sel) return undefined;
          return providers().find((p) => p.id === sel.providerID);
        });

        const activeModel = createMemo(() => {
          const sel = currentSelection();
          if (!sel) return undefined;
          return models().find((m) => m.providerID === sel.providerID && m.id === sel.modelID);
        });

        createEffect(() => {
          const sel = currentSelection();
          const prov = activeProvider();
          const mod = activeModel();
          const settings = (mod?.settings ?? prov?.settings) as
            | { readonly baseURL?: string; readonly apiKey?: string }
            | undefined;
          const next = {
            providerID: sel?.providerID,
            baseURL: settings?.baseURL,
            apiKey: settings?.apiKey,
            apiModelID: mod?.modelID ?? sel?.modelID,
            aliasModelID: sel?.modelID,
          };
          const key = `${next.providerID ?? ""}|${next.apiModelID ?? ""}|${next.baseURL ?? ""}`;
          currentTarget = next;
          if (key !== lastTargetKey) {
            lastTargetKey = key;
            setAccountLimits(undefined);
            setLimitsChecked(false);
            setUsageUnavailable(false);
            void pollAlbert();
          }
        });

        const sessionTotals = createMemo<SessionTokenTotals>(() => {
          const sid = slotInput.sessionID;
          if (!sid) {
            return { lastContextTokens: 0, sessionInputTokens: 0, sessionOutputTokens: 0 };
          }
          const messages = context.data.session.message.list(sid) ?? [];
          return summarizeSessionMessages(messages);
        });

        const isRunning = createMemo(() => {
          const sid = slotInput.sessionID;
          return sid ? context.data.session.status(sid) === "running" : false;
        });

        const lines = createMemo(() => {
          const sel = currentSelection();
          const prov = activeProvider();
          const mod = activeModel();
          const settings = (mod?.settings ?? prov?.settings) as { readonly baseURL?: string } | undefined;
          const totals = sessionTotals();
          const lastCtx = Math.max(totals.lastContextTokens, isRunning() ? lastDispatchContext() : 0);

          return buildStatusLines({
            providerID: sel?.providerID,
            providerName: prov?.name,
            baseURL: settings?.baseURL,
            modelDisplayID: sel?.modelID,
            apiModelID: mod?.modelID ?? sel?.modelID,
            thinkingLevel: sel?.variant,
            cwd: context.location?.directory ?? process.cwd(),
            gitBranch: vcs()?.branch?.current,
            running: isRunning(),
            contextWindow: mod?.limit?.context ?? 0,
            lastContextTokens: lastCtx,
            sessionInputTokens: totals.sessionInputTokens,
            sessionOutputTokens: totals.sessionOutputTokens,
            requests: requests(),
            now: now(),
            accountLimits: accountLimits(),
            limitsChecked: limitsChecked(),
            dailyUsage: dailyUsage(),
            usageUnavailable: usageUnavailable(),
            config,
            env: process.env,
          });
        });

        const toneColor = (tone: SegmentTone) => {
          const theme = context.theme;
          switch (tone) {
            case "accent":
              return theme.hue.accent[500];
            case "muted":
              return theme.text.muted;
            case "mutedBar":
              return theme.text.faint;
            case "success":
              return theme.text.feedback.success.base;
            case "warning":
              return theme.text.feedback.warning.base;
            case "error":
              return theme.text.feedback.error.base;
            default:
              return theme.text.base;
          }
        };

        onCleanup(() => {
          // Slot unmounted; intervals stay owned by plugin cleanup.
        });

        return (
          <box flexDirection="column" width="100%" flexGrow={1} flexShrink={1} minWidth={0}>
            <For each={lines()}>
              {(line) => (
                <text wrapMode="none" truncate>
                  <For each={line}>
                    {(seg) => <span style={{ fg: toneColor(seg.tone) }}>{seg.text}</span>}
                  </For>
                </text>
              )}
            </For>
          </box>
        );
      },
    });

    return () => {
      clearInterval(pollTimer);
      clearInterval(windowTimer);
      unsubStepStarted();
      unsubStepEnded();
      unslot();
    };
  },
});
