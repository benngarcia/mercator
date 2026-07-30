// This module is the locality view's whole reading of a Booking Decision. It
// answers one question per Run: of the content that Run needed, what was already
// on the machine it landed on, and what had to cross the wire to get there.
//
// That question is worth its own module because it is the one the product is
// sold on and the one the console has never drawn. A decision states it per
// candidate as image_locality, artifact_evidence and cache_evidence, and the
// canvas renders none of them: a reader today learns that a Run was placed, not
// that placing it there saved forty gigabytes of transfer.
//
// Everything is derived from the SELECTED candidate. The others are the
// placement's own subject and belong to a view about how the answer was chosen;
// this view is about what the answer cost.
//
// WHAT IS OWED, NEVER WHAT WAS SAVED. ArtifactEvidence.fetch_bytes is "what this
// host still owes", so a hot Artifact carries no byte count at all: it owes
// nothing, the field is omitempty, and the wire drops it. The size it would have
// cost lives on the catalog's ArtifactVersion and never reaches a decision. So
// this module counts owed bytes, which are real, and counts held PIECES, which
// are also real, and refuses to add a saving nobody recorded. Filed as
// mercator#244.
//
// TWO UNITS, ON PURPOSE. An image's cost is stated in seconds and an Artifact's
// in bytes, because that is what the contract measures: LaunchStageEstimates
// carries image_fetch_seconds and unpack_seconds, and only ArtifactEvidence
// carries fetch_bytes. Converting one into the other would need a transfer rate
// this module does not have and would present an invented number beside a
// measured one. A cache is recorded and never priced at all, so it carries
// neither.
//
// WHAT THIS CAN AND CANNOT SAY. A decision records what the control plane FOUND
// at placement time, which is a prediction about the read that follows, not an
// observation of it. The Lab's Effect Ledger holds observations and production
// has no equivalent, so nothing here claims a byte moved: it says a byte was
// owed. Where plan and outcome disagree, the disagreement is the calibration
// question phase 6 exists to answer, and a view that quietly presented the plan
// as the outcome would be the reason nobody noticed.

import type {
  ArtifactEvidence,
  BookingDecision,
  CacheEvidence,
  CandidateDecision,
} from "./api/types";

// ContentLocality keeps the contract's own vocabulary rather than collapsing to
// a boolean, because "unknown" is a third answer and not a kind of cold. A host
// that could not enumerate its copies has said something different from a host
// that enumerated and found nothing, and pricing them alike is exactly what the
// scheduler refuses to do.
export type ContentLocality = "hot" | "partial" | "cold" | "unknown";

export type ContentKind = "image" | "artifact" | "cache";

export interface HeldContent {
  readonly kind: ContentKind;
  readonly name: string;
  readonly locality: ContentLocality;
  // fetchBytes is what this host still owed on this content, and is null wherever
  // the contract prices it in something other than bytes. Null is not zero: a
  // cache owes nothing anybody counted, which is a different statement from a
  // cache that owes nothing.
  readonly fetchBytes: number | null;
  // fetchSeconds is the same debt where the contract states it as time, which is
  // how an image is stated.
  readonly fetchSeconds: number | null;
}

// LaunchStage is one segment of the eight a launch is made of. Drawn end to end
// they are where a Run's start actually goes, and locality is visible as the
// segments that collapsed.
export interface LaunchStage {
  readonly name: string;
  readonly seconds: number;
  // contentBound marks the stages locality can shrink. The others are the
  // machine's own cost and no amount of warmth touches them, so a view that let
  // them share an accent would claim credit locality never earned.
  readonly contentBound: boolean;
}

export interface RunLocality {
  readonly runID: string;
  readonly offerSnapshotID: string | null;
  readonly disposition: CandidateDecision["disposition"] | null;
  readonly content: readonly HeldContent[];
  readonly stages: readonly LaunchStage[];
  // owedBytes is what the selected machine still had to fetch, counted only over
  // content the contract prices in bytes, which today is Artifacts alone.
  readonly owedBytes: number;
  // heldPieces and owedPieces are counts rather than sizes, because a hot
  // Artifact's size is not on the decision. A count is the strongest true thing
  // this view can say about what warmth was worth.
  readonly heldPieces: number;
  readonly owedPieces: number;
  // Counted over the content-bound stages: what the image cost, and what it
  // would have cost had nothing been held.
  readonly fetchSeconds: number;
}

// crossesTheWire reports whether this content had to be fetched. Unknown counts
// as crossing, for the same reason the scheduler prices it as cold: a host that
// cannot say what it holds has not said it holds anything, and drawing silence
// as warmth would show a saving nobody established. Partial also counts, because
// some layers still moved.
export function crossesTheWire(locality: ContentLocality): boolean {
  return locality !== "hot";
}

