// Root route: owns the console frame (<AppShell/>) and the validated
// workspace_id search param. The param defaults to the session workspace and is
// inherited by every child route, making a workspace view deep-linkable. The
// token never enters the URL (session.ts / localStorage only, per the design).
//
// Two keyed resources keep the URL search param and the session workspace in
// sync. An explicit URL wins for deep links and back/forward navigation. When
// the URL omits a Workspace, the persisted session fills in that default.

import {
  createRootRoute,
  Link,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";

import { AppShell } from "@/components/layout";
import { Button } from "@/components/ui/button";
import { useSession } from "@/hooks/useSession";
import { useAuthSession } from "@/lib/api/queries";
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

function UrlWorkspaceSync({
  sessionWorkspace,
  setWorkspace,
  urlWorkspace,
}: {
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

function SessionWorkspaceSync({
  navigate,
  sessionWorkspace,
  urlWorkspace,
}: {
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

  if (search.workspace_id && search.workspace_id !== workspace) {
    return (
      <UrlWorkspaceSync
        key={search.workspace_id}
        sessionWorkspace={workspace}
        setWorkspace={setWorkspace}
        urlWorkspace={search.workspace_id}
      />
    );
  }
  return workspace && !search.workspace_id ? (
    <SessionWorkspaceSync
      key={workspace}
      navigate={navigate}
      sessionWorkspace={workspace}
      urlWorkspace={search.workspace_id}
    />
  ) : null;
}

export const rootRoute = createRootRoute({
  validateSearch,
  component: RootComponent,
  notFoundComponent: NotFoundPage,
});

function NotFoundPage() {
  return (
    <div className="flex min-h-full items-center justify-center p-6">
      <div className="flex max-w-md flex-col items-center gap-3 text-center">
        <h1 className="text-xl font-semibold tracking-tight">Page not found</h1>
        <p className="text-sm text-muted-foreground">
          This console destination does not exist.
        </p>
        <Button asChild size="sm">
          <Link to="/canvas" search={true}>
            Return to Workspace
          </Link>
        </Button>
      </div>
    </div>
  );
}

function RootComponent() {
  const auth = useAuthSession();
  if (auth.data === undefined && !auth.isError) {
    return <div className="h-screen bg-background" />;
  }
  return (
    <>
      <WorkspaceSync />
      <AppShell />
    </>
  );
}
