import type { CloudEvent } from "../api/types";

import type {
  WorkspaceFeedError,
  WorkspaceFeedStatus,
  WorkspaceSignal,
} from "./feed";
import type {
  ScenarioFidelity,
  ScenarioPlaybackSnapshot,
} from "./playback";
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
  readonly playback: ScenarioPlaybackSnapshot | null;
  readonly fidelity: ScenarioFidelity | null;
  readonly status: WorkspaceFeedStatus;
  readonly error: WorkspaceFeedError | null;
}

export function initialWorkspaceFeedSnapshot(
  workspaceId: string,
): WorkspaceFeedSnapshot {
  return {
    workspace: createWorkspace(workspaceId),
    events: [],
    playback: null,
    fidelity: null,
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
    case "playback":
      return { ...current, playback: signal.playback };
    case "reset":
      return resetSnapshot(
        current,
        signal.messages,
        signal.playback,
        signal.fidelity,
      );
    case "message":
      return applyMessage(current, signal.message);
  }
}

function resetSnapshot(
  current: WorkspaceFeedSnapshot,
  messages: readonly WorkspaceMessage[],
  playback: ScenarioPlaybackSnapshot,
  fidelity: ScenarioFidelity,
): WorkspaceFeedSnapshot {
  const initial = {
    ...initialWorkspaceFeedSnapshot(current.workspace.id),
    status: current.status,
    playback,
    fidelity,
  };
  return messages.reduce(applyMessage, initial);
}

function applyMessage(
  current: WorkspaceFeedSnapshot,
  message: WorkspaceMessage,
): WorkspaceFeedSnapshot {
  if (
    message.type === "domain_event" &&
    current.events.some((event) => event.id === message.event.id)
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
