import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { expect, request, test, type APIRequestContext, type APIResponse, type Page } from "@playwright/test";

const apiBaseURL = process.env.E2E_API_BASE_URL?.trim() || "http://127.0.0.1:8080";
const workspaceOrgId = process.env.E2E_WORKSPACE_ORG_ID?.trim() || "local-org";
const mockHTTPPort = parseMockHTTPPort(process.env.E2E_HTTP_MOCK_PORT);
const mockHTTPListenHost = process.env.E2E_HTTP_MOCK_LISTEN_HOST?.trim() || "127.0.0.1";
const mockHTTPConnectHost = process.env.E2E_HTTP_MOCK_CONNECT_HOST?.trim() || "127.0.0.1";
const secretName = "e2e_http_api_key";
const secretValueRef = "AGT_DEMO_HTTP_API_KEY";
const connectorName = "e2e_http";

test("本地真实演示：真实前后端、HTTP 上游、审批、审计与 Secret 删除保护", async ({ page }) => {
  const mockHTTP = await startMockHTTPServer();
  const api = await request.newContext({
    baseURL: apiBaseURL,
    extraHTTPHeaders: {
      "X-Workspace-Org-Id": workspaceOrgId,
    },
  });

  try {
    await ensureDemoFixtures(api);
    await assertCorePagesReachable(page);

    await runMockEchoAndAssertAudit(page, api);
    await assertPolicySimulator(api);
    await runHTTPGetAndAssertAudit(api, mockHTTP);
    await runHTTPWriteSelfReviewBlockedFlow(api, mockHTTP);
    await runHTTPWriteRejectSelfReviewBlockedFlow(api, mockHTTP);
    await assertSecretUsageDeleteGuardAndFailClosed(api, mockHTTP);
  } finally {
    await api.dispose();
    await mockHTTP.close();
  }
});

async function assertCorePagesReachable(page: Page) {
  await page.addInitScript(() => {
    window.localStorage.setItem("agt.locale", "zh-CN");
  });

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Agent 工具治理总览|Agent tool governance at a glance/ })).toBeVisible({
    timeout: 30_000,
  });

  const pages: Array<{ path: string; heading: RegExp }> = [
    { path: "/tools", heading: /管理当前工作区工具/ },
    { path: "/policies", heading: /策略管理与模拟|Policy management and simulation/ },
    { path: "/secrets", heading: /Secret 管理/ },
    { path: "/connectors", heading: /管理 Connector 引用/ },
    { path: "/approvals", heading: /Review pending tool calls|待审批工具调用/ },
    { path: "/audit", heading: /已记录的工具调用|Recorded tool calls/ },
  ];

  for (const item of pages) {
    await page.goto(item.path);
    await expect(page.getByRole("heading", { name: item.heading })).toBeVisible({ timeout: 30_000 });
  }

  await page.goto("/secrets");
  const secretRow = page.getByRole("row").filter({ hasText: secretName });
  await expect(secretRow).toBeVisible({ timeout: 30_000 });
  await expect(secretRow).toContainText("headerSecretRefs.X-Api-Key");
}

async function runMockEchoAndAssertAudit(page: Page, api: APIRequestContext) {
  const response = await api.post("/api/tool-calls", {
    data: {
      tool: "mock.echo",
      arguments: {
        message: "hello from local real demo e2e",
      },
    },
  });
  await expectAPIResponseOK(response);
  const body = (await response.json()) as { status?: string; callId?: string };
  expect(body.status).toBe("success");
  expect(body.callId).toBeTruthy();

  await page.goto(`/audit?tool=${encodeURIComponent("mock.echo")}&status=success&pageSize=10`);
  await expect(page.getByRole("heading", { name: /已记录的工具调用|Recorded tool calls/ })).toBeVisible({ timeout: 30_000 });
  const audit = await listToolCalls(api, { tool: "mock.echo", status: "success", pageSize: 10 });
  expect(audit.items.some((call) => call.id === body.callId && call.toolKey === "mock.echo" && call.status === "success")).toBeTruthy();
}

