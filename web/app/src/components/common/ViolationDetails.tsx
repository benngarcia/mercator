import type { Violation } from "@/lib/api/types";
import { cn } from "@/lib/utils";

export interface ViolationDetailsProps {
  violations: Violation[];
  className?: string;
}

function render(value: unknown): string {
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
 * ViolationDetails is the compact { code, path, required vs offered, message }
 * renderer used inside <ErrorState> for mutation/validation envelopes. (The
 * richer per-candidate "why was this offer rejected" ViolationList lives in
 * the runs/ feature area.)
 */
export function ViolationDetails({
  violations,
  className,
}: ViolationDetailsProps) {
  if (violations.length === 0) return null;
  return (
    <ul
      className={cn(
        "flex flex-col gap-2 rounded-md border border-border bg-card/60 p-3 text-left",
        className,
      )}
    >
      {violations.map((v, i) => (
        <li key={`${v.code}-${v.path}-${i}`} className="flex flex-col gap-0.5">
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
            <span className="font-mono text-xs font-medium text-destructive">
              {v.code}
            </span>
            {v.path ? (
              <span className="font-mono text-xs text-muted-foreground">
                {v.path}
              </span>
            ) : null}
          </div>
          {v.message ? (
            <span className="text-xs text-foreground">{v.message}</span>
          ) : null}
          {v.required !== undefined || v.offered !== undefined ? (
            <div className="flex flex-wrap gap-x-4 text-xs text-muted-foreground">
              {v.required !== undefined ? (
                <span>
                  required{" "}
                  <span className="font-mono text-foreground">
                    {render(v.required)}
                  </span>
                </span>
              ) : null}
              {v.offered !== undefined ? (
                <span>
                  offered{" "}
                  <span className="font-mono text-foreground">
                    {render(v.offered)}
                  </span>
                </span>
              ) : null}
            </div>
          ) : null}
        </li>
      ))}
    </ul>
  );
}
