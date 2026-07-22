// PROTOTYPE C: dense capacity matrix with one explicit column per schedule slot.

import { ArrowUpRight, Box, Server, ShieldCheck } from "lucide-react";

import { cn } from "@/lib/utils";

import type {
  PrototypeRental,
  PrototypeRun,
  PrototypeWorkspace,
} from "./data";

function MatrixRun({
  run,
  workspaceID,
  position,
}: {
  run: PrototypeRun;
  workspaceID: string;
  position: "running" | number;
}) {
  return (
    <a
      href={`/runs/${run.id}?workspace_id=${workspaceID}`}
      className={cn(
        "group flex min-h-[6.75rem] flex-col justify-between border-l-2 bg-card px-3 py-2.5 transition hover:bg-accent/55",
        position === "running" ? "border-l-phase-running" : "border-l-primary/45",
        run.phase === "provisioning" && "border-l-phase-launching bg-phase-launching/[0.035]",
      )}
      aria-label={`Open decision evidence for ${run.id}`}
    >
      <div>
        <div className="flex items-start justify-between gap-2">
          <p className="line-clamp-2 text-xs font-semibold leading-snug">{run.label}</p>
          <ArrowUpRight className="size-3 shrink-0 text-label-3 group-hover:text-primary" />
        </div>
        <p className="mt-1 truncate font-mono text-[0.5625rem] text-muted-foreground">{run.id}</p>
      </div>
      <div>
        {position !== "running" && (
          <p className="mb-1 text-[0.625rem] font-medium text-primary">
            Starts +{run.projectedStartMinutes}m
          </p>
        )}
        <div className="grid grid-cols-2 divide-x rounded-md border bg-background/70 text-center font-mono text-[0.625rem]">
          <div className="px-1 py-1">
            <span className="block text-[0.5rem] uppercase tracking-wide text-label-3">p50</span>
            {run.expectedMinutes}m
          </div>
          <div className="px-1 py-1">
            <span className="block text-[0.5rem] uppercase tracking-wide text-label-3">max</span>
            {run.maxMinutes}m
          </div>
        </div>
      </div>
    </a>
  );
}

function EmptySlot({ index }: { index: number }) {
  return (
    <div className="flex min-h-[6.75rem] flex-col items-center justify-center border-l border-dashed bg-surface-2/20 text-center text-[0.625rem] text-label-3">
      <span className="font-mono">Q{index}</span>
      <span className="mt-1">Available</span>
    </div>
  );
}

function RentalMatrixRow({
  rental,
  workspaceID,
}: {
  rental: PrototypeRental;
  workspaceID: string;
}) {
  return (
    <div className="grid min-w-[66rem] grid-cols-[13rem_repeat(5,minmax(8.5rem,1fr))_7rem] border-t bg-card/40">
      <div className="sticky left-0 z-10 flex flex-col justify-between border-r bg-card/95 p-3 backdrop-blur">
        <div>
          <div className="flex items-center gap-2">
            <span
              className={cn(
                "size-2 rounded-full",
                rental.phase === "active" ? "bg-phase-succeeded" : "animate-pulse bg-phase-launching",
              )}
            />
            <h3 className="truncate text-sm font-semibold">{rental.label}</h3>
          </div>
          <p className="mt-1 truncate font-mono text-[0.625rem] text-muted-foreground">{rental.id}</p>
        </div>
        <div className="mt-3 space-y-1 text-[0.625rem] text-muted-foreground">
          <p>{rental.provider} · ${rental.ratePerHourUSD.toFixed(2)}/h</p>
          <p>{rental.cacheLabel}</p>
        </div>
      </div>

      {rental.running ? (
        <MatrixRun run={rental.running} workspaceID={workspaceID} position="running" />
      ) : (
        <div className="flex min-h-[6.75rem] items-center justify-center bg-surface-2/20 text-xs text-muted-foreground">
          Idle
        </div>
      )}

      {Array.from({ length: rental.queueLimit }, (_, index) => {
        const run = rental.queued[index];
        return run ? (
          <MatrixRun
            key={run.id}
            run={run}
            workspaceID={workspaceID}
            position={index + 1}
          />
        ) : (
          <EmptySlot key={`empty-${index}`} index={index + 1} />
        );
      })}

      <div className="flex flex-col items-center justify-center border-l bg-surface-2/30 px-2 text-center">
        <span
          className={cn(
            "font-mono text-lg font-semibold",
            rental.queued.length === rental.queueLimit ? "text-phase-launching" : "text-foreground",
          )}
        >
          {rental.queued.length}/{rental.queueLimit}
        </span>
        <span className="text-[0.5625rem] uppercase tracking-wide text-muted-foreground">queue</span>
        {rental.queued.length === rental.queueLimit && (
          <span className="mt-1 rounded bg-phase-launching/10 px-1.5 py-0.5 text-[0.5rem] font-semibold uppercase tracking-wide text-phase-launching">
            full
          </span>
        )}
      </div>
    </div>
  );
}

