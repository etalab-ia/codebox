import assert from "node:assert/strict";
import http from "node:http";
import test from "node:test";
import {
  buildGauge,
  buildStatusLines,
  estimateTokens,
  fetchAlbertLimits,
  fetchAlbertUsage,
  formatGramsCO2,
  formatTokens,
  getPublicAlbertLimits,
  isAlbertProvider,
  parseLimitsResponse,
  parseUsageResponse,
  renderPlainLines,
  resolveAlbertBaseURL,
  resolveFolderName,
  resolveProviderDisplayName,
  shortModelName,
  summarizePiBranch,
} from "../../../assets/status-core.ts";

test("Albert 3-line layout with OpenGateLLM 0.9.3 /v1/limits matches design", () => {
  const now = 1_700_000_000_000;
  const lines = buildStatusLines({
    providerID: "albert",
    providerName: "Albert API (État)",
    baseURL: "https://albert.api.etalab.gouv.fr/v1",
    modelDisplayID: "gpt-oss-120b",
    apiModelID: "openai/gpt-oss-120b",
    thinkingLevel: "medium",
    cwd: "/workspace",
    gitBranch: "main",
    contextWindow: 131_072,
    lastContextTokens: 40_632, // 31%
    sessionInputTokens: 400_000,
    sessionOutputTokens: 81_000,
    requests: [],
    now,
    accountLimits: {
      model: "openai/gpt-oss-120b",
      rpm: { limit: 50, used: 12, remaining: 38 },
      tpm: { limit: 246_000, used: 200_000, remaining: 46_000 },
      rpd: { limit: 500, used: 167, remaining: 333 },
      fetchedAt: now - 1_000,
    },
    limitsChecked: true,
    dailyUsage: {
      requests: 167,
      kgCO2eq: 0.0048,
      completionTokens: 4_000_000,
    },
    config: { projectName: "mon-projet" },
  });

  const plain = renderPlainLines(lines);
  assert.equal(plain.length, 3);
  assert.equal(plain[0], "Albert API · gpt-oss-120b · medium │ mon-projet (main)");
  assert.equal(plain[1], "Contexte ███░░░░░░░ 31% │ RPM ██░░░░░░░░ 12/50 │ TPM ████████░░ 200k/246k");
  assert.equal(plain[2], "Session 481k tok · ≈ 0,1 gCO2e │ Jour ███░░░░░░░ 167/500 req · 4,8 gCO2e");
  assert.ok(!plain[1].includes("(grille publique)"));
});

test("Non-Albert provider renders 2 lines only with no rate limits or usage", () => {
  const lines = buildStatusLines({
    providerID: "mistral",
    providerName: "Mistral AI",
    baseURL: "https://api.mistral.ai/v1",
    modelDisplayID: "mistral-large-latest",
    apiModelID: "mistral-large-latest",
    cwd: "/workspace",
    gitBranch: "main",
    contextWindow: 128_000,
    lastContextTokens: 39_680, // 31%
    sessionInputTokens: 400_000,
    sessionOutputTokens: 81_000,
    requests: [{ t: 1000, tokens: 1000 }],
    now: 2000,
    config: { projectName: "mon-projet" },
  });

  const plain = renderPlainLines(lines);
  assert.equal(plain.length, 2);
  assert.equal(plain[0], "Mistral AI · mistral-large-latest │ mon-projet (main)");
  assert.equal(plain[1], "Contexte ███░░░░░░░ 31% │ Session 481k tok");
});

test("Thinking level is hidden when unset, off, none, or default", () => {
  for (const level of [undefined, "", "off", "none", "default"]) {
    const plain = renderPlainLines(
      buildStatusLines({
        providerID: "albert",
        modelDisplayID: "deepseek-v4-flash",
        thinkingLevel: level,
        cwd: "/home/user/demo",
        gitBranch: null,
        contextWindow: 131_072,
        lastContextTokens: 0,
        sessionInputTokens: 0,
        sessionOutputTokens: 0,
        requests: [],
        now: 10_000,
        limitsChecked: true,
      }),
    );
    assert.equal(plain[0], "Albert API · deepseek-v4-flash │ demo");
  }
});

