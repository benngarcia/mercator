import { flushSync } from "react-dom";

import { ApiError, apiStream } from "@/lib/api/client";
import type { CloudEvent } from "@/lib/api/types";

import {
  createWorkspace,
  reduceWorkspace,
  type OfferCatalogReplacement,
  type Workspace,
  type WorkspaceMessage,
} from "./reducer";

export type WorkspaceFeedStatus =
  | "idle"
  | "connecting"
  | "live"
  | "degraded"
  | "error";

export interface WorkspaceFeedSnapshot {
  workspace: Workspace;
  status: WorkspaceFeedStatus;
  error: Error | null;
}

export type WorkspaceMessageListener = (
  message: WorkspaceMessage,
  snapshot: WorkspaceFeedSnapshot,
) => void;

export interface WorkspaceFeedStore {
  subscribe(listener: () => void): () => void;
  getSnapshot(): WorkspaceFeedSnapshot;
  start(): void;
  stop(): void;
}

interface SSEFrame {
  id: string;
  event: string;
  data: string;
}

export class ConsoleEventFeed implements WorkspaceFeedStore {
  private readonly workspaceID: string;
  private readonly onMessage?: WorkspaceMessageListener;
  private readonly listeners = new Set<() => void>();
  private controller: AbortController | null = null;
  private lastEventID = "";
  private activeTransition: ViewTransition | null = null;
  private snapshot: WorkspaceFeedSnapshot;

  constructor(workspaceID: string, onMessage?: WorkspaceMessageListener) {
    this.workspaceID = workspaceID;
    this.onMessage = onMessage;
    this.snapshot = {
      workspace: createWorkspace(workspaceID),
      status: "idle",
      error: null,
    };
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): WorkspaceFeedSnapshot => this.snapshot;

  start(): void {
    if (this.controller) return;
    this.controller = new AbortController();
    this.setConnection("connecting", null);
    void this.connect(this.controller.signal);
  }

  stop(): void {
    this.controller?.abort();
    this.controller = null;
  }

  private async connect(signal: AbortSignal): Promise<void> {
    let delayMilliseconds = 500;
    while (!signal.aborted) {
      try {
        const response = await apiStream("/v1/console/events", {
          workspaceScope: { workspaceId: this.workspaceID },
          signal,
          lastEventID: this.lastEventID,
        });
        await readSSE(response, (frame) => this.receive(frame));
        if (signal.aborted) return;
        this.setConnection("connecting", null);
        delayMilliseconds = 500;
      } catch (error) {
        if (signal.aborted) return;
        const resolved = error instanceof Error ? error : new Error(String(error));
        if (isPermanentFeedError(error)) {
          this.setConnection("error", resolved);
          return;
        }
        this.setConnection("connecting", resolved);
      }
      await abortableDelay(delayMilliseconds, signal);
      delayMilliseconds = Math.min(delayMilliseconds * 2, 5_000);
    }
  }

  private receive(frame: SSEFrame): void {
    if (frame.id) this.lastEventID = frame.id;
    let message: WorkspaceMessage;
    switch (frame.event) {
      case "domain_event":
        message = {
          type: "domain_event",
          event: JSON.parse(frame.data) as CloudEvent,
        };
        break;
      case "offers_replaced":
        message = {
          type: "offers_replaced",
          catalog: JSON.parse(frame.data) as OfferCatalogReplacement,
        };
        break;
      case "offers_unavailable":
        message = { type: "offers_unavailable" };
        break;
      case "ready": {
        const ready = JSON.parse(frame.data) as {
          through_global_position: number;
        };
        message = {
          type: "ready",
          throughGlobalPosition: ready.through_global_position,
        };
        break;
      }
      default:
        return;
    }
    this.apply(message);
  }

  private apply(message: WorkspaceMessage): void {
    const update = () => {
      const workspace = reduceWorkspace(this.snapshot.workspace, message);
      let status = this.snapshot.status;
      if (message.type === "ready") status = "live";
      if (message.type === "offers_unavailable") status = "degraded";
      if (message.type === "offers_replaced" && workspace.ready) status = "live";
      this.snapshot = { workspace, status, error: null };
      for (const listener of this.listeners) listener();
      this.onMessage?.(message, this.snapshot);
    };
    if (shouldAnimate(this.snapshot.workspace, message)) {
      if (this.activeTransition) {
        update();
        return;
      }
      const transition = document.startViewTransition(() => {
        flushSync(update);
      });
      this.activeTransition = transition;
      void transition.finished.finally(() => {
        if (this.activeTransition === transition) {
          this.activeTransition = null;
        }
      });
      return;
    }
    update();
  }

