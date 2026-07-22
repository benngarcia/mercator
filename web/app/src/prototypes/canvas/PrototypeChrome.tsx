// PROTOTYPE: shared evaluation chrome. This file does not belong on master.

import { useRef } from "react";
import {
  ArrowLeft,
  ArrowRight,
  CircleDot,
  FlaskConical,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { useMountEffect } from "@/hooks/useMountEffect";
import { cn } from "@/lib/utils";

import type { PrototypeStep, PrototypeWorkspace } from "./data";

export type PrototypeVariant = "A" | "B" | "C";

const VARIANTS: ReadonlyArray<{
  key: PrototypeVariant;
  name: string;
}> = [
  { key: "A", name: "Fleet board" },
  { key: "B", name: "Time lanes" },
  { key: "C", name: "Capacity matrix" },
];

const STEPS: ReadonlyArray<{
  key: PrototypeStep;
  label: string;
}> = [
  { key: "requested", label: "Requested" },
  { key: "provisioning", label: "Provisioning" },
  { key: "running", label: "Running" },
];

export function PrototypeHeader({
  workspace,
  onStep,
}: {
  workspace: PrototypeWorkspace;
  onStep: (step: PrototypeStep) => void;
}) {
  const activeRunCount = workspace.rentals.reduce(
    (count, rental) => count + (rental.running ? 1 : 0) + rental.queued.length,
    workspace.intake.length,
  );

  return (
    <header className="border-b bg-card/65 px-5 py-4 backdrop-blur">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <span>Workspace</span>
            <span className="text-label-3">/</span>
            <span className="font-mono">{workspace.id}</span>
            <span className="inline-flex items-center gap-1 rounded-full bg-phase-running/10 px-2 py-0.5 text-phase-running">
              <CircleDot className="size-3" /> Live
            </span>
          </div>
          <h1 className="mt-1 text-xl font-semibold tracking-tight">
            {workspace.name}
          </h1>
          <p className="mt-1 max-w-3xl text-sm leading-relaxed text-muted-foreground">
            {workspace.scenarioSummary}
          </p>
        </div>

        <div className="flex rounded-lg border bg-background p-1 shadow-sm" aria-label="Scenario event">
          {STEPS.map((step) => (
            <button
              key={step.key}
              type="button"
              className={cn(
                "rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                workspace.step === step.key
                  ? "bg-foreground text-background shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
              onClick={() => onStep(step.key)}
            >
              {step.label}
            </button>
          ))}
        </div>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2 border-t pt-3 text-xs">
        <span className="inline-flex items-center gap-1.5 text-muted-foreground">
          <FlaskConical className="size-3.5" />
          <span className="font-mono">{workspace.scenarioName}</span>
        </span>
        <span>
          <strong className="font-mono font-medium">{workspace.rentals.length}</strong>{" "}
          <span className="text-muted-foreground">Rentals</span>
        </span>
        <span>
          <strong className="font-mono font-medium">{activeRunCount}</strong>{" "}
          <span className="text-muted-foreground">active Runs</span>
        </span>
        <span>
          <strong className="font-mono font-medium">{workspace.offers.length}</strong>{" "}
          <span className="text-muted-foreground">marketplace Offer</span>
        </span>
        <span className="ml-auto truncate font-mono text-[0.6875rem] text-muted-foreground">
          {workspace.sourceEvent}
        </span>
      </div>
    </header>
  );
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.matches("input, textarea, select, [contenteditable='true']") ||
    Boolean(target.closest("input, textarea, select, [contenteditable='true']"))
  );
}

export function PrototypeSwitcher({
  current,
  onSelect,
}: {
  current: PrototypeVariant;
  onSelect: (variant: PrototypeVariant) => void;
}) {
  const state = useRef({ current, onSelect });
  state.current = { current, onSelect };

  const cycle = (direction: -1 | 1) => {
    const index = VARIANTS.findIndex((variant) => variant.key === current);
    const next = VARIANTS[(index + direction + VARIANTS.length) % VARIANTS.length];
    if (next) onSelect(next.key);
  };

  useMountEffect(() => {
    const handleKey = (event: KeyboardEvent) => {
      if (isEditableTarget(event.target)) return;
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const { current: active, onSelect: select } = state.current;
      const index = VARIANTS.findIndex((variant) => variant.key === active);
      const direction = event.key === "ArrowLeft" ? -1 : 1;
      const next = VARIANTS[(index + direction + VARIANTS.length) % VARIANTS.length];
      if (next) select(next.key);
    };
    window.addEventListener("keydown", handleKey);
    return () => window.removeEventListener("keydown", handleKey);
  });

  if (process.env.NODE_ENV === "production") return null;

  const selected = VARIANTS.find((variant) => variant.key === current) ?? VARIANTS[0];

  return (
    <div className="fixed bottom-5 left-1/2 z-50 flex -translate-x-1/2 items-center gap-1 rounded-full border border-white/15 bg-neutral-950/95 p-1.5 text-white shadow-2xl backdrop-blur">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-8 rounded-full text-white hover:bg-white/10 hover:text-white"
        aria-label="Previous prototype variant"
        onClick={() => cycle(-1)}
      >
        <ArrowLeft className="size-4" />
      </Button>
      <div className="min-w-40 px-3 text-center text-xs">
        <span className="font-mono font-semibold">{selected?.key}</span>
        <span className="px-1.5 text-white/40">/</span>
        <span className="font-medium">{selected?.name}</span>
      </div>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-8 rounded-full text-white hover:bg-white/10 hover:text-white"
        aria-label="Next prototype variant"
        onClick={() => cycle(1)}
      >
        <ArrowRight className="size-4" />
      </Button>
    </div>
  );
}