test("Pre-0.9.3 fallback uses public plan table, local 60s window, and marks (grille publique)", () => {
  const now = 120_000;
  const lines = buildStatusLines({
    providerID: "albert",
    modelDisplayID: "gpt-oss-120b",
    apiModelID: "openai/gpt-oss-120b",
    cwd: "/workspace",
    gitBranch: "feat/x",
    contextWindow: 131_072,
    lastContextTokens: 13_107, // 10%
    sessionInputTokens: 50_000,
    sessionOutputTokens: 10_000,
    requests: [
      { t: now - 70_000, tokens: 99_000 }, // expired (>60s)
      { t: now - 20_000, tokens: 20_000 },
      { t: now - 5_000, tokens: 20_000 },
    ],
    now,
    accountLimits: undefined,
    limitsChecked: true,
    dailyUsage: { requests: 42, kgCO2eq: 0.0012, completionTokens: 200_000 },
    config: { projectName: "mon-projet" },
  });

  const plain = renderPlainLines(lines);
  assert.equal(plain.length, 3);
  assert.match(plain[1], /RPM ██░░░░░░░░ 2\/10 │ TPM ███░░░░░░░ 40k\/128k \(grille publique\)$/);
  assert.match(plain[2], /Jour 42 req · 1,2 gCO2e$/);
});

test("Usage failure shows usage Albert indisponible in error tone on line 3", () => {
  const lines = buildStatusLines({
    providerID: "albert",
    modelDisplayID: "deepseek-v4-flash",
    cwd: "/workspace",
    contextWindow: 131_072,
    lastContextTokens: 0,
    sessionInputTokens: 10_000,
    sessionOutputTokens: 2_000,
    requests: [],
    now: 10_000,
    limitsChecked: true,
    usageUnavailable: true,
    config: { projectName: "mon-projet" },
  });

  const plain = renderPlainLines(lines);
  assert.equal(plain[2], "Session 12k tok │ usage Albert indisponible");
  const errSeg = lines[2].find((s) => s.text === "usage Albert indisponible");
  assert.equal(errSeg?.tone, "error");
});

test("Gauge thresholds: normal < 70%, orange (warning) at 70%, red (error) at 90% and limit=0", () => {
  assert.equal(buildGauge(69, 100).severity, "normal");
  assert.equal(buildGauge(70, 100).severity, "warning");
  assert.equal(buildGauge(89, 100).severity, "warning");
  assert.equal(buildGauge(90, 100).severity, "error");
  assert.equal(buildGauge(120, 100).severity, "error");
  assert.equal(buildGauge(0, 0).severity, "error");
  assert.equal(buildGauge(0, 0).barFilled, "██████████");
});

test("Unlimited limits (value: null) render without gauge bar and do not break RPD", () => {
  const now = 50_000;
  const parsed = parseLimitsResponse(
    {
      object: "list",
      data: [
        {
          object: "model_limits",
          router_id: 7,
          model: "deepseek-ai/DeepSeek-V4-Flash-0731",
          limits: [
            { type: "rpm", value: null, remaining: 0 },
            { type: "tpm", value: null, remaining: 0 },
            { type: "rpd", value: null, remaining: 0 },
            { type: "tpd", value: null, remaining: 0 },
          ],
        },
      ],
    },
    ["deepseek-ai/DeepSeek-V4-Flash-0731", "deepseek-v4-flash"],
    now,
  );
  assert.ok(parsed);
  assert.equal(parsed.rpm?.limit, null);
  assert.equal(parsed.tpm?.limit, null);
  assert.equal(parsed.rpd?.limit, null);

  const plain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "deepseek-v4-flash",
      apiModelID: "deepseek-ai/DeepSeek-V4-Flash-0731",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 0,
      sessionOutputTokens: 0,
      requests: [{ t: now - 1_000, tokens: 1_500 }],
      now,
      accountLimits: parsed,
      limitsChecked: true,
      dailyUsage: { requests: 12, kgCO2eq: 0.0025, completionTokens: 50_000 },
      config: { projectName: "mon-projet" },
    }),
  );
  assert.equal(plain[1], "Contexte ░░░░░░░░░░ 0% │ RPM 1/∞ │ TPM 2k/∞");
  assert.equal(plain[2], "Session 0 tok │ Jour 12 req · 2,5 gCO2e");
});