  private setConnection(status: WorkspaceFeedStatus, error: Error | null): void {
    this.snapshot = { ...this.snapshot, status, error };
    for (const listener of this.listeners) listener();
  }
}

export class FixtureEventFeed implements WorkspaceFeedStore {
  private readonly messages: Promise<WorkspaceMessage[]>;
  private readonly onMessage?: WorkspaceMessageListener;
  private readonly listeners = new Set<() => void>();
  private readonly playbackDelay: number;
  private generation = 0;
  private nextMessageIndex = 0;
  private live = false;
  private snapshot: WorkspaceFeedSnapshot;

  constructor(
    workspaceID: string,
    messages: WorkspaceMessage[] | Promise<WorkspaceMessage[]>,
    onMessage?: WorkspaceMessageListener,
    playbackDelay = 0,
  ) {
    this.messages = Promise.resolve(messages);
    this.onMessage = onMessage;
    this.playbackDelay = playbackDelay;
    this.snapshot = {
      workspace: createWorkspace(workspaceID),
      status: "idle",
      error: null,
    };
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): WorkspaceFeedSnapshot => this.snapshot;

  start(): void {
    this.generation += 1;
    void this.play(this.generation);
  }

  stop(): void {
    this.generation += 1;
  }

  private async play(generation: number): Promise<void> {
    const messages = await this.messages;
    if (generation !== this.generation) return;
    while (this.nextMessageIndex < messages.length) {
      if (this.live) {
        await new Promise((resolve) =>
          window.setTimeout(resolve, this.playbackDelay),
        );
      }
      if (generation !== this.generation) return;
      const message = messages[this.nextMessageIndex];
      if (!message) return;
      this.apply(message, this.live);
      this.nextMessageIndex += 1;
      this.live = this.live || message.type === "ready";
    }
  }

  private apply(message: WorkspaceMessage, animate: boolean): void {
    const update = () => {
      this.snapshot = {
        workspace: reduceWorkspace(this.snapshot.workspace, message),
        status: message.type === "ready" ? "live" : this.snapshot.status,
        error: null,
      };
      if (this.snapshot.status === "idle") {
        this.snapshot = { ...this.snapshot, status: "connecting" };
      }
      for (const listener of this.listeners) listener();
      this.onMessage?.(message, this.snapshot);
    };
    if (
      animate &&
      typeof document !== "undefined" &&
      "startViewTransition" in document &&
      !window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      document.startViewTransition(() => flushSync(update));
      return;
    }
    update();
  }
}

async function readSSE(
  response: Response,
  receive: (frame: SSEFrame) => void,
): Promise<void> {
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Console event feed returned no reader.");
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value, { stream: !done }).replaceAll("\r\n", "\n");
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const raw = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const frame = parseSSEFrame(raw);
      if (frame) receive(frame);
      boundary = buffer.indexOf("\n\n");
    }
    if (done) return;
  }
}

function parseSSEFrame(raw: string): SSEFrame | null {
  if (raw === "" || raw.startsWith(":")) return null;
  const frame: SSEFrame = { id: "", event: "message", data: "" };
  const data: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith("id:")) frame.id = line.slice(3).trimStart();
    if (line.startsWith("event:")) frame.event = line.slice(6).trimStart();
    if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
  }
  frame.data = data.join("\n");
  return frame;
}

function shouldAnimate(
  workspace: Workspace,
  message: WorkspaceMessage,
): boolean {
  return (
    workspace.ready &&
    message.type !== "ready" &&
    typeof document !== "undefined" &&
    "startViewTransition" in document &&
    !window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

function isPermanentFeedError(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    [400, 401, 403, 501].includes(error.status)
  );
}

function abortableDelay(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = window.setTimeout(resolve, milliseconds);
    signal.addEventListener(
      "abort",
      () => {
        window.clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}
