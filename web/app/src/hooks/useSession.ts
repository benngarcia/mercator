import { useAtomSet, useAtomValue } from "@effect/atom-react";
import * as AsyncResult from "effect/unstable/reactivity/AsyncResult";

import {
  sessionAtom,
  setTokenAtom,
  setWorkspaceAtom,
} from "@/lib/session-atoms";

export interface UseSession {
  readonly token: string | null;
  readonly workspace: string | null;
  readonly hasToken: boolean;
  readonly setToken: (token: string | null) => void;
  readonly setWorkspace: (workspaceID: string | null) => void;
}

const emptySession = { token: null, workspace: null } as const;

export function useSession(): UseSession {
  const result = useAtomValue(sessionAtom);
  const setToken = useAtomSet(setTokenAtom);
  const setWorkspace = useAtomSet(setWorkspaceAtom);
  const session = AsyncResult.getOrElse(result, () => emptySession);

  return {
    ...session,
    hasToken: session.token !== null,
    setToken,
    setWorkspace,
  };
}
