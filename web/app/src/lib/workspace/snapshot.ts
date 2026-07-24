import type { CloudEvent } from "../api/types";

import type {
  WorkspaceFeedError,
  WorkspaceFeedStatus,
  WorkspaceSignal,
} from "./feed";
import {
  createWorkspace,
  reduceWorkspace,
  type Workspace,
  type WorkspaceMessage,
} from "./reducer";

const EVENT_LIMIT = 100;

export interface WorkspaceFeedSnapshot {
  readonly workspace: Workspace;
  readonly events: readonly CloudEvent[];
  readonly status: WorkspaceFeedStatus;
  readonly error: WorkspaceFeedError | null;
}

export function initialWorkspaceFeedSnapshot(
  workspaceId: string,
): WorkspaceFeedSnapshot {
  return {
    workspace: createWorkspace(workspaceId),
    events: [],
    status: "idle",
    error: null,
  };
}

export function reduceWorkspaceFeed(
  current: WorkspaceFeedSnapshot,
  signal: WorkspaceSignal,
): WorkspaceFeedSnapshot {
  switch (signal.type) {
    case "connecting":
      return { ...current, status: "connecting" };
    case "message":
      return applyMessage(current, signal.message);
  }
}

function applyMessage(
  current: WorkspaceFeedSnapshot,
  message: WorkspaceMessage,
): WorkspaceFeedSnapshot {
  // Positions already incorporated must be skipped, not just recent ids: a
  // reconnect without a cursor replays the whole history onto a retained
  // snapshot, and the id window only remembers the last EVENT_LIMIT events.
  if (
    message.type === "domain_event" &&
    (message.event.globalposition <=
      current.workspace.throughGlobalPosition ||
      current.events.some((event) => event.id === message.event.id))
  ) {
    return current;
  }
  const workspace = reduceWorkspace(current.workspace, message);
  const events =
    message.type === "domain_event"
      ? [message.event, ...current.events].slice(0, EVENT_LIMIT)
      : current.events;
  return {
    ...current,
    workspace,
    events,
    status: messageStatus(current.status, message, workspace),
    error: null,
  };
}

function messageStatus(
  current: WorkspaceFeedStatus,
  message: WorkspaceMessage,
  workspace: Workspace,
): WorkspaceFeedStatus {
  if (message.type === "ready") return "live";
  if (message.type === "offers_unavailable") return "degraded";
  if (message.type === "offers_replaced" && workspace.ready) return "live";
  return current;
}
