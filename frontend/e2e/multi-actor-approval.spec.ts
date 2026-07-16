import { expect, request, test, type APIRequestContext, type Page } from "@playwright/test";
import type { ApprovalRequest, MeResponse, Tool, ToolCall } from "../src/types";

const apiBaseURL = process.env.E2E_API_BASE_URL?.trim() || "http://127.0.0.1:8080";
const workspaceOrgId = process.env.E2E_WORKSPACE_ORG_ID?.trim() || "local-org";
const enabled = process.env.E2E_MULTI_ACTOR_APPROVAL?.trim() === "1";
const phase = process.env.E2E_MULTI_ACTOR_PHASE?.trim().toLowerCase();
const runId = sanitizeRunId(process.env.E2E_MULTI_ACTOR_RUN_ID?.trim() || `manual-${Date.now().toString(36)}`);
const toolName = `write_${runId}`;
const toolKey = `mock.${toolName}`;
const toolDisplayName = `多 Actor 审批工具 ${runId}`;
const approvalReason = `reviewer approve ${runId}`;
const toolArguments = {
  message: `multi-actor payload ${runId}`,
  runId,
  stage: "requester",
};

test.describe.configure({ mode: "serial" });
test.skip(!enabled, "多 Actor 审批验收仅在 verify-local.ps1 -WithMultiActorE2E 中运行");

test.describe("Requester 阶段", () => {
  test.skip(phase !== "requester", "仅运行 requester 阶段");

  test("Requester 自批应返回 403 且审批保持 pending", async ({ page }) => {
    const api = await createApiContext();
    try {
      const me = await readMe(api);
      const requesterSubject = identitySubject(me);
      expect(me.user.role).toBe("owner");
      expect(requesterSubject).toContain("requester-");

      await prepareLocalPage(page);
      await page.goto("/");
      await expect(page.getByRole("heading", { name: /Agent 工具治理总览|Agent tool governance at a glance/ })).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.locator('aside a[href="/connectors"]')).toBeVisible();
      await expect(page.locator('aside a[href="/secrets"]')).toBeVisible();
      await expect(page.locator('aside a[href="/approvals"]')).toBeVisible();

      await ensureApprovalTool(api);

      const createResponse = await api.post("/api/tool-calls", {
        data: {
          tool: toolKey,
          arguments: toolArguments,
        },
      });
      expect(createResponse.ok()).toBeTruthy();
      const createBody = (await createResponse.json()) as {
        status?: string;
        callId?: string;
        approvalId?: string;
        approvalStatus?: string;
      };
      expect(createBody.status).toBe("approval_required");
      expect(createBody.callId).toBeTruthy();
      expect(createBody.approvalId).toBeTruthy();
      expect(createBody.approvalStatus).toBe("pending");

      const pendingCall = await readToolCall(api, createBody.callId!);
      expect(pendingCall.status).toBe("approval_required");
      expect(pendingCall.approvalStatus).toBe("pending");
      expect(JSON.stringify(pendingCall.inputRedactedJson)).toContain(toolArguments.message);
      expect(JSON.stringify(pendingCall.inputRedactedJson)).toContain(runId);

      const approvalBeforeReview = await readApproval(api, createBody.approvalId!);
      expect(approvalBeforeReview.status).toBe("pending");
      expect(approvalBeforeReview.requestedBy).toBe(requesterSubject);
      expect(approvalBeforeReview.reviewedBy ?? "").toBe("");

      await page.goto("/approvals");
      const pendingRow = page.getByRole("row").filter({ hasText: toolDisplayName });
      await expect(pendingRow).toBeVisible({ timeout: 30_000 });
      await expect(pendingRow).toContainText("pending");
      await expect(pendingRow).toContainText(requesterSubject);

      await pendingRow.getByRole("button", { name: "批准" }).click();
      await expect(page.getByRole("heading", { name: "批准工具调用" })).toBeVisible();
      await page.getByLabel("审批备注").fill("self review must be blocked");
      await page.getByRole("button", { name: "批准并执行" }).click();
      await expect(page.getByText("当前角色无权执行该操作")).toBeVisible({ timeout: 30_000 });
      await page.getByRole("button", { name: "关闭" }).click();

      const approvalAfterReview = await readApproval(api, createBody.approvalId!);
      expect(approvalAfterReview.status).toBe("pending");
      expect(approvalAfterReview.requestedBy).toBe(requesterSubject);
      expect(approvalAfterReview.reviewedBy ?? "").toBe("");

      const callAfterReview = await readToolCall(api, createBody.callId!);
      expect(callAfterReview.status).toBe("approval_required");
      expect(callAfterReview.approvalStatus).toBe("pending");
      expect(JSON.stringify(callAfterReview.inputRedactedJson)).toContain(toolArguments.message);
    } finally {
      await api.dispose();
    }
  });
});

