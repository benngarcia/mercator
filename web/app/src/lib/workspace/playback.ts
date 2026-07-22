import type { WorkspaceMessage } from "./reducer";

export type ScenarioPlaybackSpeed = 1 | 2 | 4;
export type ScenarioPlaybackStatus = "playing" | "paused" | "finished";

export interface ScenarioPlaybackSnapshot {
  readonly status: ScenarioPlaybackStatus;
  readonly cursor: number;
  readonly cueCount: number;
  readonly elapsedMillis: number;
  readonly durationMillis: number;
  readonly speed: ScenarioPlaybackSpeed;
}

export interface ScenarioFidelity {
  readonly offerSource: string;
  readonly provenCapabilities: readonly string[];
  readonly targetCapabilities: readonly string[];
}

export type ScenarioPlaybackCommand =
  | { readonly type: "play" }
  | { readonly type: "pause" }
  | { readonly type: "previous" }
  | { readonly type: "next" }
  | { readonly type: "restart" }
  | {
      readonly type: "set_speed";
      readonly speed: ScenarioPlaybackSpeed;
    };

export async function sendScenarioPlaybackCommand(
  workspaceId: string,
  token: string | null,
  command: ScenarioPlaybackCommand,
): Promise<void> {
  const headers = new Headers({
    Accept: "application/json",
    "Content-Type": "application/json",
  });
  if (token !== null) headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(
    `/v1/dev/scenario-sessions/${encodeURIComponent(workspaceId)}/commands`,
    {
      method: "POST",
      credentials: "same-origin",
      headers,
      body: JSON.stringify(command),
    },
  );
  if (!response.ok) {
    throw new Error(`Scenario command failed with HTTP ${response.status}.`);
  }
  const body: unknown = await response.json();
  if (
    typeof body !== "object" ||
    body === null ||
    !("accepted" in body) ||
    body.accepted !== true
  ) {
    throw new Error("The scenario command response was invalid.");
  }
}

export type ScenarioPlaybackEmission =
  | {
      readonly type: "reset";
      readonly messages: readonly WorkspaceMessage[];
      readonly playback: ScenarioPlaybackSnapshot;
      readonly fidelity: ScenarioFidelity;
    }
  | { readonly type: "message"; readonly message: WorkspaceMessage }
  | {
      readonly type: "playback";
      readonly playback: ScenarioPlaybackSnapshot;
    };