async function assertPolicySimulator(api: APIRequestContext) {
  const response = await api.post("/api/policies/simulate", {
    data: {
      connectorType: "http",
      toolName: "http.request",
      operationType: "post",
      riskLevel: "medium",
      resource: `127.0.0.1:${mockHTTPPort}`,
    },
  });
  await expectAPIResponseOK(response);
  const body = (await response.json()) as {
    decision?: string;
    explanation?: string;
    evaluationTrace?: unknown[];
  };
  expect(body.decision).toBeTruthy();
  expect(body.explanation).toBeTruthy();
  expect(Array.isArray(body.evaluationTrace)).toBeTruthy();
}

async function runHTTPGetAndAssertAudit(api: APIRequestContext, mockHTTP: MockHTTPServer) {
  const before = mockHTTP.count("GET", "/status");
  const response = await api.post("/api/tool-calls", {
    data: {
      tool: "http.request",
      arguments: {
        method: "GET",
        url: `${mockHTTP.url}/status`,
        headers: {
          "X-Demo": "hello",
        },
        headerSecretRefs: {
          "X-Api-Key": secretName,
        },
      },
    },
  });
  await expectAPIResponseOK(response);
  const body = (await response.json()) as { status?: string };
  expect(body.status).toBe("success");
  expect(mockHTTP.count("GET", "/status")).toBe(before + 1);

  const calls = await listToolCalls(api, { tool: "http.request", status: "success", pageSize: 5 });
  const successCall = calls.items.find((call) => call.toolKey === "http.request" && call.status === "success");
  expect(successCall).toBeTruthy();
  expect(successCall?.traceId).toBeTruthy();
  expect(successCall?.policyDecision).toBeTruthy();
  const outputAudit = JSON.stringify(successCall?.outputRedactedJson ?? {});
  expect(outputAudit).not.toContain("mock-token-should-be-redacted");
  expect(outputAudit).not.toContain("mock-secret-should-be-redacted");
  expect(outputAudit).not.toContain("mock-response-header-should-be-redacted");
}

async function runHTTPWriteSelfReviewBlockedFlow(api: APIRequestContext, mockHTTP: MockHTTPServer) {
  const before = mockHTTP.count("POST", "/items");
  const response = await api.post("/api/tool-calls", {
    data: {
      tool: "http.request",
      arguments: {
        method: "POST",
        url: `${mockHTTP.url}/items`,
        headers: {
          "X-Demo": "write-approval",
        },
        headerSecretRefs: {
          "X-Api-Key": secretName,
        },
        body: {
          message: "审批通过后才会触达上游",
        },
      },
    },
  });
  await expectAPIResponseOK(response);
  const body = (await response.json()) as { status?: string; approvalId?: string };
  expect(body.status).toBe("approval_required");
  expect(body.approvalId).toBeTruthy();
  expect(mockHTTP.count("POST", "/items")).toBe(before);

  const approveResponse = await api.post(`/api/approvals/${body.approvalId}/approve`, {
    data: {
      reason: "local demo self-review should be blocked",
    },
  });
  expect(approveResponse.status()).toBe(403);
  expect(mockHTTP.count("POST", "/items")).toBe(before);
}

async function runHTTPWriteRejectSelfReviewBlockedFlow(api: APIRequestContext, mockHTTP: MockHTTPServer) {
  const before = mockHTTP.count("POST", "/items");
  const response = await api.post("/api/tool-calls", {
    data: {
      tool: "http.request",
      arguments: {
        method: "POST",
        url: `${mockHTTP.url}/items`,
        headerSecretRefs: {
          "X-Api-Key": secretName,
        },
        body: {
          message: "拒绝后不能触达上游",
        },
      },
    },
  });
  await expectAPIResponseOK(response);
  const body = (await response.json()) as { status?: string; approvalId?: string };
  expect(body.status).toBe("approval_required");
  expect(body.approvalId).toBeTruthy();
  expect(mockHTTP.count("POST", "/items")).toBe(before);

  const rejectResponse = await api.post(`/api/approvals/${body.approvalId}/reject`, {
    data: {
      reason: "local demo self-review should be blocked",
    },
  });
  expect(rejectResponse.status()).toBe(403);
  expect(mockHTTP.count("POST", "/items")).toBe(before);
}

