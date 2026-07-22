import { CircleSlash } from "lucide-react";

import type { BookingDecision } from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { phaseLabel, shortDigest } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import { StatBlock, CopyButton, RelativeTime } from "@/components/common";

export interface DecisionPanelProps {
  decision: BookingDecision;
  className?: string;
}

// objectiveLabel humanizes a placement objective enum.
function objectiveLabel(objective: string): string {
  return phaseLabel(objective);
}

interface ConnectionGroupProps {
  label: string;
  ids: string[] | undefined;
  tone: string;
}

function ConnectionGroup({ label, ids, tone }: ConnectionGroupProps) {
  const list = ids ?? [];
  return (
    <div className="flex flex-col gap-1">
      <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
        {label} ({list.length})
      </span>
      {list.length === 0 ? (
        <span className="text-xs text-muted-foreground">—</span>
      ) : (
        <div className="flex flex-wrap gap-1">
          {list.map((id) => (
            <span
              key={id}
              className={cn(
                "rounded border px-1.5 py-0.5 font-mono text-[0.6875rem]",
                tone,
              )}
            >
              {id}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * DecisionPanel summarizes a BookingDecision: the selected offer, the policy
 * objective and constraints, the model version, the human-readable selection
 * reason codes, and the collection report (which connections were queried,
 * served from cache, or excluded). It pairs with CandidateTable to answer
 * "what did the broker decide, and why".
 */
export function DecisionPanel({ decision, className }: DecisionPanelProps) {
  const { policy, collection_report: report } = decision;
  const selected = decision.selected_offer_snapshot_id;

  return (
    <div className={cn("flex flex-col", className)}>
      {/* Headline: selected offer + objective, with reason codes inline. */}
      <div className="flex flex-col gap-4 pb-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-2">
            <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
              Selected offer
            </span>
            {selected ? (
              <div className="flex items-center gap-1.5">
                <span className="truncate font-mono text-base font-medium text-primary">
                  {selected}
                </span>
                <CopyButton value={selected} label="Copy offer id" />
              </div>
            ) : (
              <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
                <CircleSlash className="size-4" />
                No offer selected
              </span>
            )}
            {decision.selection_reason_codes.length > 0 ? (
              <div className="flex flex-wrap gap-1.5 pt-0.5">
                {decision.selection_reason_codes.map((code) => (
                  <Badge
                    key={code}
                    variant="outline"
                    className="border-border font-mono text-[0.6875rem] text-muted-foreground"
                  >
                    {code}
                  </Badge>
                ))}
              </div>
            ) : null}
          </div>
          <Badge className="border-primary/30 bg-primary/10 text-primary">
            {objectiveLabel(policy.objective)}
          </Badge>
        </div>

        <div className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-4">
          <StatBlock label="Model version" value={decision.model_version} mono />
          <StatBlock
            label="Candidates"
            value={decision.candidates.length}
            mono
          />
          <StatBlock
            label="Evaluated"
            value={<RelativeTime iso={decision.evaluated_at} className="text-foreground" />}
          />
          <StatBlock
            label="Revision digest"
            value={shortDigest(decision.workload_revision_digest, 12)}
            mono
            trailing={
              decision.workload_revision_digest ? (
                <CopyButton
                  value={decision.workload_revision_digest}
                  label="Copy digest"
                />
              ) : undefined
            }
          />
          {policy.max_p90_start_seconds !== undefined ? (
            <StatBlock
              label="Max p90 start"
              value={`${policy.max_p90_start_seconds}s`}
              mono
            />
          ) : null}
          {policy.max_expected_cost_usd !== undefined ? (
            <StatBlock
              label="Max expected cost"
              value={`$${policy.max_expected_cost_usd}`}
              mono
            />
          ) : null}
        </div>
      </div>

      {/* Collection report */}
      <div className="flex flex-col gap-3 border-t pt-5">
        <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
          Collection report
        </span>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <ConnectionGroup
            label="Queried"
            ids={report.connections_queried}
            tone="border-phase-running/30 bg-phase-running/10 text-phase-running"
          />
          <ConnectionGroup
            label="From cache"
            ids={report.connections_from_cache}
            tone="border-phase-launching/30 bg-phase-launching/10 text-phase-launching"
          />
          <ConnectionGroup
            label="Excluded"
            ids={report.excluded_connections}
            tone="border-phase-cancelled/30 bg-phase-cancelled/10 text-muted-foreground"
          />
        </div>
      </div>
    </div>
  );
}
