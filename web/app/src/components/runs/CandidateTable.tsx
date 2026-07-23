import * as React from "react";
import { ChevronRight, Star } from "lucide-react";

import type {
  CandidateDecision,
  Estimate,
} from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { duration, usd } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { CopyButton } from "@/components/common";
import { ViolationList } from "./ViolationList";

export interface CandidateTableProps {
  candidates: CandidateDecision[];
  /** The winning offer; its row is highlighted and badged. */
  selectedOfferId?: string;
  className?: string;
}

type EstimateKind = "duration" | "usd";

// EstimateCell renders an Estimate as a stacked p50 · p90 · expected triple,
// the core of the candidate scoring view. Confidence (if present) shades the
// cell so low-confidence estimates read as softer.
function EstimateCell({
  estimate,
  kind,
}: {
  estimate: Estimate | undefined;
  kind: EstimateKind;
}) {
  const fmt = kind === "usd" ? usd : duration;
  if (!estimate) {
    return <span className="text-muted-foreground">—</span>;
  }
  const { p50, p90, expected, confidence } = estimate;
  const allEmpty =
    p50 === undefined && p90 === undefined && expected === undefined;
  if (allEmpty) {
    return <span className="text-muted-foreground">—</span>;
  }
  const lowConfidence = confidence !== undefined && confidence < 0.5;
  return (
    <div
      className={cn(
        "flex flex-col items-end gap-px font-mono text-[0.6875rem] leading-tight tabular",
        lowConfidence && "opacity-60",
      )}
      title={
        confidence !== undefined
          ? `confidence ${(confidence * 100).toFixed(0)}%`
          : undefined
      }
    >
      <span className="text-foreground">{fmt(expected ?? p50 ?? p90)}</span>
      <span className="text-muted-foreground">
        {fmt(p50)} · {fmt(p90)}
      </span>
    </div>
  );
}

const ESTIMATE_COLUMNS: Array<{
  key: keyof CandidateDecision["estimates"];
  label: string;
  kind: EstimateKind;
}> = [
  { key: "queue_seconds", label: "Queue", kind: "duration" },
  { key: "provision_seconds", label: "Provision", kind: "duration" },
  { key: "pull_seconds", label: "Pull", kind: "duration" },
  { key: "start_seconds", label: "Start", kind: "duration" },
  { key: "cost_usd", label: "Cost", kind: "usd" },
];

// Total column count drives the expansion row's colSpan.
const COLUMN_COUNT = 5 + ESTIMATE_COLUMNS.length; // offer, adapter, disposition, feasible, score + estimates

interface CandidateRowProps {
  candidate: CandidateDecision;
  selected: boolean;
}

