import type {
  ApiList,
  ApiPage,
  ApprovalActionResponse,
  ApprovalRequest,
  ApprovalReviewInput,
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

const API_BASE_URL =
  import.meta.env?.VITE_API_BASE_URL ??
  (typeof process !== "undefined" ? process.env.VITE_API_BASE_URL : undefined) ??
  "http://localhost:8080";

export function buildApiUrl(path: string): string {
  return new URL(path, API_BASE_URL).toString();
}

type JsonObject = Record<string, unknown>;

type RequestOptions = {
  token?: string | null;
  workspaceOrgId?: string | null;
  body?: unknown;
  method?: string;
};

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
  });

  const text = await response.text();
  const payload = text ? JSON.parse(text) : null;
  if (!response.ok) {
    if (response.status === 403) {
      throw new Error("当前角色无权执行该操作");
    }
    const message = payload?.error ?? payload?.message ?? response.statusText;
    throw new Error(message);
  }
  return payload as T;
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
  return requestJson<ApiList<ApprovalRequest> | null>("/api/approvals", { token, workspaceOrgId }).then(normalizeApiList);
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
      if (!response.ok || !response.body) {
        const text = await response.text().catch(() => "");
        throw new Error(text || response.statusText || "approval stream request failed");
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
