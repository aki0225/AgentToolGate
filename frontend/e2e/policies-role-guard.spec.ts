import { expect, test, type Page, type Route } from "@playwright/test";
import type { PolicyRule, PolicyRuleInput, User, Workspace } from "../src/types";

const timestamp = "2026-06-08T00:00:00Z";

type PolicyPageMockState = {
  getSimulatorCalled: () => boolean;
  getPolicyWrites: () => string[];
};

type PolicyPageMockOptions = {
  role: "member" | "owner";
  policyOverrides?: Partial<PolicyRule> & Partial<PolicyRuleInput>;
};

test("策略页：member 角色只读且 simulator 可用", async ({ page }) => {
  const mockState = await installPolicyPageMocks(page, { role: "member" });

  await page.goto("/policies");

  await expect(page.getByRole("heading", { name: "策略管理与模拟" })).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('aside a[href="/policies"]')).toHaveCount(0);
  await expect(page.getByText("只读策略视图")).toBeVisible();
  await expect(page.getByText(/角色:\s*member/)).toBeVisible();
  await expect(page.getByText("当前角色仅可查看和模拟，策略新增、编辑、删除和启用状态切换需要 owner/admin。")).toBeVisible();

  await expect(page.getByRole("button", { name: "创建规则" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "更新规则" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "编辑" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "删除" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "已启用" })).toHaveCount(0);

  await expect(page.getByRole("button", { name: "模拟" })).toBeVisible();
  await page.getByRole("button", { name: "模拟" }).click();
  await expect(page.getByText("member role simulation is read-only", { exact: true })).toBeVisible();

  expect(mockState.getSimulatorCalled()).toBeTruthy();
  expect(mockState.getPolicyWrites()).toEqual([]);
});

test("策略页：owner 角色可见管理入口且可触发策略更新", async ({ page }) => {
  const mockState = await installPolicyPageMocks(page, { role: "owner" });

  await page.goto("/policies");

  await expect(page.getByRole("heading", { name: "策略管理与模拟" })).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('aside a[href="/policies"]')).toBeVisible();
  await expect(page.getByText("只读策略视图")).toHaveCount(0);

  await expect(page.getByRole("heading", { name: "新建策略规则" })).toBeVisible();
  await expect(page.getByRole("button", { name: "创建规则" })).toBeVisible();
  await expect(page.getByRole("button", { name: "编辑" })).toBeVisible();
  await expect(page.getByRole("button", { name: "删除" })).toBeVisible();
  await expect(page.getByText("操作: 匹配全部 (*)", { exact: true })).toBeVisible();
  await expect(page.getByText("风险: 匹配全部 (*)", { exact: true })).toBeVisible();
  await expect(page.getByText("资源: 匹配全部 (*)", { exact: true })).toBeVisible();

  const enabledToggle = page.getByRole("button", { name: "已启用" });
  await expect(enabledToggle).toBeVisible();
  await enabledToggle.click();
  await expect(page.getByRole("button", { name: "已禁用" })).toBeVisible();

  await page.getByRole("button", { name: "模拟" }).click();
  await expect(page.getByText("owner role simulation can inspect policy outcome", { exact: true })).toBeVisible();

  expect(mockState.getPolicyWrites()).toContain("PUT /api/policies/policy_deny_mock_echo");
  expect(mockState.getSimulatorCalled()).toBeTruthy();
});

test("策略页：新规则默认禁用，启用全通配拒绝前必须确认影响", async ({ page }) => {
  await installPolicyPageMocks(page, { role: "owner" });

  await page.goto("/policies");

  const enabledSelect = page.getByRole("combobox", { name: "启用", exact: true });
  const createButton = page.getByRole("button", { name: "创建规则" });
  const formCard = page.getByRole("heading", { name: "新建策略规则" }).locator("..").locator("..");
  await formCard.getByLabel("连接器类型", { exact: true }).fill("**");
  await formCard.getByLabel("工具名模式", { exact: true }).fill("***");
  await formCard.getByLabel("操作类型", { exact: true }).fill("**");
  await formCard.getByLabel("风险等级", { exact: true }).fill("***");
  await formCard.getByLabel("资源模式", { exact: true }).fill("**");
  await expect(enabledSelect).toHaveValue("false");
  await expect(page.getByText("这条拒绝规则会匹配全部受治理调用")).toBeVisible();
  await expect(page.getByText("规则目前保持禁用；以后启用时会阻断所有匹配的工具调用。")).toBeVisible();
  await expect(page.getByText(/当前模拟输入会命中此草稿/)).toBeVisible();
  await expect(page.getByText(/草稿当前已禁用/)).toBeVisible();
  await expect(createButton).toBeEnabled();

  await enabledSelect.selectOption("true");
  const acknowledgement = page.getByRole("checkbox", {
    name: "我理解影响范围，仍要保存这条已启用的全局拒绝规则。",
  });
  await expect(acknowledgement).toBeVisible();
  await expect(createButton).toBeDisabled();

  await acknowledgement.check();
  await expect(createButton).toBeEnabled();
});