test("Optimistic local requests after fetchedAt move RPM/TPM immediately without double counting", () => {
  const fetchedAt = 100_000;
  const now = 102_000;
  const plain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "gpt-oss-120b",
      apiModelID: "openai/gpt-oss-120b",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 0,
      sessionOutputTokens: 0,
      requests: [
        { t: fetchedAt - 2_000, tokens: 5_000 }, // already included in snapshot
        { t: fetchedAt + 500, tokens: 10_000 }, // sent after snapshot
      ],
      now,
      accountLimits: {
        model: "openai/gpt-oss-120b",
        rpm: { limit: 50, used: 10, remaining: 40 },
        tpm: { limit: 246_000, used: 100_000, remaining: 146_000 },
        rpd: { limit: 500, used: 20, remaining: 480 },
        fetchedAt,
      },
      limitsChecked: true,
      dailyUsage: { requests: 20, kgCO2eq: 0.001, completionTokens: 10_000 },
      config: { projectName: "mon-projet" },
    }),
  );
  assert.match(plain[1], /RPM ██░░░░░░░░ 11\/50 │ TPM ████░░░░░░ 110k\/246k$/);
  assert.match(plain[2], /Jour ░░░░░░░░░░ 21\/500 req/);
});

test("Security: refuses non-Albert hosts and never follows redirects with Authorization", async () => {
  assert.equal(resolveAlbertBaseURL("https://evil.example/v1"), undefined);
  assert.equal(resolveAlbertBaseURL("http://albert.api.etalab.gouv.fr/v1"), undefined);
  assert.equal(resolveAlbertBaseURL("https://albert.api.etalab.gouv.fr:8443/v1"), undefined);
  assert.equal(resolveAlbertBaseURL("https://albert.api.etalab.gouv.fr/v1/"), "https://albert.api.etalab.gouv.fr/v1");

  let sawAuth = false;
  const fakeFetch = async (_url, init) => {
    sawAuth = Boolean(init?.headers?.Authorization);
    assert.equal(init?.redirect, "manual");
    return new Response("", { status: 302, headers: { location: "https://evil.example/steal" } });
  };

  // Non-Albert URL must fail closed before calling fetch at all.
  await assert.rejects(() =>
    fetchAlbertUsage("https://evil.example/v1", "synthetic-secret", 1_700_000_000_000, {
      fetchFn: fakeFetch,
    }),
  );
  assert.equal(sawAuth, false);

  // Redirect on Albert host must fail closed without following.
  await assert.rejects(() =>
    fetchAlbertUsage("https://albert.api.etalab.gouv.fr/v1", "synthetic-secret", 1_700_000_000_000, {
      fetchFn: fakeFetch,
    }),
  );
  assert.equal(sawAuth, true);
});

