// Root route: owns the console frame (<AppShell/>) and the validated
// workspace_id search param. The param defaults to the session workspace and is
// inherited by every child route, making a workspace view deep-linkable. The
// token never enters the URL (session.ts / localStorage only, per the design).
//
// A small effect keeps the URL search param and the session workspace in sync:
// when the param changes (deep link, back/forward) we write it into the session
// (the canonical store the data hooks read); when the session changes (Topbar
// WorkspaceSwitcher) we mirror it back into the URL. The session write is the
// source of truth; the URL is a shareable projection of it.

import { useEffect } from "react";
import {
  createRootRoute,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";

import { AppShell } from "@/components/layout";
import { getWorkspace } from "@/lib/session";
import { useSession } from "@/hooks/useSession";

export interface RootSearch {
  workspace_id?: string;
}

// validateSearch coerces ?workspace_id into a trimmed string (or drops it).
// The default is resolved at render time from the session rather than baked in
// here, so a freshly-set workspace becomes the implicit default without forcing
// it into every URL.
function validateSearch(search: Record<string, unknown>): RootSearch {
  const raw = search.workspace_id;
  if (typeof raw === "string" && raw.trim() !== "") {
    return { workspace_id: raw.trim() };
  }
  return {};
}

function WorkspaceSync() {
  const navigate = useNavigate();
  const search = useSearch({ from: "__root__" });
  const { workspace, setWorkspace } = useSession();

  // URL -> session: a workspace in the URL wins on load / navigation.
  useEffect(() => {
    if (search.workspace_id && search.workspace_id !== workspace) {
      setWorkspace(search.workspace_id);
    }
    // Only react to the URL value here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.workspace_id]);

  // session -> URL: mirror the session workspace into the search param so the
  // view stays shareable. Skips when already in sync to avoid nav churn.
  useEffect(() => {
    const ws = workspace ?? getWorkspace();
    if (ws && ws !== search.workspace_id) {
      void navigate({
        to: ".",
        search: (prev) => ({ ...prev, workspace_id: ws }),
        replace: true,
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspace]);

  return null;
}

export const rootRoute = createRootRoute({
  validateSearch,
  component: RootComponent,
});

function RootComponent() {
  return (
    <>
      <WorkspaceSync />
      <AppShell />
    </>
  );
}
