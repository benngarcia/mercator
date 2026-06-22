// Session storage: the bearer token and the default workspace id live in
// localStorage. The token is NEVER placed in the URL; workspace_id may be a
// route search param, but its default is read from here. A tiny subscribe
// helper lets React components stay in sync via useSyncExternalStore.

const TOKEN_KEY = "mercator.token";
const WORKSPACE_KEY = "mercator.workspace";
const RECENT_WORKSPACES_KEY = "mercator.recentWorkspaces";
const MAX_RECENTS = 8;

type Listener = () => void;

const listeners = new Set<Listener>();

function emit(): void {
  for (const listener of listeners) {
    listener();
  }
}

// hasStorage guards against SSR / non-browser contexts and privacy modes that
// throw on localStorage access.
function hasStorage(): boolean {
  try {
    return typeof localStorage !== "undefined";
  } catch {
    return false;
  }
}

function read(key: string): string | null {
  if (!hasStorage()) {
    return null;
  }
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key: string, value: string | null): void {
  if (!hasStorage()) {
    return;
  }
  try {
    if (value === null || value === "") {
      localStorage.removeItem(key);
    } else {
      localStorage.setItem(key, value);
    }
  } catch {
    // best-effort: ignore quota / privacy errors.
  }
  emit();
}

export function getToken(): string | null {
  return read(TOKEN_KEY);
}

export function setToken(token: string | null): void {
  write(TOKEN_KEY, token);
}

export function getWorkspace(): string | null {
  return read(WORKSPACE_KEY);
}

export function setWorkspace(workspaceID: string | null): void {
  if (workspaceID) {
    pushRecentWorkspace(workspaceID);
  }
  write(WORKSPACE_KEY, workspaceID);
}

export function getRecentWorkspaces(): string[] {
  const raw = read(RECENT_WORKSPACES_KEY);
  if (!raw) {
    return [];
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      return parsed.filter((entry): entry is string => typeof entry === "string");
    }
  } catch {
    // fall through to empty.
  }
  return [];
}

function pushRecentWorkspace(workspaceID: string): void {
  const existing = getRecentWorkspaces().filter((entry) => entry !== workspaceID);
  const next = [workspaceID, ...existing].slice(0, MAX_RECENTS);
  if (!hasStorage()) {
    return;
  }
  try {
    localStorage.setItem(RECENT_WORKSPACES_KEY, JSON.stringify(next));
  } catch {
    // ignore.
  }
}

// subscribe registers a listener invoked on any session mutation. It also
// listens for cross-tab `storage` events so multiple tabs stay coherent.
// Returns an unsubscribe function (useSyncExternalStore contract).
export function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  const onStorage = (event: StorageEvent) => {
    if (
      event.key === null ||
      event.key === TOKEN_KEY ||
      event.key === WORKSPACE_KEY ||
      event.key === RECENT_WORKSPACES_KEY
    ) {
      listener();
    }
  };
  if (typeof window !== "undefined") {
    window.addEventListener("storage", onStorage);
  }
  return () => {
    listeners.delete(listener);
    if (typeof window !== "undefined") {
      window.removeEventListener("storage", onStorage);
    }
  };
}

// snapshot returns a stable string used by useSyncExternalStore to detect
// changes. It intentionally excludes the recents list since the token/workspace
// pair is what drives request behavior.
export function snapshot(): string {
  return `${getWorkspace() ?? ""}::${getToken() ? "1" : "0"}`;
}
