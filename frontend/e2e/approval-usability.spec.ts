import { expect, test, type Page, type Route } from "@playwright/test";
import {
  ApiError,
  approveApproval,
  getApiErrorMessage,
  listApprovalRequestsPage,
} from "../src/api/client";
import type {
  ApprovalRequest,
  ApprovalRequestPage,
  ApprovalStatus,
  ApprovalStatusCounts,
  MeResponse,
  ToolCall,
  User,
  Workspace,
} from "../src/types";

const timestamp = "2026-08-09T00:00:00Z";
const sensitiveExecutionError =
  "request failed for https://approval-error-user:approval-error-password@example.test/run?token=approval-error-secret#approval-error-fragment";

test("审批页：展示脱敏冻结请求，连接器失败不展示原始错误", async ({ page }) => {
  const mocks = await installApprovalMocks(page);
  const { approval, toolCall } = mocks;

  await page.goto("/approvals");

  const pendingRow = page.getByRole("row").filter({ hasText: approval.toolDisplayName });
  await expect(pendingRow).toBeVisible({ timeout: 30_000 });
  await expect(pendingRow).toContainText("待处理");
  await pendingRow.getByRole("button", { name: "批准", exact: true }).click();

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
  expect(mocks.toolCallListRequestCount()).toBe(0);
});

test("审批页：使用服务端分页和状态 tab，直接使用审批 callId 打开审计", async ({ page }) => {
  const statusCounts: ApprovalStatusCounts = {
    pending: 21,
    approved: 2,
    rejected: 1,
    expired: 1,
    consumed: 1,
  };
  const mocks = await installApprovalMocks(page, {
    listPage: ({ approval, page: requestedPage, pageSize, status }) => {
      const pendingApproval = {
        ...approval,
        id: `approval-pending-page-${requestedPage}`,
        callId: `call-pending-page-${requestedPage}`,
        toolDisplayName: `Pending Page ${requestedPage}`,
      };
      const approvedApproval: ApprovalRequest = {
        ...approval,
        id: "approval-approved-page-1",
        callId: "call-approved-page-1",
        toolDisplayName: "Approved Page 1",
        status: "approved",
        reviewedBy: "reviewer-owner",
      };
      const itemsByStatus: Record<ApprovalStatus, ApprovalRequest[]> = {
        pending: [pendingApproval],
        approved: [approvedApproval],
        rejected: [],
        expired: [],
        consumed: [],
      };
      return {
        items: itemsByStatus[status],
        total: statusCounts[status],
        page: requestedPage,
        pageSize,
        statusCounts,
      };
    },
  });

  await page.goto("/approvals");

  await expect(page.getByText("Pending Page 1", { exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: /待处理\s*21/ })).toBeVisible();
  await expect(page.getByRole("tab", { name: /已批准\s*2/ })).toBeVisible();
  await expect(page.getByRole("tab", { name: /已消费\s*1/ })).toBeVisible();
  await expect(page.getByText("第 1 / 2 页 · 共 21 条请求", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "打开审计记录" })).toHaveAttribute(
    "href",
    "/audit?call=call-pending-page-1",
  );

  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByText("Pending Page 2", { exact: true })).toBeVisible();
  await expect.poll(() => mocks.approvalListRequests()).toContainEqual({
    status: "pending",
    page: 2,
    pageSize: 20,
  });

  await page.getByRole("tab", { name: /已批准\s*2/ }).click();
  await expect(page.getByText("Approved Page 1", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "打开审计记录" })).toHaveAttribute(
    "href",
    "/audit?call=call-approved-page-1",
  );
  await expect.poll(() => mocks.approvalListRequests()).toContainEqual({
    status: "approved",
    page: 1,
    pageSize: 20,
  });
  expect(mocks.toolCallListRequestCount()).toBe(0);
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

test("审批页：已消费响应明确提示不会重复执行", async ({ page }) => {
  const mocks = await installApprovalMocks(page, { reviewScenario: "consumed" });

  await page.goto("/approvals");
  await submitApproval(page, "zh-CN");

  await expect(
    page.getByText("HTTP Request 已被其他请求消费，本次没有重复执行动作。", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "打开相关审计" })).toHaveAttribute(
    "href",
    `/audit?call=${mocks.approval.callId}`,
  );
  expect(mocks.toolCallListRequestCount()).toBe(0);
});

test("审批页：审批成功后的列表刷新失败不覆盖动作反馈", async ({ page }) => {
  const mocks = await installApprovalMocks(page, {
    reviewScenario: "success",
    listFailureAfterReview: true,
  });

  await page.goto("/approvals");
  await submitApproval(page, "zh-CN");

  await expect.poll(() => mocks.approvalListRequests().length).toBeGreaterThan(1);
  await expect(page.getByRole("heading", { name: "审批已提交" })).toBeVisible();
  await expect(
    page.getByText("HTTP Request 审批已通过，连接器执行成功。", { exact: true }),
  ).toBeVisible();

  await page.getByRole("button", { name: "关闭" }).click();
  await expect(page.getByRole("alert")).toContainText("审批服务暂时不可达");
});

