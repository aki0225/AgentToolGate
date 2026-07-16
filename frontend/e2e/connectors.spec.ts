import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { expect, request, test, type Page } from "@playwright/test";

test("连接器：创建 MCP connector 并同步到工具列表", async ({ page }) => {
  const mockServer = await startMockMcpServer();
  const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
  const connectorName = `weather_${suffix}`;
  const displayName = `Weather MCP ${suffix}`;
  const toolNamespace = `mcp_${connectorName}`;

  try {
    await page.addInitScript(() => {
      window.localStorage.setItem("agt.locale", "zh-CN");
    });
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Agent 工具治理总览" })).toBeVisible({ timeout: 30_000 });
    const workspaceOrgId = await readWorkspaceOrgId(page);

    const api = await request.newContext({
      baseURL: "http://127.0.0.1:8080",
      extraHTTPHeaders: {
        "X-Workspace-Org-Id": workspaceOrgId,
      },
    });

    try {
      const createResponse = await api.post("/api/connectors", {
        data: {
          type: "mcp",
          name: connectorName,
          displayName,
          configJson: {
            transport: "sse",
            url: `${mockServer.url}/mcp/sse`,
          },
          enabled: true,
        },
      });
      expect(createResponse.ok()).toBeTruthy();
      const createdConnector = (await createResponse.json()) as { id: string };

      await page.goto("/connectors");
      await expect(page.getByRole("heading", { name: "管理 Connector 引用" })).toBeVisible({ timeout: 30_000 });
      const row = page.getByRole("row").filter({ hasText: displayName });
      await expect(row).toBeVisible({ timeout: 30_000 });

      const syncResponsePromise = page.waitForResponse((response) => {
        return response.url().includes(`/api/connectors/${createdConnector.id}/sync`) && response.request().method() === "POST";
      });
      await row.getByRole("button", { name: "同步" }).click();

      const syncResponse = await syncResponsePromise;
      const syncBodyText = await syncResponse.text();
      expect(syncResponse.ok(), syncBodyText).toBeTruthy();
      const syncBody = JSON.parse(syncBodyText) as { createdTools?: string[]; updatedTools?: string[]; staleTools?: string[] };
      expect(syncBody.createdTools).toEqual(
        expect.arrayContaining([`${toolNamespace}.get_forecast`, `${toolNamespace}.create_note`]),
      );
      expect(syncBody.updatedTools ?? []).toHaveLength(0);
      expect(syncBody.staleTools ?? []).toHaveLength(0);

      expect(mockServer.methods).toEqual(["initialize", "tools/list"]);

      const toolsResponse = await api.get("/api/tools");
      expect(toolsResponse.ok()).toBeTruthy();
      const toolsBody = (await toolsResponse.json()) as { items?: Array<{ namespace?: string; name?: string }> };
      const readTool = toolsBody.items?.find((item) => item.namespace === toolNamespace && item.name === "get_forecast");
      expect(readTool).toBeTruthy();
      expect(toolsBody.items?.some((item) => item.namespace === toolNamespace && item.name === "create_note")).toBeTruthy();

      await page.goto("/tools");
      const toolRow = page.getByRole("row").filter({ hasText: `${toolNamespace}.get_forecast` });
      await expect(toolRow).toBeVisible({ timeout: 30_000 });
      await toolRow.getByRole("link", { name: "打开" }).click();
      await expect(page.getByRole("heading", { name: "Get Forecast" })).toBeVisible({ timeout: 30_000 });
      await page.getByLabel("MCP 参数 JSON").fill(`{"city":"Shanghai"}`);
      await page.getByRole("button", { name: "执行" }).click();
      await expect(page.getByText("success").first()).toBeVisible({ timeout: 30_000 });
      expect(mockServer.methods).toEqual(["initialize", "tools/list", "initialize", "tools/call"]);

      const writeCallsBeforeApproval = countToolCalls(mockServer.methods);
      const writeResponse = await api.post("/api/tool-calls", {
        data: {
          tool: `${toolNamespace}.create_note`,
          arguments: {
            title: "E2E write approval",
            body: "body should stay governed",
          },
        },
      });
      expect(writeResponse.ok()).toBeTruthy();
      const writeBody = (await writeResponse.json()) as { status?: string; approvalId?: string };
      expect(writeBody.status).toBe("approval_required");
      expect(writeBody.approvalId).toBeTruthy();
      expect(countToolCalls(mockServer.methods)).toBe(writeCallsBeforeApproval);

      const approveResponse = await api.post(`/api/approvals/${writeBody.approvalId}/approve`, {
        data: {
          reason: "local self-review should be blocked",
        },
      });
      expect(approveResponse.status()).toBe(403);
      expect(countToolCalls(mockServer.methods)).toBe(writeCallsBeforeApproval);

      const rejectCandidateResponse = await api.post("/api/tool-calls", {
        data: {
          tool: `${toolNamespace}.create_note`,
          arguments: {
            title: "E2E write reject",
          },
        },
      });
      expect(rejectCandidateResponse.ok()).toBeTruthy();
      const rejectCandidate = (await rejectCandidateResponse.json()) as { status?: string; approvalId?: string };
      expect(rejectCandidate.status).toBe("approval_required");
      expect(rejectCandidate.approvalId).toBeTruthy();
      expect(countToolCalls(mockServer.methods)).toBe(writeCallsBeforeApproval);

      const rejectResponse = await api.post(`/api/approvals/${rejectCandidate.approvalId}/reject`, {
        data: {
          reason: "local self-review should be blocked",
        },
      });
      expect(rejectResponse.status()).toBe(403);
      expect(countToolCalls(mockServer.methods)).toBe(writeCallsBeforeApproval);
    } finally {
      await api.dispose();
    }
  } finally {
    await mockServer.close();
  }
});