test.describe("Reviewer 阶段", () => {
  test.skip(phase !== "reviewer", "仅运行 reviewer 阶段");

  test("Reviewer approve 后按冻结参数执行并写回 reviewedBy/reason", async ({ page }) => {
    const api = await createApiContext();
    try {
      const me = await readMe(api);
      const reviewerSubject = identitySubject(me);
      expect(me.user.role).toBe("approver");
      expect(reviewerSubject).toContain("reviewer-");

      await prepareLocalPage(page);
      await page.goto("/");
      await expect(page.getByRole("heading", { name: /Agent 工具治理总览|Agent tool governance at a glance/ })).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.locator('aside a[href="/connectors"]')).toHaveCount(0);
      await expect(page.locator('aside a[href="/secrets"]')).toHaveCount(0);
      await expect(page.locator('aside a[href="/approvals"]')).toBeVisible();

      const approval = await findApprovalByToolDisplayName(api, toolDisplayName);
      expect(approval).toBeTruthy();
      expect(approval?.status).toBe("pending");
      expect(approval?.requestedBy).toContain("requester-");

      await page.goto("/approvals");
      const pendingRow = page.getByRole("row").filter({ hasText: toolDisplayName });
      await expect(pendingRow).toBeVisible({ timeout: 30_000 });
      await expect(pendingRow).toContainText("pending");
      await expect(pendingRow).toContainText(approval?.requestedBy ?? "");

      await pendingRow.getByRole("button", { name: "批准" }).click();
      await expect(page.getByRole("heading", { name: "批准工具调用" })).toBeVisible();
      await page.getByLabel("审批备注").fill(approvalReason);
      await page.getByRole("button", { name: "批准并执行" }).click();
      await expect(page.getByText("审批已提交")).toBeVisible({ timeout: 30_000 });
      await page.getByRole("button", { name: "关闭" }).click();

      await page.getByRole("tab", { name: "已批准" }).click();
      const approvedRow = page.getByRole("row").filter({ hasText: toolDisplayName });
      await expect(approvedRow).toBeVisible({ timeout: 30_000 });
      await expect(approvedRow).toContainText("approved");
      await expect(approvedRow).toContainText(approvalReason);
      await expect(approvedRow).toContainText(reviewerSubject);

      const approvalAfterReview = await readApproval(api, approval!.id);
      expect(approvalAfterReview.status).toBe("approved");
      expect(approvalAfterReview.requestedBy).toContain("requester-");
      expect(approvalAfterReview.reviewedBy).toBe(reviewerSubject);
      expect(approvalAfterReview.reason).toBe(approvalReason);

      const call = await readToolCallByKey(api, toolKey);
      expect(call.status).toBe("success");
      expect(call.approvalStatus).toBe("approved");
      expect(call.policyDecision).toBe("require_approval");
      expect("inputExecutionJson" in call).toBe(false);
      expect(JSON.stringify(call.outputRedactedJson)).toContain(toolArguments.message);
      expect(JSON.stringify(call.outputRedactedJson)).toContain(runId);
    } finally {
      await api.dispose();
    }
  });
});

