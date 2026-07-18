import { expect, test, type Page, type Route } from "@playwright/test";
import type { User as OidcUser } from "oidc-client-ts";
import { ApiError, getApiErrorMessage, listTools } from "../src/api/client";
import { bootstrapAuthSession } from "../src/auth/bootstrap";
import type { DashboardSummary, MeResponse, Tool, ToolCall, User, Workspace } from "../src/types";

const timestamp = "2026-07-18T00:00:00Z";

test("认证 bootstrap：OIDC 刷新后恢复已存储用户和 workspace session", async () => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const oidcUser = {
    expired: false,
    id_token: "stored-id-token",
  } as unknown as OidcUser;
  let receivedToken = "";

  const result = await bootstrapAuthSession({
    authMode: "oidc",
    selectedWorkspaceOrgId: workspace.zitadelOrganizationId,
    userManager: {
      getUser: async () => oidcUser,
      removeUser: async () => {},
    },
    listWorkspaces: async () => ({ items: [workspace] }),
    loadMe: async (token) => {
      receivedToken = token ?? "";
      return createMe(workspace);
    },
  });

  expect(result.oidcUser).toBe(oidcUser);
  expect(result.me?.workspace.id).toBe(workspace.id);
  expect(result.selectedWorkspaceOrgId).toBe(workspace.zitadelOrganizationId);
  expect(receivedToken).toBe("stored-id-token");
  expect(result.sessionError).toBeNull();
});

test("认证 bootstrap：OIDC 会话被拒绝时清理失效身份并保留 workspace 列表", async () => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const oidcUser = {
    expired: false,
    id_token: "rejected-id-token",
  } as unknown as OidcUser;
  let removeUserCount = 0;

  const result = await bootstrapAuthSession({
    authMode: "oidc",
    selectedWorkspaceOrgId: workspace.zitadelOrganizationId,
    userManager: {
      getUser: async () => oidcUser,
      removeUser: async () => {
        removeUserCount += 1;
      },
    },
    listWorkspaces: async () => ({ items: [workspace] }),
    loadMe: async () => {
      throw new ApiError(401, "expired session");
    },
  });

  expect(result.workspaces).toEqual([workspace]);
  expect(result.selectedWorkspaceOrgId).toBeNull();
  expect(result.me).toBeNull();
  expect(result.oidcUser).toBeNull();
  expect(result.sessionError).toMatchObject({ status: 401 });
  expect(removeUserCount).toBe(1);
});

test("认证 bootstrap：OIDC 临时失败时保留身份和 workspace 以便重试", async () => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const oidcUser = {
    expired: false,
    id_token: "stored-id-token",
  } as unknown as OidcUser;
  let removeUserCount = 0;

  const result = await bootstrapAuthSession({
    authMode: "oidc",
    selectedWorkspaceOrgId: workspace.zitadelOrganizationId,
    userManager: {
      getUser: async () => oidcUser,
      removeUser: async () => {
        removeUserCount += 1;
      },
    },
    listWorkspaces: async () => ({ items: [workspace] }),
    loadMe: async () => {
      throw new ApiError(503, "temporarily unavailable");
    },
  });

  expect(result.workspaces).toEqual([workspace]);
  expect(result.selectedWorkspaceOrgId).toBe(workspace.zitadelOrganizationId);
  expect(result.me).toBeNull();
  expect(result.oidcUser).toBe(oidcUser);
  expect(result.sessionError).toMatchObject({ status: 503 });
  expect(removeUserCount).toBe(0);
});

