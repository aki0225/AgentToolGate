import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { User as OidcUser } from "oidc-client-ts";
import { useNavigate } from "react-router-dom";
import { createUserManager, getAuthMode, organizationScope } from "./oidc";
import { getMe, listPublicWorkspaces } from "../api/client";
import type { MeResponse, Workspace } from "../types";
import { bootstrapAuthSession } from "./bootstrap";

const selectedWorkspaceStorageKey = "agt.selectedWorkspaceOrgId";

type AuthContextValue = {
  authMode: "local" | "oidc";
  workspaces: Workspace[];
  selectedWorkspaceOrgId: string | null;
  currentWorkspace: Workspace | null;
  me: MeResponse | null;
  oidcUser: OidcUser | null;
  isLoading: boolean;
  error: string | null;
  login: (workspace: Workspace) => Promise<void>;
  logout: () => Promise<void>;
  completeOidcLogin: () => Promise<void>;
  refreshSession: (workspaceOrgId?: string | null, tokenOverride?: string | null) => Promise<void>;
};

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const authMode = getAuthMode();
  const navigate = useNavigate();
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [selectedWorkspaceOrgId, setSelectedWorkspaceOrgId] = useState<string | null>(
    window.sessionStorage.getItem(selectedWorkspaceStorageKey)
  );
  const [me, setMe] = useState<MeResponse | null>(null);
  const [oidcUser, setOidcUser] = useState<OidcUser | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const userManager = useMemo(() => {
    if (authMode !== "oidc") {
      return null;
    }
    return createUserManager();
  }, [authMode]);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const result = await bootstrapAuthSession({
          authMode,
          selectedWorkspaceOrgId: window.sessionStorage.getItem(selectedWorkspaceStorageKey),
          userManager,
          listWorkspaces: listPublicWorkspaces,
          loadMe: (token, workspaceOrgId) => getMe(token, workspaceOrgId),
        });
        if (cancelled) {
          return;
        }
        setWorkspaces(result.workspaces);
        setSelectedWorkspaceOrgId(result.selectedWorkspaceOrgId);
        setMe(result.me);
        setOidcUser(result.oidcUser);
        setError(result.sessionError?.message ?? null);
        if (result.selectedWorkspaceOrgId) {
          window.sessionStorage.setItem(selectedWorkspaceStorageKey, result.selectedWorkspaceOrgId);
        } else {
          window.sessionStorage.removeItem(selectedWorkspaceStorageKey);
        }
      } catch (err) {
        if (!cancelled) {
          setWorkspaces([]);
          setMe(null);
          setOidcUser(null);
          setError(err instanceof Error ? err.message : "Failed to initialize session");
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [authMode, userManager]);

  async function refreshSession(workspaceOrgId?: string | null, tokenOverride?: string | null) {
    const nextWorkspaceOrgId = workspaceOrgId ?? selectedWorkspaceOrgId;
    if (nextWorkspaceOrgId) {
      setSelectedWorkspaceOrgId(nextWorkspaceOrgId);
      window.sessionStorage.setItem(selectedWorkspaceStorageKey, nextWorkspaceOrgId);
    }

    try {
      const token = tokenOverride ?? oidcUser?.id_token ?? null;
      const meResponse = await getMe(token, nextWorkspaceOrgId ?? undefined);
      setMe(meResponse);
      setError(null);
    } catch (err) {
      setMe(null);
      if (err instanceof Error) {
        setError(err.message);
      }
    }
  }

  async function login(workspace: Workspace) {
    if (authMode === "local") {
      setSelectedWorkspaceOrgId(workspace.zitadelOrganizationId);
      window.sessionStorage.setItem(selectedWorkspaceStorageKey, workspace.zitadelOrganizationId);
      await refreshSession(workspace.zitadelOrganizationId, null);
      navigate("/");
      return;
    }

    if (!userManager) {
      throw new Error("OIDC client is not configured");
    }

    window.sessionStorage.setItem(selectedWorkspaceStorageKey, workspace.zitadelOrganizationId);
    await userManager.signinRedirect({
      scope: organizationScope(workspace.zitadelOrganizationId),
    });
  }

  async function completeOidcLogin() {
    if (!userManager) {
      throw new Error("OIDC client is not configured");
    }
    const user = await userManager.signinRedirectCallback();
    setOidcUser(user);
    const workspaceOrgId = window.sessionStorage.getItem(selectedWorkspaceStorageKey);
    if (workspaceOrgId) {
      setSelectedWorkspaceOrgId(workspaceOrgId);
    }
    await refreshSession(workspaceOrgId, user.id_token ?? null);
    navigate("/");
  }

  async function logout() {
    if (userManager && authMode === "oidc") {
      try {
        await userManager.removeUser();
      } catch {
        // ignore
      }
    }
    window.sessionStorage.removeItem(selectedWorkspaceStorageKey);
    setSelectedWorkspaceOrgId(null);
    setOidcUser(null);
    setMe(null);
    navigate("/login");
  }

  const currentWorkspace =
    me?.workspace ??
    workspaces.find((workspace) => workspace.zitadelOrganizationId === selectedWorkspaceOrgId) ??
    (workspaces.length === 1 ? workspaces[0] : null) ??
    null;

  return (
    <AuthContext.Provider
      value={{
        authMode,
        workspaces,
        selectedWorkspaceOrgId,
        currentWorkspace,
        me,
        oidcUser,
        isLoading,
        error,
        login,
        logout,
        completeOidcLogin,
        refreshSession,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) {
    throw new Error("useAuth must be used within AuthProvider");
  }
  return value;
}
