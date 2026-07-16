import { expect, test, type Page } from "@playwright/test";
import type { DashboardSummary, User, Workspace } from "../src/types";

const timestamp = "2026-06-08T00:00:00Z";

test("locale switch syncs html lang and survives reload", async ({ page }) => {
  await page.addInitScript(() => {
    if (!window.localStorage.getItem("agt.locale")) {
      window.localStorage.setItem("agt.locale", "zh-CN");
    }
  });
  await installDashboardMocks(page);

  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Agent 工具治理总览" })).toBeVisible({ timeout: 30_000 });
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");

  await page.locator("aside").getByRole("button", { name: "EN" }).click();

  await expect(page.getByRole("heading", { name: "Agent tool governance at a glance" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect.poll(() => page.evaluate(() => window.localStorage.getItem("agt.locale"))).toBe("en-US");

  await page.reload();

  await expect(page.getByRole("heading", { name: "Agent tool governance at a glance" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
});

async function installDashboardMocks(page: Page) {
  const workspace = createWorkspace();
  const user = createUser(workspace);

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

    if (pathname === "/api/dashboard/summary" && method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(createDashboardSummary(workspace)),
      });
      return;
    }

    if (pathname === "/api/tools" && method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [] }),
      });
      return;
    }

    if (pathname === "/api/tool-calls" && method === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], total: 0, page: 1, pageSize: 25 }),
      });
      return;
    }

    await route.abort();
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
