// PROTOTYPE A: spatial fleet board with explicit intake, Rental nodes, and Offer dock.

import {
  ArrowUpRight,
  Box,
  CircleGauge,
  Clock3,
  Layers3,
  Server,
} from "lucide-react";

import { cn } from "@/lib/utils";

import {
  runtimePercent,
  type PrototypeOffer,
  type PrototypeRental,
  type PrototypeRun,
  type PrototypeWorkspace,
} from "./data";

function RuntimeGauge({ run }: { run: PrototypeRun }) {
  return (
    <div className="mt-2">
      <div className="mb-1 flex items-center justify-between font-mono text-[0.625rem] text-muted-foreground">
        <span>p50 {run.expectedMinutes}m</span>
        <span>max {run.maxMinutes}m</span>
      </div>
      <div className="relative h-1.5 overflow-hidden rounded-full border border-phase-running/25 bg-background">
        <div
          className="absolute inset-y-0 left-0 rounded-full bg-phase-running"
          style={{ width: `${runtimePercent(run)}%` }}
        />
      </div>
    </div>
  );
}

function RunCard({ run, workspaceID }: { run: PrototypeRun; workspaceID: string }) {
  const queued = run.phase === "queued";
  return (
    <a
      href={`/runs/${run.id}?workspace_id=${workspaceID}`}
      className={cn(
        "group block rounded-xl border bg-card p-3 shadow-sm transition hover:-translate-y-0.5 hover:border-primary/30 hover:shadow-md",
        run.phase === "running" && "border-phase-running/30 bg-phase-running/[0.045]",
        run.phase === "provisioning" && "border-phase-launching/35 bg-phase-launching/[0.05]",
      )}
      aria-label={`Open decision evidence for ${run.id}`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate text-xs font-semibold">{run.label}</p>
          <p className="mt-0.5 truncate font-mono text-[0.625rem] text-muted-foreground">
            {run.id}
          </p>
        </div>
        <ArrowUpRight className="size-3.5 shrink-0 text-label-3 transition group-hover:text-primary" />
      </div>
      {queued && (
        <div className="mt-2 flex items-center gap-1 text-[0.6875rem] font-medium text-primary">
          <Clock3 className="size-3" />
          Starts in {run.projectedStartMinutes}m
        </div>
      )}
      <RuntimeGauge run={run} />
    </a>
  );
}

function RentalNode({ rental, workspaceID }: { rental: PrototypeRental; workspaceID: string }) {
  return (
    <section
      className={cn(
        "flex w-[19rem] shrink-0 flex-col overflow-hidden rounded-2xl border bg-card shadow-sm",
        rental.phase === "provisioning" && "border-phase-launching/40",
      )}
    >
      <header className="flex items-start justify-between gap-3 border-b bg-surface-2/55 px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span
              className={cn(
                "size-2 rounded-full",
                rental.phase === "active" ? "bg-phase-succeeded" : "animate-pulse bg-phase-launching",
              )}
            />
            <h2 className="truncate text-sm font-semibold">{rental.label}</h2>
          </div>
          <p className="mt-1 truncate font-mono text-[0.625rem] text-muted-foreground">
            {rental.id}
          </p>
        </div>
        <span className="rounded-md border bg-background px-1.5 py-0.5 font-mono text-[0.625rem] text-muted-foreground">
          {rental.provider}
        </span>
      </header>

      <div className="border-b px-3 py-3">
        <div className="mb-2 flex items-center justify-between text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
          <span>{rental.phase === "provisioning" ? "Provisioning" : "Running"}</span>
          <CircleGauge className="size-3.5" />
        </div>
        {rental.running ? (
          <RunCard run={rental.running} workspaceID={workspaceID} />
        ) : (
          <div className="rounded-xl border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">
            Idle
          </div>
        )}
      </div>

      <div className="flex-1 px-3 py-3">
        <div className="mb-2 flex items-center justify-between">
          <span className="text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
            Queue
          </span>
          <span className="font-mono text-[0.6875rem] text-muted-foreground">
            {rental.queued.length} / {rental.queueLimit}
          </span>
        </div>
        <div className="space-y-2">
          {Array.from({ length: rental.queueLimit }, (_, index) => {
            const run = rental.queued[index];
            return run ? (
              <div key={run.id} className="relative pl-5">
                <span className="absolute left-0 top-3 font-mono text-[0.625rem] text-label-3">
                  {index + 1}
                </span>
                <RunCard run={run} workspaceID={workspaceID} />
              </div>
            ) : (
              <div
                key={`empty-${index}`}
                className="flex h-8 items-center gap-2 rounded-lg border border-dashed px-2 text-[0.625rem] text-label-3"
              >
                <span className="w-3 text-center font-mono">{index + 1}</span>
                Available
              </div>
            );
          })}
        </div>
      </div>

      <footer className="flex items-center justify-between border-t bg-surface-2/35 px-4 py-2 text-[0.625rem] text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <Layers3 className="size-3" /> {rental.cacheLabel}
        </span>
        <span className="font-mono">${rental.ratePerHourUSD.toFixed(2)}/h</span>
      </footer>
    </section>
  );
}

function OfferCard({ offer }: { offer: PrototypeOffer }) {
  return (
    <div
      className={cn(
        "flex min-w-64 items-center gap-3 rounded-xl border bg-card/65 px-3 py-2.5",
        offer.selected && "border-phase-launching/30 bg-phase-launching/[0.035]",
      )}
    >
      <div className="flex size-8 items-center justify-center rounded-lg bg-surface-2 text-muted-foreground">
        <Server className="size-4" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-mono text-[0.6875rem]">{offer.id}</span>
          {offer.selected && (
            <span className="rounded bg-phase-launching/10 px-1.5 py-0.5 text-[0.5625rem] font-semibold uppercase tracking-wide text-phase-launching">
              selected
            </span>
          )}
        </div>
        <p className="mt-0.5 text-[0.625rem] text-muted-foreground">
          {offer.provider} · p50 {offer.provisionExpectedMinutes}m · p90 {offer.provisionP90Minutes}m
        </p>
      </div>
      <span className="font-mono text-[0.6875rem]">${offer.ratePerHourUSD.toFixed(2)}/h</span>
    </div>
  );
}

export function VariantA({ workspace }: { workspace: PrototypeWorkspace }) {
  return (
    <div className="flex min-h-[calc(100vh-12rem)] flex-col bg-background pb-20">
      <div className="grid flex-1 grid-cols-[12rem_minmax(0,1fr)]">
        <aside className="border-r bg-surface-2/30 p-3">
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-1.5 text-xs font-semibold">
              <Box className="size-3.5 text-muted-foreground" /> Intake
            </div>
            <span className="rounded-full bg-surface-3 px-2 py-0.5 font-mono text-[0.625rem] text-muted-foreground">
              {workspace.intake.length}
            </span>
          </div>
          {workspace.intake.length > 0 ? (
            workspace.intake.map((run) => (
              <RunCard key={run.id} run={run} workspaceID={workspace.id} />
            ))
          ) : (
            <div className="rounded-xl border border-dashed p-4 text-center text-xs leading-relaxed text-muted-foreground">
              New Runs appear here before the Broker decides.
            </div>
          )}
        </aside>

        <main className="min-w-0 p-4">
          <div className="mb-3 flex items-center justify-between">
            <div>
              <h2 className="text-sm font-semibold">Rental fleet</h2>
              <p className="text-xs text-muted-foreground">Live schedules in this Workspace</p>
            </div>
            <div className="flex items-center gap-2 text-[0.6875rem] text-muted-foreground">
              <span className="size-2 rounded-full bg-phase-running" /> p50
              <span className="ml-1 h-2 w-px bg-border" />
              <span className="h-2 w-4 rounded-full border border-phase-running/30" /> enforced max
            </div>
          </div>
          <div className="flex min-h-[30rem] gap-4 overflow-x-auto pb-3">
            {workspace.rentals.map((rental) => (
              <RentalNode key={rental.id} rental={rental} workspaceID={workspace.id} />
            ))}
          </div>
        </main>
      </div>

      <section className="material sticky bottom-0 z-20 border-t px-4 py-3">
        <div className="mb-2 flex items-center gap-2 text-[0.6875rem] font-medium uppercase tracking-wider text-muted-foreground">
          <Server className="size-3.5" /> Marketplace Offers
        </div>
        <div className="flex gap-3 overflow-x-auto">
          {workspace.offers.map((offer) => (
            <OfferCard key={offer.id} offer={offer} />
          ))}
        </div>
      </section>
    </div>
  );
}
