import { beforeEach, describe, expect, test } from "bun:test";

import {
  getWorkspace,
  getRecentWorkspaces,
  setWorkspace,
  workspaceOptions,
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

  test("defaults to the most recently added workspace", () => {
    setWorkspace("ws_one");
    setWorkspace("ws_two"); // most recent
    // Clear the explicit current selection; the default should be the latest recent.
    (globalThis as unknown as { localStorage: Storage }).localStorage.removeItem(
      "mercator.workspace",
    );
    expect(getWorkspace()).toBe("ws_two");
  });
});

describe("setWorkspace", () => {
  test("records the committed workspace in recents, most-recent first", () => {
    setWorkspace("ws_a");
    setWorkspace("ws_b");
    expect(getRecentWorkspaces()).toEqual(["ws_b", "ws_a"]);
  });
});

describe("workspaceOptions", () => {
  test("lists the active workspace first, then recents, deduped", () => {
    expect(workspaceOptions("ws_x", ["ws_a", "ws_x"])).toEqual(["ws_x", "ws_a"]);
  });

  test("omits an empty active workspace", () => {
    expect(workspaceOptions("", ["ws_a"])).toEqual(["ws_a"]);
  });

  test("does not invent a default when nothing exists", () => {
    expect(workspaceOptions(null, [])).toEqual([]);
  });
});
