import { expect, test } from "@playwright/test";
import { ApiError, connectApprovalStream, readApprovalStream } from "../src/api/client";

test("审批流客户端：OIDC 使用 Authorization header，不把 token 或 workspace 写进 query", async () => {
  const originalFetch = globalThis.fetch;
  let requestedUrl = "";
  let requestedHeaders: HeadersInit | undefined;

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requestedUrl = String(input);
    requestedHeaders = init?.headers;
    return new Response(sseBody(["event: approval", "data: {}", ""]), {
      status: 200,
      headers: {
        "Content-Type": "text/event-stream",
      },
    });
  }) as typeof fetch;

  try {
    let approvalEvents = 0;
    const connection = connectApprovalStream({
      token: "oidc-token-secret",
      workspaceOrgId: "oidc-org",
      onApproval: () => {
        approvalEvents += 1;
      },
    });

    await expect.poll(() => approvalEvents).toBe(1);
    connection.close();

    const url = new URL(requestedUrl);
    expect(url.pathname).toBe("/api/approvals/stream");
    expect(url.searchParams.get("workspaceOrgId")).toBeNull();
    expect(url.toString()).not.toContain("oidc-token-secret");

    const headers = requestedHeaders as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer oidc-token-secret");
    expect(headers["X-Workspace-Org-Id"]).toBe("oidc-org");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("审批流客户端：local 模式继续使用 EventSource query workspace fallback", () => {
  const originalEventSource = globalThis.EventSource;
  let requestedUrl = "";
  let requestedWithCredentials = false;

  class FakeEventSource extends EventTarget {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSED = 2;
    readonly readyState = 1;
    readonly url: string;
    readonly withCredentials: boolean;
    onerror: ((this: EventSource, ev: Event) => unknown) | null = null;
    onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null;
    onopen: ((this: EventSource, ev: Event) => unknown) | null = null;

    constructor(url: string | URL, eventSourceInitDict?: EventSourceInit) {
      super();
      this.url = String(url);
      this.withCredentials = eventSourceInitDict?.withCredentials ?? false;
      requestedUrl = this.url;
      requestedWithCredentials = this.withCredentials;
    }

    close() {}
  }

  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;

  try {
    const connection = connectApprovalStream({
      workspaceOrgId: "local-org",
      onApproval: () => {},
    });
    connection.close();

    const url = new URL(requestedUrl);
    expect(url.pathname).toBe("/api/approvals/stream");
    expect(url.searchParams.get("workspaceOrgId")).toBe("local-org");
    expect(requestedWithCredentials).toBe(true);
  } finally {
    globalThis.EventSource = originalEventSource;
  }
});

test("审批流客户端：token fetch 非 2xx 返回 typed ApiError 以便 UI fallback polling", async () => {
  const originalFetch = globalThis.fetch;

  globalThis.fetch = (async () => {
    return new Response("unauthorized", { status: 401, statusText: "Unauthorized" });
  }) as typeof fetch;

  try {
    const error = await waitForApprovalStreamError({
      token: "expired-token",
      workspaceOrgId: "oidc-org",
    });

    expect(error instanceof ApiError).toBe(true);
    expect(error).toMatchObject({
      name: "ApiError",
      status: 401,
      message: "Unauthorized",
    });
    expect(error.message).not.toContain("unauthorized");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("审批流客户端：token fetch HTML 502 不泄漏响应正文", async () => {
  const originalFetch = globalThis.fetch;

  globalThis.fetch = (async () => {
    return new Response("<html><body>internal-token=super-secret</body></html>", {
      status: 502,
      statusText: "Bad Gateway",
      headers: { "Content-Type": "text/html" },
    });
  }) as typeof fetch;

  try {
    const error = await waitForApprovalStreamError({
      token: "stream-token",
      workspaceOrgId: "oidc-org",
    });

    expect(error instanceof ApiError).toBe(true);
    expect(error).toMatchObject({
      name: "ApiError",
      status: 502,
      message: "Bad Gateway",
    });
    expect(error.message).not.toContain("internal-token");
    expect(error.message).not.toContain("<html>");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("审批流解析器：忽略其他事件，只响应 approval", async () => {
  let approvalEvents = 0;
  await readApprovalStream(
    sseBody([
      "event: ping",
      "data: {}",
      "",
      "event: approval",
      "data: {\"id\":\"approval_1\"}",
      "",
    ]),
    () => false,
    () => {
      approvalEvents += 1;
    },
  );

  expect(approvalEvents).toBe(1);
});

function sseBody(lines: string[]): ReadableStream<Uint8Array> {
  return new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(`${lines.join("\n")}\n`));
      controller.close();
    },
  });
}

async function waitForApprovalStreamError(options: {
  token: string;
  workspaceOrgId?: string;
}): Promise<Error> {
  let connection: { close: () => void } | null = null;
  try {
    return await new Promise<Error>((resolve, reject) => {
      const timeout = setTimeout(() => {
        connection?.close();
        reject(new Error("approval stream error not emitted"));
      }, 1000);

      connection = connectApprovalStream({
        token: options.token,
        workspaceOrgId: options.workspaceOrgId,
        onApproval: () => {},
        onError: (error) => {
          clearTimeout(timeout);
          connection?.close();
          resolve(error);
        },
      });
    });
  } finally {
    connection?.close();
  }
}
