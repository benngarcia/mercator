import { Link } from "@tanstack/react-router";
import { Box, Cpu, Gauge, Snowflake, Zap } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { CloudEvent, OfferSnapshot } from "@/lib/api/types";
import { duration, usd } from "@/lib/format";
import { cn } from "@/lib/utils";
import type {
  Rental,
  Workspace,
  WorkspaceBooking,
  WorkspaceRun,
} from "@/lib/workspace";
import type {
  ScenarioFidelity,
  ScenarioPlaybackSnapshot,
} from "@/lib/workspace/playback";
import type { WorkspacePlaybackControls } from "@/lib/workspace/react";

import { ScenarioControls } from "./ScenarioControls";
import { WorkspaceEventFeed } from "./WorkspaceEventFeed";

const BASE_PIXELS_PER_MINUTE = 24;
const MINIMUM_RUN_WIDTH = 72;
const MINIMUM_HORIZON_MINUTES = 60;
const QUEUE_CAPACITY = 4;
const LANE_LABEL_WIDTH = 224;

export function WorkspaceCanvas({
  controls,
  events,
  fidelity,
  playback,
  workspace,
}: {
  controls: WorkspacePlaybackControls | null;
  events: readonly CloudEvent[];
  fidelity: ScenarioFidelity | null;
  playback: ScenarioPlaybackSnapshot | null;
  workspace: Workspace;
}) {
  const now = Date.now();
  const rentals = Object.values(workspace.rentals).sort((a, b) => {
    const sourceOrder = sourceRank(a) - sourceRank(b);
    return sourceOrder || a.id.localeCompare(b.id);
  });
  const incoming = Object.values(workspace.runs)
    .filter((run) => run.phase === "requested" && !run.bookingID)
    .sort((a, b) => a.requestedAt.localeCompare(b.requestedAt));
  const marketplace = workspace.offers.filter(
    (offer) => offer.kind === "provisionable",
  );
  const pixelsPerMinute = readablePixelsPerMinute(workspace);
  const horizonMinutes = workspaceHorizon(workspace, now);
  const timelineWidth = horizonMinutes * pixelsPerMinute;

  return (
    <div className="flex min-h-full flex-col">
      <div className="border-b px-5 py-4">
        <div className="flex items-center justify-between gap-4">
          <h1 className="text-base font-semibold tracking-tight">Workspace</h1>
          <div className="flex items-center gap-5">
            {playback && controls ? (
              <ScenarioControls playback={playback} controls={controls} />
            ) : null}
            <span className="font-mono text-xs text-muted-foreground">
              {workspace.id}
            </span>
          </div>
        </div>
      </div>

      <div className="flex min-h-0 flex-1">
        <div className="min-w-0 flex-1 overflow-auto">
          <div
            className="grid min-w-full"
            style={{
              gridTemplateColumns: `${LANE_LABEL_WIDTH}px minmax(${timelineWidth}px, 1fr)`,
            }}
          >
            <div className="sticky left-0 z-30 border-b border-r bg-background" />
            <TimeAxis
              horizonMinutes={horizonMinutes}
              pixelsPerMinute={pixelsPerMinute}
            />

            <LaneLabel title="Incoming">
              <span className="size-1.5 rounded-full bg-phase-requested" />
            </LaneLabel>
            <TimelineTrack
              horizonMinutes={horizonMinutes}
              pixelsPerMinute={pixelsPerMinute}
            >
              <div className="flex h-full items-center gap-2 px-3">
                {incoming.map((run) => (
                  <RunBlock
                    key={run.id}
                    run={run}
                    left={0}
                    maxSeconds={run.maxRuntimeSeconds}
                    expectedSeconds={run.expectedRuntimeSeconds}
                    pixelsPerMinute={pixelsPerMinute}
                    compact
                  />
                ))}
              </div>
            </TimelineTrack>

            {rentals.map((rental) => (
              <RentalLane
                key={rental.id}
                rental={rental}
                workspace={workspace}
                horizonMinutes={horizonMinutes}
                pixelsPerMinute={pixelsPerMinute}
                now={now}
              />
            ))}
          </div>

          <Marketplace
            offers={marketplace}
            available={workspace.offersAvailable}
          />
        </div>
        <WorkspaceEventFeed events={events} fidelity={fidelity} />
      </div>
    </div>
  );
}

