// Browser-side navigation for the server's /auth login surface. /auth is
// cookie-authenticated with no bearer token or workspace_id.

// signInUrl routes into the server's OIDC login flow, preserving the current
// page as the post-login destination.
export function signInUrl(): string {
  const here = window.location.pathname + window.location.search;
  return "/auth/login?next=" + encodeURIComponent(here);
}
