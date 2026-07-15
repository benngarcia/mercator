// Root route: owns the console frame (<AppShell/>) and the validated
// workspace_id search param. The param defaults to the session workspace and is
// inherited by every child route, making a workspace view deep-linkable. The
// token never enters the URL (session.ts / localStorage only, per the design).
//
// Two keyed resources keep the URL search param and the session workspace in sync:
// when the param changes (deep link, back/forward) we write it into the session
// (the canonical store the data hooks read); when the session changes (Topbar
// WorkspaceSwitcher) we mirror it back into the URL. The session write is the
// source of truth; the URL is a shareable projection of it.

import { useRef } from "react";
import {
  createRootRoute,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";

import { AppShell } from "@/components/layout";
import { getWorkspace } from "@/lib/session";
import { useSession } from "@/hooks/useSession";
import { useMountEffect } from "@/hooks/useMountEffect";

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

function UrlWorkspaceSync({ sessionWorkspace, setWorkspace, urlWorkspace }: {
  sessionWorkspace: string | null;
  setWorkspace: (workspace: string) => void;
  urlWorkspace: string;
}) {
  useMountEffect(() => {
    if (urlWorkspace !== sessionWorkspace) {
      setWorkspace(urlWorkspace);
    }
  });
  return null;
}

function SessionWorkspaceSync({ navigate, sessionWorkspace, urlWorkspace }: {
  navigate: ReturnType<typeof useNavigate>;
  sessionWorkspace: string;
  urlWorkspace: string | undefined;
}) {
  useMountEffect(() => {
    if (sessionWorkspace !== urlWorkspace) {
      void navigate({
        to: ".",
        search: (previous) => ({ ...previous, workspace_id: sessionWorkspace }),
        replace: true,
      });
    }
  });
  return null;
}

function WorkspaceSync() {
  const navigate = useNavigate();
  const search = useSearch({ from: "__root__" });
  const { workspace, setWorkspace } = useSession();
  const sessionWorkspace = workspace ?? getWorkspace();
  const previousUrlWorkspace = useRef<string | undefined | null>(null);
  const urlChanged = previousUrlWorkspace.current !== search.workspace_id;
  previousUrlWorkspace.current = search.workspace_id;

  if (search.workspace_id && urlChanged) {
    return (
      <UrlWorkspaceSync
        key={search.workspace_id}
        sessionWorkspace={workspace}
        setWorkspace={setWorkspace}
        urlWorkspace={search.workspace_id}
      />
    );
  }
  return sessionWorkspace ? (
    <SessionWorkspaceSync
      key={sessionWorkspace}
      navigate={navigate}
      sessionWorkspace={sessionWorkspace}
      urlWorkspace={search.workspace_id}
    />
  ) : null;
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
