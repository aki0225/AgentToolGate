import type { User as OidcUser } from "oidc-client-ts";
import { ApiError } from "../api/client";
import type { MeResponse, Workspace } from "../types";

type AuthMode = "local" | "oidc";

type OidcSessionReader = {
  getUser: () => Promise<OidcUser | null>;
  removeUser: () => Promise<void>;
};

type BootstrapAuthInput = {
  authMode: AuthMode;
  selectedWorkspaceOrgId: string | null;
  userManager: OidcSessionReader | null;
  listWorkspaces: () => Promise<{ items: Workspace[] }>;
  loadMe: (token: string | null, workspaceOrgId: string) => Promise<MeResponse>;
};

export type BootstrapAuthResult = {
  workspaces: Workspace[];
  selectedWorkspaceOrgId: string | null;
  me: MeResponse | null;
  oidcUser: OidcUser | null;
  sessionError: Error | null;
};

export async function bootstrapAuthSession(input: BootstrapAuthInput): Promise<BootstrapAuthResult> {
  const { items } = await input.listWorkspaces();
  const workspaces = Array.isArray(items) ? items : [];
  const selectedWorkspaceOrgId = resolveInitialWorkspaceOrgId(input.selectedWorkspaceOrgId, workspaces);

  if (input.authMode === "local") {
    if (!selectedWorkspaceOrgId) {
      return {
        workspaces,
        selectedWorkspaceOrgId: null,
        me: null,
        oidcUser: null,
        sessionError: null,
      };
    }
    try {
      const me = await input.loadMe(null, selectedWorkspaceOrgId);
      return {
        workspaces,
        selectedWorkspaceOrgId,
        me,
        oidcUser: null,
        sessionError: null,
      };
    } catch (error) {
      return {
        workspaces,
        selectedWorkspaceOrgId,
        me: null,
        oidcUser: null,
        sessionError: normalizeBootstrapError(error),
      };
    }
  }

  if (!input.userManager) {
    throw new Error("OIDC client is not configured");
  }

  let oidcUser: OidcUser | null;
  try {
    oidcUser = await input.userManager.getUser();
  } catch (error) {
    return {
      workspaces,
      selectedWorkspaceOrgId,
      me: null,
      oidcUser: null,
      sessionError: normalizeBootstrapError(error),
    };
  }

  if (oidcUser?.expired) {
    try {
      await input.userManager.removeUser();
    } catch (error) {
      return {
        workspaces,
        selectedWorkspaceOrgId: null,
        me: null,
        oidcUser: null,
        sessionError: normalizeBootstrapError(error),
      };
    }
    oidcUser = null;
  }

  if (!oidcUser || !selectedWorkspaceOrgId) {
    return {
      workspaces,
      selectedWorkspaceOrgId,
      me: null,
      oidcUser,
      sessionError: null,
    };
  }

  try {
    const me = await input.loadMe(oidcUser.id_token ?? null, selectedWorkspaceOrgId);
    return {
      workspaces,
      selectedWorkspaceOrgId,
      me,
      oidcUser,
      sessionError: null,
    };
  } catch (error) {
    const sessionError = normalizeBootstrapError(error);
    if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
      try {
        await input.userManager.removeUser();
      } catch {
        // 后端已拒绝该会话，前端仍按未登录处理，避免继续使用失效身份。
      }
      return {
        workspaces,
        selectedWorkspaceOrgId: null,
        me: null,
        oidcUser: null,
        sessionError,
      };
    }
    return {
      workspaces,
      selectedWorkspaceOrgId,
      me: null,
      oidcUser,
      sessionError,
    };
  }
}

function normalizeBootstrapError(error: unknown): Error {
  return error instanceof Error ? error : new Error("Failed to initialize session");
}

export function resolveInitialWorkspaceOrgId(
  storedWorkspaceOrgId: string | null,
  workspaces: Workspace[],
): string | null {
  const storedWorkspaceExists =
    storedWorkspaceOrgId !== null &&
    workspaces.some((workspace) => workspace.zitadelOrganizationId === storedWorkspaceOrgId);
  if (storedWorkspaceExists) {
    return storedWorkspaceOrgId;
  }
  return workspaces.length === 1 ? workspaces[0].zitadelOrganizationId : null;
}
