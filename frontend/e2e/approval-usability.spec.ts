import { expect, test, type Page, type Route } from "@playwright/test";
import type { ApprovalRequest, MeResponse, ToolCall, User, Workspace } from "../src/types";

const timestamp = "2026-08-09T00:00:00Z";
const sensitiveExecutionError =
  "request failed for https://approval-error-user:approval-error-password@example.test/run?token=approval-error-secret#approval-error-fragment";

test("审批页：展示脱敏冻结请求，连接器失败不展示原始错误", async ({ page }) => {
  const { approval, toolCall } = await installApprovalMocks(page);

  await page.goto("/approvals");

  await expect(page.getByText("待处理", { exact: true }).first()).toBeVisible();
  await page.getByRole("button", { name: "批准", exact: true }).click();

  await expect(page.getByText("冻结请求详情")).toBeVisible();
  await expect(page.getByText("高风险", { exact: true })).toBeVisible();
  await expect(page.getByText("写入", { exact: true })).toBeVisible();
  await expect(page.getByText(/token=%5BREDACTED%5D/).first()).toBeVisible();
  for (const secret of [
    "approval-target-user",
    "approval-target-password",
    "synthetic-secret-value",
    "approval-target-fragment",
  ]) {
    await expect(page.locator("body")).not.toContainText(secret);
  }
  await expect(page.getByText(approval.fingerprint!, { exact: true })).toBeVisible();
  await expect(page.getByText(approval.contentHash!, { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "打开审计记录" })).toHaveAttribute(
    "href",
    `/audit?call=${toolCall.id}`,
  );

  await page.getByRole("button", { name: "批准，等待客户端重试" }).click();

  await expect(page.getByRole("heading", { name: "执行失败" })).toBeVisible();
  await expect(page.getByText("HTTP Request 审批已通过，但连接器执行失败", { exact: true })).toBeVisible();
  for (const secret of [
    sensitiveExecutionError,
    "approval-error-user",
    "approval-error-password",
    "approval-error-secret",
    "approval-error-fragment",
  ]) {
    await expect(page.locator("body")).not.toContainText(secret);
  }
  await expect(page.getByRole("link", { name: "打开相关审计" })).toHaveAttribute(
    "href",
    `/audit?call=${toolCall.id}`,
  );
});

test("审批页：本地独立审批令牌只用于当前请求", async ({ page }) => {
  const reviewerToken = "synthetic-local-reviewer-token-1234";
  await installApprovalMocks(page, {
    requestedBy: "reviewer-owner",
    expectedReviewerToken: reviewerToken,
  });

  await page.goto("/approvals");
  await page.getByRole("button", { name: "批准", exact: true }).click();
  const reviewerTokenInput = page.getByLabel("本地独立审批令牌");
  await expect(reviewerTokenInput).toHaveAttribute("autocomplete", "off");
  await expect(reviewerTokenInput).toHaveAttribute("data-1p-ignore", "true");
  await expect(reviewerTokenInput).toHaveAttribute("data-lpignore", "true");
  await reviewerTokenInput.fill(reviewerToken);
  await page.getByRole("button", { name: "批准，等待客户端重试" }).click();

  await expect(page.getByRole("heading", { name: "执行失败" })).toBeVisible();
  const storedValues = await page.evaluate(() => [
    ...Object.values(window.localStorage),
    ...Object.values(window.sessionStorage),
  ]);
  expect(storedValues.join("\n")).not.toContain(reviewerToken);
});

