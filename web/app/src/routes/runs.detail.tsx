// /runs/$runId — run detail. A RunPhaseTimeline header plus RunActions, then
// three tabs: Overview (run facts via StatBlocks), Events (EventTimeline,
// polled alongside the run), and Decision (DecisionPanel + CandidateTable, or a
// "no decision yet" empty state on 404). Polling cadence is driven by the run's
// terminal state inside the hooks.

import { createRoute } from "@tanstack/react-router";
import { Compass } from "lucide-react";

import { rootRoute } from "./root";
import {
  CopyButton,
  EmptyState,
  ErrorState,
  PageHeader,
  StatBlock,
} from "@/components/common";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  CandidateTable,
  DecisionPanel,
  EventTimeline,
  RunActions,
  RunPhaseTimeline,
  RunStatusBadge,
} from "@/components/runs";
import { useRun, useRunDecision, useRunEvents } from "@/lib/api/queries";

function RunDetailPage() {
  const { runId } = runsDetailRoute.useParams();
  const run = useRun(runId);
  const events = useRunEvents(runId, { run: run.data });
  const decision = useRunDecision(runId);

  if (run.isLoading) {
    return (
      <div className="flex flex-col gap-4 p-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (run.isError) {
    return (
      <div className="p-4">
        <ErrorState error={run.error} onRetry={() => void run.refetch()} />
      </div>
    );
  }

  const data = run.data;
  if (!data) {
    return null;
  }

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            <span className="font-mono text-base">{data.id}</span>
            <CopyButton value={data.id} />
          </span>
        }
        description={
          <span className="flex items-center gap-2">
            <RunStatusBadge
              phase={data.phase}
              outcome={data.outcome}
              closed={data.closed}
            />
          </span>
        }
        actions={<RunActions run={data} />}
      />

      <RunPhaseTimeline run={data} />

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="events">Events</TabsTrigger>
          <TabsTrigger value="decision">Decision</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="pt-4">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            <StatBlock label="Phase" value={data.phase} />
            <StatBlock label="Outcome" value={data.outcome ?? "—"} />
            <StatBlock
              label="Exit code"
              value={data.exit_code ?? "—"}
              mono
            />
            <StatBlock label="Cleanup" value={data.cleanup} />
            <StatBlock label="Disposition" value={data.disposition ?? "—"} />
            <StatBlock label="Closed" value={data.closed ? "yes" : "no"} />
            <StatBlock
              label="Revision"
              value={data.workload_revision_id}
              mono
            />
            <StatBlock label="Workspace" value={data.workspace_id} mono />
          </div>
        </TabsContent>

        <TabsContent value="events" className="pt-4">
          {events.isError ? (
            <ErrorState
              error={events.error}
              onRetry={() => void events.refetch()}
            />
          ) : (
            <EventTimeline
              events={events.data ?? []}
              isLoading={events.isLoading}
            />
          )}
        </TabsContent>

        <TabsContent value="decision" className="pt-4">
          {decision.isLoading ? (
            <Skeleton className="h-48 w-full" />
          ) : decision.isError ? (
            <ErrorState
              error={decision.error}
              onRetry={() => void decision.refetch()}
            />
          ) : decision.data ? (
            <div className="flex flex-col gap-4">
              <DecisionPanel decision={decision.data} />
              <CandidateTable
                candidates={decision.data.candidates ?? []}
                selectedOfferId={decision.data.selected_offer_snapshot_id}
              />
            </div>
          ) : (
            <EmptyState
              icon={Compass}
              title="No placement decision yet"
              description="A decision appears once the scheduler evaluates offers for this run."
            />
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}

export const runsDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  component: RunDetailPage,
});