async function startMockMcpServer(): Promise<MockMcpServer> {
  const sessions = new Map<string, ServerResponse>();
  const methods: string[] = [];

  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://127.0.0.1");
    if (req.method === "GET" && url.pathname === "/mcp/sse") {
      const sessionId = `session-${randomUUID()}`;
      sessions.set(sessionId, res);

      res.setHeader("Content-Type", "text/event-stream; charset=utf-8");
      res.setHeader("Cache-Control", "no-cache");
      res.setHeader("Connection", "keep-alive");
      res.flushHeaders();
      res.write(`event: endpoint\ndata: /mcp/sse?sessionId=${sessionId}\n\n`);

      req.on("close", () => {
        sessions.delete(sessionId);
      });
      return;
    }

    if (req.method === "POST" && url.pathname === "/mcp/sse") {
      const sessionId = String(url.searchParams.get("sessionId") ?? "").trim();
      if (!sessionId) {
        res.statusCode = 404;
        res.end("missing session");
        return;
      }

      const session = sessions.get(sessionId);
      if (!session) {
        res.statusCode = 404;
        res.end("session not found");
        return;
      }

      readJsonBody(req)
        .then((payload) => {
          methods.push(String(payload.method ?? ""));
          const response = buildMcpResponse(payload);
          session.write(`event: message\ndata: ${JSON.stringify(response)}\n\n`);
          res.statusCode = 202;
          res.end();
        })
        .catch((error: unknown) => {
          res.statusCode = 400;
          res.end(error instanceof Error ? error.message : "invalid json");
        });
      return;
    }

    res.statusCode = 404;
    res.end("not found");
  });

  const listenHost = process.env.E2E_MCP_MOCK_LISTEN_HOST?.trim() || "127.0.0.1";
  const connectHost = process.env.E2E_MCP_MOCK_CONNECT_HOST?.trim() || listenHost;

  await new Promise<void>((resolve) => {
    server.listen(0, listenHost, resolve);
  });

  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new Error("未能启动 mock MCP server");
  }

  return {
    url: `http://${connectHost}:${address.port}`,
    methods,
    async close() {
      await new Promise<void>((resolve) => {
        for (const session of sessions.values()) {
          session.destroy();
        }
        sessions.clear();
        server.close(() => resolve());
      });
    },
  };
}

function buildMcpResponse(payload: { method?: string; id?: unknown }) {
  switch (payload.method) {
    case "initialize":
      return {
        jsonrpc: "2.0",
        id: payload.id ?? "initialize",
        result: {
          protocolVersion: "2024-11-05",
          serverInfo: {
            name: "mock-mcp",
            version: "1.0.0",
          },
        },
      };
    case "tools/list":
      return {
        jsonrpc: "2.0",
        id: payload.id ?? "tools/list",
        result: {
          tools: [
            {
              name: "get_forecast",
              title: "Get Forecast",
              description: "Returns a demo weather forecast.",
              inputSchema: {
                type: "object",
                properties: {
                  city: {
                    type: "string",
                  },
                },
              },
            },
            {
              name: "create_note",
              title: "Create Note",
              description: "Creates a demo note.",
              inputSchema: {
                type: "object",
                properties: {
                  title: {
                    type: "string",
                  },
                },
              },
            },
          ],
        },
      };
    case "tools/call":
      return {
        jsonrpc: "2.0",
        id: payload.id ?? "tools/call",
        result: {
          content: [
            {
              type: "text",
              text: "forecast ok",
            },
          ],
          isError: false,
        },
      };
    default:
      return {
        jsonrpc: "2.0",
        id: payload.id ?? "unknown",
        error: {
          code: -32601,
          message: `unsupported method: ${String(payload.method ?? "")}`,
        },
      };
  }
}

async function readJsonBody(req: IncomingMessage): Promise<{ method?: string; id?: unknown }> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }
  const raw = Buffer.concat(chunks).toString("utf-8");
  if (!raw.trim()) {
    throw new Error("请求体不能为空");
  }
  return JSON.parse(raw) as { method?: string; id?: unknown };
}

async function readWorkspaceOrgId(page: Page): Promise<string> {
  const stored = await page.evaluate(() => window.sessionStorage.getItem("agt.selectedWorkspaceOrgId"));
  if (stored) {
    return stored;
  }

  const text = (await page.locator("aside").textContent()) ?? "";
  const match = text.match(/(?:local-org|org-[A-Za-z0-9-]+)/);
  if (!match) {
    throw new Error(`未能从侧栏解析 workspace org id：${text}`);
  }
  return match[0];
}

type MockMcpServer = {
  url: string;
  methods: string[];
  close: () => Promise<void>;
};

function countToolCalls(methods: string[]): number {
  return methods.filter((method) => method === "tools/call").length;
}
