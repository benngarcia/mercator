import type { WorkloadRevision } from "@/lib/api/types";
import { bytes, shortDigest, usd } from "@/lib/format";
import { cn } from "@/lib/utils";
import { StatBlock } from "@/components/common/StatBlock";
import { CopyButton } from "@/components/common/CopyButton";
import { JsonViewer } from "@/components/common/JsonViewer";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";

export interface RevisionViewerProps {
  revision: WorkloadRevision;
  className?: string;
}

const OBJECTIVE_LABELS: Record<string, string> = {
  cheapest: "Cheapest",
  fastest_start: "Fastest start",
  fastest_completion: "Fastest completion",
  balanced: "Balanced",
};

/**
 * RevisionViewer renders a human spec summary of a workload revision —
 * identity, container images/ports, resource requirements, network and
 * placement policy — followed by the full raw spec in a JsonViewer for the
 * complete, copyable source of truth.
 */
export function RevisionViewer({ revision, className }: RevisionViewerProps) {
  const spec = revision.spec;
  const containers = spec?.containers ?? [];
  const resources = spec?.resources;
  const placement = spec?.placement;
  const network = spec?.network;
  const execution = spec?.execution;

  const accelerators = resources?.accelerators ?? [];

  return (
    <div className={cn("flex flex-col gap-5", className)}>
      <section className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <StatBlock
          label="Revision id"
          mono
          value={revision.id}
          trailing={<CopyButton value={revision.id} label="Copy revision id" />}
        />
        <StatBlock
          label="Workload id"
          mono
          value={revision.workload_id}
          trailing={
            <CopyButton value={revision.workload_id} label="Copy workload id" />
          }
        />
        <StatBlock
          label="Digest"
          mono
          value={revision.digest ? shortDigest(revision.digest) : undefined}
          trailing={
            revision.digest ? (
              <CopyButton value={revision.digest} label="Copy digest" />
            ) : undefined
          }
        />
        <StatBlock label="Workspace" mono value={revision.workspace_id} />
      </section>

      {containers.length > 0 ? (
        <section className="flex flex-col gap-2">
          <h4 className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
            Containers
          </h4>
          <div className="flex flex-col gap-2">
            {containers.map((c, i) => (
              <div
                key={`${c.name || "container"}-${i}`}
                className="rounded-md border border-border bg-card/60 p-3"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium text-foreground">
                    {c.name || `container ${i + 1}`}
                  </span>
                  {c.platform ? (
                    <Badge variant="outline" className="font-mono text-[0.6875rem]">
                      {c.platform.os}/{c.platform.architecture}
                    </Badge>
                  ) : null}
                </div>
                <div className="mt-1.5 flex items-center gap-1.5">
                  <span className="font-mono text-[0.8125rem] text-foreground">
                    {c.image}
                  </span>
                  <CopyButton value={c.image} label="Copy image" />
                </div>
                {c.args && c.args.length > 0 ? (
                  <p className="mt-1.5 truncate font-mono text-xs text-muted-foreground">
                    args: {c.args.join(" ")}
                  </p>
                ) : null}
                {c.ports && c.ports.length > 0 ? (
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    {c.ports.map((p, pi) => (
                      <Badge
                        key={`${p.name || "port"}-${pi}`}
                        variant="secondary"
                        className="font-mono text-[0.6875rem]"
                      >
                        {p.container_port}/{p.protocol} · {p.exposure}
                      </Badge>
                    ))}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        </section>
      ) : null}

      <Separator />

      <section className="grid grid-cols-2 gap-4 sm:grid-cols-3">
        <StatBlock
          label="CPU"
          mono
          value={
            resources?.cpu
              ? `${resources.cpu.min_millis} m`
              : undefined
          }
        />
        <StatBlock
          label="Memory"
          mono
          value={resources?.memory ? bytes(resources.memory.min_bytes) : undefined}
        />
        <StatBlock
          label="Ephemeral disk"
          mono
          value={
            resources?.ephemeral_disk
              ? bytes(resources.ephemeral_disk.min_bytes)
              : undefined
          }
        />
        {accelerators.map((a, i) => (
          <StatBlock
            key={`accel-${i}`}
            label={`Accelerator ${i + 1}`}
            mono
            value={`${a.count}× ${a.vendor} ${a.model_any_of?.join("/") ?? ""} · ${bytes(a.memory_min_bytes)}`}
          />
        ))}
      </section>

      <Separator />

      <section className="grid grid-cols-2 gap-4 sm:grid-cols-3">
        <StatBlock
          label="Objective"
          value={
            placement
              ? (OBJECTIVE_LABELS[placement.objective] ?? placement.objective)
              : undefined
          }
        />
        <StatBlock
          label="Max p90 start"
          mono
          value={
            placement?.max_p90_start_seconds !== undefined
              ? `${placement.max_p90_start_seconds}s`
              : undefined
          }
        />
        <StatBlock
          label="Max expected cost"
          mono
          value={
            placement?.max_expected_cost_usd !== undefined
              ? usd(placement.max_expected_cost_usd)
              : undefined
          }
        />
        <StatBlock
          label="Inbound network"
          value={network ? network.inbound : undefined}
        />
        <StatBlock
          label="Max runtime"
          mono
          value={
            execution?.max_runtime_seconds !== undefined
              ? `${execution.max_runtime_seconds}s`
              : undefined
          }
        />
        <StatBlock
          label="Max pre-start attempts"
          mono
          value={execution?.max_pre_start_attempts}
        />
      </section>

      <section className="flex flex-col gap-2">
        <h4 className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
          Raw spec
        </h4>
        <JsonViewer value={spec} collapsed />
      </section>
    </div>
  );
}