test("API client：非 JSON 错误不泄漏 HTML，403 可按 locale 映射", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () =>
    new Response("<html><body>proxy internal details</body></html>", {
      status: 502,
      statusText: "Bad Gateway",
      headers: { "Content-Type": "text/html" },
    });

  try {
    await expect(listTools()).rejects.toMatchObject({
      name: "ApiError",
      status: 502,
      message: "Bad Gateway",
    });
    expect(getApiErrorMessage(new ApiError(403, "forbidden"), "fallback", "Permission denied")).toBe(
      "Permission denied",
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("本地多 workspace：未选择时进入登录页，选择后再建立 session", async ({ page }) => {
  const workspaceA = createWorkspace("workspace-a", "Workspace A", "org-a");
  const workspaceB = createWorkspace("workspace-b", "Workspace B", "org-b");
  const mocks = await installFrontendMocks(page, {
    workspaces: [workspaceA, workspaceB],
  });

  await page.goto("/", { waitUntil: "domcontentloaded" });

  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("button", { name: /Workspace A/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Workspace B/ })).toBeVisible();
  expect(mocks.meWorkspaceOrgIds).toEqual([]);

  await page.getByRole("button", { name: /Workspace B/ }).click();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Agent 工具治理总览" })).toBeVisible();
  expect(mocks.meWorkspaceOrgIds).toContain("org-b");
});

test("workspace 加载失败：显示本地化错误并可重试恢复", async ({ page }) => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const mocks = await installFrontendMocks(page, {
    workspaces: [workspace],
    workspaceFailureUntilReload: true,
  });

  await page.goto("/", { waitUntil: "domcontentloaded" });

  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByText("无法加载工作区或会话状态。")).toBeVisible();
  await expect(page.getByText(/workspace lookup failed/)).toHaveCount(0);
  const failedRequestCount = mocks.workspaceRequestCount();

  await page.getByRole("button", { name: "重试" }).click();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Agent 工具治理总览" })).toBeVisible();
  expect(mocks.workspaceRequestCount()).toBeGreaterThan(failedRequestCount);
});

test("Dashboard：后端返回 HTML 错误时展示可重试错误态", async ({ page }) => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  await installFrontendMocks(page, {
    workspaces: [workspace],
    dashboardFailure: true,
  });

  await page.goto("/", { waitUntil: "domcontentloaded" });

  await expect(page.getByText("Dashboard 暂时不可用")).toBeVisible();
  await expect(page.getByRole("button", { name: "重试" })).toBeVisible();
  await expect(page.getByText(/proxy internal details/)).toHaveCount(0);
});

test("工具详情：加载失败显示错误和重试，不会永久 loading", async ({ page }) => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  await installFrontendMocks(page, {
    workspaces: [workspace],
    toolLoadFailure: true,
  });

  await page.goto("/tools/missing-tool", { waitUntil: "domcontentloaded" });

  await expect(page.getByText("工具详情暂时不可用")).toBeVisible();
  await expect(page.getByRole("button", { name: "重试" })).toBeVisible();
  await expect(page.getByText("正在加载工具...")).toHaveCount(0);
});

test("工具详情：英文 locale 下 database schema 403 显示本地化权限文案", async ({ page }) => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const tool = createDatabaseTool(workspace);
  await installFrontendMocks(page, {
    workspaces: [workspace],
    tool,
    databaseSchemaForbidden: true,
    locale: "en-US",
  });

  await page.goto(`/tools/${tool.id}`, { waitUntil: "domcontentloaded" });
  await page.getByRole("tab", { name: "Schema" }).click();

  await expect(page.getByText("Current role is not allowed to perform this action")).toBeVisible();
  await expect(page.getByText("forbidden", { exact: true })).toHaveCount(0);
});

test("工具执行：连续点击只发送一个 tool call", async ({ page }) => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const tool = createTool(workspace);
  const mocks = await installFrontendMocks(page, {
    workspaces: [workspace],
    tool,
    toolCallDelayMs: 250,
  });

  await page.goto(`/tools/${tool.id}`, { waitUntil: "domcontentloaded" });
  const executeButton = page.getByRole("button", { name: "执行", exact: true });
  await expect(executeButton).toBeEnabled();

  const runForm = executeButton.locator("xpath=ancestor::form");
  await runForm.evaluate((form) => {
    const executionForm = form as HTMLFormElement;
    executionForm.requestSubmit();
    executionForm.requestSubmit();
  });

  await expect(page.getByText("响应", { exact: true })).toBeVisible();
  expect(mocks.createToolCallCount()).toBe(1);
});

