import * as React from "react";
import { Inbox, type LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";

export interface EmptyStateProps {
  /** Defaults to an inbox glyph. */
  icon?: LucideIcon;
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Optional call-to-action (e.g. a "Create run" button). */
  action?: React.ReactNode;
  className?: string;
  /** Render compactly (e.g. inside a table cell). */
  compact?: boolean;
}

/**
 * EmptyState is the calm zero-data placeholder: a muted glyph, a title, an
 * optional description and an optional action. Used by DataTable's emptyState
 * slot and by pages with no resources yet.
 */
export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
  compact,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center text-center",
        compact ? "gap-2 py-8" : "gap-3 py-16",
        className,
      )}
    >
      <div
        className={cn(
          "flex items-center justify-center rounded-full border border-border bg-muted/40 text-muted-foreground",
          compact ? "size-9 [&_svg]:size-4" : "size-12 [&_svg]:size-5",
        )}
      >
        <Icon />
      </div>
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {description ? (
          <p className="mx-auto max-w-sm text-sm text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {action ? <div className="mt-1">{action}</div> : null}
    </div>
  );
}
