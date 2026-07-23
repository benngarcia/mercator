import * as React from "react";
import { ChevronRight, Radio } from "lucide-react";
import * as Result from "effect/Result";
import * as Schema from "effect/Schema";

import type { BookingDecision, CandidateDecision, CloudEvent } from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { humanizeEventType, usd } from "@/lib/format";
import { JsonViewer, RelativeTime, EmptyState, CopyButton } from "@/components/common";
import { BookingDecidedData } from "@/lib/workspace/contracts";

export interface EventTimelineProps {
  events: readonly CloudEvent[];
  className?: string;
  isLoading?: boolean;
  dense?: boolean;
  highlightLatest?: boolean;
}

// Map a humanized event family to a phase tone so the timeline color-tracks the
// run lifecycle. We key off substrings of the raw type for resilience to new
// event names.
function toneForType(type: string): string {
  const t = type.toLowerCase();
  if (t.includes("fail") || t.includes("error") || t.includes("reject")) {
    return "bg-phase-failed";
  }
  if (t.includes("cancel")) return "bg-phase-cancelled";
  if (t.includes("succeed") || t.includes("complete") || t.includes("confirm")) {
    return "bg-phase-succeeded";
  }
  if (t.includes("cleanup") || t.includes("clean_up") || t.includes("release")) {
    return "bg-phase-cleaning_up";
  }
  if (t.includes("start") || t.includes("running") || t.includes("launch")) {
    return "bg-phase-running";
  }
  if (t.includes("decided") || t.includes("booking") || t.includes("accepted")) {
    return "bg-phase-launching";
  }
  return "bg-phase-requested";
}

interface EventRowProps {
  event: CloudEvent;
  isLast: boolean;
  dense: boolean;
  highlighted: boolean;
}

function EventRow({ event, isLast, dense, highlighted }: EventRowProps) {
  const [open, setOpen] = React.useState(false);
  const tone = toneForType(event.type);
  const decision = decisionForEvent(event);
  const hasData =
    event.data !== null &&
    event.data !== undefined &&
    !(typeof event.data === "object" && Object.keys(event.data).length === 0);

  return (
    <li
      data-event-id={event.id}
      className={cn(
        "relative flex gap-3 rounded-md pb-3 last:pb-0",
        dense && "px-2 pt-2",
        highlighted && "bg-accent-soft",
      )}
    >
      {/* Rail + node */}
      <div className="flex flex-col items-center">
        <span
          className={cn(
            "mt-1 size-2.5 shrink-0 rounded-full ring-4 ring-background",
            tone,
          )}
        />
        {!isLast ? <span className="w-px flex-1 bg-border" /> : null}
      </div>

      <div className="min-w-0 flex-1">
        <button
          type="button"
          onClick={() => hasData && setOpen((v) => !v)}
          aria-expanded={hasData ? open : undefined}
          disabled={!hasData}
          className={cn(
            "group flex w-full items-baseline gap-2 rounded text-left",
            hasData && "cursor-pointer",
          )}
        >
          {hasData ? (
            <ChevronRight
              className={cn(
                "mt-0.5 size-3.5 shrink-0 text-muted-foreground transition-transform",
                open && "rotate-90",
              )}
            />
          ) : (
            <span className="mt-0.5 size-3.5 shrink-0" />
          )}
          <span className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2 gap-y-0.5">
            <span className="text-sm font-medium text-foreground group-hover:text-primary">
              {humanizeEventType(event.type)}
            </span>
            <span className="font-mono text-[0.6875rem] text-muted-foreground">
              #{event.globalposition}
            </span>
            {event.subject ? (
              <span className="truncate font-mono text-[0.6875rem] text-muted-foreground/80">
                {event.subject}
              </span>
            ) : null}
          </span>
          <RelativeTime
            iso={event.time}
            className="shrink-0 text-[0.6875rem] text-muted-foreground"
          />
        </button>

        {decision ? <SelectedDecision decision={decision} /> : null}

        {hasData && open ? (
          <div className="mt-2 space-y-2">
            {decision ? <CandidateEvidence candidates={decision.candidates} /> : null}
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[0.6875rem] text-muted-foreground">
              <span className="font-mono">{event.type}</span>
              <span className="flex items-center gap-1">
                <span className="text-muted-foreground/70">stream</span>
                <span className="font-mono text-foreground">
                  {event.streamversion}
                </span>
              </span>
              <span className="flex items-center gap-1 font-mono">
                {event.id}
                <CopyButton value={event.id} label="Copy event id" />
              </span>
            </div>
            <JsonViewer value={event.data} maxHeight="20rem" />
          </div>
        ) : null}
      </div>
    </li>
  );
}

