import { afterEach, beforeEach, expect, test } from "bun:test";

import { setWorkspace } from "../session";
import { apiFetch } from "./client";

let requestedURL = "";
const originalFetch = globalThis.fetch;

beforeEach(() => {
  (globalThis as unknown as { localStorage: Storage }).localStorage =
    new MemoryStorage() as unknown as Storage;
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    requestedURL = String(input);
    return new Response("{}", { status: 200 });
  }) as typeof fetch;
  requestedURL = "";
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("workspace scope none omits the selected workspace", async () => {
  setWorkspace("ws_selected");

  await apiFetch("/v1/workspaces", { workspaceScope: "none" });

  expect(requestedURL).toBe("/v1/workspaces");
});

test("explicit workspace scope overrides the selected workspace", async () => {
  setWorkspace("ws_selected");

  await apiFetch("/v1/runs", {
    workspaceScope: { workspaceId: "ws_explicit" },
  });

  expect(requestedURL).toBe("/v1/runs?workspace_id=ws_explicit");
});

class MemoryStorage {
  private values = new Map<string, string>();

  getItem(key: string): string | null {
    return this.values.get(key) ?? null;
  }

  setItem(key: string, value: string): void {
    this.values.set(key, value);
  }

  removeItem(key: string): void {
    this.values.delete(key);
  }
}
