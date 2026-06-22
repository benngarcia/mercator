// /runs — the runs list. Polls every 3s (useRuns), renders RunsTable, and
// navigates to the detail route on row click. Query errors degrade to
// <ErrorState> (401 -> "set a token" guidance via ErrorState's handling).

import { createRoute, useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";

import { rootRoute } from "./root";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState, PageHeader } from "@/components/common";
import { RunsTable } from "@/components/runs";
import { useRuns } from "@/lib/api/queries";

function RunsPage() {
  const navigate = useNavigate();
  const { data, isLoading, isError, error, refetch } = useRuns();

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Runs"
        description="Compute runs in the active workspace."
        actions={
          <Button size="sm" onClick={() => void navigate({ to: "/runs/new" })}>
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
            void navigate({ to: "/runs/$runId", params: { runId: run.id } })
          }
          emptyState={
            <EmptyState
              icon={Plus}
              title="No runs yet"
              description="Create a run to place a workload on an offer."
              action={
                <Button
                  size="sm"
                  onClick={() => void navigate({ to: "/runs/new" })}
                >
                  Create run
                </Button>
              }
            />
          }
        />
      )}
    </div>
  );
}

export const runsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs",
  component: RunsPage,
});
