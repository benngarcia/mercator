import type {
  CloudEvent,
  OfferSnapshot,
  WorkloadRevision,
} from "@/lib/api/types";
import * as Result from "effect/Result";
import * as Schema from "effect/Schema";

import {
  BookingDecidedData,
  BookingDispatchedData,
  LaunchIntentData,
  ObservedRunData,
  OutcomeData,
  RentalBookingData,
  RentalRemovalData,
  RequestedData,
  type StoredBookingDecision,
} from "./contracts";

export type DeploymentRunPhase =
  "requested" | "provisioning" | "running" | "cleaning" | "closed";

export interface DeploymentRun {
  id: string;
  requestedAt: string;
  workload: WorkloadRevision;
  expectedRuntimeSeconds: number | null;
  maxRuntimeSeconds: number;
  phase: DeploymentRunPhase;
  decision?: StoredBookingDecision;
  selectedOfferID?: string;
  bookingID?: string;
  runningAt?: string;
  outcome?: string;
}

export interface DeploymentBooking {
  id: string;
  rentalID: string;
  runID: string;
  state: "running" | "queued";
  afterBookingID?: string;
  projectedStartAt?: string;
  latestStartAt?: string;
  scheduleVersion: number;
}

export interface Rental {
  id: string;
  source: "standing" | "provisioned" | "unknown";
  phase: "idle" | "provisioning" | "active";
  offer?: OfferSnapshot;
  runningBookingID?: string;
  queuedBookingIDs: string[];
}

export interface Deployment {
  ready: boolean;
  throughGlobalPosition: number;
  lastChange: "initial" | "live";
  offersAvailable: boolean;
  offers: OfferSnapshot[];
  runs: Record<string, DeploymentRun>;
  bookings: Record<string, DeploymentBooking>;
  rentals: Record<string, Rental>;
}

export interface OfferCatalogReplacement {
  revision: string;
  observed_at: string;
  offers: OfferSnapshot[];
  failures: unknown[];
}

export type DeploymentMessage =
  | { type: "domain_event"; event: CloudEvent }
  | { type: "offers_replaced"; catalog: OfferCatalogReplacement }
  | { type: "offers_unavailable" }
  | { type: "ready"; throughGlobalPosition: number };

export function createDeployment(): Deployment {
  return {
    ready: false,
    throughGlobalPosition: 0,
    lastChange: "initial",
    offersAvailable: true,
    offers: [],
    runs: {},
    bookings: {},
    rentals: {},
  };
}

export function reduceDeployment(
  deployment: Deployment,
  message: DeploymentMessage,
): Deployment {
  switch (message.type) {
    case "ready":
      return {
        ...deployment,
        ready: true,
        throughGlobalPosition: message.throughGlobalPosition,
      };
    case "offers_unavailable":
      return { ...deployment, offersAvailable: false };
    case "offers_replaced":
      return replaceOffers(deployment, message.catalog.offers);
    case "domain_event":
      return applyDomainEvent(deployment, message.event);
  }
}

function replaceOffers(
  deployment: Deployment,
  offers: OfferSnapshot[],
): Deployment {
  const rentals = { ...deployment.rentals };
  const standingRentalIDs = new Set<string>();
  for (const offer of offers) {
    if (offer.kind !== "standing" || !offer.rental_id) continue;
    standingRentalIDs.add(offer.rental_id);
    const existing = rentals[offer.rental_id];
    rentals[offer.rental_id] = {
      id: offer.rental_id,
      source: "standing",
      phase: existing?.runningBookingID ? "active" : "idle",
      offer,
      runningBookingID: existing?.runningBookingID,
      queuedBookingIDs: existing?.queuedBookingIDs ?? [],
    };
  }
  for (const rental of Object.values(rentals)) {
    if (
      rental.source === "standing" &&
      !standingRentalIDs.has(rental.id) &&
      !rental.runningBookingID &&
      rental.queuedBookingIDs.length === 0
    ) {
      delete rentals[rental.id];
    }
  }
  for (const run of Object.values(deployment.runs)) {
    if (!run.bookingID || !run.selectedOfferID) continue;
    const booking = deployment.bookings[run.bookingID];
    const selected = offers.find((offer) => offer.id === run.selectedOfferID);
    if (!booking || !selected || selected.kind !== "provisionable") continue;
    const existing = rentals[booking.rentalID];
    rentals[booking.rentalID] = {
      id: booking.rentalID,
      source: "provisioned",
      phase: run.phase === "running" ? "active" : "provisioning",
      offer: selected,
      runningBookingID: existing?.runningBookingID ?? booking.id,
      queuedBookingIDs: existing?.queuedBookingIDs ?? [],
    };
  }
  return changed(deployment, {
    offers,
    offersAvailable: true,
    rentals,
  });
}