export function selectedCandidate(
  decision: BookingDecision,
): CandidateDecision | undefined {
  const selected = decision.selected_offer_snapshot_id;
  if (!selected) return undefined;
  return decision.candidates.find(
    (candidate) => candidate.offer_snapshot_id === selected,
  );
}

const expected = (estimate: { expected?: number } | undefined): number =>
  estimate?.expected ?? 0;

function stagesOf(candidate: CandidateDecision): LaunchStage[] {
  const stages = candidate.estimates.stages;
  return [
    { name: "acquire", seconds: expected(stages.acquisition_seconds), contentBound: false },
    { name: "boot", seconds: expected(stages.boot_seconds), contentBound: false },
    { name: "agent ready", seconds: expected(stages.agent_ready_seconds), contentBound: false },
    { name: "image fetch", seconds: expected(stages.image_fetch_seconds), contentBound: true },
    { name: "unpack", seconds: expected(stages.unpack_seconds), contentBound: true },
    { name: "artifact fetch", seconds: expected(stages.artifact_fetch_seconds), contentBound: true },
    { name: "container start", seconds: expected(stages.container_start_seconds), contentBound: false },
    { name: "app ready", seconds: expected(stages.application_ready_seconds), contentBound: false },
  ];
}

function imageContent(candidate: CandidateDecision, name: string): HeldContent | null {
  const locality = candidate.image_locality;
  if (!locality) return null;
  const stages = candidate.estimates.stages;
  return {
    kind: "image",
    name,
    locality,
    fetchBytes: null,
    fetchSeconds: expected(stages.image_fetch_seconds) + expected(stages.unpack_seconds),
  };
}

const artifactContent = (evidence: readonly ArtifactEvidence[]): HeldContent[] =>
  evidence.map((entry) => ({
    kind: "artifact" as const,
    name: entry.artifact_id,
    locality: entry.locality,
    fetchBytes: entry.fetch_bytes ?? 0,
    fetchSeconds: null,
  }));

const cacheContent = (evidence: readonly CacheEvidence[]): HeldContent[] =>
  evidence.map((entry) => ({
    kind: "cache" as const,
    name: entry.name,
    locality: entry.locality,
    fetchBytes: null,
    fetchSeconds: null,
  }));

// localityOf reads one Run's answer out of its decision chain. It takes the LAST
// decision rather than the first: a Run whose first answer was replaced landed
// where the replacement sent it, and the content it found there is the
// replacement's evidence.
export function localityOf(
  runID: string,
  decisions: readonly BookingDecision[] | null | undefined,
  imageName: string,
): RunLocality | null {
  const decision = decisions?.at(-1);
  if (!decision) return null;
  const candidate = selectedCandidate(decision);
  if (!candidate) return null;

  const image = imageContent(candidate, imageName);
  const content: HeldContent[] = [
    ...(image ? [image] : []),
    ...artifactContent(candidate.artifact_evidence ?? []),
    ...cacheContent(candidate.cache_evidence ?? []),
  ];

  let owedBytes = 0;
  let heldPieces = 0;
  let owedPieces = 0;
  for (const piece of content) {
    if (crossesTheWire(piece.locality)) {
      owedPieces += 1;
      owedBytes += piece.fetchBytes ?? 0;
    } else {
      heldPieces += 1;
    }
  }

  const stages = stagesOf(candidate);
  const fetchSeconds = stages
    .filter((stage) => stage.contentBound)
    .reduce((total, stage) => total + stage.seconds, 0);

  return {
    runID,
    offerSnapshotID: decision.selected_offer_snapshot_id ?? null,
    disposition: candidate.disposition,
    content,
    stages,
    owedBytes,
    heldPieces,
    owedPieces,
    fetchSeconds,
  };
}

// fleetTotals is the scoreboard. Owed is in bytes and held is in pieces, and the
// two deliberately do not share a unit: making them look comparable would imply a
// saving this data cannot state.
export function fleetTotals(records: readonly RunLocality[]): {
  owedBytes: number;
  heldPieces: number;
  owedPieces: number;
  fetchSeconds: number;
} {
  return records.reduce(
    (total, record) => ({
      owedBytes: total.owedBytes + record.owedBytes,
      heldPieces: total.heldPieces + record.heldPieces,
      owedPieces: total.owedPieces + record.owedPieces,
      fetchSeconds: total.fetchSeconds + record.fetchSeconds,
    }),
    { owedBytes: 0, heldPieces: 0, owedPieces: 0, fetchSeconds: 0 },
  );
}
