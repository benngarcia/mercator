import type {
  BookingDecision,
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
} from "./contracts";

export type WorkspaceRunPhase =
  | "requested"
  | "provisioning"
  | "running"
  | "cleaning"
  | "closed";

export interface WorkspaceRun {
  id: string;
  requestedAt: string;
  workload: WorkloadRevision;
  expectedRuntimeSeconds: number | null;
  maxRuntimeSeconds: number;
  phase: WorkspaceRunPhase;
  decision?: BookingDecision;
  selectedOfferID?: string;
  bookingID?: string;
  runningAt?: string;
  outcome?: string;
}

export interface WorkspaceBooking {
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

export interface Workspace {
  id: string;
  ready: boolean;
  throughGlobalPosition: number;
  lastChange: "initial" | "live";
  offersAvailable: boolean;
  offers: OfferSnapshot[];
  runs: Record<string, WorkspaceRun>;
  bookings: Record<string, WorkspaceBooking>;
  rentals: Record<string, Rental>;
}

export interface OfferCatalogReplacement {
  workspace_id: string;
  revision: string;
  observed_at: string;
  offers: OfferSnapshot[];
  failures: unknown[];
}

export type WorkspaceMessage =
  | { type: "domain_event"; event: CloudEvent }
  | { type: "offers_replaced"; catalog: OfferCatalogReplacement }
  | { type: "offers_unavailable" }
  | { type: "ready"; throughGlobalPosition: number };

export function createWorkspace(id: string): Workspace {
  return {
    id,
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

export function reduceWorkspace(
  workspace: Workspace,
  message: WorkspaceMessage,
): Workspace {
  switch (message.type) {
    case "ready":
      return {
        ...workspace,
        ready: true,
        throughGlobalPosition: message.throughGlobalPosition,
      };
    case "offers_unavailable":
      return { ...workspace, offersAvailable: false };
    case "offers_replaced":
      return replaceOffers(workspace, message.catalog.offers);
    case "domain_event":
      return applyDomainEvent(workspace, message.event);
  }
}

function replaceOffers(
  workspace: Workspace,
  offers: OfferSnapshot[],
): Workspace {
  const rentals = { ...workspace.rentals };
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
  for (const run of Object.values(workspace.runs)) {
    if (!run.bookingID || !run.selectedOfferID) continue;
    const booking = workspace.bookings[run.bookingID];
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
  return changed(workspace, {
    offers,
    offersAvailable: true,
    rentals,
  });
}

function applyDomainEvent(workspace: Workspace, event: CloudEvent): Workspace {
  if (event.workspaceid !== workspace.id) return workspace;
  const next = applyRunEvent(workspace, event);
  return {
    ...next,
    throughGlobalPosition: Math.max(
      next.throughGlobalPosition,
      event.globalposition,
    ),
    lastChange: workspace.ready ? "live" : "initial",
  };
}

function applyRunEvent(workspace: Workspace, event: CloudEvent): Workspace {
  switch (event.type) {
    case "compute.run.requested.v1":
      return requestRun(workspace, event);
    case "compute.run.booking_decided.v1":
      return decideBooking(workspace, event);
    case "compute.run.booking_dispatched.v1":
      return dispatchBooking(workspace, event);
    case "compute.run.launch_intent_recorded.v1":
      return recordLaunchIntent(workspace, event);
    case "compute.run.launch_accepted.v1":
    case "compute.run.external_state_observed.v1":
      return observeRun(workspace, event);
    case "compute.run.outcome_recorded.v1":
      return recordOutcome(workspace, event);
    case "compute.run.cleanup_requested.v1":
      return updateRunPhase(workspace, event, "cleaning");
    case "compute.run.closed.v1":
      return closeRun(workspace, event);
    case "compute.rental.booking_queued.v1":
    case "compute.rental.booking_dispatched.v1":
    case "compute.rental.booking_moved.v1":
      return applyRentalBookingEvent(workspace, event);
    case "compute.rental.booking_expired.v1":
    case "compute.rental.booking_cancelled.v1":
      return removeRentalBooking(workspace, event);
    default:
      return workspace;
  }
}

function dispatchBooking(workspace: Workspace, event: CloudEvent): Workspace {
  const { booking: source } = decodeEventData(BookingDispatchedData, event);
  const run = requiredRun(workspace, source.run_id, event.type);
  const booking: WorkspaceBooking = {
    id: source.id,
    rentalID: source.rental_id,
    runID: source.run_id,
    state: "running",
    scheduleVersion: source.schedule_version,
  };
  const bookings = { ...workspace.bookings, [booking.id]: booking };
  return changed(workspace, {
    bookings,
    rentals: insertBooking(workspace.rentals, bookings, booking),
    runs: {
      ...workspace.runs,
      [run.id]: { ...run, bookingID: booking.id },
    },
  });
}

function requestRun(workspace: Workspace, event: CloudEvent): Workspace {
  const data = decodeEventData(RequestedData, event);
  const runID = data.run_id;
  const workload: WorkloadRevision = data.workload_revision;
  const expected = workload.spec.placement.expected_runtime_seconds;
  const max = workload.spec.execution.max_runtime_seconds;
  // A malformed expected runtime in durable history must degrade this one
  // run, never throw: a reducer throw is a non-retryable feed error that
  // would brick the canvas for every viewer replaying the workspace.
  const run: WorkspaceRun = {
    id: runID,
    requestedAt: event.time,
    workload,
    expectedRuntimeSeconds:
      expected !== undefined && expected <= max ? expected : null,
    maxRuntimeSeconds: max,
    phase: "requested",
  };
  return changed(workspace, { runs: { ...workspace.runs, [runID]: run } });
}

function decideBooking(workspace: Workspace, event: CloudEvent): Workspace {
  const data = decodeEventData(BookingDecidedData, event);
  const decision: BookingDecision = data.decision;
  const runID = decision.run_id ?? event.correlationid;
  if (!runID) throw new Error(`${event.type} requires decision.run_id`);
  const run = requiredRun(workspace, runID, event.type);
  if (!decision.booking || !decision.selected_offer_snapshot_id) {
    return changed(workspace, {
      runs: {
        ...workspace.runs,
        [runID]: { ...run, decision },
      },
    });
  }
  const sourceBooking = decision.booking;
  const booking: WorkspaceBooking = {
    id: sourceBooking.id,
    rentalID: sourceBooking.rental_id,
    runID,
    state: sourceBooking.state,
    afterBookingID: sourceBooking.after_booking_id,
    projectedStartAt: sourceBooking.projected_start_at,
    latestStartAt: sourceBooking.latest_start_at,
    scheduleVersion: sourceBooking.schedule_version,
  };
  const current = detachSupersededBooking(workspace, run, booking.id);
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
  return changed(workspace, {
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
  workspace: Workspace,
  run: WorkspaceRun,
  nextBookingID: string,
): Workspace {
  if (!run.bookingID || run.bookingID === nextBookingID) return workspace;
  return detachBooking(workspace, run.bookingID);
}

function insertBooking(
  rentals: Record<string, Rental>,
  bookings: Record<string, WorkspaceBooking>,
  booking: WorkspaceBooking,
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
  bookings: Record<string, WorkspaceBooking>,
  rentalID: string,
): string[] {
  const candidates = Object.values(bookings).filter(
    (booking) => booking.rentalID === rentalID && booking.state === "queued",
  );
  const byPredecessor = new Map<string, WorkspaceBooking>();
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
  workspace: Workspace,
  event: CloudEvent,
): Workspace {
  const runID = runIDForEvent(event);
  const run = requiredRun(workspace, runID, event.type);
  if (!run.bookingID) return workspace;
  const booking = workspace.bookings[run.bookingID];
  if (!booking) return workspace;
  const data = decodeEventData(LaunchIntentData, event);
  const rental = workspace.rentals[booking.rentalID];
  if (!rental) return workspace;
  const provisioned = data.disposition === "terminate";
  return changed(workspace, {
    rentals: {
      ...workspace.rentals,
      [rental.id]: {
        ...rental,
        source: provisioned ? "provisioned" : "standing",
        phase: provisioned ? "provisioning" : "active",
      },
    },
    runs: {
      ...workspace.runs,
      [runID]: { ...run, phase: provisioned ? "provisioning" : run.phase },
    },
  });
}

function observeRun(workspace: Workspace, event: CloudEvent): Workspace {
  const data = decodeEventData(ObservedRunData, event);
  const phase = data.phase;
  if (phase !== "running") return workspace;
  const runID = runIDForEvent(event);
  const run = requiredRun(workspace, runID, event.type);
  const runs = {
    ...workspace.runs,
    [runID]: { ...run, phase: "running" as const, runningAt: event.time },
  };
  if (!run.bookingID) return changed(workspace, { runs });
  const booking = workspace.bookings[run.bookingID];
  const rental = booking ? workspace.rentals[booking.rentalID] : undefined;
  if (!rental) return changed(workspace, { runs });
  return changed(workspace, {
    runs,
    rentals: {
      ...workspace.rentals,
      [rental.id]: { ...rental, phase: "active" },
    },
  });
}

function recordOutcome(workspace: Workspace, event: CloudEvent): Workspace {
  const runID = runIDForEvent(event);
  const run = requiredRun(workspace, runID, event.type);
  const data = decodeEventData(OutcomeData, event);
  return changed(workspace, {
    runs: {
      ...workspace.runs,
      [runID]: {
        ...run,
        phase: "cleaning",
        outcome: data.outcome,
      },
    },
  });
}

function updateRunPhase(
  workspace: Workspace,
  event: CloudEvent,
  phase: WorkspaceRunPhase,
): Workspace {
  const runID = runIDForEvent(event);
  const run = requiredRun(workspace, runID, event.type);
  return changed(workspace, {
    runs: { ...workspace.runs, [runID]: { ...run, phase } },
  });
}

function closeRun(workspace: Workspace, event: CloudEvent): Workspace {
  const runID = runIDForEvent(event);
  const run = requiredRun(workspace, runID, event.type);
  if (!run.bookingID) {
    return changed(workspace, {
      runs: {
        ...workspace.runs,
        [runID]: { ...run, phase: "closed" },
      },
    });
  }
  return detachBooking(workspace, run.bookingID, {
    ...run,
    phase: "closed",
  });
}

function applyRentalBookingEvent(
  workspace: Workspace,
  event: CloudEvent,
): Workspace {
  const data = decodeEventData(RentalBookingData, event);
  const source = data.booking ?? data;
  const runID = data.run_id;
  const booking: WorkspaceBooking = {
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
  const bookings = { ...workspace.bookings, [booking.id]: booking };
  return changed(workspace, {
    bookings,
    rentals: insertBooking(workspace.rentals, bookings, booking),
  });
}

function removeRentalBooking(
  workspace: Workspace,
  event: CloudEvent,
): Workspace {
  const data = decodeEventData(RentalRemovalData, event);
  const bookingID =
    optionalString(data.booking_id) ?? optionalString(data.id) ?? "";
  return bookingID ? detachBooking(workspace, bookingID) : workspace;
}

function detachBooking(
  workspace: Workspace,
  bookingID: string,
  closedRun?: WorkspaceRun,
): Workspace {
  const booking = workspace.bookings[bookingID];
  if (!booking) return workspace;
  const bookings = { ...workspace.bookings };
  delete bookings[bookingID];
  const rental = workspace.rentals[booking.rentalID];
  const rentals = { ...workspace.rentals };
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
  return changed(workspace, {
    bookings,
    rentals,
    runs: closedRun
      ? { ...workspace.runs, [closedRun.id]: closedRun }
      : workspace.runs,
  });
}

function changed(workspace: Workspace, values: Partial<Workspace>): Workspace {
  return {
    ...workspace,
    ...values,
    lastChange: workspace.ready ? "live" : "initial",
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
  workspace: Workspace,
  runID: string,
  eventType: string,
): WorkspaceRun {
  const run = workspace.runs[runID];
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