function applyDomainEvent(
  deployment: Deployment,
  event: CloudEvent,
): Deployment {
  const next = applyRunEvent(deployment, event);
  return {
    ...next,
    throughGlobalPosition: Math.max(
      next.throughGlobalPosition,
      event.globalposition,
    ),
    lastChange: deployment.ready ? "live" : "initial",
  };
}

function applyRunEvent(deployment: Deployment, event: CloudEvent): Deployment {
  switch (event.type) {
    case "compute.run.requested.v1":
      return requestRun(deployment, event);
    case "compute.run.booking_decided.v1":
      return decideBooking(deployment, event);
    case "compute.run.booking_dispatched.v1":
      return dispatchBooking(deployment, event);
    case "compute.run.launch_intent_recorded.v1":
      return recordLaunchIntent(deployment, event);
    case "compute.run.launch_accepted.v1":
    case "compute.run.external_state_observed.v1":
      return observeRun(deployment, event);
    case "compute.run.outcome_recorded.v1":
      return recordOutcome(deployment, event);
    case "compute.run.cleanup_requested.v1":
      return updateRunPhase(deployment, event, "cleaning");
    case "compute.run.closed.v1":
      return closeRun(deployment, event);
    case "compute.rental.booking_queued.v1":
    case "compute.rental.booking_dispatched.v1":
    case "compute.rental.booking_moved.v1":
      return applyRentalBookingEvent(deployment, event);
    case "compute.rental.booking_expired.v1":
    case "compute.rental.booking_cancelled.v1":
      return removeRentalBooking(deployment, event);
    default:
      return deployment;
  }
}

function dispatchBooking(
  deployment: Deployment,
  event: CloudEvent,
): Deployment {
  const { booking: source } = decodeEventData(BookingDispatchedData, event);
  const run = requiredRun(deployment, source.run_id, event.type);
  const booking: DeploymentBooking = {
    id: source.id,
    rentalID: source.rental_id,
    runID: source.run_id,
    state: "running",
    scheduleVersion: source.schedule_version,
  };
  const bookings = { ...deployment.bookings, [booking.id]: booking };
  return changed(deployment, {
    bookings,
    rentals: insertBooking(deployment.rentals, bookings, booking),
    runs: {
      ...deployment.runs,
      [run.id]: { ...run, bookingID: booking.id },
    },
  });
}

function requestRun(deployment: Deployment, event: CloudEvent): Deployment {
  const data = decodeEventData(RequestedData, event);
  const runID = data.run_id;
  const workload: WorkloadRevision = data.workload_revision;
  const expected = workload.spec.placement.expected_runtime_seconds;
  const max = workload.spec.execution.max_runtime_seconds;
  // A malformed expected runtime in durable history must degrade this one
  // run, never throw: a reducer throw is a non-retryable feed error that
  // would brick the canvas for every viewer replaying the deployment.
  const run: DeploymentRun = {
    id: runID,
    requestedAt: event.time,
    workload,
    expectedRuntimeSeconds:
      expected !== undefined && expected <= max ? expected : null,
    maxRuntimeSeconds: max,
    phase: "requested",
  };
  return changed(deployment, { runs: { ...deployment.runs, [runID]: run } });
}

function decideBooking(deployment: Deployment, event: CloudEvent): Deployment {
  const data = decodeEventData(BookingDecidedData, event);
  const decision: StoredBookingDecision = data.decision;
  const runID = decision.run_id ?? event.correlationid;
  if (!runID) throw new Error(`${event.type} requires decision.run_id`);
  const run = requiredRun(deployment, runID, event.type);
  if (!decision.booking || !decision.selected_offer_snapshot_id) {
    return changed(deployment, {
      runs: {
        ...deployment.runs,
        [runID]: { ...run, decision },
      },
    });
  }
  const sourceBooking = decision.booking;
  const booking: DeploymentBooking = {
    id: sourceBooking.id,
    rentalID: sourceBooking.rental_id,
    runID,
    state: sourceBooking.state,
    afterBookingID: sourceBooking.after_booking_id,
    projectedStartAt: sourceBooking.projected_start_at,
    latestStartAt: sourceBooking.latest_start_at,
    scheduleVersion: sourceBooking.schedule_version,
  };
  const current = detachSupersededBooking(deployment, run, booking.id);
  const selectedOffer = current.offers.find(
    (offer) => offer.id === decision.selected_offer_snapshot_id,
  );
  const rentals = insertBooking(
    current.rentals,
    { ...current.bookings, [booking.id]: booking },
    booking,
    selectedOffer,
  );
  const phase =
    selectedOffer?.kind === "provisionable"
      ? "provisioning"
      : booking.state === "running"
        ? "running"
        : "requested";
  return changed(deployment, {
    bookings: { ...current.bookings, [booking.id]: booking },
    rentals,
    runs: {
      ...current.runs,
      [runID]: {
        ...run,
        phase,
        decision,
        selectedOfferID: decision.selected_offer_snapshot_id,
        bookingID: booking.id,
        runningAt: phase === "running" ? event.time : undefined,
      },
    },
  });
}

