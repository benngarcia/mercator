import { CircleSlash } from "lucide-react";

import type { StoredBookingDecision } from "@/lib/workspace/contracts";
import { cn } from "@/lib/utils";
import { phaseLabel, shortDigest } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import { StatBlock, CopyButton, RelativeTime } from "@/components/common";

// DecisionChainOf is a Run's decisions, oldest first, and it is a non-empty type
// on purpose: a Run nothing has decided about has no panel to draw, and stating
// that in the type is what keeps the answer that stands from being a value every
// reader below has to re-check for absence.
export type DecisionChainOf = readonly [StoredBookingDecision, ...StoredBookingDecision[]];

export interface DecisionPanelProps {
  // decisions is every decision recorded for this Run, oldest first. The panel is
  // given the chain rather than one decision because a decision is appended and
  // never rewritten: the answer that stands is the last entry, and the entries
  // before it are the answers it replaced and the only place a reader can see that
  // this Run was answered more than once.
  decisions: DecisionChainOf;
  className?: string;
}

// standingDecision is the answer that stands: the last entry of the chain.
export function standingDecision(decisions: DecisionChainOf): StoredBookingDecision {
  const [first, ...later] = decisions;
  return later.at(-1) ?? first;
}

// serviceClassLabel humanizes the class of work a Run said it is.
function serviceClassLabel(serviceClass: string): string {
  return phaseLabel(serviceClass);
}

// waitingRate is what this decision's weights say a second of waiting was worth
// to the Run, and to which moment it was counted. It is the exchange rate the
// score was computed over, so a reader who wants to know why the costliest
// machine won reads it here rather than inferring it.
function waitingRate(weights: StoredBookingDecision["weights"]): string | null {
  const start = weights.start_latency_usd_per_second ?? 0;
  const completion = weights.completion_latency_usd_per_second ?? 0;
  if (start > 0) return `$${start}/s to start`;
  if (completion > 0) return `$${completion}/s to finish`;
  return null;
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

// supersessionLabel humanizes why one decision replaced another.
function supersessionLabel(reason: string): string {
  if (reason === "PREVIOUS_LAUNCH_FAILED") return "the machine refused the launch";
  if (reason === "PREVIOUS_DECISION_SELECTED_NOTHING")
    return "the previous answer placed this run nowhere";
  return phaseLabel(reason);
}

interface DecisionChainProps {
  decisions: DecisionChainOf;
}

/**
 * DecisionChain lists every decision this Run has, newest first, with what each
 * one chose and, for the answers that replaced another, which one they replaced
 * and why. It is what a reader needs to tell a Run answered once from a Run whose
 * first answer was superseded, and it is the only place the answers that no longer
 * stand can be seen at all.
 */
function DecisionChain({ decisions }: DecisionChainProps) {
  return (
    <div className="flex flex-col gap-3 border-t pt-5">
      <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
        Decision chain ({decisions.length})
      </span>
      <ol className="flex flex-col gap-2">
        {decisions
          .map((entry, index) => ({ decision: entry, index }))
          .reverse()
          .map(({ decision, index }) => (
            <li
              key={decision.id}
              className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs"
            >
              <span className="font-mono text-muted-foreground">#{index + 1}</span>
              <span className="font-mono">{decision.id}</span>
              <span className="text-muted-foreground">
                {decision.selected_offer_snapshot_id
                  ? `chose ${decision.selected_offer_snapshot_id}`
                  : "chose nothing"}
              </span>
              <RelativeTime
                iso={decision.evaluated_at}
                className="text-muted-foreground"
              />
              {decision.supersedes ? (
                <span className="text-muted-foreground">
                  replaces{" "}
                  <span className="font-mono">{decision.supersedes}</span>
                  {decision.supersedes_reason
                    ? `, because ${supersessionLabel(decision.supersedes_reason)}`
                    : null}
                </span>
              ) : null}
            </li>
          ))}
      </ol>
    </div>
  );
}

/**
 * DecisionPanel summarizes the decision that stands for a Run: the selected
 * offer, the Run's service class and the rates it was scored at, the policy
 * constraints, the model version, the human-readable selection reason codes, and
 * the collection report (which connections were queried, served from cache, or
 * excluded). Beneath it, the chain names every answer this Run was given and what
 * each one replaced. It pairs with CandidateTable to answer "what did the broker
 * decide, and why".
 */
export function DecisionPanel({ decisions, className }: DecisionPanelProps) {
  const decision = standingDecision(decisions);
  const { policy, weights, collection_report: report } = decision;
  const waiting = waitingRate(weights);
  const selected = decision.selected_offer_snapshot_id;

  return (
    <div className={cn("flex flex-col", className)}>
      {/* Headline: selected offer + service class, with reason codes inline. */}
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
            {serviceClassLabel(policy.service_class)}
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
          {waiting !== null ? (
            <StatBlock label="Waiting priced at" value={waiting} mono />
          ) : null}
          {weights.uncertainty_penalty_usd !== undefined &&
          weights.uncertainty_penalty_usd > 0 ? (
            <StatBlock
              label="Doubt priced at"
              value={`$${weights.uncertainty_penalty_usd}/point`}
              mono
            />
          ) : null}
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

      <DecisionChain decisions={decisions} />

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
