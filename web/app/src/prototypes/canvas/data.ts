// PROTOTYPE: throwaway UI data derived from the full-schedule scenario.
// Question: which Workspace layout makes live Rental schedules easiest to read?

import scenario from "../../../../../internal/scenario/scenarios/full-schedule-forces-fresh-capacity.json";

export type PrototypeStep = "requested" | "provisioning" | "running";

export interface PrototypeRun {
  id: string;
  label: string;
  image: string;
  expectedMinutes: number;
  maxMinutes: number;
  projectedStartMinutes?: number;
  phase: "requested" | "provisioning" | "running" | "queued";
}

export interface PrototypeRental {
  id: string;
  label: string;
  provider: string;
  ratePerHourUSD: number;
  phase: "active" | "provisioning";
  running?: PrototypeRun;
  queued: PrototypeRun[];
  queueLimit: number;
  cacheLabel: string;
}

export interface PrototypeOffer {
  id: string;
  provider: string;
  ratePerHourUSD: number;
  provisionExpectedMinutes: number;
  provisionP90Minutes: number;
  selected: boolean;
}

export interface PrototypeWorkspace {
  id: string;
  name: string;
  scenarioName: string;
  scenarioSummary: string;
  sourceEvent: string;
  step: PrototypeStep;
  intake: PrototypeRun[];
  rentals: PrototypeRental[];
  offers: PrototypeOffer[];
}

const schedule = scenario.world.rental_schedules[0]!;
const warmRental = scenario.world.rentals[0]!;
const freshOffer = scenario.world.marketplace[0]!;

if (!schedule || !schedule.running || !warmRental || !freshOffer) {
  throw new Error("Prototype scenario must include one busy Rental and one Offer.");
}

function durationMinutes(duration: string): number {
  const hours = Number(duration.match(/(\d+)h/)?.[1] ?? 0);
  const minutes = Number(duration.match(/(\d+)m/)?.[1] ?? 0);
  return hours * 60 + minutes;
}

const activeRun: PrototypeRun = {
  id: schedule.running.run,
  label: "Active training",
  image: "worker:v1",
  expectedMinutes: durationMinutes(schedule.running.remaining_max_runtime),
  maxMinutes: durationMinutes(schedule.running.remaining_max_runtime),
  phase: "running",
};

let projectedStartMinutes = activeRun.expectedMinutes;
const queuedRuns: PrototypeRun[] = schedule.queued.map((booking, index) => {
  const run: PrototypeRun = {
    id: booking.run,
    label: `Queued training ${index + 1}`,
    image: "worker:v1",
    expectedMinutes: durationMinutes(booking.expected_runtime),
    maxMinutes: durationMinutes(booking.max_runtime),
    projectedStartMinutes,
    phase: "queued",
  };
  projectedStartMinutes += run.expectedMinutes;
  return run;
});

const incomingRun: PrototypeRun = {
  id: "run-fresh-capacity",
  label: "Large training run",
  image: scenario.request.image,
  expectedMinutes: durationMinutes(scenario.request.expected_runtime),
  maxMinutes: durationMinutes(scenario.request.max_runtime),
  phase: "requested",
};

function scheduledRun(step: PrototypeStep): PrototypeRun {
  return {
    ...incomingRun,
    phase: step === "running" ? "running" : "provisioning",
  };
}

function selectedOffer(step: PrototypeStep): PrototypeOffer {
  return {
    id: freshOffer.id,
    provider: freshOffer.provider,
    ratePerHourUSD: freshOffer.rate_per_hour_usd,
    provisionExpectedMinutes: durationMinutes(freshOffer.provisioning.expected),
    provisionP90Minutes: durationMinutes(freshOffer.provisioning.p90),
    selected: step !== "requested",
  };
}

const warmRentalView: PrototypeRental = {
  id: warmRental.id,
  label: "Warm worker",
  provider: "Docker",
  ratePerHourUSD: warmRental.rate_per_hour_usd,
  phase: "active",
  running: activeRun,
  queued: queuedRuns,
  queueLimit: 4,
  cacheLabel: "worker:v1 cached",
};

export function workspaceFor(step: PrototypeStep): PrototypeWorkspace {
  const freshRental: PrototypeRental | undefined =
    step === "requested"
      ? undefined
      : {
          id: "rental-fresh-capacity",
          label: step === "running" ? "Fresh worker" : "New Rental",
          provider: freshOffer.provider,
          ratePerHourUSD: freshOffer.rate_per_hour_usd,
          phase: step === "running" ? "active" : "provisioning",
          running: scheduledRun(step),
          queued: [],
          queueLimit: 4,
          cacheLabel: step === "running" ? "image ready" : "pull pending",
        };

  const sourceEvent = {
    requested: "compute.run.requested.v1",
    provisioning: "compute.run.booking_decided.v1",
    running: "compute.run.launch_accepted.v1",
  }[step];

  return {
    id: "ws_training_west",
    name: "Training west",
    scenarioName: "full-schedule-forces-fresh-capacity",
    scenarioSummary: scenario.summary,
    sourceEvent,
    step,
    intake: step === "requested" ? [incomingRun] : [],
    rentals: freshRental ? [warmRentalView, freshRental] : [warmRentalView],
    offers: [selectedOffer(step)],
  };
}

export function runtimePercent(run: PrototypeRun): number {
  return Math.max(6, Math.min(100, (run.expectedMinutes / run.maxMinutes) * 100));
}
