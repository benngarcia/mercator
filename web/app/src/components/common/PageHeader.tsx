import * as React from "react";

import { cn } from "@/lib/utils";

export interface PageHeaderProps {
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Right-aligned actions (buttons, switchers). */
  actions?: React.ReactNode;
  className?: string;
}

/**
 * PageHeader is the consistent top band for every page: a title, optional
 * supporting description, and a right-aligned action slot. It wraps gracefully
 * on narrow viewports.
 */
export function PageHeader({
  title,
  description,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <header
      className={cn(
        "flex flex-col gap-3 border-b border-border pb-4 sm:flex-row sm:items-start sm:justify-between",
        className,
      )}
    >
      <div className="flex min-w-0 flex-col gap-1">
        <h1 className="text-xl font-semibold tracking-tight text-foreground">
          {title}
        </h1>
        {description ? (
          <p className="max-w-2xl text-sm text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex shrink-0 items-center gap-2">{actions}</div>
      ) : null}
    </header>
  );
}