async function createApiContext(): Promise<APIRequestContext> {
  return request.newContext({
    baseURL: apiBaseURL,
    extraHTTPHeaders: {
      "X-Workspace-Org-Id": workspaceOrgId,
    },
  });
}

async function prepareLocalPage(page: Page) {
  await page.addInitScript(
    (orgId: string) => {
      window.sessionStorage.setItem("agt.selectedWorkspaceOrgId", orgId);
      window.localStorage.setItem("agt.locale", "zh-CN");
    },
    workspaceOrgId,
  );
}

async function ensureApprovalTool(api: APIRequestContext) {
  const tools = await listTools(api);
  const existing = tools.items.find((item) => item.namespace === "mock" && item.name === toolName);
  if (existing) {
    return existing;
  }

  const response = await api.post("/api/tools", {
    data: {
      namespace: "mock",
      name: toolName,
      displayName: toolDisplayName,
      description: "多 Actor 审批端到端验收使用的 mock 写工具。",
      operationType: "write",
      riskLevel: "low",
      requiresApproval: true,
      inputSchemaJson: {
        type: "object",
        properties: {
          message: { type: "string" },
          runId: { type: "string" },
          stage: { type: "string" },
        },
        required: ["message", "runId", "stage"],
      },
      outputSchemaJson: {
        type: "object",
        properties: {
          echo: { type: "object" },
        },
      },
      enabled: true,
    },
  });
  expect(response.ok()).toBeTruthy();
  return (await response.json()) as Tool;
}

async function listTools(api: APIRequestContext) {
  const response = await api.get("/api/tools");
  await expectApiOk(response);
  return (await response.json()) as { items?: Tool[] };
}

async function readMe(api: APIRequestContext): Promise<MeResponse> {
  const response = await api.get("/api/me");
  await expectApiOk(response);
  return (await response.json()) as MeResponse;
}

function identitySubject(me: MeResponse): string {
  const legacyIdentity = me.identity as MeResponse["identity"] & { Subject?: string };
  return me.identity.subject || legacyIdentity.Subject || "";
}

async function readApproval(api: APIRequestContext, approvalId: string): Promise<ApprovalRequest> {
  const response = await api.get("/api/approvals");
  await expectApiOk(response);
  const body = (await response.json()) as { items?: ApprovalRequest[] };
  const approval = body.items?.find((item) => item.id === approvalId);
  if (!approval) {
    throw new Error(`未找到审批单：${approvalId}`);
  }
  return approval;
}

async function findApprovalByToolDisplayName(api: APIRequestContext, displayName: string): Promise<ApprovalRequest | null> {
  const response = await api.get("/api/approvals");
  await expectApiOk(response);
  const body = (await response.json()) as { items?: ApprovalRequest[] };
  return body.items?.find((item) => item.toolDisplayName === displayName) ?? null;
}

async function readToolCall(api: APIRequestContext, callId: string): Promise<ToolCall> {
  const response = await api.get(`/api/tool-calls/${callId}`);
  await expectApiOk(response);
  return (await response.json()) as ToolCall;
}

async function readToolCallByKey(api: APIRequestContext, key: string): Promise<ToolCall> {
  const params = new URLSearchParams({
    tool: key,
    pageSize: "5",
  });
  const response = await api.get(`/api/tool-calls?${params.toString()}`);
  await expectApiOk(response);
  const body = (await response.json()) as { items?: ToolCall[] };
  const call = body.items?.find((item) => item.toolKey === key);
  if (!call) {
    throw new Error(`未找到工具调用：${key}`);
  }
  return call;
}

async function expectApiOk(response: { ok: () => boolean; status: () => number; text: () => Promise<string> }) {
  if (!response.ok()) {
    throw new Error(`API request failed with ${response.status()}: ${await response.text()}`);
  }
}

function sanitizeRunId(value: string): string {
  const sanitized = value.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/_+/g, "_").replace(/^_|_$/g, "");
  return sanitized || "multi_actor";
}
