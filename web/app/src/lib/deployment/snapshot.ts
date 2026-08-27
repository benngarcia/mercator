import type { CloudEvent } from "../api/types";

import type {
  DeploymentFeedError,
  DeploymentFeedStatus,
  DeploymentSignal,
} from "./feed";
import type { ScenarioFidelity, ScenarioPlaybackSnapshot } from "./playback";
import {
  createDeployment,
  reduceDeployment,
  type Deployment,
  type DeploymentMessage,
} from "./reducer";

const EVENT_LIMIT = 100;

export interface DeploymentFeedSnapshot {
  readonly deployment: Deployment;
  readonly events: readonly CloudEvent[];
  readonly playback: ScenarioPlaybackSnapshot | null;
  readonly fidelity: ScenarioFidelity | null;
  readonly status: DeploymentFeedStatus;
  readonly error: DeploymentFeedError | null;
}

export function initialDeploymentFeedSnapshot(): DeploymentFeedSnapshot {
  return {
    deployment: createDeployment(),
    events: [],
    playback: null,
    fidelity: null,
    status: "idle",
    error: null,
  };
}

export function reduceDeploymentFeed(
  current: DeploymentFeedSnapshot,
  signal: DeploymentSignal,
): DeploymentFeedSnapshot {
  switch (signal.type) {
    case "connecting":
      return { ...initialDeploymentFeedSnapshot(), status: "connecting" };
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
  current: DeploymentFeedSnapshot,
  messages: readonly DeploymentMessage[],
  playback: ScenarioPlaybackSnapshot,
  fidelity: ScenarioFidelity,
): DeploymentFeedSnapshot {
  const initial = {
    ...initialDeploymentFeedSnapshot(),
    status: current.status,
    playback,
    fidelity,
  };
  return messages.reduce(applyMessage, initial);
}

function applyMessage(
  current: DeploymentFeedSnapshot,
  message: DeploymentMessage,
): DeploymentFeedSnapshot {
  if (
    message.type === "domain_event" &&
    (message.event.globalposition <= current.deployment.throughGlobalPosition ||
      current.events.some((event) => event.id === message.event.id))
  ) {
    return current;
  }
  const deployment = reduceDeployment(current.deployment, message);
  const events =
    message.type === "domain_event"
      ? [message.event, ...current.events].slice(0, EVENT_LIMIT)
      : current.events;
  return {
    ...current,
    deployment,
    events,
    status: messageStatus(current.status, message, deployment),
    error: null,
  };
}

function messageStatus(
  current: DeploymentFeedStatus,
  message: DeploymentMessage,
  deployment: Deployment,
): DeploymentFeedStatus {
  if (message.type === "ready") return "live";
  if (message.type === "offers_unavailable") return "degraded";
  if (message.type === "offers_replaced" && deployment.ready) return "live";
  return current;
}
