import { ArrowRight } from "lucide-react";

import type { Violation } from "@/lib/api/types";
import { cn } from "@/lib/utils";

export interface ViolationListProps {
  violations: Violation[];
  className?: string;
}

// renderValue coerces a Violation's required/offered value (any JSON shape)
// into a compact mono string for the required-vs-offered comparison.
function renderValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

/**
 * ViolationList is the rich "why was this offer rejected" view rendered inside
 * an infeasible candidate's expansion. Each violation shows its code + path,
 * the message, and a required → offered comparison so an operator can see
 * exactly which constraint the offer failed.
 */
export function ViolationList({ violations, className }: ViolationListProps) {
  if (violations.length === 0) {
    return (
      <p className={cn("text-xs text-muted-foreground", className)}>
        No specific rejections were recorded for this candidate.
      </p>
    );
  }
  return (
    <ul className={cn("flex flex-col gap-2", className)}>
      {violations.map((v, i) => {
        const hasComparison =
          v.required !== undefined || v.offered !== undefined;
        return (
          <li
            key={`${v.code}-${v.path}-${i}`}
            className="rounded-md border border-phase-failed/25 bg-phase-failed/[0.06] p-2.5"
          >
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
              <span className="font-mono text-xs font-semibold text-phase-failed">
                {v.code}
              </span>
              {v.path ? (
                <span className="font-mono text-[0.6875rem] text-muted-foreground">
                  {v.path}
                </span>
              ) : null}
            </div>
            {v.message ? (
              <p className="mt-1 text-xs leading-relaxed text-foreground">
                {v.message}
              </p>
            ) : null}
            {hasComparison ? (
              <div className="mt-2 flex flex-wrap items-center gap-2 text-[0.6875rem]">
                <div className="flex flex-col gap-0.5">
                  <span className="uppercase tracking-wider text-muted-foreground">
                    required
                  </span>
                  <span className="font-mono text-foreground">
                    {renderValue(v.required)}
                  </span>
                </div>
                <ArrowRight className="size-3 shrink-0 text-muted-foreground/60" />
                <div className="flex flex-col gap-0.5">
                  <span className="uppercase tracking-wider text-muted-foreground">
                    offered
                  </span>
                  <span className="font-mono text-phase-failed">
                    {renderValue(v.offered)}
                  </span>
                </div>
              </div>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
