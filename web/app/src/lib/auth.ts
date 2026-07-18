// Browser-side helpers for the server's /auth login surface. These are plain
// fetch/navigation calls (not apiFetch): /auth is cookie-authenticated with no
// bearer token, no workspace_id, and no {code, message} error envelope.

// signInUrl routes into the server's OIDC login flow, preserving the current
// page as the post-login destination.
export function signInUrl(): string {
  const here = window.location.pathname + window.location.search;
  return "/auth/login?next=" + encodeURIComponent(here);
}

// signOut clears the server session then reloads from the root, which lands on
// the login flow. The redirect the server issues is not followed by fetch
// (redirect: manual) — the navigation below owns where the browser goes.
export async function signOut(): Promise<void> {
  await fetch("/auth/logout", { method: "POST", redirect: "manual" });
  window.location.assign("/");
}
