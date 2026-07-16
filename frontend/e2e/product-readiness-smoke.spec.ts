import { expect, test, type Page, type Route } from "@playwright/test";
import type {
  ApiList,
  ApiPage,
  DashboardSummary,
  MeResponse,
  PolicyRule,
  PolicySimulationResponse,
  Tool,
  ToolCall,
  User,
  Workspace,
} from "../src/types";

const timestamp = "2026-06-09T00:00:00Z";

test("产品化验收 smoke：核心页面可访问且基础空状态不崩", async ({ page }) => {
  await installProductReadinessMocks(page);

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Agent 工具治理总览" })).toBeVisible({ timeout: 30_000 });

  await page.goto("/tools");
  await expect(page.getByRole("heading", { name: "管理当前工作区工具" })).toBeVisible();
  await expect(page.getByText("还没有工具")).toBeVisible();

  await page.goto("/policies");
  await expect(page.getByRole("heading", { name: "策略管理与模拟" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "策略模拟器" })).toBeVisible();
  await page.getByRole("button", { name: "模拟" }).click();
  await expect(page.getByText("产品化 smoke 默认策略", { exact: true })).toBeVisible();

  await page.goto("/secrets");
  await expect(page.getByRole("heading", { name: "Secret 管理" })).toBeVisible();
  await expect(page.getByText("暂无 Secret")).toBeVisible();

  await page.goto("/connectors");
  await expect(page.getByRole("heading", { name: "管理 Connector 引用" })).toBeVisible();
  await expect(page.getByText("暂无 Connector")).toBeVisible();

  await page.goto("/audit");
  await expect(page.getByRole("heading", { name: "已记录的工具调用" })).toBeVisible();
  await expect(page.getByText("没有审计记录")).toBeVisible();
});

async function installProductReadinessMocks(page: Page) {
  await page.addInitScript(() => {
    window.localStorage.setItem("agt.locale", "zh-CN");
  });

  const workspace = createWorkspace();
  const user = createUser(workspace);
  const tools: Tool[] = [];
  const toolCalls: ToolCall[] = [];
  const policies: PolicyRule[] = [];

  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const { pathname } = url;
    const method = request.method();

    if (!pathname.startsWith("/api/")) {
      await route.fallback();
      return;
    }

    if (pathname === "/api/public/workspaces" && method === "GET") {
      await fulfillJson<ApiList<Workspace>>(route, { items: [workspace] });
      return;
    }

    if (pathname === "/api/me" && method === "GET") {
      await fulfillJson<MeResponse>(route, {
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
      });
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
      await fulfillJson<DashboardSummary>(route, createDashboardSummary(workspace));
      return;
    }

    if (pathname === "/api/tools" && method === "GET") {
      await fulfillJson<ApiList<Tool>>(route, { items: tools });
      return;
    }

    if (pathname === "/api/tool-calls" && method === "GET") {
      await fulfillJson<ApiPage<ToolCall>>(route, {
        items: toolCalls,
        total: 0,
        page: Number(url.searchParams.get("page") ?? "1"),
        pageSize: Number(url.searchParams.get("pageSize") ?? "10"),
      });
      return;
    }

    if (pathname === "/api/policies" && method === "GET") {
      await fulfillJson<ApiList<PolicyRule>>(route, { items: policies });
      return;
    }

    if (pathname === "/api/policies/simulate" && method === "POST") {
      await fulfillJson<PolicySimulationResponse>(route, {
        decision: "allow",
        explanation: "产品化 smoke 默认策略",
        defaulted: true,
        evaluationTrace: [
          {
            matched: true,
            decision: "allow",
            reason: "产品化 smoke 默认策略",
          },
        ],
      });
      return;
    }

    if (pathname === "/api/secrets" && method === "GET") {
      await fulfillJson(route, { items: [] });
      return;
    }

    if (pathname === "/api/connectors" && method === "GET") {
      await fulfillJson(route, { items: [] });
      return;
    }

    await route.abort();
  });
}

async function fulfillJson<T>(route: Route, body: T) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

function createWorkspace(): Workspace {
  return {
    id: "workspace_default",
    name: "Default Workspace",
    slug: "default",
    zitadelOrganizationId: "local-org",
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createUser(workspace: Workspace): User {
  return {
    id: "user_local_dev",
    workspaceId: workspace.id,
    zitadelUserId: "local-dev",
    email: "dev@agenttoolgate.local",
    name: "Local Developer",
    role: "owner",
    createdAt: timestamp,
    updatedAt: timestamp,
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
