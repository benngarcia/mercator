import type { PlacementDecision } from "@/lib/api/types";
import { cn } from "@/lib/utils";
// These live in the runs/ feature area (authored in parallel) and resolve at
// build time. PreviewResult is the read-only composition of the same decision
// views used on the run detail page — no run is created.
import { DecisionPanel } from "@/components/runs/DecisionPanel";
import { CandidateTable } from "@/components/runs/CandidateTable";

export interface PreviewResultProps {
  /** Dry-run placement decision returned by usePreviewPlacement. */
  decision: PlacementDecision;
  className?: string;
}

/**
 * PreviewResult renders a non-binding placement preview: the selected offer and
 * collection report (DecisionPanel) above the full candidate breakdown with
 * estimates and per-candidate rejection Violations (CandidateTable). This is
 * the exact decision surface from the run detail page, reused so a "what would
 * Mercator do" preview is visually identical to "what Mercator did".
 */
export function PreviewResult({ decision, className }: PreviewResultProps) {
  return (
    <div className={cn("flex flex-col gap-4", className)}>
      <DecisionPanel decision={decision} />
      <CandidateTable
        candidates={decision.candidates}
        selectedOfferId={decision.selected_offer_snapshot_id}
      />
    </div>
  );
}
