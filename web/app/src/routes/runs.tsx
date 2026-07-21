// /runs — the runs list. Polls every 3s (useRuns), renders RunsTable, and
// navigates to the detail route on row click. Query errors degrade to
// <ErrorState> (401 -> "set a token" guidance via ErrorState's handling).

import {
  createRoute,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router";
import { Plus } from "lucide-react";
import * as React from "react";

import { rootRoute } from "./root";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState, PageHeader } from "@/components/common";
import { CreateRunSheet, RunsTable } from "@/components/runs";
import { useRuns } from "@/lib/api/queries";

interface RunsSearch {
  action?: "create";
}

function validateSearch(search: Record<string, unknown>): RunsSearch {
  return search.action === "create" ? { action: "create" } : {};
}

function RunsPage() {
  const navigate = useNavigate({ from: "/runs" });
  const router = useRouter();
  const location = useLocation();
  const search = runsRoute.useSearch();
  const { data, isLoading, isError, error, refetch } = useRuns();
  const createTriggerRef = React.useRef<HTMLElement | null>(null);

  const openCreate = (trigger: HTMLElement) => {
    createTriggerRef.current = trigger;
    void navigate({
      search: (previous) => ({ ...previous, action: "create" }),
      state: (previous) => ({
        ...previous,
        runsCreateOrigin: location.href,
      }),
    });
  };

  const dismissCreate = () => {
    if (location.state.runsCreateOrigin) {
      router.history.back();
      return;
    }
    void navigate({
      search: (previous) => ({ ...previous, action: undefined }),
      replace: true,
    });
  };

  const openCreatedRun = (runId: string) => {
    void navigate({
      to: "/runs/$runId",
      params: { runId },
      search: (previous) => ({ ...previous, action: undefined }),
      replace: true,
    });
  };

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Runs"
        description="Compute runs in the active workspace."
        actions={
          <Button size="sm" onClick={(event) => openCreate(event.currentTarget)}>
            <Plus className="size-4" />
            Create run
          </Button>
        }
      />
      {isError ? (
        <ErrorState error={error} onRetry={() => void refetch()} />
      ) : (
        <RunsTable
          runs={data ?? []}
          isLoading={isLoading}
          onSelect={(run) =>
            void navigate({
              to: "/runs/$runId",
              params: { runId: run.id },
              search: (previous) => ({ ...previous, action: undefined }),
            })
          }
          emptyState={
            <EmptyState
              icon={Plus}
              title="No runs yet"
              description="Create a run to place a workload on an offer."
              action={
                <Button
                  size="sm"
                  onClick={(event) => openCreate(event.currentTarget)}
                >
                  Create run
                </Button>
              }
            />
          }
        />
      )}
      <CreateRunSheet
        open={search.action === "create"}
        onDismiss={dismissCreate}
        onCreated={openCreatedRun}
        returnFocusRef={createTriggerRef}
      />
    </div>
  );
}

export const runsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs",
  validateSearch,
  component: RunsPage,
});

declare module "@tanstack/history" {
  interface HistoryState {
    runsCreateOrigin?: string;
  }
}
