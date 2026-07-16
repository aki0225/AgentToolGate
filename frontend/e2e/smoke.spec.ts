import { expect, request, test, type Locator, type Page } from "@playwright/test";

test("审批硬化：本地同一发起人不能自批，审批弹窗与错误提示可见", async ({ page }) => {
  const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
  const namespace = "mock";
  const name = `write-${suffix}`;
  const displayName = `E2E 审批工具 ${suffix}`;
  const toolKey = `${namespace}.${name}`;

  page.on("response", (response) => {
    const url = response.url();
    if (url.includes("/api/approvals") || url.includes("/api/tool-calls")) {
      console.log(`[response] ${response.status()} ${url}`);
    }
  });
  page.on("requestfailed", (request) => {
    const url = request.url();
    if (url.includes("/api/approvals") || url.includes("/api/tool-calls")) {
      console.log(`[requestfailed] ${url} ${request.failure()?.errorText ?? "unknown"}`);
    }
  });

  const streamResponsePromise = page.waitForResponse((response) => response.url().includes("/api/approvals/stream"), {
    timeout: 30_000,
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Agent tool governance at a glance" })).toBeVisible({ timeout: 30_000 });

  const workspaceOrgId = await readWorkspaceOrgId(page);
  const streamResponse = await streamResponsePromise;
  expect(streamResponse.status()).toBe(200);

  const api = await request.newContext({
    baseURL: "http://127.0.0.1:8080",
    extraHTTPHeaders: {
      "X-Workspace-Org-Id": workspaceOrgId,
    },
  });
  const approvalsBaseline = await readPendingApprovalCountFromApi(api);
  const approvalsNav = page.locator('aside a[href="/approvals"]');
  const createResponse = await api.post("/api/tools", {
    data: {
      namespace,
      name,
      displayName,
      description: "用于验证 approval SSE 和审计链路的 E2E 工具。",
      operationType: "write",
      riskLevel: "low",
      requiresApproval: true,
      inputSchemaJson: {
        type: "object",
        properties: {
          message: {
            type: "string",
          },
        },
        required: ["message"],
      },
      outputSchemaJson: {
        type: "object",
        properties: {
          echo: {
            type: "object",
          },
        },
      },
      enabled: true,
    },
  });
  expect(createResponse.ok()).toBeTruthy();
  await createResponse.json();

  const callResponse = await api.post("/api/tool-calls", {
    data: {
      tool: toolKey,
      arguments: {
        message: "hello from e2e",
      },
    },
  });
  expect(callResponse.ok()).toBeTruthy();
  const toolCallResult = (await callResponse.json()) as { status?: string };
  expect(toolCallResult.status).toBe("approval_required");
  await api.dispose();

  await expect.poll(async () => readPendingApprovalCount(approvalsNav), { timeout: 15_000 }).toBe(approvalsBaseline + 1);

  await page.goto("/approvals");
  await expect(page.getByRole("heading", { name: "Review pending tool calls" })).toBeVisible();
  const pendingRow = page.getByRole("row").filter({ hasText: displayName });
  await expect(pendingRow).toBeVisible({ timeout: 30_000 });
  await pendingRow.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByRole("heading", { name: "Approve tool call" })).toBeVisible();
  await page.getByLabel("Review note").fill("self review should be blocked");
  await page.getByRole("button", { name: "Approve and execute" }).click();
  await expect(page.getByText(/Current role is not allowed to perform this action|当前角色无权执行该操作/)).toBeVisible({
    timeout: 30_000,
  });
  await expect.poll(async () => readPendingApprovalCount(approvalsNav), { timeout: 15_000 }).toBe(approvalsBaseline + 1);
});

async function readPendingApprovalCount(locator: Locator): Promise<number> {
  const text = (await locator.textContent()) ?? "";
  const match = text.match(/\d+/);
  return match ? Number(match[0]) : 0;
}

async function readPendingApprovalCountFromApi(api: import("@playwright/test").APIRequestContext): Promise<number> {
  const response = await api.get("/api/approvals");
  if (!response.ok()) {
    throw new Error(`未能读取审批列表：${response.status()}`);
  }
  const payload = (await response.json()) as { items?: Array<{ status?: string }> };
  const items = payload.items ?? [];
  return items.filter((item) => item.status === "pending").length;
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
