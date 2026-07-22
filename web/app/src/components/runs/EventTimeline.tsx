import * as React from "react";
import { ChevronRight, Radio } from "lucide-react";

import type { CloudEvent } from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { humanizeEventType } from "@/lib/format";
import { JsonViewer, RelativeTime, EmptyState, CopyButton } from "@/components/common";

export interface EventTimelineProps {
  events: CloudEvent[];
  className?: string;
  isLoading?: boolean;
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
}

function EventRow({ event, isLast }: EventRowProps) {
  const [open, setOpen] = React.useState(false);
  const tone = toneForType(event.type);
  const hasData =
    event.data !== null &&
    event.data !== undefined &&
    !(typeof event.data === "object" && Object.keys(event.data).length === 0);

  return (
    <li className="relative flex gap-3 pb-3 last:pb-0">
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

        {hasData && open ? (
          <div className="mt-2 space-y-2">
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
        />
      ))}
    </ol>
  );
}