function detachSupersededBooking(
  deployment: Deployment,
  run: DeploymentRun,
  nextBookingID: string,
): Deployment {
  if (!run.bookingID || run.bookingID === nextBookingID) return deployment;
  return detachBooking(deployment, run.bookingID);
}

function insertBooking(
  rentals: Record<string, Rental>,
  bookings: Record<string, DeploymentBooking>,
  booking: DeploymentBooking,
  offer?: OfferSnapshot,
): Record<string, Rental> {
  const next = { ...rentals };
  const existing = next[booking.rentalID];
  const provisioned = offer?.kind === "provisionable";
  const rental: Rental = {
    id: booking.rentalID,
    source: provisioned ? "provisioned" : (existing?.source ?? "unknown"),
    phase: provisioned
      ? "provisioning"
      : booking.state === "running"
        ? "active"
        : (existing?.phase ?? "idle"),
    offer: offer ?? existing?.offer,
    runningBookingID:
      booking.state === "running" ? booking.id : existing?.runningBookingID,
    queuedBookingIDs: existing?.queuedBookingIDs ?? [],
  };
  next[booking.rentalID] = {
    ...rental,
    queuedBookingIDs: orderedQueuedBookings(bookings, booking.rentalID),
  };
  return next;
}

function orderedQueuedBookings(
  bookings: Record<string, DeploymentBooking>,
  rentalID: string,
): string[] {
  const candidates = Object.values(bookings).filter(
    (booking) => booking.rentalID === rentalID && booking.state === "queued",
  );
  const byPredecessor = new Map<string, DeploymentBooking>();
  for (const booking of candidates) {
    byPredecessor.set(booking.afterBookingID ?? "", booking);
  }
  const rentalBookings = Object.values(bookings).filter(
    (booking) => booking.rentalID === rentalID,
  );
  const running = rentalBookings.find((booking) => booking.state === "running");
  const ordered: string[] = [];
  let predecessor = running?.id ?? "";
  while (byPredecessor.has(predecessor)) {
    const booking = byPredecessor.get(predecessor);
    if (!booking || ordered.includes(booking.id)) break;
    ordered.push(booking.id);
    predecessor = booking.id;
  }
  for (const booking of candidates) {
    if (!ordered.includes(booking.id)) ordered.push(booking.id);
  }
  return ordered;
}

function recordLaunchIntent(
  deployment: Deployment,
  event: CloudEvent,
): Deployment {
  const runID = runIDForEvent(event);
  const run = requiredRun(deployment, runID, event.type);
  if (!run.bookingID) return deployment;
  const booking = deployment.bookings[run.bookingID];
  if (!booking) return deployment;
  const data = decodeEventData(LaunchIntentData, event);
  const rental = deployment.rentals[booking.rentalID];
  if (!rental) return deployment;
  const provisioned = data.disposition === "terminate";
  return changed(deployment, {
    rentals: {
      ...deployment.rentals,
      [rental.id]: {
        ...rental,
        source: provisioned ? "provisioned" : "standing",
        phase: provisioned ? "provisioning" : "active",
      },
    },
    runs: {
      ...deployment.runs,
      [runID]: { ...run, phase: provisioned ? "provisioning" : run.phase },
    },
  });
}

function observeRun(deployment: Deployment, event: CloudEvent): Deployment {
  const data = decodeEventData(ObservedRunData, event);
  const phase = data.phase;
  if (phase !== "running") return deployment;
  const runID = runIDForEvent(event);
  const run = requiredRun(deployment, runID, event.type);
  const runs = {
    ...deployment.runs,
    [runID]: { ...run, phase: "running" as const, runningAt: event.time },
  };
  if (!run.bookingID) return changed(deployment, { runs });
  const booking = deployment.bookings[run.bookingID];
  const rental = booking ? deployment.rentals[booking.rentalID] : undefined;
  if (!rental) return changed(deployment, { runs });
  return changed(deployment, {
    runs,
    rentals: {
      ...deployment.rentals,
      [rental.id]: { ...rental, phase: "active" },
    },
  });
}

function recordOutcome(deployment: Deployment, event: CloudEvent): Deployment {
  const runID = runIDForEvent(event);
  const run = requiredRun(deployment, runID, event.type);
  const data = decodeEventData(OutcomeData, event);
  return changed(deployment, {
    runs: {
      ...deployment.runs,
      [runID]: {
        ...run,
        phase: "cleaning",
        outcome: data.outcome,
      },
    },
  });
}

