import { useSyncExternalStore } from "react";

interface Clock {
  now: number;
  listeners: Set<() => void>;
  timer: ReturnType<typeof setInterval> | null;
}

const clocks = new Map<number, Clock>();

function clockFor(intervalMs: number): Clock {
  let clock = clocks.get(intervalMs);
  if (!clock) {
    clock = { now: Date.now(), listeners: new Set(), timer: null };
    clocks.set(intervalMs, clock);
  }
  return clock;
}

export function useNow(intervalMs: number): number {
  const clock = clockFor(intervalMs);
  return useSyncExternalStore(
    (listener) => {
      clock.listeners.add(listener);
      if (!clock.timer) {
        clock.timer = setInterval(() => {
          clock.now = Date.now();
          for (const notify of clock.listeners) notify();
        }, intervalMs);
      }
      return () => {
        clock.listeners.delete(listener);
        if (clock.listeners.size === 0 && clock.timer) {
          clearInterval(clock.timer);
          clock.timer = null;
        }
      };
    },
    () => clock.now,
  );
}
