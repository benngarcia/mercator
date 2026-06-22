import { usd } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { PriceModel } from "@/lib/api/types";

export interface PriceTagProps {
  pricing: PriceModel | null | undefined;
  className?: string;
}

/**
 * PriceTag renders an offer's price model: the per-second rate as the primary
 * figure with a setup fee subline. When pricing is not `known` it degrades to a
 * muted "unknown pricing" so the operator can tell missing evidence apart from
 * a genuine $0 rate.
 */
export function PriceTag({ pricing, className }: PriceTagProps) {
  if (!pricing || !pricing.known) {
    return (
      <span
        className={cn(
          "font-mono text-[0.8125rem] text-muted-foreground",
          className,
        )}
      >
        unknown pricing
      </span>
    );
  }

  const hasSetup = pricing.setup_fee_usd > 0;

  return (
    <div className={cn("flex flex-col gap-0.5", className)}>
      <span className="font-mono tabular text-[0.8125rem] text-foreground">
        {usd(pricing.rate_per_second_usd)}
        <span className="text-muted-foreground">/s</span>
      </span>
      {hasSetup ? (
        <span className="font-mono tabular text-[0.6875rem] text-muted-foreground">
          {usd(pricing.setup_fee_usd)} setup
        </span>
      ) : null}
    </div>
  );
}