function updateRunPhase(
  deployment: Deployment,
  event: CloudEvent,
  phase: DeploymentRunPhase,
): Deployment {
  const runID = runIDForEvent(event);
  const run = requiredRun(deployment, runID, event.type);
  return changed(deployment, {
    runs: { ...deployment.runs, [runID]: { ...run, phase } },
  });
}

function closeRun(deployment: Deployment, event: CloudEvent): Deployment {
  const runID = runIDForEvent(event);
  const run = requiredRun(deployment, runID, event.type);
  if (!run.bookingID) {
    return changed(deployment, {
      runs: {
        ...deployment.runs,
        [runID]: { ...run, phase: "closed" },
      },
    });
  }
  return detachBooking(deployment, run.bookingID, {
    ...run,
    phase: "closed",
  });
}

function applyRentalBookingEvent(
  deployment: Deployment,
  event: CloudEvent,
): Deployment {
  const data = decodeEventData(RentalBookingData, event);
  const source = data.booking ?? data;
  const runID = data.run_id;
  const booking: DeploymentBooking = {
    id: requiredValue(source.id, "id", event.type),
    rentalID: requiredValue(source.rental_id, "rental_id", event.type),
    runID,
    state: event.type.endsWith("booking_dispatched.v1") ? "running" : "queued",
    afterBookingID: optionalString(source.after_booking_id),
    projectedStartAt: optionalString(source.projected_start_at),
    latestStartAt: optionalString(source.latest_start_at),
    scheduleVersion: requiredNumberValue(
      source.schedule_version,
      "schedule_version",
      event.type,
    ),
  };
  const bookings = { ...deployment.bookings, [booking.id]: booking };
  return changed(deployment, {
    bookings,
    rentals: insertBooking(deployment.rentals, bookings, booking),
  });
}

function removeRentalBooking(
  deployment: Deployment,
  event: CloudEvent,
): Deployment {
  const data = decodeEventData(RentalRemovalData, event);
  const bookingID =
    optionalString(data.booking_id) ?? optionalString(data.id) ?? "";
  return bookingID ? detachBooking(deployment, bookingID) : deployment;
}

function detachBooking(
  deployment: Deployment,
  bookingID: string,
  closedRun?: DeploymentRun,
): Deployment {
  const booking = deployment.bookings[bookingID];
  if (!booking) return deployment;
  const bookings = { ...deployment.bookings };
  delete bookings[bookingID];
  const rental = deployment.rentals[booking.rentalID];
  const rentals = { ...deployment.rentals };
  if (rental) {
    // Detaching a queued booking must not stomp the phase of a rental whose
    // running booking survives.
    const runningBookingID =
      rental.runningBookingID === bookingID
        ? undefined
        : rental.runningBookingID;
    const nextRental = {
      ...rental,
      phase: runningBookingID ? rental.phase : ("idle" as const),
      runningBookingID,
      queuedBookingIDs: orderedQueuedBookings(bookings, rental.id),
    };
    if (
      nextRental.source === "provisioned" &&
      !nextRental.runningBookingID &&
      nextRental.queuedBookingIDs.length === 0
    ) {
      delete rentals[rental.id];
    } else {
      rentals[rental.id] = nextRental;
    }
  }
  return changed(deployment, {
    bookings,
    rentals,
    runs: closedRun
      ? { ...deployment.runs, [closedRun.id]: closedRun }
      : deployment.runs,
  });
}

function changed(
  deployment: Deployment,
  values: Partial<Deployment>,
): Deployment {
  return {
    ...deployment,
    ...values,
    lastChange: deployment.ready ? "live" : "initial",
  };
}

function decodeEventData<Type>(
  schema: Schema.ConstraintDecoder<Type>,
  event: CloudEvent,
): Type {
  const decoded = Schema.decodeUnknownResult(schema)(event.data);
  if (Result.isFailure(decoded)) {
    throw new Error(
      `${event.type} has invalid data: ${decoded.failure.message}`,
    );
  }
  return decoded.success;
}

function requiredRun(
  deployment: Deployment,
  runID: string,
  eventType: string,
): DeploymentRun {
  const run = deployment.runs[runID];
  if (!run) throw new Error(`${eventType} references unknown Run ${runID}`);
  return run;
}

function runIDForEvent(event: CloudEvent): string {
  const fromSubject = event.subject.startsWith("runs/")
    ? event.subject.slice("runs/".length)
    : "";
  const runID = event.correlationid ?? fromSubject;
  if (!runID) throw new Error(`${event.type} requires a Run correlation`);
  return runID;
}

function requiredValue(
  value: string | undefined,
  field: string,
  eventType: string,
): string {
  if (value === undefined || value === "") {
    throw new Error(`${eventType} requires ${field}`);
  }
  return value;
}

function requiredNumberValue(
  value: number | undefined,
  field: string,
  eventType: string,
): number {
  if (value === undefined) {
    throw new Error(`${eventType} requires ${field}`);
  }
  return value;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}
