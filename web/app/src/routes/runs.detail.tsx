// /runs/$runId — run detail. A RunPhaseTimeline header plus RunActions, then
// three tabs: Overview (run facts via StatBlocks), Events (EventTimeline,
// polled alongside the run), and Decision (DecisionPanel + CandidateTable, or a
// "no decision yet" empty state on 404). Polling cadence is driven by the run's
// terminal state inside the hooks.

import { createRoute, notFound } from "@tanstack/react-router";
import { Compass } from "lucide-react";

import { rootRoute } from "./root";
import {
  CopyButton,
  EmptyState,
  ErrorState,
  PageHeader,
  StatBlock,
} from "@/components/common";
import { Card, CardContent } from "@/components/ui/card";
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
    <div className="flex flex-col gap-5 p-5">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            <span className="font-mono text-base">{data.id}</span>
            <CopyButton value={data.id} />
          </span>
        }
        description={
          <RunStatusBadge
            phase={data.phase}
            outcome={data.outcome}
            closed={data.closed}
          />
        }
        actions={<RunActions run={data} />}
      />

      {/* Summary: lifecycle + key facts, always visible. */}
      <Card>
        <CardContent className="flex flex-col gap-6 p-5">
          <RunPhaseTimeline run={data} />
          <div className="grid grid-cols-2 gap-x-4 gap-y-4 border-t pt-5 sm:grid-cols-4">
            <StatBlock label="Phase" value={data.phase} />
            <StatBlock label="Outcome" value={data.outcome ?? "—"} />
            <StatBlock label="Exit code" value={data.exit_code ?? "—"} mono />
            <StatBlock label="Cleanup" value={data.cleanup} />
            <StatBlock label="Disposition" value={data.disposition ?? "—"} />
            <StatBlock label="Closed" value={data.closed ? "yes" : "no"} />
            <StatBlock
              label="Revision"
              value={data.workload_revision_id || "—"}
              mono
            />
            <StatBlock label="Workspace" value={data.workspace_id} mono />
            <StatBlock label="Created by" value={data.created_by ?? "—"} />
            <StatBlock label="Cancelled by" value={data.cancelled_by ?? "—"} />
          </div>
        </CardContent>
      </Card>

      {/* Details: Decision / Events, with the switcher anchored in the card. */}
      <Tabs defaultValue="decision">
        <Card className="overflow-hidden">
          <div className="border-b px-4 py-3">
            <TabsList>
              <TabsTrigger value="decision">Decision</TabsTrigger>
              <TabsTrigger value="events">Events</TabsTrigger>
            </TabsList>
          </div>

          <TabsContent value="decision" className="mt-0 p-5">
            {decision.isLoading ? (
              <Skeleton className="h-48 w-full" />
            ) : decision.isError ? (
              <ErrorState
                error={decision.error}
                onRetry={() => void decision.refetch()}
              />
            ) : decision.data ? (
              <div className="flex flex-col gap-5">
                <DecisionPanel decision={decision.data} />
                <div className="flex flex-col gap-3 border-t pt-5">
                  <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
                    Candidates
                  </span>
                  <CandidateTable
                    candidates={decision.data.candidates ?? []}
                    selectedOfferId={decision.data.selected_offer_snapshot_id}
                  />
                </div>
              </div>
            ) : (
              <EmptyState
                icon={Compass}
                title="No booking decision yet"
                description="A decision appears once the scheduler evaluates offers for this run."
              />
            )}
          </TabsContent>

          <TabsContent value="events" className="mt-0 p-5">
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
        </Card>
      </Tabs>
    </div>
  );
}

export const runsDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  beforeLoad: ({ params }) => {
    if (params.runId === "new") throw notFound();
  },
  component: RunDetailPage,
});