async function assertSecretUsageDeleteGuardAndFailClosed(api: APIRequestContext, mockHTTP: MockHTTPServer) {
  const secret = await findSecretByName(api, secretName);
  expect(secret?.id).toBeTruthy();

  const blockedDelete = await api.delete(`/api/secrets/${secret!.id}`);
  expect(blockedDelete.status()).toBe(409);
  const usage = (await blockedDelete.json()) as {
    canDelete?: boolean;
    usages?: Array<{ connectorName?: string; field?: string; target?: string }>;
  };
  expect(usage.canDelete).toBe(false);
  expect(usage.usages ?? []).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        connectorName,
        field: "headerSecretRefs.X-Api-Key",
        target: "http.e2e_http",
      }),
    ]),
  );

  const forceDelete = await api.delete(`/api/secrets/${secret!.id}?force=true`);
  await expectAPIResponseOK(forceDelete);

  const before = mockHTTP.count("GET", "/status");
  const failedCall = await api.post("/api/tool-calls", {
    data: {
      tool: "http.request",
      arguments: {
        method: "GET",
        url: `${mockHTTP.url}/status`,
        headerSecretRefs: {
          "X-Api-Key": secretName,
        },
      },
    },
  });
  expect(failedCall.status()).toBe(400);
  const failureText = await failedCall.text();
  const demoSecret = process.env.AGT_DEMO_HTTP_API_KEY?.trim();
  if (demoSecret) {
    expect(failureText).not.toContain(demoSecret);
  }
  expect(mockHTTP.count("GET", "/status")).toBe(before);

  const failedAudit = await listToolCalls(api, { tool: "http.request", status: "failed", pageSize: 10 });
  expect(failedAudit.items.some((call) => call.errorMessage.includes(`secret ref ${secretName} was not found`))).toBeTruthy();
}

async function ensureDemoFixtures(api: APIRequestContext) {
  await ensureSecret(api);
  await ensureHTTPConnector(api);
}

async function ensureSecret(api: APIRequestContext) {
  const existing = await findSecretByName(api, secretName);
  const payload = {
    name: secretName,
    description: "E2E 本地真实演示 HTTP headerSecretRefs 使用的 env-backed Secret",
    enabled: true,
    secretType: "api_key",
    valueSource: "env",
    valueRef: secretValueRef,
    metadata: {
      scope: "local-real-demo-e2e",
    },
  };
  if (existing) {
    const response = await api.put(`/api/secrets/${existing.id}`, { data: payload });
    await expectAPIResponseOK(response);
    return;
  }
  const response = await api.post("/api/secrets", { data: payload });
  await expectAPIResponseOK(response);
}

async function ensureHTTPConnector(api: APIRequestContext) {
  const existing = await findConnector(api, "http", connectorName);
  const configJson = {
    mode: "local-real-demo-e2e",
    allowedHosts: uniqueStrings([`127.0.0.1:${mockHTTPPort}`, `localhost:${mockHTTPPort}`, `${mockHTTPConnectHost}:${mockHTTPPort}`]),
    allowedMethods: ["GET", "POST"],
    headerSecretRefs: {
      "X-Api-Key": secretName,
    },
  };
  if (existing) {
    const response = await api.patch(`/api/connectors/${existing.id}`, {
      data: {
        displayName: "E2E HTTP",
        configJson,
        enabled: true,
      },
    });
    await expectAPIResponseOK(response);
    return;
  }
  const response = await api.post("/api/connectors", {
    data: {
      type: "http",
      name: connectorName,
      displayName: "E2E HTTP",
      configJson,
      enabled: true,
    },
  });
  await expectAPIResponseOK(response);
}

async function findSecretByName(api: APIRequestContext, name: string): Promise<{ id: string; name: string } | null> {
  const response = await api.get("/api/secrets");
  await expectAPIResponseOK(response);
  const body = (await response.json()) as { items?: Array<{ id: string; name: string }> };
  return body.items?.find((item) => item.name === name) ?? null;
}

async function findConnector(
  api: APIRequestContext,
  type: string,
  name: string,
): Promise<{ id: string; type: string; name: string } | null> {
  const response = await api.get("/api/connectors");
  await expectAPIResponseOK(response);
  const body = (await response.json()) as { items?: Array<{ id: string; type: string; name: string }> };
  return body.items?.find((item) => item.type === type && item.name === name) ?? null;
}

