import { cn } from "@/lib/utils";
import { phaseLabel } from "@/lib/format";
import type { RunOutcome } from "@/lib/api/types";

export interface RunStatusBadgeProps {
  /** Run lifecycle phase (free string; the V1 set is requested..closed). */
  phase: string;
  /** Terminal outcome, present once the run is closed. */
  outcome?: RunOutcome;
  /** Authoritative terminal flag. When closed, the outcome drives the color. */
  closed: boolean;
  className?: string;
}

// Each phase / outcome maps to a CSS phase color token (see index.css). We use
// the token both as a left-edge dot and as a tinted background so the badge
// reads at a glance in dense tables.
type PhaseTone =
  | "requested"
  | "launching"
  | "running"
  | "cleaning_up"
  | "succeeded"
  | "failed"
  | "cancelled";

const TONE_CLASS: Record<PhaseTone, { dot: string; chip: string }> = {
  requested: {
    dot: "bg-phase-requested",
    chip: "border-phase-requested/30 bg-phase-requested/10 text-phase-requested",
  },
  launching: {
    dot: "bg-phase-launching",
    chip: "border-phase-launching/30 bg-phase-launching/10 text-phase-launching",
  },
  running: {
    dot: "bg-phase-running",
    chip: "border-phase-running/30 bg-phase-running/10 text-phase-running",
  },
  cleaning_up: {
    dot: "bg-phase-cleaning_up",
    chip: "border-phase-cleaning_up/30 bg-phase-cleaning_up/10 text-phase-cleaning_up",
  },
  succeeded: {
    dot: "bg-phase-succeeded",
    chip: "border-phase-succeeded/30 bg-phase-succeeded/10 text-phase-succeeded",
  },
  failed: {
    dot: "bg-phase-failed",
    chip: "border-phase-failed/30 bg-phase-failed/10 text-phase-failed",
  },
  cancelled: {
    dot: "bg-phase-cancelled",
    chip: "border-phase-cancelled/30 bg-phase-cancelled/10 text-phase-cancelled",
  },
};

const OUTCOME_LABEL: Record<RunOutcome, string> = {
  succeeded: "Succeeded",
  failed: "Failed",
  cancelled: "Cancelled",
};

// resolve picks the visible label + tone. A closed run shows its outcome (with
// outcome-tinted color); an open run shows its phase with the phase color.
function resolve(
  phase: string,
  closed: boolean,
  outcome?: RunOutcome,
): { label: string; tone: PhaseTone; pulse: boolean } {
  if (closed) {
    if (outcome) {
      return { label: OUTCOME_LABEL[outcome], tone: outcome, pulse: false };
    }
    // Closed with no outcome recorded: render as a neutral terminal state.
    return { label: phaseLabel(phase) || "Closed", tone: "cancelled", pulse: false };
  }
  const known: PhaseTone | undefined = (
    ["requested", "launching", "running", "cleaning_up"] as const
  ).find((p) => p === phase);
  const tone: PhaseTone = known ?? "requested";
  // Active intermediate phases pulse the dot to signal liveness.
  const pulse = phase === "launching" || phase === "running" || phase === "cleaning_up";
  return { label: phaseLabel(phase), tone, pulse };
}

/**
 * RunStatusBadge renders the run's lifecycle state using the phase color
 * system. Open runs show the phase (with a pulsing dot for active phases);
 * closed runs show the terminal outcome in its own color (succeeded·emerald,
 * failed·red, cancelled·zinc).
 */
export function RunStatusBadge({
  phase,
  outcome,
  closed,
  className,
}: RunStatusBadgeProps) {
  const { label, tone, pulse } = resolve(phase, closed, outcome);
  const tones = TONE_CLASS[tone];
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-0.5 text-xs font-medium",
        tones.chip,
        className,
      )}
    >
      <span className="relative flex size-1.5">
        {pulse ? (
          <span
            className={cn(
              "absolute inline-flex size-full animate-ping rounded-full opacity-75",
              tones.dot,
            )}
          />
        ) : null}
        <span
          className={cn("relative inline-flex size-1.5 rounded-full", tones.dot)}
        />
      </span>
      {label}
    </span>
  );
}
