import * as React from "react";

import type { WorkloadRevision } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import { shortDigest } from "@/lib/format";
import { cn } from "@/lib/utils";
import { DataTable, type Column } from "@/components/common/DataTable";
import { CopyButton } from "@/components/common/CopyButton";
import { EmptyState } from "@/components/common/EmptyState";
import { ServiceDisabled } from "@/components/common/ServiceDisabled";

export interface WorkloadRevisionsTableProps {
  revisions: WorkloadRevision[];
  onSelect?: (revision: WorkloadRevision) => void;
  selectedId?: string;
  isLoading?: boolean;
  /**
   * Query error. A 501 (serviceDisabled) degrades gracefully to a
   * <ServiceDisabled feature="Workloads"/> panel instead of the table.
   */
  error?: ApiError | null;
  className?: string;
}

// containerImages joins the (typically single) container images of a revision
// into a compact, mono summary for the dense table cell.
function containerImages(revision: WorkloadRevision): string[] {
  return revision.spec?.containers?.map((c) => c.image) ?? [];
}

/**
 * WorkloadRevisionsTable lists the revisions of a workload: revision id (mono),
 * a truncated content digest with a copy affordance, and the container images
 * the revision pins. Rows are selectable to drive the RevisionViewer.
 */
export function WorkloadRevisionsTable({
  revisions,
  onSelect,
  selectedId,
  isLoading,
  error,
  className,
}: WorkloadRevisionsTableProps) {
  const columns = React.useMemo<Column<WorkloadRevision>[]>(
    () => [
      {
        id: "id",
        header: "Revision",
        sortable: true,
        sortValue: (r) => r.id,
        cell: (r) => (
          <div className="flex items-center gap-1.5">
            <span className="font-mono text-[0.8125rem] text-foreground">
              {r.id}
            </span>
            <CopyButton value={r.id} label="Copy revision id" />
          </div>
        ),
      },
      {
        id: "digest",
        header: "Digest",
        sortable: true,
        sortValue: (r) => r.digest,
        cell: (r) =>
          r.digest ? (
            <div className="flex items-center gap-1.5">
              <span className="font-mono text-[0.8125rem] text-muted-foreground">
                {shortDigest(r.digest)}
              </span>
              <CopyButton value={r.digest} label="Copy digest" />
            </div>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: "images",
        header: "Container images",
        cell: (r) => {
          const images = containerImages(r);
          if (images.length === 0) {
            return <span className="text-muted-foreground">—</span>;
          }
          return (
            <div className="flex flex-col gap-0.5">
              {images.map((image, i) => (
                <span
                  key={`${r.id}-img-${i}`}
                  className="font-mono text-[0.8125rem] text-foreground"
                >
                  {image}
                </span>
              ))}
            </div>
          );
        },
      },
    ],
    [],
  );

  if (error instanceof ApiError && error.serviceDisabled) {
    return <ServiceDisabled feature="Workloads" className={className} />;
  }

  return (
    <DataTable
      className={cn(className)}
      columns={columns}
      data={revisions}
      rowKey={(r) => r.id}
      onRowClick={onSelect}
      selectedKey={selectedId}
      isLoading={isLoading}
      emptyState={
        <EmptyState
          compact
          title="No revisions"
          description="This workload has no revisions yet. Create one to pin a spec."
        />
      }
    />
  );
}
