import * as React from "react";

import { cn } from "@/lib/utils";

export interface StatBlockProps {
  label: string;
  value: React.ReactNode;
  /** Render the value in the mono stack with tabular numerics. */
  mono?: boolean;
  /** Optional trailing element (badge, copy button) aligned with the value. */
  trailing?: React.ReactNode;
  className?: string;
}

/**
 * StatBlock is the atomic label/value pair used across detail panes and
 * summary cards. Labels are small, uppercase, muted; values lean on the mono
 * stack for ids/digests/positions/scores when `mono` is set.
 */
export function StatBlock({
  label,
  value,
  mono,
  trailing,
  className,
}: StatBlockProps) {
  const empty = value === null || value === undefined || value === "";
  return (
    <div className={cn("flex flex-col gap-1", className)}>
      <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      <div className="flex min-w-0 items-center gap-1.5">
        <span
          className={cn(
            "min-w-0 truncate text-sm text-foreground",
            mono && "font-mono tabular text-[0.8125rem]",
            empty && "text-muted-foreground",
          )}
        >
          {empty ? "—" : value}
        </span>
        {trailing}
      </div>
    </div>
  );
}
