// /workloads/$workloadId — the revisions of one workload. WorkloadRevisionsTable
// lists them (and degrades to <ServiceDisabled> on a 501 via its `error` prop);
// selecting a revision renders it in RevisionViewer. CreateRevisionDialog adds a
// new revision under this workload.

import { useState } from "react";
import { createRoute } from "@tanstack/react-router";
import { Plus } from "lucide-react";

import { rootRoute } from "./root";
import { CopyButton, PageHeader } from "@/components/common";
import { Button } from "@/components/ui/button";
import {
  CreateRevisionDialog,
  RevisionViewer,
  WorkloadRevisionsTable,
} from "@/components/workloads";
import { useWorkloadRevisions } from "@/lib/api/queries";
import type { WorkloadRevision } from "@/lib/api/types";

function WorkloadDetailPage() {
  const { workloadId } = workloadsDetailRoute.useParams();
  const { data, isLoading, error } = useWorkloadRevisions(workloadId);
  const [selected, setSelected] = useState<WorkloadRevision | null>(null);

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            <span className="font-mono text-base">{workloadId}</span>
            <CopyButton value={workloadId} />
          </span>
        }
        description="Workload revisions."
        actions={
          <CreateRevisionDialog
            workloadId={workloadId}
            trigger={
              <Button size="sm">
                <Plus className="size-4" />
                New revision
              </Button>
            }
            onCreated={(rev) => setSelected(rev)}
          />
        }
      />

      <WorkloadRevisionsTable
        revisions={data ?? []}
        isLoading={isLoading}
        error={error}
        selectedId={selected?.id}
        onSelect={setSelected}
      />

      {selected ? <RevisionViewer revision={selected} /> : null}
    </div>
  );
}

export const workloadsDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workloads/$workloadId",
  component: WorkloadDetailPage,
});