function IntakeSummary({ workspace }: { workspace: PrototypeWorkspace }) {
  return (
    <section className="flex items-center gap-4 border-b bg-surface-2/35 px-5 py-3">
      <div className="flex items-center gap-2 text-xs font-semibold">
        <Box className="size-3.5 text-muted-foreground" /> Intake
        <span className="rounded-full bg-surface-3 px-2 py-0.5 font-mono text-[0.625rem] text-muted-foreground">
          {workspace.intake.length}
        </span>
      </div>
      {workspace.intake.length > 0 ? (
        workspace.intake.map((run) => (
          <a
            key={run.id}
            href={`/runs/${run.id}?workspace_id=${workspace.id}`}
            className="flex min-w-72 items-center justify-between rounded-lg border bg-card px-3 py-2 text-xs shadow-sm hover:border-primary/30"
          >
            <div>
              <span className="font-semibold">{run.label}</span>
              <span className="ml-2 font-mono text-[0.625rem] text-muted-foreground">{run.id}</span>
            </div>
            <span className="font-mono text-[0.625rem] text-muted-foreground">
              p50 {run.expectedMinutes}m / max {run.maxMinutes}m
            </span>
          </a>
        ))
      ) : (
        <p className="text-xs text-muted-foreground">No Runs are waiting for a decision.</p>
      )}
    </section>
  );
}

export function VariantC({ workspace }: { workspace: PrototypeWorkspace }) {
  return (
    <div className="min-h-[calc(100vh-12rem)] bg-background pb-20">
      <IntakeSummary workspace={workspace} />

      <section className="overflow-x-auto">
        <div className="grid min-w-[66rem] grid-cols-[13rem_repeat(5,minmax(8.5rem,1fr))_7rem] bg-card/75 text-[0.625rem] font-medium uppercase tracking-wider text-muted-foreground">
          <div className="sticky left-0 z-10 border-r bg-card px-3 py-2">Rental</div>
          <div className="px-3 py-2">Running</div>
          {[1, 2, 3, 4].map((position) => (
            <div key={position} className="border-l px-3 py-2">
              Queue {position}
            </div>
          ))}
          <div className="border-l px-2 py-2 text-center">Depth</div>
        </div>
        {workspace.rentals.map((rental) => (
          <RentalMatrixRow key={rental.id} rental={rental} workspaceID={workspace.id} />
        ))}
      </section>

      <div className="flex items-center gap-5 border-y bg-card/45 px-5 py-2 text-[0.625rem] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <span className="h-3 w-0.5 bg-phase-running" /> Running boundary
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-3 w-0.5 bg-primary/45" /> Queued boundary
        </span>
        <span className="inline-flex items-center gap-1.5">
          <ShieldCheck className="size-3" /> Max runtime is enforced
        </span>
      </div>

      <section className="px-5 py-4">
        <div className="mb-2 flex items-center gap-2 text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
          <Server className="size-3.5" /> Marketplace Offers
        </div>
        <div className="overflow-hidden rounded-xl border bg-card/55">
          {workspace.offers.map((offer) => (
            <div
              key={offer.id}
              className="grid grid-cols-[minmax(12rem,1fr)_7rem_7rem_7rem] items-center border-b px-3 py-2.5 text-xs last:border-b-0"
            >
              <div className="min-w-0">
                <span className="truncate font-mono">{offer.id}</span>
                <span className="ml-2 text-muted-foreground">{offer.provider}</span>
              </div>
              <span className="font-mono">p50 {offer.provisionExpectedMinutes}m</span>
              <span className="font-mono text-muted-foreground">p90 {offer.provisionP90Minutes}m</span>
              <span className="text-right font-mono">${offer.ratePerHourUSD.toFixed(2)}/h</span>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

