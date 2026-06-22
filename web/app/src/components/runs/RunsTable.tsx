import { Rocket } from "lucide-react";

import type { Run } from "@/lib/api/types";
import { shortDigest } from "@/lib/format";
import { DataTable, type Column, EmptyState } from "@/components/common";
import { CopyButton } from "@/components/common";
import { RunStatusBadge } from "./RunStatusBadge";

export interface RunsTableProps {
  runs: Run[];
  selectedId?: string;
  onSelect?: (run: Run) => void;
  isLoading?: boolean;
  emptyState?: React.ReactNode;
}

// cleanup / disposition collapse into a single dense column: the disposition
// (release/terminate) is the operator intent; the cleanup state is its status.
function cleanupSummary(run: Run): string {
  const disposition = run.disposition ? run.disposition : null;
  const cleanup = run.cleanup;
  if (disposition && cleanup && cleanup !== "not_required") {
    return `${disposition} · ${cleanup}`;
  }
  if (disposition) return disposition;
  if (cleanup && cleanup !== "not_required") return cleanup;
  return "—";
}

const columns: Column<Run>[] = [
  {
    id: "id",
    header: "Run",
    sortable: true,
    sortValue: (r) => r.id,
    className: "max-w-[18rem]",
    cell: (r) => (
      <div className="flex items-center gap-1">
        <span className="truncate font-mono text-[0.8125rem] text-foreground">
          {r.id}
        </span>
        <CopyButton value={r.id} label="Copy run id" />
      </div>
    ),
  },
  {
    id: "phase",
    header: "Status",
    sortable: true,
    sortValue: (r) => (r.closed ? (r.outcome ?? "closed") : r.phase),
    cell: (r) => (
      <RunStatusBadge phase={r.phase} outcome={r.outcome} closed={r.closed} />
    ),
  },
  {
    id: "workload_revision_id",
    header: "Revision",
    sortable: true,
    sortValue: (r) => r.workload_revision_id,
    className: "max-w-[14rem]",
    cell: (r) => (
      <span className="truncate font-mono text-xs text-muted-foreground">
        {shortDigest(r.workload_revision_id, 16)}
      </span>
    ),
  },
  {
    id: "cleanup",
    header: "Cleanup",
    sortable: true,
    sortValue: (r) => cleanupSummary(r),
    cell: (r) => (
      <span className="font-mono text-xs text-muted-foreground">
        {cleanupSummary(r)}
      </span>
    ),
  },
  {
    id: "exit_code",
    header: "Exit",
    align: "right",
    sortable: true,
    sortValue: (r) => (r.exit_code ?? null),
    cell: (r) => (
      <span
        className={
          r.exit_code === undefined
            ? "font-mono text-xs text-muted-foreground"
            : r.exit_code === 0
              ? "font-mono text-xs text-phase-succeeded"
              : "font-mono text-xs text-phase-failed"
        }
      >
        {r.exit_code === undefined ? "—" : r.exit_code}
      </span>
    ),
  },
];

/**
 * RunsTable is the master list of runs: id (mono, copyable), status badge,
 * workload revision, cleanup/disposition, and exit code. Rows are selectable
 * for a master/detail layout.
 */
export function RunsTable({
  runs,
  selectedId,
  onSelect,
  isLoading,
  emptyState,
}: RunsTableProps) {
  return (
    <DataTable<Run>
      columns={columns}
      data={runs}
      rowKey={(r) => r.id}
      onRowClick={onSelect}
      selectedKey={selectedId}
      isLoading={isLoading}
      emptyState={
        emptyState ?? (
          <EmptyState
            icon={Rocket}
            title="No runs yet"
            description="Created runs in this workspace will appear here."
          />
        )
      }
    />
  );
}
