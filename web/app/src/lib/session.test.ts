import { beforeEach, describe, expect, test } from "bun:test";

import {
  getWorkspace,
  setWorkspace,
} from "./session";

// Minimal in-memory localStorage so the browser-targeted session module is
// exercisable under Bun's DOM-less test runtime.
class MemStorage {
  private m = new Map<string, string>();
  getItem(k: string): string | null {
    return this.m.has(k) ? (this.m.get(k) as string) : null;
  }
  setItem(k: string, v: string): void {
    this.m.set(k, String(v));
  }
  removeItem(k: string): void {
    this.m.delete(k);
  }
  clear(): void {
    this.m.clear();
  }
  key(i: number): string | null {
    return [...this.m.keys()][i] ?? null;
  }
  get length(): number {
    return this.m.size;
  }
}

beforeEach(() => {
  (globalThis as unknown as { localStorage: Storage }).localStorage =
    new MemStorage() as unknown as Storage;
});

describe("getWorkspace", () => {
  test("is null when nothing has been chosen (no hardcoded default)", () => {
    expect(getWorkspace()).toBeNull();
  });

  test("returns the stored workspace once set", () => {
    setWorkspace("ws_42");
    expect(getWorkspace()).toBe("ws_42");
  });

  test("does not reconstruct a default from legacy recents", () => {
    localStorage.setItem("mercator.recentWorkspaces", JSON.stringify(["ws_old"]));
    expect(getWorkspace()).toBeNull();
  });
});