function TimeAxis({
  horizonMinutes,
  pixelsPerMinute,
}: {
  horizonMinutes: number;
  pixelsPerMinute: number;
}) {
  const ticks = Array.from(
    { length: Math.floor(horizonMinutes / 10) + 1 },
    (_, index) => index * 10,
  );
  return (
    <div className="sticky top-0 z-20 h-10 border-b bg-background/95 backdrop-blur">
      {ticks.map((minutes) => (
        <div
          key={minutes}
          className="absolute inset-y-0 border-l border-border/70"
          style={{ left: minutes * pixelsPerMinute }}
        >
          <span className="absolute left-1.5 top-2 font-mono text-[10px] tabular text-muted-foreground">
            {minutes === 0 ? "now" : `+${minutes}m`}
          </span>
        </div>
      ))}
    </div>
  );
}

function LaneLabel({
  children,
  title,
}: {
  children?: ReactNode;
  title: string;
}) {
  return (
    <div className="sticky left-0 z-10 flex min-h-24 items-center gap-2 border-b border-r bg-background px-5">
      <span className="text-xs font-medium text-muted-foreground">{title}</span>
      {children}
    </div>
  );
}

function TimelineTrack({
  children,
  horizonMinutes,
  pixelsPerMinute,
}: {
  children: ReactNode;
  horizonMinutes: number;
  pixelsPerMinute: number;
}) {
  const ticks = Array.from(
    { length: Math.floor(horizonMinutes / 10) + 1 },
    (_, index) => index * 10,
  );
  return (
    <div className="relative min-h-24 overflow-hidden border-b">
      {ticks.map((minutes) => (
        <div
          key={minutes}
          className="pointer-events-none absolute inset-y-0 border-l border-border/50"
          style={{ left: minutes * pixelsPerMinute }}
        />
      ))}
      {children}
    </div>
  );
}

function RentalLane({
  horizonMinutes,
  now,
  pixelsPerMinute,
  rental,
  workspace,
}: {
  horizonMinutes: number;
  now: number;
  pixelsPerMinute: number;
  rental: Rental;
  workspace: Workspace;
}) {
  const running = rental.runningBookingID
    ? workspace.bookings[rental.runningBookingID]
    : undefined;
  const queued = rental.queuedBookingIDs
    .map((id) => workspace.bookings[id])
    .filter((booking): booking is WorkspaceBooking => Boolean(booking));
  const runningRun = running ? workspace.runs[running.runID] : undefined;
  const provision = rental.phase === "provisioning" ? rental.offer?.provisioning : undefined;
  const provisionExpected = provision?.expected ?? provision?.p50 ?? 0;
  const provisionMax = provision?.p90 ?? provisionExpected;
  let nextStartSeconds =
    provisionExpected > 0 ? provisionExpected : remainingExpected(runningRun, now);

  return (
    <>
      <div className="sticky left-0 z-10 min-h-28 border-b border-r bg-background px-5 py-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span
                className={cn(
                  "size-1.5 shrink-0 rounded-full",
                  rental.phase === "provisioning"
                    ? "bg-phase-launching"
                    : rental.phase === "active"
                      ? "bg-phase-running"
                      : "bg-muted-foreground",
                )}
              />
              <span className="truncate font-mono text-xs font-medium">
                {shortID(rental.id)}
              </span>
            </div>
            <RentalFacts offer={rental.offer} />
          </div>
          <QueueSlots count={queued.length} />
        </div>
      </div>
      <TimelineTrack
        horizonMinutes={horizonMinutes}
        pixelsPerMinute={pixelsPerMinute}
      >
        {provisionMax > 0 ? (
          <BoundedSpan
            leftSeconds={0}
            maxSeconds={provisionMax}
            expectedSeconds={provisionExpected}
            pixelsPerMinute={pixelsPerMinute}
            className="top-5 h-3 border-phase-launching/50 bg-phase-launching/10"
            fillClassName="bg-phase-launching/35"
            label={`Provisioning: expected ${duration(provisionExpected)}, p90 ${duration(provisionMax)}`}
          />
        ) : null}
        {running && runningRun ? (
          <RunBlock
            run={runningRun}
            left={
              rental.phase === "provisioning"
                ? provisionExpected
                : Math.max(0, secondsUntil(running.projectedStartAt, now))
            }
            maxSeconds={remainingMax(runningRun, now)}
            expectedSeconds={remainingExpected(runningRun, now)}
            pixelsPerMinute={pixelsPerMinute}
          />
        ) : null}
        {queued.map((booking) => {
          const run = workspace.runs[booking.runID];
          if (!run) return null;
          const left =
            secondsUntil(booking.projectedStartAt, now) || nextStartSeconds;
          nextStartSeconds = left + (run.expectedRuntimeSeconds ?? 0);
          return (
            <RunBlock
              key={booking.id}
              run={run}
              left={left}
              maxSeconds={run.maxRuntimeSeconds}
              expectedSeconds={run.expectedRuntimeSeconds}
              pixelsPerMinute={pixelsPerMinute}
              queued
            />
          );
        })}
      </TimelineTrack>
    </>
  );
}

