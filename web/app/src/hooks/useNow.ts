import { useAtomValue } from "@effect/atom-react";
import * as AsyncResult from "effect/unstable/reactivity/AsyncResult";
import * as Atom from "effect/unstable/reactivity/Atom";

import { clock } from "@/lib/clock";
import { runtime } from "@/lib/runtime";

const clockAtom = Atom.family((intervalMs: number) =>
  runtime.atom(clock(intervalMs), { initialValue: Date.now() }),
);

export function useNow(intervalMs: number): number {
  return AsyncResult.getOrElse(useAtomValue(clockAtom(intervalMs)), Date.now);
}
