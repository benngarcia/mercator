// usePollInterval centralizes the polling cadences from the design spec and
// produces a refetchInterval value suitable for TanStack Query: a number of
// milliseconds while live, or `false` once a stop condition is met.
//
//   runs lists      -> 3s
//   run / events    -> 2s while !closed, then stop
//   offers / conns  -> 10s
//
// TanStack Query's refetchInterval can also be a function of the current data;
// the helpers here cover both the static and data-derived cases.

import type { Run } from "@/lib/api/types";
import { isTerminal } from "./useIsTerminal";

export const POLL = {
  runs: 3_000,
  run: 2_000,
  events: 2_000,
  offers: 10_000,
  connections: 10_000,
} as const;

export type RefetchInterval = number | false;

// runRefetchInterval stops polling once the run is closed (terminal).
export function runRefetchInterval(
  run: Run | null | undefined,
  base: number = POLL.run,
): RefetchInterval {
  return isTerminal(run) ? false : base;
}

// usePollInterval returns a fixed interval, or `false` when disabled. Useful
// for list views with a constant cadence.
export function usePollInterval(
  base: number,
  enabled: boolean = true,
): RefetchInterval {
  return enabled ? base : false;
}