test("HTTP integration against local mock OpenGateLLM server: 0.9.3 limits, 0.9.2 404 fallback, and usage", async () => {
  const receivedAuth = [];
  let limitsStatus = 200;
  let usageRouteStatus = { meUsage: 200, usage: 200 };

  const server = http.createServer((req, res) => {
    receivedAuth.push({ url: req.url, auth: req.headers.authorization });
    if (req.url === "/v1/limits") {
      if (limitsStatus !== 200) {
        res.writeHead(limitsStatus, { "content-type": "application/json" });
        res.end(JSON.stringify({ detail: "Not Found" }));
        return;
      }
      res.writeHead(200, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          object: "list",
          data: [
            {
              object: "model_limits",
              router_id: 1,
              model: "openai/gpt-oss-120b",
              limits: [
                { type: "rpm", value: 50, remaining: 38 },
                { type: "tpm", value: 246_000, remaining: 46_000 },
                { type: "rpd", value: 500, remaining: 333 },
                { type: "tpd", value: null, remaining: 0 },
              ],
            },
          ],
        }),
      );
      return;
    }
    if (req.url?.startsWith("/v1/me/usage?")) {
      if (usageRouteStatus.meUsage !== 200) {
        res.writeHead(usageRouteStatus.meUsage, { "content-type": "application/json" });
        res.end(JSON.stringify({ detail: "err" }));
        return;
      }
      res.writeHead(200, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          data: [{ requests: 167, completion_tokens: 4_000_000, impacts: { kgCO2eq: 0.0048 } }],
        }),
      );
      return;
    }
    if (req.url?.startsWith("/v1/usage?")) {
      res.writeHead(usageRouteStatus.usage, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          data: [{ requests: 9, completion_tokens: 1_000, impacts: { kgCO2eq: 0.0005 } }],
        }),
      );
      return;
    }
    res.writeHead(404);
    res.end();
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  const base = `http://127.0.0.1:${port}/v1`;

  try {
    // 1. OpenGateLLM 0.9.3 /v1/limits works.
    const limits = await fetchAlbertLimits(base, "synthetic-key-1", ["openai/gpt-oss-120b", "gpt-oss-120b"], 1_700_000_000_000, {
      allowCustomBaseURL: true,
    });
    assert.ok(limits);
    assert.equal(limits.rpm?.limit, 50);
    assert.equal(limits.rpm?.used, 12);
    assert.equal(limits.tpm?.limit, 246_000);
    assert.equal(limits.tpm?.used, 200_000);
    assert.equal(limits.rpd?.limit, 500);
    assert.equal(limits.rpd?.used, 167);
    assert.equal(limits.tpd?.limit, null);

    // 2. /v1/me/usage works.
    const usage = await fetchAlbertUsage(base, "synthetic-key-1", 1_700_000_000_000, {
      allowCustomBaseURL: true,
    });
    assert.equal(usage.requests, 167);
    assert.equal(usage.kgCO2eq, 0.0048);
    assert.equal(usage.completionTokens, 4_000_000);

    // 3. Pre-0.9.3 /v1/limits 404 returns undefined (triggers public table fallback).
    limitsStatus = 404;
    const missing = await fetchAlbertLimits(base, "synthetic-key-1", ["openai/gpt-oss-120b"], 1_700_000_000_000, {
      allowCustomBaseURL: true,
    });
    assert.equal(missing, undefined);

    // 4. Deprecated /v1/me/usage 404 falls back cleanly to /v1/usage.
    usageRouteStatus = { meUsage: 404, usage: 200 };
    const fallbackUsage = await fetchAlbertUsage(base, "synthetic-key-1", 1_700_000_000_000, {
      allowCustomBaseURL: true,
    });
    assert.equal(fallbackUsage.requests, 9);

    // 5. Server 500 on usage throws so UI shows "usage Albert indisponible".
    usageRouteStatus = { meUsage: 500, usage: 500 };
    await assert.rejects(() =>
      fetchAlbertUsage(base, "synthetic-key-1", 1_700_000_000_000, { allowCustomBaseURL: true }),
    );

    assert.ok(receivedAuth.every((r) => r.auth === "Bearer synthetic-key-1"));
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("Helper formatting, folder resolution, provider names, and env overrides", () => {
  assert.equal(shortModelName("openai/gpt-oss-120b"), "gpt-oss-120b");
  assert.equal(shortModelName("mistral-large-latest"), "mistral-large-latest");
  assert.equal(shortModelName(""), "aucun modèle");
  assert.equal(formatTokens(481_000), "481k");
  assert.equal(formatTokens(1_250_000), "1,3M");
  assert.equal(formatGramsCO2(0.0001), "0,1");
  assert.equal(formatGramsCO2(0.0048), "4,8");
  assert.equal(formatGramsCO2(0.0124), "12");
  assert.equal(estimateTokens("12345678"), 2);
  assert.equal(resolveFolderName("/workspace", "mon-projet"), "mon-projet");
  assert.equal(resolveFolderName("/Volumes/My Shared Files/workspace", "mon-projet"), "mon-projet");
  assert.equal(resolveFolderName("/workspace/apps/web", "mon-projet"), "web");
  assert.equal(resolveProviderDisplayName("albert", "Albert API (État)"), "Albert API");
  assert.equal(resolveProviderDisplayName("openai-compatible", undefined, { "openai-compatible": "Scaleway" }), "Scaleway");
  assert.equal(isAlbertProvider("albert", undefined), true);
  assert.equal(isAlbertProvider("custom", "https://albert.api.etalab.gouv.fr/v1"), true);
  assert.equal(isAlbertProvider("mistral", "https://api.mistral.ai/v1"), false);
  assert.deepEqual(
    getPublicAlbertLimits(["openai/gpt-oss-120b"], { ALBERT_RPM: "99", ALBERT_TPM: "300000", ALBERT_RPD: "2500" }),
    { rpm: 99, tpm: 300_000, rpd: 2500 },
  );
  assert.deepEqual(parseUsageResponse({ data: [] }), { requests: 0, kgCO2eq: 0, completionTokens: 0 });
});

test("Local RPM/TPM counts stay scoped to the active model after /model switch", () => {
  const now = 60_000;
  const requests = [
    { t: now - 5_000, tokens: 10_000, model: "deepseek-ai/DeepSeek-V4-Flash-0731" },
    { t: now - 2_000, tokens: 20_000, model: "openai/gpt-oss-120b" },
  ];
  const plain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "gpt-oss-120b",
      apiModelID: "openai/gpt-oss-120b",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 30_000,
      sessionOutputTokens: 0,
      requests,
      now,
      limitsChecked: true,
      config: { projectName: "mon-projet" },
    }),
  );
  assert.match(plain[1], /RPM █░░░░░░░░░ 1\/10 │ TPM ██░░░░░░░░ 20k\/128k \(grille publique\)$/);
});

