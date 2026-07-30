// The locality page. The Workspace projection says which Runs exist and where
// each landed; the locality payload lives on the Booking Decision, which the
// reducer deliberately does not keep ("a copy kept here was a second store of the
// same facts that nothing rendered"). So this page joins the two: the feed for
// the skeleton, one decisions query per placed Run for the evidence.
//
// One query per Run rather than one for the Workspace, because that is the API
// there is. It is bounded by the Runs the projection is already holding, and each
// is cached and invalidated by the same booking_decided event that drives the
// feed, so a quiet Workspace issues no traffic.
//
// Each Run gets its own component because a hook cannot be called in a loop over
// a list whose length changes between renders. They report what they derived
// through an effect rather than during render, so nothing mutates a parent while
// React is rendering it.

import { createRoute } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useState } from "react";

import { LocalityStage } from "@/components/locality";
import { Skeleton } from "@/components/ui/skeleton";
import { useSession } from "@/hooks/useSession";
import { useRunDecisions } from "@/lib/api/queries";
import { localityOf, type RunLocality } from "@/lib/locality";
import { useWorkspaceFeed, type Workspace, type WorkspaceRun } from "@/lib/workspace";

import { rootRoute } from "./root";

// imageNameOf is the short name of what this Run runs. A decision does not carry
// the image reference, so it comes off the workload the projection already holds.
function imageNameOf(run: WorkspaceRun): string {
  const reference = run.workload.spec.containers[0]?.image ?? "";
  const withoutDigest = reference.split("@")[0] ?? reference;
  const short = withoutDigest.split("/").pop();
  return short && short.length > 0 ? short : "image";
}

function PlacedRun({
  run,
  report,
}: {
  run: WorkspaceRun;
  report: (runID: string, record: RunLocality | null) => void;
}) {
  const decisions = useRunDecisions(run.id);
  const data = decisions.data ?? null;
  // Memoised so the effect below fires when the decision changes rather than on
  // every render, which a freshly derived object would otherwise do for ever.
  const record = useMemo(
    () => localityOf(run.id, data, imageNameOf(run)),
    [run, data],
  );
  useEffect(() => {
    report(run.id, record);
  }, [report, run.id, record]);
  return null;
}

function LocalityBoard({
  runs,
  labels,
}: {
  runs: readonly WorkspaceRun[];
  labels: Record<string, string>;
}) {
  const [derived, setDerived] = useState<Record<string, RunLocality | null>>({});

  const report = useCallback((runID: string, record: RunLocality | null) => {
    setDerived((previous) =>
      previous[runID] === record ? previous : { ...previous, [runID]: record },
    );
  }, []);

  // A Run that left the projection must leave the scoreboard with it, or a closed
  // Workspace keeps totalling machines it no longer holds.
  const present = useMemo(() => new Set(runs.map((run) => run.id)), [runs]);
  const records = useMemo(
    () =>
      Object.entries(derived)
        .filter(([runID, record]) => record !== null && present.has(runID))
        .map(([, record]) => record as RunLocality)
        .sort((a, b) => a.runID.localeCompare(b.runID)),
    [derived, present],
  );

  return (
    <>
      {runs.map((run) => (
        <PlacedRun key={run.id} run={run} report={report} />
      ))}
      <LocalityStage records={records} machineLabels={labels} />
    </>
  );
}

function machineLabels(workspace: Workspace): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const rental of Object.values(workspace.rentals)) {
    labels[rental.id] = rental.id;
  }
  return labels;
}

function LocalityPage() {
  const { workspace } = useSession();
  const feed = useWorkspaceFeed();

  if (!workspace) {
    return (
      <div className="flex min-h-full items-center justify-center p-8 text-sm text-muted-foreground">
        Select a Workspace
      </div>
    );
  }
  if (!feed || !feed.workspace.ready) {
    return <LocalitySkeleton />;
  }

  const placed = Object.values(feed.workspace.runs).filter(
    (run) => run.selectedOfferID !== undefined,
  );

  return <LocalityBoard runs={placed} labels={machineLabels(feed.workspace)} />;
}

function LocalitySkeleton() {
  return (
    <div className="flex flex-col">
      <div className="border-b px-5 py-4">
        <Skeleton className="h-5 w-24" />
        <Skeleton className="mt-2 h-3 w-96" />
      </div>
      <div className="grid grid-cols-2 gap-px border-b bg-border sm:grid-cols-4">
        {Array.from({ length: 4 }, (_, index) => (
          <div key={index} className="bg-background px-5 py-3">
            <Skeleton className="h-3 w-20" />
            <Skeleton className="mt-2 h-6 w-16" />
          </div>
        ))}
      </div>
      <div className="flex flex-col gap-4 p-5">
        {Array.from({ length: 2 }, (_, index) => (
          <Skeleton key={index} className="h-32 w-full" />
        ))}
      </div>
    </div>
  );
}

export const localityRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/locality",
  component: LocalityPage,
});
