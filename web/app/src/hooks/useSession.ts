// useSession exposes the workspace default + bearer token from session.ts as
// reactive state via useSyncExternalStore, plus setters that persist to
// localStorage and notify all subscribers (including other tabs).

import { useCallback, useSyncExternalStore } from "react";

import {
  getRecentWorkspaces,
  getToken,
  getWorkspace,
  setToken as persistToken,
  setWorkspace as persistWorkspace,
  snapshot,
  subscribe,
} from "@/lib/session";

export interface UseSession {
  token: string | null;
  workspace: string | null;
  recentWorkspaces: string[];
  hasToken: boolean;
  setToken: (token: string | null) => void;
  setWorkspace: (workspaceID: string | null) => void;
}

export function useSession(): UseSession {
  // snapshot() is a primitive string so useSyncExternalStore's default
  // referential check is stable across renders without memoization.
  const snap = useSyncExternalStore(subscribe, snapshot, snapshot);

  const setToken = useCallback((token: string | null) => {
    persistToken(token);
  }, []);

  const setWorkspace = useCallback((workspaceID: string | null) => {
    persistWorkspace(workspaceID);
  }, []);

  // snap is read so the hook re-runs (and re-reads the getters) on change.
  void snap;

  const token = getToken();
  return {
    token,
    workspace: getWorkspace(),
    recentWorkspaces: getRecentWorkspaces(),
    hasToken: Boolean(token),
    setToken,
    setWorkspace,
  };
}