function decisionForEvent(event: CloudEvent): BookingDecision | null {
  if (event.type !== "compute.run.booking_decided.v1") return null;
  const decoded = Schema.decodeUnknownResult(BookingDecidedData)(event.data);
  return Result.isSuccess(decoded) ? decoded.success.decision : null;
}

function SelectedDecision({ decision }: { decision: BookingDecision }) {
  const selected = decision.candidates.find(
    (candidate) => candidate.offer_snapshot_id === decision.selected_offer_snapshot_id,
  );
  if (!selected) return null;
  return (
    <div className="ml-5 mt-1.5 flex items-center gap-2 text-[0.6875rem]">
      <span className="rounded border border-primary/25 bg-primary/10 px-1.5 py-0.5 font-medium text-primary">
        {dispositionLabel(selected.disposition)}
      </span>
      <span className="font-mono text-muted-foreground">
        {selected.score_usd === undefined ? "no score" : `${usd(selected.score_usd)} score`}
      </span>
      <span className="text-muted-foreground">
        {decision.candidates.length} candidates
      </span>
    </div>
  );
}

function CandidateEvidence({ candidates }: { candidates: CandidateDecision[] }) {
  return (
    <div className="overflow-hidden rounded-md border bg-background/60">
      {candidates.map((candidate) => (
        <div
          key={candidate.offer_snapshot_id}
          className="grid grid-cols-[minmax(0,1fr)_auto] gap-2 border-b px-2 py-1.5 text-[0.6875rem] last:border-b-0"
        >
          <div className="min-w-0">
            <div className="truncate font-mono text-foreground">
              {candidate.offer_snapshot_id}
            </div>
            <div className="text-muted-foreground">
              {dispositionLabel(candidate.disposition)}
              {!candidate.feasible && candidate.rejections?.[0]
                ? ` · ${candidate.rejections[0].code}`
                : ""}
            </div>
          </div>
          <div className="self-center font-mono tabular text-muted-foreground">
            {candidate.feasible && candidate.score_usd !== undefined
              ? usd(candidate.score_usd)
              : "rejected"}
          </div>
        </div>
      ))}
    </div>
  );
}

function dispositionLabel(disposition: CandidateDecision["disposition"]): string {
  switch (disposition) {
    case "run_now_existing_rental":
      return "reuse now";
    case "queue_existing_rental":
      return "queue";
    case "provision_fresh_rental":
      return "fresh";
  }
}

/**
 * EventTimeline renders the public run CloudEvents as a vertical timeline. Each
 * row shows the humanized event type, its global position, subject, and
 * relative time; rows with a data payload expand to a JsonViewer of
 * event.data. Events are shown most-recent-first.
 */
export function EventTimeline({
  events,
  className,
  isLoading,
  dense = false,
  highlightLatest = false,
}: EventTimelineProps) {
  // Sort by global position descending so the latest event is on top; this is
  // a total order across the workspace stream.
  const ordered = React.useMemo(
    () => [...events].sort((a, b) => b.globalposition - a.globalposition),
    [events],
  );

  if (!isLoading && ordered.length === 0) {
    return (
      <EmptyState
        icon={Radio}
        title="No events yet"
        description="Run events stream here as the run progresses."
        compact
        className={className}
      />
    );
  }

  return (
    <ol className={cn("flex flex-col", className)}>
      {ordered.map((event, i) => (
        <EventRow
          key={`${event.globalposition}-${event.id}`}
          event={event}
          isLast={i === ordered.length - 1}
          dense={dense}
          highlighted={highlightLatest && i === 0}
        />
      ))}
    </ol>
  );
}
