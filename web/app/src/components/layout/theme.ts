// Theme handling for the console: dark-first with a light toggle, persisted to
// localStorage and applied by toggling the `.dark` class on <html> (which the
// index.css tokens key off via @custom-variant dark). A small external store +
// useSyncExternalStore keeps every ThemeToggle instance and tab in sync.
//
// This module owns ONLY the runtime class/persistence wiring; the actual color
// tokens live in index.css and are not duplicated here.

import { useCallback, useSyncExternalStore } from "react";

export type Theme = "dark" | "light";

const THEME_KEY = "mercator.theme";

type Listener = () => void;
const listeners = new Set<Listener>();

function hasStorage(): boolean {
  try {
    return typeof localStorage !== "undefined";
  } catch {
    return false;
  }
}

function readStored(): Theme | null {
  if (!hasStorage()) return null;
  try {
    const raw = localStorage.getItem(THEME_KEY);
    return raw === "dark" || raw === "light" ? raw : null;
  } catch {
    return null;
  }
}

// resolveInitial: stored preference wins; otherwise dark-first per the design.
function resolveInitial(): Theme {
  return readStored() ?? "dark";
}

function applyToDocument(theme: Theme): void {
  if (typeof document === "undefined") return;
  document.documentElement.classList.toggle("dark", theme === "dark");
}

let current: Theme = resolveInitial();

function emit(): void {
  for (const listener of listeners) listener();
}

// applyInitialTheme is invoked once at startup (from main.tsx) so the class is
// set before first paint. Safe to call repeatedly.
export function applyInitialTheme(): void {
  current = resolveInitial();
  applyToDocument(current);
}

export function getTheme(): Theme {
  return current;
}

export function setTheme(theme: Theme): void {
  current = theme;
  applyToDocument(theme);
  if (hasStorage()) {
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      // ignore quota / privacy errors.
    }
  }
  emit();
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  const onStorage = (event: StorageEvent) => {
    if (event.key === THEME_KEY || event.key === null) {
      current = resolveInitial();
      applyToDocument(current);
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

export interface UseTheme {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  toggle: () => void;
}

export function useTheme(): UseTheme {
  const theme = useSyncExternalStore(subscribe, getTheme, getTheme);
  const set = useCallback((next: Theme) => setTheme(next), []);
  const toggle = useCallback(
    () => setTheme(getTheme() === "dark" ? "light" : "dark"),
    [],
  );
  return { theme, setTheme: set, toggle };
}