test("summarizePiBranch reconstructs resumed sessions and resets to zero for empty sessions", () => {
  assert.deepEqual(summarizePiBranch([]), {
    lastContextTokens: 0,
    sessionInputTokens: 0,
    sessionOutputTokens: 0,
  });

  // Resumed branch with reported provider usage (e.g. Mistral).
  const reported = summarizePiBranch([
    { type: "message", message: { role: "user", content: "hello" } },
    {
      type: "message",
      message: {
        role: "assistant",
        content: "world",
        usage: { input: 1_200, output: 300, cacheRead: 100, cacheWrite: 50 },
      },
    },
    { type: "message", message: { role: "user", content: "follow up" } },
    {
      type: "message",
      message: {
        role: "assistant",
        content: "done",
        usage: { input: 2_000, output: 400 },
      },
    },
  ]);
  assert.deepEqual(reported, {
    lastContextTokens: 2_000,
    sessionInputTokens: 3_350,
    sessionOutputTokens: 700,
  });

  // Resumed branch where Albert returns prompt_tokens=0 -> estimates from message content.
  const estimated = summarizePiBranch([
    { type: "message", message: { role: "user", content: "a".repeat(400) } },
    { type: "message", message: { role: "assistant", content: "b".repeat(200), usage: { input: 0, output: 0 } } },
  ]);
  assert.equal(estimated.lastContextTokens, 100);
  assert.equal(estimated.sessionInputTokens, 100);
  assert.equal(estimated.sessionOutputTokens, 50);
});