function RentalFacts({ offer }: { offer?: OfferSnapshot }) {
  if (!offer) return null;
  const accelerator = offer.resources.accelerators?.[0];
  const hourly = offer.pricing.known
    ? usd(offer.pricing.rate_per_second_usd * 3600)
    : "—";
  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
      <span className="inline-flex items-center gap-1">
        {accelerator ? <Zap className="size-3" /> : <Cpu className="size-3" />}
        {accelerator
          ? `${accelerator.count}× ${accelerator.model}`
          : `${Math.max(1, Math.floor(offer.resources.cpu_millis / 1000))} vCPU`}
      </span>
      <span className="font-mono tabular">{hourly}/h</span>
      {offer.image_cache.known && offer.image_cache.manifest_cached ? (
        <Tooltip>
          <TooltipTrigger asChild>
            <Snowflake className="size-3.5 text-phase-running" aria-label="Image cached" />
          </TooltipTrigger>
          <TooltipContent>Selected image is cached</TooltipContent>
        </Tooltip>
      ) : null}
    </div>
  );
}

function QueueSlots({ count }: { count: number }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          className="flex gap-1"
          aria-label={`${count} of ${QUEUE_CAPACITY} Booking positions occupied`}
        >
          {Array.from({ length: QUEUE_CAPACITY }, (_, index) => (
            <span
              key={index}
              className={cn(
                "h-5 w-1.5 rounded-full border",
                index < count
                  ? "border-primary/50 bg-primary/45"
                  : "border-border bg-surface-2",
              )}
            />
          ))}
        </div>
      </TooltipTrigger>
      <TooltipContent>
        {count} of {QUEUE_CAPACITY} Booking positions occupied
      </TooltipContent>
    </Tooltip>
  );
}