async function installApprovalMocks(
  page: Page,
  options: { requestedBy?: string; expectedReviewerToken?: string } = {},
): Promise<{ approval: ApprovalRequest; toolCall: ToolCall }> {
  await page.addInitScript(() => {
    window.localStorage.setItem("agt.locale", "zh-CN");
  });

  const workspace = createWorkspace();
  const user = createUser(workspace);
  const approval = createApproval(workspace, options.requestedBy);
  const toolCall = createToolCall(workspace, approval);

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
      await fulfillJson(route, { items: [workspace] });
      return;
    }

    if (pathname === "/api/me" && method === "GET") {
      await fulfillJson(route, createMe(workspace, user));
      return;
    }

    if (pathname === "/api/approvals" && method === "GET") {
      await fulfillJson(route, { items: [approval] });
      return;
    }

    if (pathname === "/api/approvals/stream" && method === "GET") {
      await route.abort();
      return;
    }

    if (pathname === "/api/tool-calls" && method === "GET") {
      await fulfillJson(route, {
        items: [toolCall],
        total: 1,
        page: 1,
        pageSize: 200,
      });
      return;
    }

    if (pathname === `/api/approvals/${approval.id}/approve` && method === "POST") {
      if (options.expectedReviewerToken) {
        expect(request.headers()["authorization"]).toBe(`Bearer ${options.expectedReviewerToken}`);
      }
      await fulfillJson(route, {
        approval: {
          ...approval,
          status: "approved",
          reviewedBy: "reviewer-owner",
          updatedAt: timestamp,
        },
        toolCall: {
          ...toolCall,
          status: "failed",
          approvalStatus: "approved",
          errorMessage: sensitiveExecutionError,
        },
      });
      return;
    }

    await route.abort();
  });

  return { approval, toolCall };
}

async function fulfillJson(route: Route, body: unknown) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

function createWorkspace(): Workspace {
  return {
    id: "workspace-default",
    name: "Default Workspace",
    slug: "default",
    zitadelOrganizationId: "local-org",
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createUser(workspace: Workspace): User {
  return {
    id: "user-reviewer",
    workspaceId: workspace.id,
    zitadelUserId: "reviewer-owner",
    email: "reviewer@example.test",
    name: "Reviewer Owner",
    role: "owner",
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createMe(workspace: Workspace, user: User): MeResponse {
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

function createApproval(workspace: Workspace, requestedBy = "requester-agent"): ApprovalRequest {
  return {
    id: "approval-rich-details",
    workspaceId: workspace.id,
    toolKey: "http.request",
    toolDisplayName: "HTTP Request",
    status: "pending",
    requestedBy,
    reason: "敏感写操作需要审批",
    fingerprint: "fingerprint-0123456789abcdef",
    adapter: "claude",
    actionType: "write",
    target:
      "https://approval-target-user:approval-target-password@example.test/upload?mode=safe&token=synthetic-secret-value#approval-target-fragment",
    canonicalTarget:
      "https://approval-target-user:approval-target-password@example.test/upload?mode=safe&token=synthetic-secret-value#approval-target-fragment",
    contentEncoding: "plain",
    contentHash: "content-hash-0123456789abcdef",
    scriptHash: "script-hash-0123456789abcdef",
    resolvedFileIdentity: "file-id-synthetic",
    parentIdentity: "parent-id-synthetic",
    decisionPayloadJson: {
      tool: "http.request",
      adapter: "claude",
      actionType: "write",
      targetCategory: "external",
      riskLevel: "high",
      isScript: false,
      contentEncoding: "plain",
      contentSensitive: true,
      contentHash: "content-hash-0123456789abcdef",
      scriptHash: "script-hash-0123456789abcdef",
      fingerprint: "fingerprint-0123456789abcdef",
    },
    expiresAt: "2026-08-09T00:10:00Z",
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function createToolCall(workspace: Workspace, approval: ApprovalRequest): ToolCall {
  return {
    id: "call-rich-details",
    requestId: "request-rich-details",
    workspaceId: workspace.id,
    actorId: "requester-agent",
    actorSubject: "requester-agent",
    actorEmail: "requester@example.test",
    actorName: "Requester Agent",
    toolId: "tool-http-request",
    toolKey: approval.toolKey,
    status: "approval_required",
    riskLevel: "high",
    policyDecision: "require_approval",
    approvalId: approval.id,
    approvalStatus: "pending",
    durationMs: 0,
    inputRedactedJson: { url: "https://example.test/upload?token=[REDACTED]" },
    outputRedactedJson: {},
    errorMessage: "",
    traceId: "trace-rich-details",
    createdAt: timestamp,
  };
}