test("审计 deep link：call query 会加载并展开目标调用", async ({ page }) => {
  const workspace = createWorkspace("workspace-a", "Workspace A", "org-a");
  const focusedCall = createToolCall(workspace, "call-focused");
  await installFrontendMocks(page, {
    workspaces: [workspace],
    focusedCall,
  });

  await page.goto(`/audit?call=${focusedCall.id}`, { waitUntil: "domcontentloaded" });

  await expect(page.getByText(focusedCall.toolKey).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "收起" })).toBeVisible();
  await expect(page.getByText("风险解释")).toBeVisible();
});

type FrontendMockOptions = {
  workspaces: Workspace[];
  workspaceFailureUntilReload?: boolean;
  dashboardFailure?: boolean;
  tool?: Tool;
  toolLoadFailure?: boolean;
  toolCallDelayMs?: number;
  focusedCall?: ToolCall;
  databaseSchemaForbidden?: boolean;
  locale?: "en-US" | "zh-CN";
};

async function installFrontendMocks(page: Page, options: FrontendMockOptions) {
  const meWorkspaceOrgIds: string[] = [];
  let workspaceRequestCount = 0;
  let toolCallCount = 0;

  await page.addInitScript((locale) => {
    window.localStorage.setItem("agt.locale", locale);
    window.sessionStorage.removeItem("agt.selectedWorkspaceOrgId");
  }, options.locale ?? "zh-CN");

  await page.route(/^https?:\/\/[^/]+\/api\//, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const { pathname } = url;
    const method = request.method();

    if (pathname === "/api/public/workspaces" && method === "GET") {
      workspaceRequestCount += 1;
      // StrictMode 重放仍属于同一次页面导航；只有用户触发真实 reload 后才恢复。
      const navigationType = await page.evaluate(() => {
        const entry = performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming | undefined;
        return entry?.type ?? "navigate";
      });
      if (options.workspaceFailureUntilReload && navigationType !== "reload") {
        await route.fulfill({
          status: 502,
          contentType: "text/html",
          body: "<html><body>workspace lookup failed</body></html>",
        });
        return;
      }
      await fulfillJson(route, { items: options.workspaces });
      return;
    }

    if (pathname === "/api/me" && method === "GET") {
      const requestedWorkspaceOrgId = request.headers()["x-workspace-org-id"] ?? "";
      meWorkspaceOrgIds.push(requestedWorkspaceOrgId);
      const workspace =
        options.workspaces.find((item) => item.zitadelOrganizationId === requestedWorkspaceOrgId) ??
        options.workspaces[0];
      await fulfillJson(route, createMe(workspace));
      return;
    }

    if (pathname === "/api/approvals" && method === "GET") {
      await fulfillJson(route, { items: [] });
      return;
    }

    if (pathname === "/api/approvals/stream" && method === "GET") {
      await route.abort();
      return;
    }

    if (pathname === "/api/dashboard/summary" && method === "GET") {
      if (options.dashboardFailure) {
        await route.fulfill({
          status: 502,
          contentType: "text/html",
          body: "<html><body>proxy internal details</body></html>",
        });
        return;
      }
      await fulfillJson(route, createDashboardSummary(options.workspaces[0]));
      return;
    }

    if (pathname === "/api/tools" && method === "GET") {
      await fulfillJson(route, { items: options.tool ? [options.tool] : [] });
      return;
    }

    if (pathname.startsWith("/api/tools/") && method === "GET") {
      if (options.toolLoadFailure || !options.tool) {
        await route.fulfill({
          status: 502,
          contentType: "text/html",
          body: "<html><body>tool lookup failed</body></html>",
        });
        return;
      }
      await fulfillJson(route, options.tool);
      return;
    }

    if (pathname === "/api/database/schema" && method === "GET") {
      if (options.databaseSchemaForbidden) {
        await route.fulfill({
          status: 403,
          contentType: "application/json",
          body: JSON.stringify({ error: "forbidden" }),
        });
        return;
      }
      await fulfillJson(route, {
        datasource: url.searchParams.get("datasource") ?? "local_postgres",
        tables: [],
      });
      return;
    }

    if (pathname === "/api/tool-calls" && method === "GET") {
      await fulfillJson(route, {
        items: [],
        total: options.focusedCall ? 1 : 0,
        page: Number(url.searchParams.get("page") ?? "1"),
        pageSize: Number(url.searchParams.get("pageSize") ?? "10"),
      });
      return;
    }

    if (pathname === "/api/tool-calls" && method === "POST") {
      toolCallCount += 1;
      if (options.toolCallDelayMs) {
        await new Promise((resolve) => setTimeout(resolve, options.toolCallDelayMs));
      }
      await fulfillJson(route, {
        status: "success",
        callId: `call-created-${toolCallCount}`,
        traceId: "trace-created",
        result: { ok: true },
      });
      return;
    }

    if (pathname.startsWith("/api/tool-calls/") && method === "GET" && options.focusedCall) {
      await fulfillJson(route, options.focusedCall);
      return;
    }

    await route.abort();
  });

  return {
    meWorkspaceOrgIds,
    workspaceRequestCount: () => workspaceRequestCount,
    createToolCallCount: () => toolCallCount,
  };
}

