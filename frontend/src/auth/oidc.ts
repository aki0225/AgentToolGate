import { UserManager, WebStorageStateStore } from "oidc-client-ts";

export function getAuthMode(): "local" | "oidc" {
  return (import.meta.env.VITE_AUTH_MODE ?? "local").toLowerCase() === "oidc" ? "oidc" : "local";
}

export function createUserManager() {
  const authority = import.meta.env.VITE_OIDC_AUTHORITY;
  const clientId = import.meta.env.VITE_OIDC_CLIENT_ID;
  const redirectUri = import.meta.env.VITE_OIDC_REDIRECT_URI ?? `${window.location.origin}/auth/callback`;
  const postLogoutRedirectUri =
    import.meta.env.VITE_OIDC_POST_LOGOUT_REDIRECT_URI ?? `${window.location.origin}/login`;

  if (!authority || !clientId) {
    throw new Error("VITE_OIDC_AUTHORITY and VITE_OIDC_CLIENT_ID are required in oidc mode");
  }

  return new UserManager({
    authority,
    client_id: clientId,
    redirect_uri: redirectUri,
    post_logout_redirect_uri: postLogoutRedirectUri,
    response_type: "code",
    scope: "openid profile email",
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
    automaticSilentRenew: false,
  });
}

export function organizationScope(organizationId: string) {
  return `openid profile email urn:zitadel:iam:org:id:${organizationId}`;
}
