// The locality view. Machines are places, content is a thing that either had to
// travel to one or was already there, and the difference is the loudest thing on
// screen.
//
// It stands beside the canvas rather than replacing it. A Gantt chart is the
// right substrate for a schedule and answers what happened when; it structurally
// cannot answer what this cost, because every fact here is a subtraction between
// what a machine held and what a Run needed, and a bar on a time axis has nowhere
// to put one.
//
// The design rule the whole thing follows: in a system whose value is AVOIDING
// work, the absence of motion has to be as legible as motion. A held read is not
// a smaller event than a fetch. It is the event that justifies the product, so it
// gets its own colour, its own column in the scoreboard, and a mark on the stage
// where nothing travelled.

import { ArrowDown, Box, Database, HardDrive } from "lucide-react";

import type { ContentKind, HeldContent, RunLocality } from "@/lib/locality";
import { crossesTheWire, fleetTotals } from "@/lib/locality";
import { cn } from "@/lib/utils";

const KIND_ICON: Record<ContentKind, typeof Box> = {
  image: Box,
  artifact: Database,
  cache: HardDrive,
};

function bytes(value: number): string {
  if (value >= 1e9) return `${(value / 1e9).toFixed(value >= 1e10 ? 0 : 1)} GB`;
  if (value >= 1e6) return `${Math.round(value / 1e6)} MB`;
  if (value >= 1e3) return `${Math.round(value / 1e3)} kB`;
  return `${value} B`;
}

function seconds(value: number): string {
  if (value >= 3600) return `${(value / 3600).toFixed(1)}h`;
  if (value >= 60) return `${Math.round(value / 60)}m`;
  return `${Math.round(value)}s`;
}

export function LocalityStage({
  records,
  machineLabels,
}: {
  records: readonly RunLocality[];
  machineLabels?: Readonly<Record<string, string>>;
}) {
  const totals = fleetTotals(records);
  const byMachine = new Map<string, RunLocality[]>();
  for (const record of records) {
    const key = record.offerSnapshotID ?? "unplaced";
    byMachine.set(key, [...(byMachine.get(key) ?? []), record]);
  }

  return (
    <div className="flex min-h-0 flex-col">
      <header className="border-b px-5 py-4">
        <h1 className="text-base font-semibold tracking-tight">Locality</h1>
        <p className="mt-1 max-w-prose text-xs text-muted-foreground">
          What each Run needed, and what the machine it landed on already had. Read
          from the selected candidate of each Run's latest Booking Decision, so
          this is what the control plane found at placement, not what a read later
          observed.
        </p>
      </header>

      <Scoreboard totals={totals} runs={records.length} />

      <div className="min-h-0 flex-1 overflow-auto p-5">
        {records.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No Run in this Deployment has been placed yet, so nothing has been found
            anywhere.
          </p>
        ) : (
          <div className="flex flex-col gap-6">
            {[...byMachine.entries()].map(([machine, placed]) => (
              <Machine
                key={machine}
                id={machine}
                label={machineLabels?.[machine] ?? machine}
                records={placed}
              />
            ))}
            <StoreRail />
          </div>
        )}
      </div>
    </div>
  );
}

// Held is a count and owed is a size, and they deliberately do not share a unit.
// A decision states what a host still OWES, so a hot Artifact carries no byte
// count at all: the saving is real and its size was never recorded. Showing it as
// "0 GB saved" would be worse than showing a count. See mercator#244.
function Scoreboard({
  totals,
  runs,
}: {
  totals: ReturnType<typeof fleetTotals>;
  runs: number;
}) {
  return (
    <dl className="grid grid-cols-2 gap-px border-b bg-border sm:grid-cols-4">
      <Tile
        label="Already held"
        value={String(totals.heldPieces)}
        note={totals.heldPieces === 1 ? "piece, size not recorded" : "pieces, size not recorded"}
        tone="held"
      />
      <Tile
        label="Owed"
        value={bytes(totals.owedBytes)}
        note={`across ${totals.owedPieces} ${totals.owedPieces === 1 ? "piece" : "pieces"}`}
        tone="wire"
      />
      <Tile label="Content-bound start" value={seconds(totals.fetchSeconds)} note="image and Artifact stages" />
      <Tile label="Runs placed" value={String(runs)} note="with a decision on record" />
    </dl>
  );
}

function Tile({
  label,
  value,
  note,
  tone,
}: {
  label: string;
  value: string;
  note: string;
  tone?: "held" | "wire";
}) {
  return (
    <div className="bg-background px-5 py-3">
      <dt className="text-[10px] font-medium uppercase tracking-widest text-muted-foreground">
        {label}
      </dt>
      <dd
        className={cn(
          "mt-1 font-mono text-xl tabular-nums",
          tone === "held" && "text-emerald-400",
          tone === "wire" && "text-orange-400",
        )}
      >
        {value}
        <span className="mt-0.5 block font-sans text-[10px] tracking-wide text-muted-foreground">
          {note}
        </span>
      </dd>
    </div>
  );
}