async function listToolCalls(
  api: APIRequestContext,
  query: { tool: string; status: string; pageSize: number },
): Promise<{ items: DemoToolCallAuditItem[] }> {
  const params = new URLSearchParams({
    tool: query.tool,
    status: query.status,
    pageSize: String(query.pageSize),
  });
  const response = await api.get(`/api/tool-calls?${params.toString()}`);
  await expectAPIResponseOK(response);
  const body = (await response.json()) as {
    items?: Array<{
      toolKey: string;
      id?: string;
      status: string;
      errorMessage: string;
      policyDecision?: string;
      traceId?: string;
      outputRedactedJson?: unknown;
    }>;
  };
  return { items: body.items ?? [] };
}

async function startMockHTTPServer(): Promise<MockHTTPServer> {
  const requests: MockHTTPRequest[] = [];
  const server = createServer(async (req, res) => {
    const url = new URL(req.url ?? "/", `http://127.0.0.1:${mockHTTPPort}`);
    requests.push({ method: req.method ?? "", path: url.pathname });

    if (req.method === "GET" && url.pathname === "/status") {
      writeJSON(res, 200, {
        ok: true,
        token: "mock-token-should-be-redacted",
        nested: {
          secret: "mock-secret-should-be-redacted",
        },
      });
      return;
    }

    if (req.method === "POST" && url.pathname === "/items") {
      await drainBody(req);
      writeJSON(res, 201, {
        ok: true,
        created: true,
        password: "mock-password-should-be-redacted",
      });
      return;
    }

    res.statusCode = 404;
    res.end("not found");
  });

  await new Promise<void>((resolve, reject) => {
    const onError = (error: Error) => {
      server.off("listening", onListening);
      reject(error);
    };
    const onListening = () => {
      server.off("error", onError);
      resolve();
    };
    server.once("error", onError);
    server.once("listening", onListening);
    server.listen(mockHTTPPort, mockHTTPListenHost);
  });

  return {
    url: `http://${mockHTTPConnectHost}:${mockHTTPPort}`,
    count(method: string, path: string) {
      return requests.filter((item) => item.method === method && item.path === path).length;
    },
    async close() {
      await closeServer(server);
    },
  };
}

function parseMockHTTPPort(raw: string | undefined): number {
  if (!raw?.trim()) {
    return 18080;
  }
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isInteger(parsed) || parsed <= 0 || parsed > 65535) {
    throw new Error("E2E_HTTP_MOCK_PORT must be a valid TCP port");
  }
  return parsed;
}

function uniqueStrings(values: string[]): string[] {
  return Array.from(new Set(values));
}

function writeJSON(res: ServerResponse, statusCode: number, payload: unknown) {
  res.statusCode = statusCode;
  res.setHeader("Content-Type", "application/json");
  res.setHeader("X-Secret-Token", "mock-response-header-should-be-redacted");
  res.end(JSON.stringify(payload));
}

async function expectAPIResponseOK(response: APIResponse) {
  if (!response.ok()) {
    throw new Error(`API request failed with ${response.status()}: ${redactDemoSecret(await response.text())}`);
  }
}

function redactDemoSecret(text: string): string {
  const demoSecret = process.env.AGT_DEMO_HTTP_API_KEY?.trim();
  if (!demoSecret) {
    return text;
  }
  return text.split(demoSecret).join("[REDACTED]");
}

async function drainBody(req: IncomingMessage): Promise<void> {
  for await (const chunk of req) {
    void chunk;
    // 只读取 body 以完成请求；不要把 payload 写入测试日志。
  }
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolve) => {
    server.close(() => resolve());
  });
}

type MockHTTPRequest = {
  method: string;
  path: string;
};

type DemoToolCallAuditItem = {
  id?: string;
  toolKey: string;
  status: string;
  errorMessage: string;
  policyDecision?: string;
  traceId?: string;
  outputRedactedJson?: unknown;
};

type MockHTTPServer = {
  url: string;
  count: (method: string, path: string) => number;
  close: () => Promise<void>;
};