async function fulfillJson(route: Route, body: unknown) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

function createWorkspace(id: string, name: string, organizationId: string): Workspace {
  return {
    id,
    name,
    slug: id,
    zitadelOrganizationId: organizationId,
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createUser(workspace: Workspace): User {
  return {
    id: `user-${workspace.id}`,
    workspaceId: workspace.id,
    zitadelUserId: `subject-${workspace.id}`,
    email: `${workspace.slug}@example.test`,
    name: `${workspace.name} Owner`,
    role: "owner",
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createMe(workspace: Workspace): MeResponse {
  const user = createUser(workspace);
  return {
    identity: {
      mode: "local",
      token: "",
      subject: user.zitadelUserId,
      email: user.email,
      name: user.name,
      organizationID: workspace.zitadelOrganizationId,
    },
    workspace,
    user,
  };
}

function createDashboardSummary(workspace: Workspace): DashboardSummary {
  return {
    workspaceId: workspace.id,
    totalCalls: 0,
    successCalls: 0,
    failedCalls: 0,
    pendingApprovalCalls: 0,
    averageDurationMs: 0,
    topTools: [],
    topErrors: [],
  };
}

function createTool(workspace: Workspace): Tool {
  return {
    id: "tool-mock-echo",
    workspaceId: workspace.id,
    namespace: "mock",
    name: "echo",
    displayName: "Mock Echo",
    description: "Reliability test tool",
    operationType: "read",
    riskLevel: "low",
    requiresApproval: false,
    inputSchemaJson: { type: "object" },
    outputSchemaJson: { type: "object" },
    enabled: true,
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createDatabaseTool(workspace: Workspace): Tool {
  return {
    ...createTool(workspace),
    id: "tool-database-query",
    namespace: "database",
    name: "query",
    displayName: "Database Query",
    description: "Reliability test database tool",
    riskLevel: "high",
    requiresApproval: true,
  };
}

function createToolCall(workspace: Workspace, id: string): ToolCall {
  return {
    id,
    requestId: `request-${id}`,
    workspaceId: workspace.id,
    actorId: "actor-owner",
    actorSubject: "owner-subject",
    actorEmail: "owner@example.test",
    actorName: "Workspace Owner",
    toolId: "tool-mock-echo",
    toolKey: "mock.echo",
    status: "success",
    riskLevel: "low",
    policyDecision: "allow",
    durationMs: 12,
    inputRedactedJson: { message: "hello" },
    outputRedactedJson: { ok: true },
    explanation: {
      targetCategory: "workspace",
      riskLevel: "low",
      matchedRule: "default allow",
      signals: ["Workspace-scoped read"],
    },
    errorMessage: "",
    traceId: "trace-focused",
    createdAt: timestamp,
  };
}
