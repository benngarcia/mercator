// PROTOTYPE B: horizontal time lanes that make expected and maximum bounds primary.

import { ArrowUpRight, Clock3, Server, TimerReset } from "lucide-react";

import { cn } from "@/lib/utils";

import {
  runtimePercent,
  type PrototypeRental,
  type PrototypeRun,
  type PrototypeWorkspace,
} from "./data";

function TimeBlock({ run, workspaceID }: { run: PrototypeRun; workspaceID: string }) {
  return (
    <a
      href={`/runs/${run.id}?workspace_id=${workspaceID}`}
      className={cn(
        "group relative block h-[4.5rem] min-w-[7rem] overflow-hidden rounded-lg border bg-card shadow-sm transition hover:border-primary/40 hover:shadow-md",
        run.phase === "running" && "border-phase-running/35",
        run.phase === "provisioning" && "border-phase-launching/40",
      )}
      aria-label={`Open decision evidence for ${run.id}`}
    >
      <div
        className={cn(
          "absolute inset-y-0 left-0 opacity-10",
          run.phase === "provisioning" ? "bg-phase-launching" : "bg-phase-running",
        )}
        style={{ width: `${runtimePercent(run)}%` }}
      />
      <div className="relative flex h-full flex-col justify-between p-2.5">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="truncate text-xs font-semibold">{run.label}</p>
            <p className="truncate font-mono text-[0.5625rem] text-muted-foreground">
              {run.id}
            </p>
          </div>
          <ArrowUpRight className="size-3 shrink-0 text-label-3 group-hover:text-primary" />
        </div>
        <div className="flex items-center justify-between font-mono text-[0.625rem]">
          <span className="text-phase-running">p50 {run.expectedMinutes}m</span>
          <span className="text-muted-foreground">max {run.maxMinutes}m</span>
        </div>
      </div>
    </a>
  );
}

function RentalLane({ rental, workspaceID }: { rental: PrototypeRental; workspaceID: string }) {
  const schedule = rental.running ? [rental.running, ...rental.queued] : rental.queued;
  return (
    <div className="grid min-w-[64rem] grid-cols-[12rem_repeat(5,minmax(8.5rem,1fr))] border-t">
      <div className="sticky left-0 z-10 flex flex-col justify-center border-r bg-card/95 px-4 py-3 backdrop-blur">
        <div className="flex items-center gap-2">
          <span
            className={cn(
              "size-2 rounded-full",
              rental.phase === "active" ? "bg-phase-succeeded" : "animate-pulse bg-phase-launching",
            )}
          />
          <span className="truncate text-sm font-semibold">{rental.label}</span>
        </div>
        <span className="mt-1 truncate font-mono text-[0.625rem] text-muted-foreground">
          {rental.id}
        </span>
        <div className="mt-2 flex items-center gap-2 font-mono text-[0.625rem] text-muted-foreground">
          <span>{rental.queued.length}/{rental.queueLimit} queued</span>
          <span>·</span>
          <span>${rental.ratePerHourUSD.toFixed(2)}/h</span>
        </div>
      </div>
      {Array.from({ length: 5 }, (_, index) => {
        const run = schedule[index];
        return (
          <div
            key={run?.id ?? `empty-${index}`}
            className="relative min-h-[6.5rem] border-r border-dashed border-border/70 p-3"
          >
            {run ? (
              <>
                <span className="mb-1.5 block font-mono text-[0.5625rem] uppercase tracking-wide text-label-3">
                  {index === 0 ? rental.phase : `queue ${index} · starts +${run.projectedStartMinutes}m`}
                </span>
                <TimeBlock run={run} workspaceID={workspaceID} />
              </>
            ) : (
              <div className="flex h-full items-center justify-center text-[0.625rem] text-label-3">
                Queue slot {index || 1} available
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function IntakeLane({ workspace }: { workspace: PrototypeWorkspace }) {
  return (
    <div className="grid min-w-[64rem] grid-cols-[12rem_repeat(5,minmax(8.5rem,1fr))] bg-surface-2/35">
      <div className="sticky left-0 z-10 flex items-center gap-2 border-r bg-surface-2/95 px-4 py-3 text-xs font-semibold backdrop-blur">
        <TimerReset className="size-3.5 text-muted-foreground" /> Intake
      </div>
      <div className="col-span-5 p-3">
        {workspace.intake.length > 0 ? (
          <div className="max-w-56">
            <TimeBlock run={workspace.intake[0]!} workspaceID={workspace.id} />
          </div>
        ) : (
          <div className="flex h-[4.5rem] items-center rounded-lg border border-dashed px-4 text-xs text-muted-foreground">
            The intake lane is clear. The selected Run has entered the fleet schedule.
          </div>
        )}
      </div>
    </div>
  );
}

export function VariantB({ workspace }: { workspace: PrototypeWorkspace }) {
  return (
    <div className="min-h-[calc(100vh-12rem)] bg-background pb-20">
      <div className="flex items-center justify-between border-b px-5 py-3">
        <div>
          <h2 className="text-sm font-semibold">Schedule horizon</h2>
          <p className="text-xs text-muted-foreground">
            Block fill is expected runtime. The outlined width is the enforced maximum.
          </p>
        </div>
        <div className="flex items-center gap-1.5 rounded-lg border bg-card px-2.5 py-1.5 font-mono text-[0.625rem] text-muted-foreground">
          <Clock3 className="size-3" /> 00:00 scenario clock
        </div>
      </div>

      <div className="overflow-x-auto border-b">
        <div className="grid min-w-[64rem] grid-cols-[12rem_repeat(5,minmax(8.5rem,1fr))] bg-card/55 text-[0.625rem] font-medium uppercase tracking-wider text-muted-foreground">
          <div className="sticky left-0 z-10 border-r bg-card/95 px-4 py-2 backdrop-blur">Capacity</div>
          {["Now", "+2m", "+3m", "+4m", "+5m"].map((time) => (
            <div key={time} className="border-r border-dashed px-3 py-2 font-mono">
              {time}
            </div>
          ))}
        </div>
        <IntakeLane workspace={workspace} />
        {workspace.rentals.map((rental) => (
          <RentalLane key={rental.id} rental={rental} workspaceID={workspace.id} />
        ))}
      </div>

      <section className="bg-card/35 px-5 py-4">
        <div className="mb-2 flex items-center gap-2 text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
          <Server className="size-3.5" /> Marketplace Offers
        </div>
        {workspace.offers.map((offer) => (
          <div
            key={offer.id}
            className="grid grid-cols-[minmax(12rem,1fr)_repeat(3,8rem)] items-center rounded-lg border bg-card px-3 py-2 text-xs shadow-sm"
          >
            <div>
              <span className="font-mono">{offer.id}</span>
              <span className="ml-2 text-muted-foreground">{offer.provider}</span>
            </div>
            <div className="text-muted-foreground">
              Provision <span className="font-mono text-foreground">p50 {offer.provisionExpectedMinutes}m</span>
            </div>
            <div className="text-muted-foreground">
              p90 <span className="font-mono text-foreground">{offer.provisionP90Minutes}m</span>
            </div>
            <div className="text-right font-mono">${offer.ratePerHourUSD.toFixed(2)}/h</div>
          </div>
        ))}
      </section>
    </div>
  );
}

