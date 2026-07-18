// Query-key factory. Keys are workspace-scoped where the underlying resource is
// workspace-scoped, so switching workspaces yields an isolated cache. Every key
// is a readonly tuple to keep TanStack Query's structural equality stable.

export const queryKeys = {
  all: ["mercator"] as const,

  health: () => ["mercator", "health"] as const,

  authSession: () => ["mercator", "authSession"] as const,

  runs: (workspaceID: string) =>
    ["mercator", "runs", { workspaceID }] as const,
  run: (workspaceID: string, runID: string) =>
    ["mercator", "run", { workspaceID, runID }] as const,
  runEvents: (workspaceID: string, runID: string) =>
    ["mercator", "run", { workspaceID, runID }, "events"] as const,
  runDecision: (workspaceID: string, runID: string) =>
    ["mercator", "run", { workspaceID, runID }, "decision"] as const,

  offers: (workspaceID: string) =>
    ["mercator", "offers", { workspaceID }] as const,
  adapters: () => ["mercator", "adapters"] as const,
  connections: (workspaceID: string) =>
    ["mercator", "connections", { workspaceID }] as const,

  workloadRevisions: (workspaceID: string, workloadID: string) =>
    ["mercator", "workload", { workspaceID, workloadID }, "revisions"] as const,
  revision: (workspaceID: string, workloadID: string, revisionID: string) =>
    [
      "mercator",
      "workload",
      { workspaceID, workloadID },
      "revision",
      revisionID,
    ] as const,

  sinkStatus: (sinkID: string) => ["mercator", "sink", sinkID] as const,
} as const;