function Machine({
  id,
  label,
  records,
}: {
  id: string;
  label: string;
  records: readonly RunLocality[];
}) {
  const held = records.flatMap((record) =>
    record.content.filter((piece) => !crossesTheWire(piece.locality)),
  );
  return (
    <section
      aria-label={`Machine ${label}`}
      data-machine={id}
      className="rounded-sm border bg-card/40"
    >
      <div className="flex items-baseline justify-between gap-4 border-b px-4 py-2.5">
        <h2 className="font-mono text-sm">{label}</h2>
        <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
          {held.length > 0
            ? `holding ${held.length} of what landed here`
            : "held nothing these Runs needed"}
        </span>
      </div>
      <div className="flex flex-col divide-y">
        {records.map((record) => (
          <Placed key={record.runID} record={record} />
        ))}
      </div>
    </section>
  );
}

function Placed({ record }: { record: RunLocality }) {
  return (
    <div className="px-4 py-3" data-run={record.runID}>
      <div className="flex items-baseline justify-between gap-4">
        <span className="font-mono text-xs">{record.runID}</span>
        <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
          {record.disposition?.replaceAll("_", " ")}
        </span>
      </div>

      <div className="mt-2.5 flex flex-wrap gap-1.5">
        {record.content.map((piece) => (
          <ContentChip key={`${piece.kind}:${piece.name}`} piece={piece} />
        ))}
      </div>

      <StageBar stages={record.stages} />
    </div>
  );
}

// A chip that had to travel carries an arrow and the wire colour. A chip that was
// already here carries neither, and that visual quiet is the point: it is the
// piece of content that cost nothing.
function ContentChip({ piece }: { piece: HeldContent }) {
  const travelled = crossesTheWire(piece.locality);
  const Icon = KIND_ICON[piece.kind];
  const cost = !travelled
    ? "held"
    : piece.fetchBytes
      ? bytes(piece.fetchBytes)
      : piece.fetchSeconds
        ? seconds(piece.fetchSeconds)
        : "not priced";

  return (
    <span
      data-locality={piece.locality}
      data-travelled={travelled}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-sm border px-2 py-1 font-mono text-[10px]",
        travelled
          ? "animate-in fade-in slide-in-from-bottom-1 border-orange-500/50 text-orange-300 duration-500"
          : "border-emerald-500/50 text-emerald-300",
      )}
      title={`${piece.kind} ${piece.name} — ${piece.locality}`}
    >
      <Icon className="size-3 shrink-0" aria-hidden />
      <span className="max-w-[18ch] truncate">{piece.name}</span>
      {travelled ? <ArrowDown className="size-3 shrink-0" aria-hidden /> : null}
      <span className="opacity-70">{cost}</span>
    </span>
  );
}

// The eight stages a launch is made of, drawn end to end. Locality is visible as
// the segments that collapsed, which is why only the content-bound ones carry the
// accent: the rest are the machine's own cost and no amount of warmth touches
// them.
function StageBar({ stages }: { stages: RunLocality["stages"] }) {
  const total = stages.reduce((sum, stage) => sum + stage.seconds, 0);
  if (total <= 0) return null;
  return (
    <div className="mt-2.5">
      <div
        className="flex h-1.5 w-full overflow-hidden rounded-sm bg-muted"
        role="img"
        aria-label={`Predicted start ${seconds(total)}, ${stages
          .filter((stage) => stage.seconds > 0)
          .map((stage) => `${stage.name} ${seconds(stage.seconds)}`)
          .join(", ")}`}
      >
        {stages.map((stage) =>
          stage.seconds <= 0 ? null : (
            <span
              key={stage.name}
              style={{ width: `${(stage.seconds / total) * 100}%` }}
              className={cn(
                "h-full transition-[width] duration-700",
                stage.contentBound ? "bg-orange-500/80" : "bg-sky-700/70",
              )}
            />
          ),
        )}
      </div>
      <p className="mt-1 font-mono text-[10px] text-muted-foreground">
        {seconds(total)} predicted start
      </p>
    </div>
  );
}

function StoreRail() {
  return (
    <div className="rounded-sm border border-dashed px-4 py-3">
      <div className="flex items-center gap-2">
        <Database className="size-3.5 text-muted-foreground" aria-hidden />
        <span className="font-mono text-xs text-muted-foreground">OBJECT STORE</span>
      </div>
      <p className="mt-1 text-[11px] text-muted-foreground">
        The durable authority. Every orange chip above is content that had to come
        from here; every green one is content that did not. A decision states what
        a host still owed, so a green chip carries no size: what it saved is real
        and was never recorded (mercator#244).
      </p>
    </div>
  );
}
