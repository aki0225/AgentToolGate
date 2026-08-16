import type {
  ApiList,
  ApiPage,
  ApprovalActionResponse,
  ApprovalRequest,
  ApprovalRequestPage,
  ApprovalReviewInput,
  ApprovalStatus,
  ApprovalStatusCounts,
  Connector,
  DashboardSummary,
  DatabaseSchemaResponse,
  MeResponse,
  PolicyRuleInput,
  PolicySimulationRequest,
  PolicySimulationResponse,
  PolicyRule,
  Secret,
  SecretInput,
  SecretUsageResponse,
  Tool,
  ToolCall,
  ToolCallResult,
  Workspace,
} from "../types";

const configuredApiBaseUrl =
  import.meta.env?.VITE_API_BASE_URL ??
  (typeof process !== "undefined" ? process.env.VITE_API_BASE_URL : undefined);
const API_BASE_URL = resolveApiBaseUrl(
  configuredApiBaseUrl,
  typeof window !== "undefined" ? window.location.origin : undefined,
  Boolean(import.meta.env?.DEV),
);

export function resolveApiBaseUrl(
  configuredBaseUrl?: string,
  browserOrigin?: string,
  development = false,
): string {
  const configured = configuredBaseUrl?.trim();
  if (configured) {
    return new URL(configured, browserOrigin ?? "http://localhost").origin;
  }
  if (development) {
    return "http://localhost:8080";
  }
  return browserOrigin?.trim() || "http://localhost";
}

export function buildApiUrl(path: string): string {
  return new URL(path, API_BASE_URL).toString();
}

type JsonObject = Record<string, unknown>;

type RequestOptions = {
  token?: string | null;
  workspaceOrgId?: string | null;
  body?: unknown;
  method?: string;
  signal?: AbortSignal;
};

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(status: number, message: string, code?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function getApiErrorMessage(
  error: unknown,
  fallback: string,
  permissionDenied: string,
): string {
  if (error instanceof ApiError && error.status === 403) {
    return permissionDenied;
  }
  if (error instanceof ApiError && error.status < 500) {
    return safeApiErrorMessage(error.message) ?? fallback;
  }
  return fallback;
}

async function requestJson<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
  };
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  if (options.token) {
    headers.Authorization = `Bearer ${options.token}`;
  }
  if (options.workspaceOrgId) {
    headers["X-Workspace-Org-Id"] = options.workspaceOrgId;
  }

  const response = await fetch(buildApiUrl(path), {
    method: options.method ?? "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
  });

  const text = await response.text();
  const payload = parseJsonResponse(text, response.status, response.ok);
  if (!response.ok) {
    throw createApiError(response.status, payload);
  }
  return payload as T;
}

function parseJsonResponse(text: string, status: number, responseOK: boolean): unknown {
  if (!text) {
    return null;
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    if (responseOK) {
      throw new ApiError(status, "Backend returned an invalid JSON response");
    }
    return null;
  }
}

function apiErrorCode(payload: unknown): string | undefined {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    return undefined;
  }
  const record = payload as Record<string, unknown>;
  const code = typeof record.code === "string" ? record.code.trim() : "";
  if (!/^[a-z][a-z0-9_]{0,63}$/.test(code)) {
    return undefined;
  }
  return code;
}

function createApiError(status: number, payload: unknown): ApiError {
  const message = status >= 400 && status < 500
    ? apiErrorMessage(payload) ?? stableHttpErrorMessage(status)
    : stableHttpErrorMessage(status);
  return new ApiError(status, message, apiErrorCode(payload));
}

function apiErrorMessage(payload: unknown): string | undefined {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    return undefined;
  }
  const record = payload as Record<string, unknown>;
  for (const key of ["message", "error"] as const) {
    const message = safeApiErrorMessage(record[key]);
    if (message) {
      return message;
    }
  }
  return undefined;
}

