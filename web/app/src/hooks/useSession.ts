import { useAtomSet, useAtomValue } from "@effect/atom-react";
import * as AsyncResult from "effect/unstable/reactivity/AsyncResult";

import {
  sessionAtom,
  setTokenAtom,
} from "@/lib/session-atoms";

export interface UseSession {
  readonly token: string | null;
  readonly hasToken: boolean;
  readonly setToken: (token: string | null) => void;
}

const emptySession = { token: null } as const;

export function useSession(): UseSession {
  const result = useAtomValue(sessionAtom);
  const setToken = useAtomSet(setTokenAtom);
  const session = AsyncResult.getOrElse(result, () => emptySession);

  return {
    ...session,
    hasToken: session.token !== null,
    setToken,
  };
}
