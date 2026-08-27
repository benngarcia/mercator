import type { StoredCandidateDecision } from "@/lib/deployment/contracts";

/**
 * priced reports whether the dollars in a candidate's cost estimate are a price
 * somebody quoted. It reads the source of the cost estimate, which is where the
 * decision states that nobody did.
 */
export function priced(candidate: StoredCandidateDecision): boolean {
  return candidate.estimates?.cost_usd?.source !== "unpriced";
}

/**
 * orderCandidates is the reading order of a Booking Decision's candidates, and it
 * is the placement's own ranking rather than a sort on the score.
 *
 * The score is in dollars and a candidate nobody quoted has none of them, so its
 * score states only the waiting and the doubt. Sorting the rows on that number
 * put the unquoted machine at the top as the cheapest thing in the fleet, at a
 * fraction of the score of the machine Mercator actually selected, which is the
 * inverse of how the placement ranked them: an unpriced candidate is taken only
 * when nothing priced will do. The decision names that rule in its selection
 * reasons, and this is the same rule in the view of it.
 */
export function orderCandidates(
  candidates: StoredCandidateDecision[],
  selectedOfferId?: string,
): StoredCandidateDecision[] {
  return [...candidates].sort((a, b) => {
    if (a.offer_snapshot_id === selectedOfferId) return -1;
    if (b.offer_snapshot_id === selectedOfferId) return 1;
    if (a.feasible !== b.feasible) return a.feasible ? -1 : 1;
    if (priced(a) !== priced(b)) return priced(a) ? -1 : 1;
    const as = a.score_usd ?? Number.POSITIVE_INFINITY;
    const bs = b.score_usd ?? Number.POSITIVE_INFINITY;
    return as - bs;
  });
}
