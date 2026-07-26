import { expect, test } from "vitest";

import type { CandidateDecision } from "@/lib/api/types";
import { orderCandidates, priced } from "./placement";

function candidate(
  id: string,
  score: number,
  costSource: string,
  feasible = true,
): CandidateDecision {
  return {
    offer_snapshot_id: id,
    connection_id: "conn_1",
    disposition: "reuse_rental",
    feasible,
    score_usd: score,
    estimates: {
      queue_seconds: { expected: 0 },
      stages: {
        acquisition_seconds: { expected: 0 },
        boot_seconds: { expected: 0 },
        agent_ready_seconds: { expected: 0 },
        image_fetch_seconds: { expected: 0 },
        unpack_seconds: { expected: 0 },
        artifact_fetch_seconds: { expected: 0 },
        container_start_seconds: { expected: 0 },
        application_ready_seconds: { expected: 0 },
      },
      start_seconds: { expected: 1 },
      established_start_seconds: { expected: 1 },
      cost_usd: { expected: score, source: costSource },
    },
  } as unknown as CandidateDecision;
}

test("a machine nobody quoted is read after every machine somebody did", () => {
  const quoted = candidate("rental-quoted", 0.3338, "offer_price");
  const unquoted = candidate("rental-unquoted", 0.0005, "unpriced");

  const ordered = orderCandidates([unquoted, quoted]);

  expect(ordered.map((row) => row.offer_snapshot_id)).toEqual([
    "rental-quoted",
    "rental-unquoted",
  ]);
  expect(priced(unquoted)).toBe(false);
});

test("the selected offer leads, and infeasible candidates come last", () => {
  const cheap = candidate("rental-cheap", 0.1, "offer_price");
  const selected = candidate("rental-selected", 0.4, "offer_price");
  const refused = candidate("rental-refused", 0, "offer_price", false);

  const ordered = orderCandidates(
    [refused, cheap, selected],
    "rental-selected",
  );

  expect(ordered.map((row) => row.offer_snapshot_id)).toEqual([
    "rental-selected",
    "rental-cheap",
    "rental-refused",
  ]);
});
