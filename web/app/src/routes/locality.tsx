// The locality page. The deployment projection says which Runs exist and where
// each landed; the locality payload lives on the Booking Decision, which the
// reducer deliberately does not keep ("a copy kept here was a second store of the
// same facts that nothing rendered"). So this page joins the two: the feed for
// the skeleton, the decisions for the evidence.
//
// The join is one derived atom rather than one hook per Run. A hook cannot be
// called in a loop over a list whose length changes between renders, and this app
// forbids React effects (test/architecture/no-direct-react-effects), so the usual
// workaround of lifting each child's result into parent state is unavailable and
// would be the wrong shape regardless. An atom that reads other atoms re-derives
// when any Run's decision changes and synchronises nothing.

import { createRoute } from "@tanstack/react-router";
import { useMemo } from "react";

import { LocalityStage } from "@/components/locality";
import { Skeleton } from "@/components/ui/skeleton";
import { useDeploymentDecisions } from "@/lib/api/queries";
import { localityOf, type RunLocality } from "@/lib/locality";
import { useDeploymentFeed, type Deployment, type DeploymentRun } from "@/lib/deployment";

import { rootRoute } from "./root";

// imageNameOf is the short name of what this Run runs. A decision does not carry
// the image reference, so it comes off the workload the projection already holds.
function imageNameOf(run: DeploymentRun): string {
  const reference = run.workload.spec.containers[0]?.image ?? "";
  const withoutDigest = reference.split("@")[0] ?? reference;
  const short = withoutDigest.split("/").pop();
  return short && short.length > 0 ? short : "image";
}

function machineLabels(deployment: Deployment): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const rental of Object.values(deployment.rentals)) {
    labels[rental.id] = rental.id;
  }
  return labels;
}

function LocalityBoard({
  runs,
  labels,
}: {
  runs: readonly DeploymentRun[];
  labels: Record<string, string>;
}) {
  const runIds = useMemo(() => runs.map((run) => run.id), [runs]);
  const chains = useDeploymentDecisions(runIds);

  const records = useMemo(() => {
    const byID = new Map(runs.map((run) => [run.id, run]));
    return chains
      .map(({ runId, decisions }) => {
        const run = byID.get(runId);
        return run ? localityOf(runId, decisions ?? null, imageNameOf(run)) : null;
      })
      .filter((record): record is RunLocality => record !== null)
      .sort((a, b) => a.runID.localeCompare(b.runID));
  }, [chains, runs]);

  return <LocalityStage records={records} machineLabels={labels} />;
}

function LocalityPage() {
  const feed = useDeploymentFeed();

  if (!feed.deployment.ready) {
    return <LocalitySkeleton />;
  }

  const placed = Object.values(feed.deployment.runs).filter(
    (run) => run.selectedOfferID !== undefined,
  );

  return <LocalityBoard runs={placed} labels={machineLabels(feed.deployment)} />;
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