function CandidateRow({ candidate, selected }: CandidateRowProps) {
  const rejections = candidate.rejections ?? [];
  const expandable = !candidate.feasible && rejections.length > 0;
  const [open, setOpen] = React.useState(false);

  return (
    <>
      <TableRow
        data-state={selected ? "selected" : undefined}
        className={cn(
          "align-middle",
          selected && "bg-primary/[0.07] hover:bg-primary/10",
          expandable && "cursor-pointer",
          !candidate.feasible && "text-muted-foreground",
        )}
        onClick={expandable ? () => setOpen((v) => !v) : undefined}
      >
        {/* Offer */}
        <TableCell className="py-2">
          <div className="flex items-center gap-1.5">
            {expandable ? (
              <ChevronRight
                className={cn(
                  "size-3.5 shrink-0 text-muted-foreground transition-transform",
                  open && "rotate-90",
                )}
              />
            ) : (
              <span className="size-3.5 shrink-0" />
            )}
            {selected ? (
              <Star className="size-3.5 shrink-0 fill-primary text-primary" />
            ) : null}
            <span className="max-w-[12rem] truncate font-mono text-[0.8125rem] text-foreground">
              {candidate.offer_snapshot_id}
            </span>
            <CopyButton
              value={candidate.offer_snapshot_id}
              label="Copy offer id"
            />
          </div>
        </TableCell>

        {/* Adapter */}
        <TableCell className="py-2">
          <span className="font-mono text-xs text-muted-foreground">
            {candidate.adapter_type ?? "—"}
          </span>
        </TableCell>

        {/* Feasible */}
        <TableCell className="py-2 text-xs text-muted-foreground">
          {dispositionLabel(candidate.disposition)}
        </TableCell>

        {/* Feasible */}
        <TableCell className="py-2">
          {candidate.feasible ? (
            <Badge className="border-phase-succeeded/30 bg-phase-succeeded/10 text-phase-succeeded">
              feasible
            </Badge>
          ) : (
            <Badge className="border-phase-failed/30 bg-phase-failed/10 text-phase-failed">
              {rejections.length > 0
                ? `${rejections.length} rejection${rejections.length === 1 ? "" : "s"}`
                : "infeasible"}
            </Badge>
          )}
        </TableCell>

        {/* Score */}
        <TableCell className="py-2 text-right">
          <span
            className={cn(
              "font-mono text-[0.8125rem] tabular",
              selected ? "font-semibold text-primary" : "text-foreground",
            )}
          >
            {candidate.score_usd === undefined ? "—" : usd(candidate.score_usd)}
          </span>
        </TableCell>

        {/* Estimates */}
        {ESTIMATE_COLUMNS.map((col) => (
          <TableCell key={col.key} className="py-2 text-right">
            <EstimateCell
              estimate={candidate.estimates?.[col.key]}
              kind={col.kind}
            />
          </TableCell>
        ))}
      </TableRow>

      {expandable && open ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={COLUMN_COUNT} className="bg-muted/20 p-3">
            <p className="mb-2 text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
              Why this offer was rejected
            </p>
            <ViolationList violations={rejections} />
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

/**
 * CandidateTable is the crown-jewel placement view: one row per candidate offer
 * with adapter, a feasible/infeasible badge, the policy score, and the five
 * estimate columns (queue / provision / pull / start / cost) each rendered as
 * expected with p50 · p90 beneath. The selected offer is starred and
 * highlighted; infeasible rows expand to a ViolationList explaining the
 * rejection.
 */
export function CandidateTable({
  candidates,
  selectedOfferId,
  className,
}: CandidateTableProps) {
  // Order: selected first, then feasible by score ascending (cheapest best),
  // then infeasible last.
  const ordered = React.useMemo(() => {
    return [...candidates].sort((a, b) => {
      if (a.offer_snapshot_id === selectedOfferId) return -1;
      if (b.offer_snapshot_id === selectedOfferId) return 1;
      if (a.feasible !== b.feasible) return a.feasible ? -1 : 1;
      const as = a.score_usd ?? Number.POSITIVE_INFINITY;
      const bs = b.score_usd ?? Number.POSITIVE_INFINITY;
      return as - bs;
    });
  }, [candidates, selectedOfferId]);

  return (
    <div
      className={cn(
        "overflow-x-auto rounded-lg border border-border bg-card",
        className,
      )}
    >
      <Table>
        <TableHeader className="bg-muted/40">
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-9 text-[0.6875rem] font-medium uppercase tracking-wider">
              Offer
            </TableHead>
            <TableHead className="h-9 text-[0.6875rem] font-medium uppercase tracking-wider">
              Adapter
            </TableHead>
            <TableHead className="h-9 text-[0.6875rem] font-medium uppercase tracking-wider">
              Disposition
            </TableHead>
            <TableHead className="h-9 text-[0.6875rem] font-medium uppercase tracking-wider">
              Feasible
            </TableHead>
            <TableHead className="h-9 text-right text-[0.6875rem] font-medium uppercase tracking-wider">
              Score
            </TableHead>
            {ESTIMATE_COLUMNS.map((col) => (
              <TableHead
                key={col.key}
                className="h-9 text-right text-[0.6875rem] font-medium uppercase tracking-wider"
              >
                {col.label}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {ordered.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell
                colSpan={COLUMN_COUNT}
                className="py-10 text-center text-sm text-muted-foreground"
              >
                No candidate offers were evaluated.
              </TableCell>
            </TableRow>
          ) : (
            ordered.map((candidate) => (
              <CandidateRow
                key={candidate.offer_snapshot_id}
                candidate={candidate}
                selected={candidate.offer_snapshot_id === selectedOfferId}
              />
            ))
          )}
        </TableBody>
      </Table>
      <p className="border-t border-border bg-muted/20 px-3 py-1.5 text-[0.625rem] text-muted-foreground">
        Estimate cells show <span className="font-mono">expected</span> with{" "}
        <span className="font-mono">p50 · p90</span> beneath. Dimmed cells are
        low-confidence.
      </p>
    </div>
  );
}

function dispositionLabel(disposition: CandidateDecision["disposition"]): string {
  switch (disposition) {
    case "run_now_existing_rental":
      return "Reuse now";
    case "queue_existing_rental":
      return "Queue";
    case "provision_fresh_rental":
      return "Fresh";
  }
}