test("策略页：从列表启用全通配拒绝也必须确认", async ({ page }) => {
  const mockState = await installPolicyPageMocks(page, {
    role: "owner",
    policyOverrides: {
      enabled: false,
      effect: "deny",
      connectorType: "**",
      toolNamePattern: "***",
      operationType: "**",
      riskLevel: "***",
      resourcePattern: "**",
    },
  });

  await page.goto("/policies");

  await expect(page.getByText("连接器: 匹配全部 (*)", { exact: true })).toBeVisible();
  await expect(page.getByText("工具: 匹配全部 (*)", { exact: true })).toBeVisible();
  await expect(page.getByText("操作: 匹配全部 (*)", { exact: true })).toBeVisible();
  await expect(page.getByText("风险: 匹配全部 (*)", { exact: true })).toBeVisible();
  await expect(page.getByText("资源: 匹配全部 (*)", { exact: true })).toBeVisible();

  const disabledToggle = page.getByRole("button", { name: "已禁用" });
  const dismissedDialog = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.message()).toBe("我理解影响范围，仍要保存这条已启用的全局拒绝规则。");
    await dialog.dismiss();
  });
  await disabledToggle.click();
  await dismissedDialog;
  await expect(disabledToggle).toBeVisible();
  expect(mockState.getPolicyWrites()).toEqual([]);

  const acceptedDialog = page.waitForEvent("dialog").then(async (dialog) => {
    await dialog.accept();
  });
  await disabledToggle.click();
  await acceptedDialog;

  await expect(page.getByRole("button", { name: "已启用" })).toBeVisible();
  expect(mockState.getPolicyWrites()).toContain("PUT /api/policies/policy_deny_mock_echo");
});

test("策略页：工具模式 *.* 也按全局拒绝要求确认", async ({ page }) => {
  await installPolicyPageMocks(page, { role: "owner" });

  await page.goto("/policies");

  const formCard = page.getByRole("heading", { name: "新建策略规则" }).locator("..").locator("..");
  await formCard.getByLabel("连接器类型", { exact: true }).fill("*");
  await formCard.getByLabel("工具名模式", { exact: true }).fill("*.*");
  await formCard.getByLabel("操作类型", { exact: true }).fill("*");
  await formCard.getByLabel("风险等级", { exact: true }).fill("*");
  await formCard.getByLabel("资源模式", { exact: true }).fill("*");
  await formCard.getByRole("combobox", { name: "启用", exact: true }).selectOption("true");

  await expect(
    page.getByRole("checkbox", {
      name: "我理解影响范围，仍要保存这条已启用的全局拒绝规则。",
    }),
  ).toBeVisible();
  await expect(formCard.getByRole("button", { name: "创建规则" })).toBeDisabled();
});

test("策略页：草稿预览与后端模拟使用相同的输入归一化", async ({ page }) => {
  await installPolicyPageMocks(page, {
    role: "owner",
    policyOverrides: {
      effect: "require_approval",
      connectorType: "mcp",
      toolNamePattern: "mcp_weather.forecast",
      operationType: "*",
      riskLevel: "low",
      resourcePattern: "*",
    },
  });

  await page.goto("/policies");

  await page.getByLabel("连接器", { exact: true }).fill("*");
  await page.getByLabel("工具", { exact: true }).fill("MCP_WEATHER.FORECAST");
  await page.getByLabel("操作", { exact: true }).fill("");
  await page.getByLabel("风险", { exact: true }).fill("");

  await expect(page.getByText(/当前模拟输入会命中此草稿/)).toBeVisible();
});

