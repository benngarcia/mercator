import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useNow } from "@/hooks/useNow";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";

export interface RelativeTimeProps {
  iso: string | null | undefined;
  className?: string;
  /** Re-render cadence in ms so "3m ago" stays fresh. Defaults to 30s. */
  refreshMs?: number;
}

const absoluteFormatter = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "medium",
});

/**
 * RelativeTime renders a humanized relative timestamp ("3m ago") that ticks
 * forward on an interval, with the full absolute timestamp in a tooltip. Uses
 * format.relativeTime as the single source of truth.
 */
export function RelativeTime({
  iso,
  className,
  refreshMs = 30_000,
}: RelativeTimeProps) {
  useNow(refreshMs);

  if (!iso) {
    return <span className={cn("text-muted-foreground", className)}>—</span>;
  }

  const date = new Date(iso);
  const valid = !Number.isNaN(date.getTime());

  const label = (
    <time
      dateTime={iso}
      className={cn("tabular text-muted-foreground", className)}
    >
      {relativeTime(iso)}
    </time>
  );

  if (!valid) return label;

  return (
    <Tooltip>
      <TooltipTrigger asChild>{label}</TooltipTrigger>
      <TooltipContent>{absoluteFormatter.format(date)}</TooltipContent>
    </Tooltip>
  );
}
