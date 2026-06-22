import * as React from "react";
import { Cpu, HardDrive, MemoryStick, Zap } from "lucide-react";

import { bytes } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { ResourceInventory } from "@/lib/api/types";

export interface ResourceSummaryProps {
  resources: ResourceInventory | null | undefined;
  className?: string;
  /** Lay the facets out vertically (detail pane) instead of inline (table). */
  orientation?: "inline" | "stack";
}

// cpuMillis renders CPU millicores as whole cores when evenly divisible
// ("2 vCPU") and as a millicore figure otherwise ("1500m").
function cpuMillis(millis: number): string {
  if (millis <= 0) return "—";
  if (millis % 1000 === 0) {
    const cores = millis / 1000;
    return `${cores} vCPU`;
  }
  return `${millis}m`;
}

function Facet({
  icon: Icon,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
}) {
  return (
    <span className="inline-flex items-center gap-1 font-mono tabular text-[0.8125rem] text-foreground">
      <Icon className="size-3.5 shrink-0 text-muted-foreground" />
      {children}
    </span>
  );
}

/**
 * ResourceSummary condenses an offer's resource inventory — CPU millis, memory
 * and ephemeral disk (formatted via IEC bytes), and any accelerators — into a
 * dense, icon-prefixed row for tables or a stacked list for detail panes.
 */
export function ResourceSummary({
  resources,
  className,
  orientation = "inline",
}: ResourceSummaryProps) {
  if (!resources) {
    return <span className={cn("text-muted-foreground", className)}>—</span>;
  }

  const accelerators = resources.accelerators ?? [];

  return (
    <div
      className={cn(
        orientation === "stack"
          ? "flex flex-col gap-1.5"
          : "flex flex-wrap items-center gap-x-3 gap-y-1",
        className,
      )}
    >
      <Facet icon={Cpu}>{cpuMillis(resources.cpu_millis)}</Facet>
      <Facet icon={MemoryStick}>{bytes(resources.memory_bytes)}</Facet>
      <Facet icon={HardDrive}>{bytes(resources.ephemeral_disk_bytes)}</Facet>
      {accelerators.map((acc, i) => (
        <Facet key={`${acc.vendor}-${acc.model}-${i}`} icon={Zap}>
          {acc.count}× {acc.vendor} {acc.model}
          {acc.memory_bytes > 0 ? (
            <span className="text-muted-foreground">
              {" "}
              ({bytes(acc.memory_bytes)})
            </span>
          ) : null}
        </Facet>
      ))}
    </div>
  );
}