function RunBlock({
  compact = false,
  expectedSeconds,
  left,
  maxSeconds,
  pixelsPerMinute,
  queued = false,
  run,
}: {
  compact?: boolean;
  expectedSeconds: number | null;
  left: number;
  maxSeconds: number;
  pixelsPerMinute: number;
  queued?: boolean;
  run: WorkspaceRun;
}) {
  const maxWidth = Math.max(24, (maxSeconds / 60) * pixelsPerMinute);
  const expectedWidth =
    expectedSeconds === null
      ? maxWidth
      : Math.max(24, (expectedSeconds / 60) * pixelsPerMinute);
  const width = compact ? Math.max(112, maxWidth) : expectedWidth;
  const fill =
    expectedSeconds === null || maxSeconds <= 0
      ? 0
      : Math.min(100, (expectedSeconds / maxSeconds) * 100);
  const image = run.workload.spec.containers[0]?.image ?? "unknown image";
  const label = `${run.id}: ${duration(expectedSeconds)} expected within ${duration(maxSeconds)} enforced maximum`;
  const style = {
    left: compact ? undefined : (left / 60) * pixelsPerMinute,
    width,
    viewTransitionName: `run-${transitionName(run.id)}`,
  } as CSSProperties;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Link
          to="/runs/$runId"
          params={{ runId: run.id }}
          search={true}
          aria-label={label}
          className={cn(
            "group z-[2] rounded-md outline-none transition-[filter]",
            "focus-visible:ring-2 focus-visible:ring-ring",
            compact
              ? "relative h-12 shrink-0"
              : "absolute top-10 h-12",
          )}
          style={style}
        >
          <span
            className={cn(
              "pointer-events-none absolute inset-y-0 left-0 z-0 rounded-md border border-dashed bg-primary/[0.025]",
              queued ? "border-primary/20" : "border-primary/30",
              run.phase === "cleaning" && "border-phase-cleaning_up/40",
            )}
            style={{ width: compact ? width : maxWidth }}
          />
          <span
            className={cn(
              "absolute inset-0 z-[1] overflow-hidden rounded-md border bg-card shadow-sm",
              queued ? "border-primary/25" : "border-primary/40",
              run.phase === "cleaning" && "border-phase-cleaning_up/50",
            )}
          >
            <span
              className={cn(
                "absolute inset-y-0 left-0",
                queued ? "bg-primary/10" : "bg-primary/18",
                run.phase === "cleaning" && "bg-phase-cleaning_up/15",
              )}
              style={{ width: `${fill}%` }}
            />
          </span>
          {expectedSeconds === null ? (
            <span className="absolute inset-0 z-[2] rounded-md bg-[repeating-linear-gradient(135deg,transparent,transparent_5px,color-mix(in_oklch,var(--border)_45%,transparent)_5px,color-mix(in_oklch,var(--border)_45%,transparent)_6px)]" />
          ) : null}
          <span className="relative z-[3] flex h-full min-w-0 flex-col justify-center px-1.5">
            <span className="truncate font-mono text-[10px] font-medium">
              {runLabel(run.id)}
            </span>
            {width >= 112 || compact ? (
              <span className="truncate text-[9px] text-muted-foreground">
                {shortImage(image)}
              </span>
            ) : null}
          </span>
        </Link>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        <div className="font-medium">{run.id}</div>
        <div className="mt-0.5 text-primary-foreground/75">
          {duration(expectedSeconds)} expected within {duration(maxSeconds)}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

function BoundedSpan({
  className,
  expectedSeconds,
  fillClassName,
  label,
  leftSeconds,
  maxSeconds,
  pixelsPerMinute,
}: {
  className?: string;
  expectedSeconds: number;
  fillClassName?: string;
  label: string;
  leftSeconds: number;
  maxSeconds: number;
  pixelsPerMinute: number;
}) {
  const fill =
    maxSeconds > 0 ? Math.min(100, (expectedSeconds / maxSeconds) * 100) : 0;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          aria-label={label}
          className={cn("absolute overflow-hidden rounded-full border", className)}
          style={{
            left: (leftSeconds / 60) * pixelsPerMinute,
            width: Math.max(16, (maxSeconds / 60) * pixelsPerMinute),
          }}
        >
          <span
            className={cn("block h-full", fillClassName)}
            style={{ width: `${fill}%` }}
          />
        </div>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function Marketplace({
  available,
  offers,
}: {
  available: boolean;
  offers: OfferSnapshot[];
}) {
  return (
    <section className="border-t bg-card/20 px-5 py-5">
      <div className="mb-3 flex items-center gap-2">
        <span className="text-[11px] font-medium uppercase tracking-[0.14em] text-muted-foreground">
          Marketplace
        </span>
        <span
          className={cn(
            "size-1.5 rounded-full",
            available ? "bg-phase-succeeded" : "bg-phase-failed",
          )}
        />
      </div>
      <div className="flex flex-wrap gap-2">
        {offers.map((offer) => (
          <MarketplaceOffer key={offer.id} offer={offer} />
        ))}
        {offers.length === 0 ? (
          <div className="h-14 w-56 rounded-lg border border-dashed bg-background/40" />
        ) : null}
      </div>
    </section>
  );
}

function MarketplaceOffer({ offer }: { offer: OfferSnapshot }) {
  const accelerator = offer.resources.accelerators?.[0];
  const hourly = offer.pricing.known
    ? usd(offer.pricing.rate_per_second_usd * 3600)
    : "—";
  const expected = offer.provisioning?.expected ?? offer.provisioning?.p50 ?? 0;
  const max = offer.provisioning?.p90 ?? expected;
  return (
    <Link
      to="/offers"
      search={true}
      aria-label={`${offer.id}: ${offer.adapter_type} Offer`}
      className="group relative flex h-16 w-60 items-center gap-3 overflow-hidden rounded-lg border bg-background px-3 outline-none transition-[border-color,box-shadow] hover:border-primary/35 focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Box className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate font-mono text-[10px]">{shortID(offer.id)}</span>
          <span className="font-mono text-[10px] tabular text-muted-foreground">
            {hourly}/h
          </span>
        </div>
        <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground">
          {accelerator ? <Zap className="size-3" /> : <Gauge className="size-3" />}
          <span className="truncate">
            {accelerator
              ? `${accelerator.count}× ${accelerator.model}`
              : offer.adapter_type}
          </span>
        </div>
        {max > 0 ? (
          <div
            className="mt-1 h-1 overflow-hidden rounded-full border border-phase-launching/40"
            aria-label={`Expected provisioning ${duration(expected)}, p90 ${duration(max)}`}
          >
            <div
              className="h-full bg-phase-launching/45"
              style={{ width: `${Math.min(100, (expected / max) * 100)}%` }}
            />
          </div>
        ) : null}
      </div>
    </Link>
  );
}

function readablePixelsPerMinute(workspace: Workspace): number {
  let shortestExpectedSeconds = Number.POSITIVE_INFINITY;
  for (const rental of Object.values(workspace.rentals)) {
    for (const bookingID of [
      rental.runningBookingID,
      ...rental.queuedBookingIDs,
    ]) {
      if (!bookingID) continue;
      const booking = workspace.bookings[bookingID];
      const expected = booking
        ? workspace.runs[booking.runID]?.expectedRuntimeSeconds
        : null;
      if (expected && expected > 0) {
        shortestExpectedSeconds = Math.min(shortestExpectedSeconds, expected);
      }
    }
  }
  if (!Number.isFinite(shortestExpectedSeconds)) {
    return BASE_PIXELS_PER_MINUTE;
  }
  return Math.max(
    BASE_PIXELS_PER_MINUTE,
    MINIMUM_RUN_WIDTH / (shortestExpectedSeconds / 60),
  );
}

function workspaceHorizon(workspace: Workspace, now: number): number {
  let seconds = MINIMUM_HORIZON_MINUTES * 60;
  for (const rental of Object.values(workspace.rentals)) {
    const provision = rental.offer?.provisioning;
    if (rental.phase === "provisioning" && provision) {
      const booking = rental.runningBookingID
        ? workspace.bookings[rental.runningBookingID]
        : undefined;
      const run = booking ? workspace.runs[booking.runID] : undefined;
      seconds = Math.max(
        seconds,
        (provision.p90 ?? provision.expected ?? provision.p50 ?? 0) +
          (run?.maxRuntimeSeconds ?? 0),
      );
    }
    for (const bookingID of [
      rental.runningBookingID,
      ...rental.queuedBookingIDs,
    ]) {
      if (!bookingID) continue;
      const booking = workspace.bookings[bookingID];
      const run = booking ? workspace.runs[booking.runID] : undefined;
      if (!booking || !run) continue;
      seconds = Math.max(
        seconds,
        secondsUntil(booking.projectedStartAt, now) + run.maxRuntimeSeconds,
      );
    }
  }
  return Math.ceil(seconds / 60 / 10) * 10;
}

function remainingExpected(run: WorkspaceRun | undefined, now: number): number {
  if (!run || run.expectedRuntimeSeconds === null) return 0;
  return Math.max(0, run.expectedRuntimeSeconds - elapsedSeconds(run, now));
}

function remainingMax(run: WorkspaceRun, now: number): number {
  return Math.max(1, run.maxRuntimeSeconds - elapsedSeconds(run, now));
}

function elapsedSeconds(run: WorkspaceRun, now: number): number {
  if (!run.runningAt) return 0;
  return Math.max(0, (now - new Date(run.runningAt).getTime()) / 1000);
}

function secondsUntil(value: string | undefined, now: number): number {
  if (!value) return 0;
  return Math.max(0, (new Date(value).getTime() - now) / 1000);
}

function shortID(value: string): string {
  return value.length > 20 ? `${value.slice(0, 17)}…` : value;
}

function shortImage(value: string): string {
  const withoutDigest = value.split("@")[0] ?? value;
  return withoutDigest.split("/").at(-1) ?? withoutDigest;
}

function transitionName(value: string): string {
  return value.replaceAll(/[^a-zA-Z0-9_-]/g, "-");
}

function runLabel(value: string): string {
  return shortID(value.replace(/^run[-_]/, ""));
}

function sourceRank(rental: Rental): number {
  if (rental.source === "standing") return 0;
  if (rental.source === "provisioned") return 1;
  return 2;
}
