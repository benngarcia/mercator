import { expect, test } from "vitest";

import type { BookingDecision, CandidateDecision } from "@/lib/api/types";
import { crossesTheWire, fleetTotals, localityOf } from "./locality";

function stages(overrides: Record<string, number> = {}) {
  const at = (key: string) => ({ expected: overrides[key] ?? 0 });
  return {
    acquisition_seconds: at("acquisition_seconds"),
    boot_seconds: at("boot_seconds"),
    agent_ready_seconds: at("agent_ready_seconds"),
    image_fetch_seconds: at("image_fetch_seconds"),
    unpack_seconds: at("unpack_seconds"),
    artifact_fetch_seconds: at("artifact_fetch_seconds"),
    container_start_seconds: at("container_start_seconds"),
    application_ready_seconds: at("application_ready_seconds"),
  };
}

function candidate(overrides: Partial<CandidateDecision> = {}): CandidateDecision {
  return {
    offer_snapshot_id: "rental-warm",
    disposition: "run_now_existing_rental",
    feasible: true,
    estimates: {
      queue_seconds: { expected: 0 },
      stages: stages(),
      start_seconds: { expected: 0 },
      established_start_seconds: { expected: 0 },
      cost_usd: { expected: 0 },
    },
    ...overrides,
  } as CandidateDecision;
}

function decision(overrides: Partial<BookingDecision> = {}): BookingDecision {
  return {
    id: "dec_1",
    workload_revision_digest: "sha256:abc",
    evaluated_at: "2030-01-01T00:00:00Z",
    model_version: "v1",
    policy: { service_class: "batch" },
    weights: {},
    collection_report: {},
    candidates: [candidate()],
    selected_offer_snapshot_id: "rental-warm",
    selection_reason_codes: [],
    ...overrides,
  } as BookingDecision;
}

// The distinction the whole view exists to draw, in the shape the API really
// sends it: fetch_bytes is what a host still OWES, so a hot Artifact carries none
// at all. This fixture is copied from a live artifact-warmth-restart decision.
test("an Artifact already on the machine is held, and states no bytes", () => {
  // Arrange
  const chain = [
    decision({
      candidates: [
        candidate({
          artifact_evidence: [
            { artifact_id: "artifact:model-checkpoint:v1", locality: "cold", fetch_bytes: 40_000_000_000 },
            { artifact_id: "artifact:tokenizer:v1", locality: "hot" },
          ],
        }),
      ],
    }),
  ];

  // Act
  const record = localityOf("run-consumer", chain, "consumer");

  // Assert
  expect(record?.owedBytes).toBe(40_000_000_000);
  expect(record?.owedPieces).toBe(1);
  expect(record?.heldPieces).toBe(1);
  const held = record?.content.find((piece) => piece.name === "artifact:tokenizer:v1");
  expect(held?.fetchBytes).toBe(0);
});

// Unknown is a third answer and must not be drawn as warmth. A host that cannot
// enumerate its copies has not said it holds anything.
test("unknown locality is owed, never held", () => {
  // Arrange
  const chain = [
    decision({
      candidates: [
        candidate({
          artifact_evidence: [
            { artifact_id: "mystery:v1", locality: "unknown", fetch_bytes: 1_000 },
          ],
        }),
      ],
    }),
  ];

  // Act
  const record = localityOf("run-1", chain, "img");

  // Assert
  expect(record?.heldPieces).toBe(0);
  expect(record?.owedBytes).toBe(1_000);
  expect(crossesTheWire("unknown")).toBe(true);
  expect(crossesTheWire("partial")).toBe(true);
  expect(crossesTheWire("hot")).toBe(false);
});

// A cache is recorded and never priced. Counting it at zero bytes would put it in
// the saved column beside an Artifact that really was forty gigabytes.
test("a cache is carried without being priced", () => {
  // Arrange
  const chain = [
    decision({
      candidates: [
        candidate({ cache_evidence: [{ name: "compiler-cache", locality: "hot" }] }),
      ],
    }),
  ];

  // Act
  const record = localityOf("run-1", chain, "img");

  // Assert
  const cache = record?.content.find((piece) => piece.kind === "cache");
  expect(cache?.fetchBytes).toBeNull();
  expect(record?.owedBytes).toBe(0);
  expect(record?.heldPieces).toBe(1);
});

// An image is priced in seconds because that is what the contract measures.
// Only the content-bound stages count toward what locality could have saved.
test("only content-bound stages are attributed to locality", () => {
  // Arrange
  const chain = [
    decision({
      candidates: [
        candidate({
          image_locality: "partial",
          estimates: {
            queue_seconds: { expected: 0 },
            stages: stages({
              acquisition_seconds: 24,
              boot_seconds: 180,
              image_fetch_seconds: 289,
              unpack_seconds: 30,
              artifact_fetch_seconds: 640,
            }),
            start_seconds: { expected: 0 },
            established_start_seconds: { expected: 0 },
            cost_usd: { expected: 0 },
          },
        } as Partial<CandidateDecision>),
      ],
    }),
  ];

  // Act
  const record = localityOf("run-1", chain, "producer");

  // Assert
  expect(record?.fetchSeconds).toBe(289 + 30 + 640);
  expect(record?.stages.find((s) => s.name === "boot")?.contentBound).toBe(false);
  const image = record?.content.find((piece) => piece.kind === "image");
  expect(image?.fetchSeconds).toBe(319);
  expect(image?.fetchBytes).toBeNull();
});

// A Run whose first answer was replaced landed where the replacement sent it.
test("the last decision in the chain is the one that placed the Run", () => {
  // Arrange
  const first = decision({
    id: "dec_1",
    selected_offer_snapshot_id: "fresh-4090",
    candidates: [candidate({ offer_snapshot_id: "fresh-4090" })],
  });
  const replacement = decision({
    id: "dec_2",
    selected_offer_snapshot_id: "rental-warm",
    supersedes: "dec_1",
  });

  // Act
  const record = localityOf("run-1", [first, replacement], "img");

  // Assert
  expect(record?.offerSnapshotID).toBe("rental-warm");
});

test("a Run nothing has decided about yet has no locality to show", () => {
  expect(localityOf("run-1", null, "img")).toBeNull();
  expect(localityOf("run-1", [], "img")).toBeNull();
});

// A decision whose selected offer is not among its candidates is a decision this
// view cannot read, and guessing a candidate would attribute one machine's
// warmth to another.
test("a selection naming no candidate yields nothing rather than a guess", () => {
  // Arrange
  const orphan = decision({ selected_offer_snapshot_id: "not-in-the-list" });

  // Act
  const record = localityOf("run-1", [orphan], "img");

  // Assert
  expect(record).toBeNull();
});

test("fleet totals add the runs up", () => {
  // Arrange
  const one = localityOf("a", [decision({ candidates: [candidate({
    artifact_evidence: [{ artifact_id: "x", locality: "hot" }],
  })] })], "img");
  const two = localityOf("b", [decision({ candidates: [candidate({
    artifact_evidence: [{ artifact_id: "y", locality: "cold", fetch_bytes: 250 }],
  })] })], "img");

  // Act
  const totals = fleetTotals([one!, two!]);

  // Assert
  expect(totals).toEqual({ owedBytes: 250, heldPieces: 1, owedPieces: 1, fetchSeconds: 0 });
});