async function installPolicyPageMocks(page: Page, options: PolicyPageMockOptions): Promise<PolicyPageMockState> {
  await page.addInitScript(() => {
    window.localStorage.setItem("agt.locale", "zh-CN");
  });

  const workspace = createWorkspace();
  const user = createUser(workspace, options.role);
  const simulationText =
    options.role === "owner" ? "owner role simulation can inspect policy outcome" : "member role simulation is read-only";
  let policies = [createPolicyRule(workspace, options.role, options.policyOverrides)];
  let simulatorCalled = false;
  const policyWrites: string[] = [];

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
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [workspace] }),
      });
      return;
    }

    if (pathname === "/api/me" && method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          identity: {
            mode: "local",
            token: "",
            subject: "local-dev",
            email: user.email,
            name: user.name,
            organizationID: workspace.zitadelOrganizationId,
          },
          workspace,
          user,
        }),
      });
      return;
    }

    if (pathname === "/api/approvals" && method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [] }),
      });
      return;
    }

    if (pathname === "/api/approvals/stream" && method === "GET") {
      await route.abort();
      return;
    }

    if (pathname === "/api/policies" && method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: policies }),
      });
      return;
    }

    if (pathname === "/api/policies" && method === "POST") {
      const body = request.postDataJSON() as PolicyRuleInput;
      const createdRule = createPolicyRule(workspace, options.role, {
        ...body,
        id: "policy_created_by_owner",
      });
      policyWrites.push(`${method} ${pathname}`);
      policies = [...policies, createdRule];
      await fulfillPolicy(route, createdRule);
      return;
    }

    const policyMatch = pathname.match(/^\/api\/policies\/([^/]+)$/);
    if (policyMatch && method === "PUT") {
      const [, policyId] = policyMatch;
      const existingPolicy = policies.find((policy) => policy.id === policyId);
      if (!existingPolicy) {
        await route.fulfill({
          status: 404,
          contentType: "application/json",
          body: JSON.stringify({ error: "policy not found" }),
        });
        return;
      }

      const body = request.postDataJSON() as PolicyRuleInput;
      const updatedRule: PolicyRule = {
        ...existingPolicy,
        ...body,
        updatedAt: timestamp,
      };
      policyWrites.push(`${method} ${pathname}`);
      policies = policies.map((policy) => (policy.id === policyId ? updatedRule : policy));
      await fulfillPolicy(route, updatedRule);
      return;
    }

    if (policyMatch && method === "DELETE") {
      const [, policyId] = policyMatch;
      policyWrites.push(`${method} ${pathname}`);
      policies = policies.filter((policy) => policy.id !== policyId);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ deleted: true }),
      });
      return;
    }

    if (pathname === "/api/policies/simulate" && method === "POST") {
      simulatorCalled = true;
      const policyRule = policies[0] ?? createPolicyRule(workspace, options.role);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          decision: "deny",
          matchedRuleId: policyRule.id,
          matchedRuleName: policyRule.name,
          explanation: simulationText,
          defaulted: true,
          evaluationTrace: [
            {
              ruleId: policyRule.id,
              ruleName: policyRule.name,
              matched: true,
              decision: "deny",
              reason: simulationText,
            },
          ],
        }),
      });
      return;
    }

    await route.abort();
  });

  return {
    getSimulatorCalled: () => simulatorCalled,
    getPolicyWrites: () => [...policyWrites],
  };
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

function createUser(workspace: Workspace, role: "member" | "owner"): User {
  return {
    id: "user_local_dev",
    workspaceId: workspace.id,
    zitadelUserId: "local-dev",
    email: "dev@agenttoolgate.local",
    name: "Local Developer",
    role,
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createPolicyRule(
  workspace: Workspace,
  role: "member" | "owner",
  overrides: Partial<PolicyRule> & Partial<PolicyRuleInput> = {}
): PolicyRule {
  return {
    id: overrides.id ?? "policy_deny_mock_echo",
    workspaceId: workspace.id,
    workspaceOrgId: workspace.zitadelOrganizationId,
    name: overrides.name ?? "deny mock echo",
    description: overrides.description ?? `${role} policy fixture`,
    enabled: overrides.enabled ?? true,
    priority: overrides.priority ?? 10,
    effect: overrides.effect ?? "deny",
    connectorType: overrides.connectorType ?? "mock",
    toolNamePattern: overrides.toolNamePattern ?? "mock.echo",
    operationType: overrides.operationType ?? "**",
    riskLevel: overrides.riskLevel ?? "***",
    resourcePattern: overrides.resourcePattern ?? "**",
    reason: overrides.reason ?? `${role} policy fixture`,
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

async function fulfillPolicy(route: Route, policyRule: PolicyRule) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(policyRule),
  });
}