test("Model aliases (e.g. deepseek-v4-flash) match API model IDs in public and optimistic account counters", () => {
  const fetchedAt = 100_000;
  const now = 102_000;
  const aliasRequests = [{ t: fetchedAt + 500, tokens: 12_000, model: "deepseek-v4-flash" }];

  // Public fallback mode must count alias-tagged sample.
  const publicPlain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "deepseek-v4-flash",
      apiModelID: "deepseek-ai/DeepSeek-V4-Flash-0731",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 12_000,
      sessionOutputTokens: 0,
      requests: aliasRequests,
      now,
      limitsChecked: true,
      config: { projectName: "mon-projet" },
    }),
  );
  assert.match(publicPlain[1], /RPM ░░░░░░░░░░ 1\/50 │ TPM ░░░░░░░░░░ 12k\/246k \(grille publique\)$/);

  // Account-limits mode must include alias-tagged sample sent after fetchedAt.
  const accountPlain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "deepseek-v4-flash",
      apiModelID: "deepseek-ai/DeepSeek-V4-Flash-0731",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 12_000,
      sessionOutputTokens: 0,
      requests: aliasRequests,
      now,
      accountLimits: {
        model: "deepseek-ai/DeepSeek-V4-Flash-0731",
        rpm: { limit: 50, used: 5, remaining: 45 },
        tpm: { limit: 246_000, used: 100_000, remaining: 146_000 },
        rpd: { limit: null, used: null, remaining: null },
        fetchedAt,
      },
      limitsChecked: true,
      dailyUsage: { requests: 5, kgCO2eq: 0.001, completionTokens: 10_000 },
      config: { projectName: "mon-projet" },
    }),
  );
  assert.match(accountPlain[1], /RPM █░░░░░░░░░ 6\/50 │ TPM █████░░░░░ 112k\/246k$/);
});

test("Fallback RPD omits quota gauge for aggregate usage buckets and scopes per-model usage accurately", () => {
  const now = 120_000;
  const baseInput = {
    providerID: "albert",
    modelDisplayID: "gpt-oss-120b",
    apiModelID: "openai/gpt-oss-120b",
    cwd: "/workspace",
    contextWindow: 131_072,
    lastContextTokens: 0,
    sessionInputTokens: 0,
    sessionOutputTokens: 0,
    requests: [],
    now,
    accountLimits: undefined,
    limitsChecked: true,
    config: { projectName: "mon-projet" },
  };

  // Aggregate usage.bucket (OpenGateLLM 0.8+): 1500 requests across models must not
  // render a red 1500/1000 gauge against gpt-oss-120b's per-model public RPD.
  const aggregateUsage = parseUsageResponse({
    object: "list",
    data: [{ object: "usage.bucket", requests: 1500, completion_tokens: 200_000, impacts: { kgCO2eq: 0.0048 } }],
  });
  assert.equal(aggregateUsage.requestsByModel, undefined);
  const aggLines = buildStatusLines({ ...baseInput, dailyUsage: aggregateUsage });
  const aggPlain = renderPlainLines(aggLines);
  assert.match(aggPlain[2], /Jour 1500 req · 4,8 gCO2e$/);
  assert.ok(!aggPlain[2].includes("/1000"));
  assert.ok(!aggLines[2].some((s) => s.tone === "error"));

  // Mixed payload (one entry missing model): also treated as aggregate without gauge.
  const mixedUsage = parseUsageResponse({
    data: [
      { model: "openai/gpt-oss-120b", requests: 10, impacts: { kgCO2eq: 0.0005 } },
      { requests: 1500, impacts: { kgCO2eq: 0.0043 } },
    ],
  });
  assert.equal(mixedUsage.requestsByModel, undefined);
  assert.match(renderPlainLines(buildStatusLines({ ...baseInput, dailyUsage: mixedUsage }))[2], /Jour 1510 req · 4,8 gCO2e$/);

  // Per-model usage with 1500 DeepSeek requests and 0 GPT-OSS requests: GPT-OSS gauge stays at 0/1000.
  const deepseekOnly = parseUsageResponse({
    data: [{ model: "deepseek-ai/DeepSeek-V4-Flash-0731", requests: 1500, completion_tokens: 200_000, impacts: { kgCO2eq: 0.0048 } }],
  });
  const zeroGptLines = buildStatusLines({ ...baseInput, dailyUsage: deepseekOnly });
  assert.match(renderPlainLines(zeroGptLines)[2], /Jour ░░░░░░░░░░ 0\/1000 req · 4,8 gCO2e$/);
  assert.ok(!zeroGptLines[2].some((s) => s.tone === "error"));

  // Per-model usage with both models: GPT-OSS gauge counts only GPT-OSS requests (42/1000).
  const multiModel = parseUsageResponse({
    data: [
      { model: "deepseek-ai/DeepSeek-V4-Flash-0731", requests: 1500, completion_tokens: 150_000, impacts: { kgCO2eq: 0.0038 } },
      { model: "openai/gpt-oss-120b", requests: 42, completion_tokens: 50_000, impacts: { kgCO2eq: 0.001 } },
    ],
  });
  assert.match(
    renderPlainLines(buildStatusLines({ ...baseInput, dailyUsage: multiModel }))[2],
    /Jour ░░░░░░░░░░ 42\/1000 req · 4,8 gCO2e$/,
  );
});

