// Session storage: the bearer token and the default workspace id live in
// localStorage. The token is NEVER placed in the URL; workspace_id may be a
// route search param, but its default is read from here. A tiny subscribe
// helper lets React components stay in sync via useSyncExternalStore.

const TOKEN_KEY = "mercator.token";
const WORKSPACE_KEY = "mercator.workspace";

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

// getWorkspace returns the operator's explicitly chosen workspace. The saved
// workspace catalog lives on the server and is never reconstructed locally.
export function getWorkspace(): string | null {
  const stored = read(WORKSPACE_KEY);
  if (stored && stored.trim() !== "") {
    return stored;
  }
  return null;
}

export function setWorkspace(workspaceID: string | null): void {
  write(WORKSPACE_KEY, workspaceID);
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
      event.key === WORKSPACE_KEY
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
