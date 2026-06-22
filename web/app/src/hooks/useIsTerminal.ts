// useIsTerminal reports whether a run has reached a terminal state. The
// authoritative signal is run.closed (the V1 lifecycle's terminal flag); a
// closed run never changes, so polling stops once it is true.

import type { Run } from "@/lib/api/types";

export function isTerminal(run: Run | null | undefined): boolean {
  return Boolean(run?.closed);
}

export function useIsTerminal(run: Run | null | undefined): boolean {
  return isTerminal(run);
}