test("审批页：快速切页会取消旧请求且不显示 AbortError", async ({ page }) => {
  const statusCounts: ApprovalStatusCounts = {
    pending: 21,
    approved: 0,
    rejected: 0,
    expired: 0,
    consumed: 0,
  };
  const mocks = await installApprovalMocks(page, {
    listPage: async ({ approval, page: requestedPage, pageSize, status }) => {
      if (requestedPage === 2) {
        await new Promise((resolve) => setTimeout(resolve, 800));
      }
      return {
        items: status === "pending"
          ? [{
              ...approval,
              id: `approval-page-${requestedPage}`,
              callId: `call-page-${requestedPage}`,
              toolDisplayName: `Pending Page ${requestedPage}`,
            }]
          : [],
        total: statusCounts[status],
        page: requestedPage,
        pageSize,
        statusCounts,
      };
    },
  });

  await page.goto("/approvals");
  await expect(page.getByText("Pending Page 1", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "下一页" }).click();
  await expect.poll(() => mocks.approvalListRequests()).toContainEqual({
    status: "pending",
    page: 2,
    pageSize: 20,
  });
  await expect(page.getByRole("status")).toHaveText("正在加载审批...");

  await page.getByRole("button", { name: "上一页" }).click();

  await expect.poll(() => mocks.abortedApprovalListRequests()).toContainEqual({
    status: "pending",
    page: 2,
    pageSize: 20,
  });
  await expect.poll(
    () => mocks.approvalListRequests().filter((request) => request.page === 1).length,
  ).toBeGreaterThan(1);
  await expect(page.getByText("Pending Page 1", { exact: true })).toBeVisible();
  await expect(page.getByText("Pending Page 2", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

const approvalFailureScenarios = [
  {
    name: "过期",
    scenario: "expired",
    locale: "zh-CN",
    expected: "该审批已过期，不能再处理。列表已刷新。",
  },
  {
    name: "并发处理",
    scenario: "already-reviewed",
    locale: "zh-CN",
    expected: "该审批已被其他请求处理或已经消费，不会重复执行。列表已刷新。",
  },
  {
    name: "重验证冲突",
    scenario: "revalidation-conflict",
    locale: "zh-CN",
    expected: "审批重验证失败，冻结请求当前不再满足执行条件；操作已安全阻止。",
  },
  {
    name: "重验证执行响应",
    scenario: "revalidation-response",
    locale: "zh-CN",
    expected: "审批重验证失败，冻结请求当前不再满足执行条件；操作已安全阻止。",
  },
  {
    name: "权限不足",
    scenario: "permission-denied",
    locale: "zh-CN",
    expected: "当前角色无权审批该请求。",
  },
  {
    name: "服务不可达英文文案",
    scenario: "service-unavailable",
    locale: "en-US",
    expected: "The approval service is unavailable. Verify that the backend is running, then try again.",
  },
] as const satisfies ReadonlyArray<{
  name: string;
  scenario: ReviewScenario;
  locale: Locale;
  expected: string;
}>;

for (const failure of approvalFailureScenarios) {
  test(`审批页：区分${failure.name}`, async ({ page }) => {
    const mocks = await installApprovalMocks(page, {
      locale: failure.locale,
      reviewScenario: failure.scenario,
    });

    await page.goto("/approvals");
    await submitApproval(page, failure.locale);

    await expect(page.getByText(failure.expected, { exact: true })).toBeVisible();
    expect(mocks.reviewRequestCount()).toBe(1);
    expect(mocks.toolCallListRequestCount()).toBe(0);
  });
}

test("审批 API client：安全 4xx 可见，敏感 4xx 和 5xx 使用稳定通用文案", async () => {
  const originalFetch = globalThis.fetch;
  const responses = [
    jsonResponse({ error: "approval expired", code: "approval_expired" }, 409),
    jsonResponse({
      error: "approval failed for https://client-user:client-password@example.test?token=value",
      code: "approval_expired",
    }, 409),
    jsonResponse({ message: "failed at C:\\Users\\example\\private.txt" }, 400),
    jsonResponse({ message: "invalid\u0000request" }, 400),
    jsonResponse({ message: "x".repeat(241) }, 400),
    jsonResponse({ error: "temporarily unavailable" }, 503),
  ];
  globalThis.fetch = (async () => {
    const response = responses.shift();
    if (!response) {
      throw new Error("missing mocked response");
    }
    return response;
  }) as typeof fetch;

  try {
    const safeError = await captureApprovalApiError();
    expect(safeError).toMatchObject({
      name: "ApiError",
      status: 409,
      code: "approval_expired",
      message: "approval expired",
    });
    expect(getApiErrorMessage(safeError, "fallback", "permission denied")).toBe("approval expired");

    const sensitiveError = await captureApprovalApiError();
    expect(sensitiveError).toMatchObject({ status: 409, message: "Conflict" });

    for (let index = 0; index < 3; index += 1) {
      const unsafeError = await captureApprovalApiError();
      expect(unsafeError).toMatchObject({ status: 400, message: "Bad Request" });
    }

    const serverError = await captureApprovalApiError();
    expect(serverError).toMatchObject({ status: 503, message: "Service Unavailable" });
    expect(getApiErrorMessage(serverError, "localized fallback", "permission denied")).toBe(
      "localized fallback",
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("审批 API client：默认分页元数据为 50 并向 fetch 透传 AbortSignal", async () => {
  const originalFetch = globalThis.fetch;
  const controller = new AbortController();
  let observedSignal: AbortSignal | null | undefined;

  globalThis.fetch = (async (_input, init) => {
    observedSignal = init?.signal;
    return new Response(JSON.stringify({ items: [], total: 0, page: 1 }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  try {
    const result = await listApprovalRequestsPage(undefined, undefined, {}, controller.signal);
    expect(result.pageSize).toBe(50);
    expect(observedSignal).toBe(controller.signal);
  } finally {
    globalThis.fetch = originalFetch;
  }

  globalThis.fetch = (async (_input, init) =>
    new Promise<Response>((_resolve, reject) => {
      observedSignal = init?.signal;
      init?.signal?.addEventListener("abort", () => {
        const error = new Error("aborted");
        error.name = "AbortError";
        reject(error);
      }, { once: true });
    })) as typeof fetch;

  try {
    const request = listApprovalRequestsPage(undefined, undefined, {}, controller.signal);
    controller.abort();
    await expect(request).rejects.toMatchObject({ name: "AbortError" });
    expect(observedSignal).toBe(controller.signal);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

type Locale = "zh-CN" | "en-US";

type ReviewScenario =
  | "connector-failure"
  | "success"
  | "consumed"
  | "expired"
  | "already-reviewed"
  | "revalidation-conflict"
  | "revalidation-response"
  | "permission-denied"
  | "service-unavailable";

type ApprovalListRequest = {
  status: ApprovalStatus;
  page: number;
  pageSize: number;
};

type ApprovalListPageContext = ApprovalListRequest & {
  approval: ApprovalRequest;
};

type ApprovalMockOptions = {
  requestedBy?: string;
  expectedReviewerToken?: string;
  locale?: Locale;
  reviewScenario?: ReviewScenario;
  listFailureAfterReview?: boolean;
  listPage?: (
    context: ApprovalListPageContext,
  ) => ApprovalRequestPage | Promise<ApprovalRequestPage>;
};

async function installApprovalMocks(
  page: Page,
  options: ApprovalMockOptions = {},
) {
  await page.addInitScript((locale) => {
    window.localStorage.setItem("agt.locale", locale);
  }, options.locale ?? "zh-CN");

  const workspace = createWorkspace();
  const user = createUser(workspace);
  const approval = createApproval(workspace, options.requestedBy);
  const toolCall = createToolCall(workspace, approval);
  const approvalListRequests: ApprovalListRequest[] = [];
  const abortedApprovalListRequests: ApprovalListRequest[] = [];
  let toolCallListRequestCount = 0;
  let reviewRequestCount = 0;

  page.on("requestfailed", (request) => {
    const url = new URL(request.url());
    if (url.pathname !== "/api/approvals" || request.method() !== "GET") {
      return;
    }
    const failedRequest = approvalListRequest(url);
    if (!abortedApprovalListRequests.some((item) => sameApprovalListRequest(item, failedRequest))) {
      abortedApprovalListRequests.push(failedRequest);
    }
  });

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
      const listRequest = approvalListRequest(url);
      approvalListRequests.push(listRequest);
      if (options.listFailureAfterReview && reviewRequestCount > 0) {
        await fulfillJson(route, { error: "refresh temporarily unavailable" }, 503);
        return;
      }
      const response = await options.listPage?.({
        approval,
        ...listRequest,
      }) ?? defaultApprovalPage(
        approval,
        listRequest.status,
        listRequest.page,
        listRequest.pageSize,
      );
      try {
        await fulfillJson(route, response);
      } catch (error) {
        if (request.failure()) {
          if (!abortedApprovalListRequests.some((item) => sameApprovalListRequest(item, listRequest))) {
            abortedApprovalListRequests.push(listRequest);
          }
          return;
        }
        throw error;
      }
      return;
    }

    if (pathname === "/api/approvals/stream" && method === "GET") {
      await route.abort();
      return;
    }

    if (pathname === "/api/tool-calls" && method === "GET") {
      toolCallListRequestCount += 1;
      await route.abort();
      return;
    }

    if (pathname === `/api/approvals/${approval.id}/approve` && method === "POST") {
      reviewRequestCount += 1;
      if (options.expectedReviewerToken) {
        expect(request.headers()["authorization"]).toBe(`Bearer ${options.expectedReviewerToken}`);
      }
      await fulfillApprovalReview(route, approval, toolCall, options.reviewScenario ?? "connector-failure");
      return;
    }

    await route.abort();
  });

  return {
    approval,
    toolCall,
    approvalListRequests: () => [...approvalListRequests],
    abortedApprovalListRequests: () => [...abortedApprovalListRequests],
    toolCallListRequestCount: () => toolCallListRequestCount,
    reviewRequestCount: () => reviewRequestCount,
  };
}

async function fulfillApprovalReview(
  route: Route,
  approval: ApprovalRequest,
  toolCall: ToolCall,
  scenario: ReviewScenario,
) {
  switch (scenario) {
    case "expired":
      await fulfillJson(route, { error: "approval expired", code: "approval_expired" }, 409);
      return;
    case "already-reviewed":
      await fulfillJson(route, { error: "approval already reviewed", code: "approval_already_reviewed" }, 409);
      return;
    case "revalidation-conflict":
      await fulfillJson(
        route,
        { error: "approval revalidation failed", code: "approval_revalidation_failed" },
        409,
      );
      return;
    case "permission-denied":
      await fulfillJson(route, { error: "forbidden", code: "permission_denied" }, 403);
      return;
    case "service-unavailable":
      await route.abort("connectionrefused");
      return;
    case "success":
      await fulfillJson(route, {
        approval: {
          ...approval,
          status: "approved",
          reviewedBy: "reviewer-owner",
          updatedAt: timestamp,
        },
        toolCall: {
          ...toolCall,
          status: "success",
          approvalStatus: "approved",
        },
      });
      return;
    case "consumed":
      await fulfillJson(route, {
        approval: {
          ...approval,
          status: "consumed",
          reviewedBy: "reviewer-owner",
          updatedAt: timestamp,
        },
        toolCall: {
          ...toolCall,
          status: "success",
          approvalStatus: "consumed",
        },
      });
      return;
    case "revalidation-response":
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
          errorMessage: "approval revalidation failed",
        },
        code: "approval_revalidation_failed",
      });
      return;
    case "connector-failure":
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
  }
}

function defaultApprovalPage(
  approval: ApprovalRequest,
  status: ApprovalStatus,
  page: number,
  pageSize: number,
): ApprovalRequestPage {
  const items = status === "pending" ? [approval] : [];
  return {
    items,
    total: items.length,
    page,
    pageSize,
    statusCounts: {
      pending: 1,
      approved: 0,
      rejected: 0,
      expired: 0,
      consumed: 0,
    },
  };
}

async function submitApproval(page: Page, locale: Locale) {
  const approveLabel = locale === "zh-CN" ? "批准" : "Approve";
  const confirmLabel = locale === "zh-CN"
    ? "批准，等待客户端重试"
    : "Approve and wait for client retry";
  await page.getByRole("button", { name: approveLabel, exact: true }).click();
  await page.getByRole("button", { name: confirmLabel, exact: true }).click();
}

function approvalStatus(value: string | null): ApprovalStatus {
  switch (value) {
    case "approved":
    case "rejected":
    case "expired":
    case "consumed":
      return value;
    default:
      return "pending";
  }
}

function positiveInteger(value: string | null, fallback: number): number {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback;
}

function approvalListRequest(url: URL): ApprovalListRequest {
  return {
    status: approvalStatus(url.searchParams.get("status")),
    page: positiveInteger(url.searchParams.get("page"), 1),
    pageSize: positiveInteger(url.searchParams.get("pageSize"), 20),
  };
}

function sameApprovalListRequest(left: ApprovalListRequest, right: ApprovalListRequest): boolean {
  return left.status === right.status && left.page === right.page && left.pageSize === right.pageSize;
}

async function fulfillJson(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

function jsonResponse(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

async function captureApprovalApiError(): Promise<ApiError> {
  const error = await approveApproval("approval-client-error").then(
    () => null,
    (failure: unknown) => failure,
  );
  expect(error).toBeInstanceOf(ApiError);
  return error as ApiError;
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
    callId: "call-rich-details",
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
    id: approval.callId,
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