test("Albert RPM/TPM and optimistic counters exclude requests from other providers sharing a model ID", () => {
  const fetchedAt = 100_000;
  const now = 102_000;
  const mixedRequests = [
    // Same model ID/short name on non-Albert providers: must be excluded.
    { t: fetchedAt + 200, tokens: 20_000, model: "openai/gpt-oss-120b", provider: "openai-compatible", baseURL: "https://api.scaleway.ai/v1" },
    { t: fetchedAt + 300, tokens: 15_000, model: "gpt-oss-120b", provider: "mistral", baseURL: "https://api.mistral.ai/v1" },
    // Albert requests (by provider ID or Albert baseURL): must be counted.
    { t: fetchedAt + 400, tokens: 10_000, model: "openai/gpt-oss-120b", provider: "albert" },
    { t: fetchedAt + 500, tokens: 5_000, model: "gpt-oss-120b", provider: "openai-compatible", baseURL: "https://albert.api.etalab.gouv.fr/v1" },
  ];

  // Public fallback mode: only the 2 Albert requests (15k tok) count.
  const publicPlain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "gpt-oss-120b",
      apiModelID: "openai/gpt-oss-120b",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 0,
      sessionOutputTokens: 0,
      requests: mixedRequests,
      now,
      limitsChecked: true,
      config: { projectName: "mon-projet" },
    }),
  );
  assert.match(publicPlain[1], /RPM ██░░░░░░░░ 2\/10 │ TPM █░░░░░░░░░ 15k\/128k \(grille publique\)$/);

  // Account-limits mode: optimistic delta adds only the 2 Albert requests (15k tok).
  const accountPlain = renderPlainLines(
    buildStatusLines({
      providerID: "albert",
      modelDisplayID: "gpt-oss-120b",
      apiModelID: "openai/gpt-oss-120b",
      cwd: "/workspace",
      contextWindow: 131_072,
      lastContextTokens: 0,
      sessionInputTokens: 0,
      sessionOutputTokens: 0,
      requests: mixedRequests,
      now,
      accountLimits: {
        model: "openai/gpt-oss-120b",
        rpm: { limit: 50, used: 10, remaining: 40 },
        tpm: { limit: 246_000, used: 100_000, remaining: 146_000 },
        rpd: { limit: 500, used: 20, remaining: 480 },
        fetchedAt,
      },
      limitsChecked: true,
      dailyUsage: { requests: 20, kgCO2eq: 0.001, completionTokens: 10_000 },
      config: { projectName: "mon-projet" },
    }),
  );
  assert.match(accountPlain[1], /RPM ██░░░░░░░░ 12\/50 │ TPM █████░░░░░ 115k\/246k$/);
  assert.match(accountPlain[2], /Jour ░░░░░░░░░░ 22\/500 req · 1,0 gCO2e$/);
});
