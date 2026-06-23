import { Check, X } from "lucide-react";

import type { Run, RunPhase } from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { phaseLabel } from "@/lib/format";

export interface RunPhaseTimelineProps {
  run: Run;
  className?: string;
}

// The closed V1 lifecycle, in order. `closed` is the authoritative terminal
// flag; the final visible step is rendered as the run's outcome.
const PHASES: RunPhase[] = [
  "requested",
  "launching",
  "running",
  "cleaning_up",
  "closed",
];

const PHASE_INDEX: Record<string, number> = {
  requested: 0,
  launching: 1,
  running: 2,
  cleaning_up: 3,
  closed: 4,
};

type StepStatus = "done" | "current" | "upcoming" | "failed";

// stepStatus decides each step's state from the run's current phase / closed
// flag. A failed/cancelled run marks its terminal step accordingly so the
// stepper reads as a clear timeline of what happened.
function stepStatus(
  stepIndex: number,
  currentIndex: number,
  run: Run,
): StepStatus {
  if (run.closed) {
    if (stepIndex === PHASES.length - 1) {
      return run.outcome === "failed" || run.outcome === "cancelled"
        ? "failed"
        : "done";
    }
    return "done";
  }
  if (stepIndex < currentIndex) return "done";
  if (stepIndex === currentIndex) return "current";
  return "upcoming";
}

const TONE: Record<StepStatus, { node: string; line: string; text: string }> = {
  done: {
    node: "border-phase-succeeded bg-phase-succeeded text-background",
    line: "bg-phase-succeeded",
    text: "text-foreground",
  },
  current: {
    node: "border-primary bg-primary/15 text-primary",
    line: "bg-border",
    text: "text-foreground",
  },
  upcoming: {
    node: "border-border bg-muted/40 text-muted-foreground",
    line: "bg-border",
    text: "text-muted-foreground",
  },
  failed: {
    node: "border-phase-failed bg-phase-failed text-background",
    line: "bg-phase-failed",
    text: "text-phase-failed",
  },
};

function stepLabel(phase: RunPhase, run: Run): string {
  if (phase === "closed" && run.closed && run.outcome) {
    return phaseLabel(run.outcome);
  }
  return phaseLabel(phase);
}

/**
 * RunPhaseTimeline is the horizontal stepper across the five lifecycle phases
 * (requested → launching → running → cleaning_up → closed). The final step
 * resolves to the run's outcome and turns red for failed/cancelled runs.
 */
export function RunPhaseTimeline({ run, className }: RunPhaseTimelineProps) {
  const currentIndex = run.closed
    ? PHASES.length - 1
    : (PHASE_INDEX[run.phase] ?? 0);
  const failedTerminal =
    run.closed && (run.outcome === "failed" || run.outcome === "cancelled");
  // Each cell is flex-1 with the node centered, so node centers sit at
  // (i + 0.5)/n of the width → 10%…90% for five steps. A single track spans the
  // first to the last node center; the progress line fills to the current step.
  const edge = 100 / (PHASES.length * 2); // 10%
  const progress =
    (currentIndex / (PHASES.length - 1)) * (100 - edge * 2); // 0…80%
  const progressTone = !run.closed
    ? "bg-primary"
    : failedTerminal
      ? "bg-phase-failed"
      : "bg-phase-succeeded";

  return (
    <div className={cn("relative", className)}>
      {/* Track + progress, vertically centered on the 28px (size-7) nodes. */}
      <div
        className="pointer-events-none absolute top-3.5 h-0.5 -translate-y-1/2 rounded-full bg-border"
        style={{ left: `${edge}%`, right: `${edge}%` }}
        aria-hidden
      />
      <div
        className={cn(
          "pointer-events-none absolute top-3.5 h-0.5 -translate-y-1/2 rounded-full transition-all",
          progressTone,
        )}
        style={{ left: `${edge}%`, width: `${progress}%` }}
        aria-hidden
      />
      <ol className="relative flex">
        {PHASES.map((phase, i) => {
          const status = stepStatus(i, currentIndex, run);
          const tone = TONE[status];
          return (
            <li key={phase} className="flex flex-1 flex-col items-center gap-2">
              <span
                className={cn(
                  "relative z-10 flex size-7 items-center justify-center rounded-full border text-xs font-medium",
                  tone.node,
                )}
              >
                {status === "done" ? (
                  <Check className="size-3.5" />
                ) : status === "failed" ? (
                  <X className="size-3.5" />
                ) : (
                  i + 1
                )}
              </span>
              <span
                className={cn(
                  "whitespace-nowrap text-xs font-medium",
                  tone.text,
                  status === "current" && "animate-pulse",
                )}
              >
                {stepLabel(phase, run)}
              </span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