function safeApiErrorMessage(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const message = value.trim();
  if (!message || message.length > 240 || /[\u0000-\u001f\u007f-\u009f]/.test(message)) {
    return undefined;
  }
  if (
    /(?:https?|ftp|file):\/\/|www\./i.test(message)
    || /(?:^|[\s"'(])(?:[a-z]:[\\/]|\\\\[^\\\s]+\\|\/(?:home|users|private|var|etc|root|tmp|opt|mnt)(?:[\\/]|$))/i.test(message)
    || /\b(?:bearer|basic)\s+\S+/i.test(message)
    || /\b(?:access[_-]?token|refresh[_-]?token|api[_-]?key|secret|password|passwd|authorization|cookie|credential|signature|session)\b/i.test(message)
    || /\bgh[pousr]_[a-z0-9]{8,}\b/i.test(message)
    || /\beyJ[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\b/i.test(message)
  ) {
    return undefined;
  }
  return message;
}

function stableHttpErrorMessage(status: number): string {
  switch (status) {
    case 400:
      return "Bad Request";
    case 401:
      return "Unauthorized";
    case 403:
      return "Forbidden";
    case 404:
      return "Not Found";
    case 409:
      return "Conflict";
    case 429:
      return "Too Many Requests";
    case 500:
      return "Internal Server Error";
    case 502:
      return "Bad Gateway";
    case 503:
      return "Service Unavailable";
    case 504:
      return "Gateway Timeout";
    default:
      return `Request failed with status ${status}`;
  }
}

function normalizeApiList<T>(payload: ApiList<T> | null | undefined): ApiList<T> {
  return {
    items: Array.isArray(payload?.items) ? payload.items : [],
  };
}

function normalizeApiPage<T>(payload: ApiPage<T> | null | undefined): ApiPage<T> {
  return {
    items: Array.isArray(payload?.items) ? payload.items : [],
    total: payload?.total ?? 0,
    page: payload?.page ?? 1,
    pageSize: payload?.pageSize && payload.pageSize > 0 ? payload.pageSize : 1,
  };
}

const approvalStatuses = [
  "pending",
  "approved",
  "rejected",
  "expired",
  "consumed",
] as const satisfies ReadonlyArray<ApprovalStatus>;

function normalizeApprovalRequestPage(
  payload: ApprovalRequestPage | null | undefined,
  requestedPage: number,
  requestedPageSize: number,
): ApprovalRequestPage {
  const items = Array.isArray(payload?.items)
    ? payload.items.map((approval) => ({
        ...approval,
        callId: typeof approval.callId === "string" ? approval.callId.trim() : "",
      }))
    : [];
  const statusCounts = emptyApprovalStatusCounts();
  if (payload?.statusCounts && typeof payload.statusCounts === "object") {
    for (const status of approvalStatuses) {
      statusCounts[status] = normalizedCount(payload.statusCounts[status]);
    }
  } else {
    for (const approval of items) {
      if (approvalStatuses.includes(approval.status)) {
        statusCounts[approval.status] += 1;
      }
    }
  }
  return {
    items,
    total: normalizedCount(payload?.total ?? items.length),
    page: normalizedPositiveInteger(payload?.page, requestedPage),
    pageSize: normalizedPositiveInteger(payload?.pageSize, requestedPageSize),
    statusCounts,
  };
}

function emptyApprovalStatusCounts(): ApprovalStatusCounts {
  return {
    pending: 0,
    approved: 0,
    rejected: 0,
    expired: 0,
    consumed: 0,
  };
}

function normalizedCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : 0;
}

function normalizedPositiveInteger(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : fallback;
}

function normalizeDashboardSummary(payload: DashboardSummary | null | undefined): DashboardSummary {
  return {
    workspaceId: payload?.workspaceId ?? "",
    totalCalls: payload?.totalCalls ?? 0,
    successCalls: payload?.successCalls ?? 0,
    failedCalls: payload?.failedCalls ?? 0,
    pendingApprovalCalls: payload?.pendingApprovalCalls ?? 0,
    averageDurationMs: payload?.averageDurationMs ?? 0,
    topTools: Array.isArray(payload?.topTools) ? payload.topTools : [],
    topErrors: Array.isArray(payload?.topErrors) ? payload.topErrors : [],
  };
}

export function listPublicWorkspaces(): Promise<ApiList<Workspace>> {
  return requestJson<ApiList<Workspace> | null>("/api/public/workspaces").then(normalizeApiList);
}

export function getMe(token?: string | null, workspaceOrgId?: string | null): Promise<MeResponse> {
  return requestJson<MeResponse>("/api/me", { token, workspaceOrgId });
}

export function listDashboardSummary(token?: string | null, workspaceOrgId?: string | null): Promise<DashboardSummary> {
  return requestJson<DashboardSummary | null>("/api/dashboard/summary", { token, workspaceOrgId }).then(normalizeDashboardSummary);
}

export function listTools(token?: string | null, workspaceOrgId?: string | null): Promise<ApiList<Tool>> {
  return requestJson<ApiList<Tool> | null>("/api/tools", { token, workspaceOrgId }).then(normalizeApiList);
}

export function getTool(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<Tool> {
  return requestJson<Tool>(`/api/tools/${id}`, { token, workspaceOrgId });
}

export function createTool(
  body: {
    namespace: string;
    name: string;
    displayName: string;
    description: string;
    operationType: string;
    riskLevel: string;
    requiresApproval: boolean;
    inputSchemaJson: JsonObject;
    outputSchemaJson: JsonObject;
    enabled: boolean;
  },
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<Tool> {
  return requestJson<Tool>("/api/tools", {
    method: "POST",
    token,
    workspaceOrgId,
    body,
  });
}

export function updateTool(
  id: string,
  body: {
    enabled: boolean;
  },
  token?: string | null,
  workspaceOrgId?: string | null,
): Promise<Tool> {
  return requestJson<Tool>(`/api/tools/${id}`, {
    method: "PATCH",
    token,
    workspaceOrgId,
    body,
  });
}

export function listToolCalls(
  token?: string | null,
  workspaceOrgId?: string | null,
  query: {
    tool?: string;
    status?: string[];
    from?: string;
    to?: string;
    page?: number;
    pageSize?: number;
  } = {}
): Promise<ApiPage<ToolCall>> {
  const searchParams = new URLSearchParams();
  if (query.tool) {
    searchParams.set("tool", query.tool);
  }
  if (query.status && query.status.length > 0) {
    searchParams.set("status", query.status.join(","));
  }
  if (query.from) {
    searchParams.set("from", query.from);
  }
  if (query.to) {
    searchParams.set("to", query.to);
  }
  if (query.page) {
    searchParams.set("page", String(query.page));
  }
  if (query.pageSize) {
    searchParams.set("pageSize", String(query.pageSize));
  }
  const suffix = searchParams.toString();
  return requestJson<ApiPage<ToolCall> | null>(suffix ? `/api/tool-calls?${suffix}` : "/api/tool-calls", {
    token,
    workspaceOrgId,
  }).then(normalizeApiPage);
}

export function getToolCall(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<ToolCall> {
  return requestJson<ToolCall>(`/api/tool-calls/${id}`, { token, workspaceOrgId });
}

export function getDatabaseSchema(
  datasource: string,
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<DatabaseSchemaResponse> {
  const query = new URLSearchParams({ datasource }).toString();
  return requestJson<DatabaseSchemaResponse>(`/api/database/schema?${query}`, { token, workspaceOrgId });
}

export function listConnectors(token?: string | null, workspaceOrgId?: string | null): Promise<ApiList<Connector>> {
  return requestJson<ApiList<Connector> | null>("/api/connectors", { token, workspaceOrgId }).then(normalizeApiList);
}

export function getConnector(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<Connector> {
  return requestJson<Connector>(`/api/connectors/${id}`, { token, workspaceOrgId });
}

export function createConnector(
  body: {
    type: string;
    name: string;
    displayName: string;
    configJson: unknown;
    enabled: boolean;
  },
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<Connector> {
  return requestJson<Connector>("/api/connectors", {
    method: "POST",
    token,
    workspaceOrgId,
    body,
  });
}

export function updateConnector(
  id: string,
  body: {
    displayName?: string;
    configJson?: unknown;
    enabled?: boolean;
  },
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<Connector> {
  return requestJson<Connector>(`/api/connectors/${id}`, {
    method: "PATCH",
    token,
    workspaceOrgId,
    body,
  });
}

export function syncConnector(
  id: string,
  token?: string | null,
  workspaceOrgId?: string | null,
): Promise<{
  connector: Connector;
  createdTools: string[];
  updatedTools: string[];
  skippedTools: string[];
  staleTools: string[];
}> {
  return requestJson<{
    connector: Connector;
    createdTools: string[];
    updatedTools: string[];
    skippedTools: string[];
    staleTools: string[];
  }>(`/api/connectors/${id}/sync`, {
    method: "POST",
    token,
    workspaceOrgId,
    body: {},
  });
}

export function listSecrets(token?: string | null, workspaceOrgId?: string | null): Promise<ApiList<Secret>> {
  return requestJson<ApiList<Secret> | null>("/api/secrets", { token, workspaceOrgId }).then(normalizeApiList);
}

export function getSecret(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<Secret> {
  return requestJson<Secret>(`/api/secrets/${id}`, { token, workspaceOrgId });
}

export function getSecretUsage(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<SecretUsageResponse> {
  return requestJson<SecretUsageResponse>(`/api/secrets/${id}/usage`, { token, workspaceOrgId });
}

export function createSecret(
  body: SecretInput,
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<Secret> {
  return requestJson<Secret>("/api/secrets", {
    method: "POST",
    token,
    workspaceOrgId,
    body,
  });
}

export function updateSecret(
  id: string,
  body: SecretInput,
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<Secret> {
  return requestJson<Secret>(`/api/secrets/${id}`, {
    method: "PUT",
    token,
    workspaceOrgId,
    body,
  });
}

export function deleteSecret(
  id: string,
  token?: string | null,
  workspaceOrgId?: string | null,
  options: { force?: boolean } = {},
): Promise<{ deleted: boolean }> {
  const path = options.force ? `/api/secrets/${id}?force=true` : `/api/secrets/${id}`;
  return requestJson<{ deleted: boolean }>(path, {
    method: "DELETE",
    token,
    workspaceOrgId,
  });
}

export function listApprovals(token?: string | null, workspaceOrgId?: string | null): Promise<ApiList<ApprovalRequest>> {
  return listApprovalRequestsPage(token, workspaceOrgId).then(({ items }) => ({ items }));
}

export function listApprovalRequestsPage(
  token?: string | null,
  workspaceOrgId?: string | null,
  query: {
    status?: ApprovalStatus;
    page?: number;
    pageSize?: number;
  } = {},
  signal?: AbortSignal,
): Promise<ApprovalRequestPage> {
  const searchParams = new URLSearchParams();
  if (query.status) {
    searchParams.set("status", query.status);
  }
  if (query.page) {
    searchParams.set("page", String(query.page));
  }
  if (query.pageSize) {
    searchParams.set("pageSize", String(query.pageSize));
  }
  const suffix = searchParams.toString();
  return requestJson<ApprovalRequestPage | null>(
    suffix ? `/api/approvals?${suffix}` : "/api/approvals",
    { token, workspaceOrgId, signal },
  ).then((payload) => normalizeApprovalRequestPage(
    payload,
    query.page ?? 1,
    query.pageSize ?? 50,
  ));
}

export function listPolicies(token?: string | null, workspaceOrgId?: string | null): Promise<ApiList<PolicyRule>> {
  return requestJson<ApiList<PolicyRule> | null>("/api/policies", { token, workspaceOrgId }).then(normalizeApiList);
}

export function createPolicy(
  body: PolicyRuleInput,
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<PolicyRule> {
  return requestJson<PolicyRule>("/api/policies", {
    method: "POST",
    token,
    workspaceOrgId,
    body,
  });
}

export function getPolicy(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<PolicyRule> {
  return requestJson<PolicyRule>(`/api/policies/${id}`, { token, workspaceOrgId });
}

export function updatePolicy(
  id: string,
  body: PolicyRuleInput,
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<PolicyRule> {
  return requestJson<PolicyRule>(`/api/policies/${id}`, {
    method: "PUT",
    token,
    workspaceOrgId,
    body,
  });
}

export function deletePolicy(id: string, token?: string | null, workspaceOrgId?: string | null): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/policies/${id}`, {
    method: "DELETE",
    token,
    workspaceOrgId,
  });
}

export function simulatePolicy(
  body: PolicySimulationRequest,
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<PolicySimulationResponse> {
  return requestJson<PolicySimulationResponse>("/api/policies/simulate", {
    method: "POST",
    token,
    workspaceOrgId,
    body,
  });
}

export function approveApproval(
  id: string,
  body: ApprovalReviewInput = {},
  token?: string | null,
  workspaceOrgId?: string | null,
): Promise<ApprovalActionResponse> {
  return requestJson<ApprovalActionResponse>(`/api/approvals/${id}/approve`, {
    method: "POST",
    body,
    token,
    workspaceOrgId,
  });
}

export function rejectApproval(
  id: string,
  body: ApprovalReviewInput = {},
  token?: string | null,
  workspaceOrgId?: string | null,
): Promise<ApprovalActionResponse> {
  return requestJson<ApprovalActionResponse>(`/api/approvals/${id}/reject`, {
    method: "POST",
    body,
    token,
    workspaceOrgId,
  });
}

export function createToolCall(
  body: {
    tool: string;
    arguments: unknown;
  },
  token?: string | null,
  workspaceOrgId?: string | null
): Promise<ToolCallResult> {
  return requestJson<ToolCallResult>("/api/tool-calls", {
    method: "POST",
    token,
    workspaceOrgId,
    body,
  });
}

type ApprovalStreamHandlers = {
  token?: string | null;
  workspaceOrgId?: string | null;
  onApproval: () => void;
  onOpen?: () => void;
  onError?: (error: Error) => void;
};

export type ApprovalStreamConnection = {
  close: () => void;
};

export function connectApprovalStream(handlers: ApprovalStreamHandlers): ApprovalStreamConnection {
  const { token, workspaceOrgId, onApproval, onOpen, onError } = handlers;
  const streamUrl = buildApprovalStreamUrl(token ? null : workspaceOrgId);

  if (!token) {
    let closed = false;
    const source = new EventSource(streamUrl.toString(), { withCredentials: true });

    source.addEventListener("approval", () => {
      onApproval();
    });
    source.onopen = () => {
      onOpen?.();
    };
    source.onerror = () => {
      if (closed) {
        return;
      }
      closed = true;
      source.close();
      onError?.(new Error("approval stream disconnected"));
    };

    return {
      close() {
        closed = true;
        source.close();
      },
    };
  }

  const controller = new AbortController();
  let closed = false;
  void (async () => {
    try {
      const response = await fetch(streamUrl.toString(), {
        method: "GET",
        headers: {
          Accept: "text/event-stream",
          Authorization: `Bearer ${token}`,
          ...(workspaceOrgId ? { "X-Workspace-Org-Id": workspaceOrgId } : {}),
        },
        signal: controller.signal,
      });
      if (!response.ok) {
        const text = await response.text().catch(() => "");
        const payload = parseJsonResponse(text, response.status, false);
        throw createApiError(response.status, payload);
      }
      if (!response.body) {
        throw new ApiError(response.status, "approval stream response body is missing");
      }
      onOpen?.();
      await readApprovalStream(response.body, () => closed || controller.signal.aborted, onApproval);
      if (!closed && !controller.signal.aborted) {
        throw new Error("approval stream disconnected");
      }
    } catch (error) {
      if (closed || controller.signal.aborted) {
        return;
      }
      onError?.(error instanceof Error ? error : new Error("approval stream disconnected"));
    }
  })();

  return {
    close() {
      closed = true;
      controller.abort();
    },
  };
}

function buildApprovalStreamUrl(workspaceOrgId?: string | null): URL {
  const url = new URL(buildApiUrl("/api/approvals/stream"));
  if (workspaceOrgId) {
    url.searchParams.set("workspaceOrgId", workspaceOrgId);
  }
  return url;
}

export async function readApprovalStream(
  body: ReadableStream<Uint8Array>,
  isClosed: () => boolean,
  onApproval: () => void
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let currentEvent = "";

  try {
    while (!isClosed()) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true });
      for (;;) {
        const newlineIndex = buffer.indexOf("\n");
        if (newlineIndex < 0) {
          break;
        }
        const line = buffer.slice(0, newlineIndex).replace(/\r$/, "");
        buffer = buffer.slice(newlineIndex + 1);
        if (line === "") {
          if (currentEvent === "approval") {
            onApproval();
          }
          currentEvent = "";
          continue;
        }
        if (line.startsWith(":")) {
          continue;
        }
        if (line.startsWith("event:")) {
          currentEvent = line.slice("event:".length).trim();
        }
      }
    }
  } finally {
    reader.releaseLock();
  }
}
