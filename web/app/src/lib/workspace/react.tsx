import { createContext, useContext, useMemo, useSyncExternalStore } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";

import { useMountEffect } from "@/hooks/useMountEffect";
import { useSession } from "@/hooks/useSession";
import { queryKeys } from "@/lib/api/keys";
import type { BookingDecision, CloudEvent } from "@/lib/api/types";

import {
  ConsoleEventFeed,
  FixtureEventFeed,
  type WorkspaceFeedSnapshot,
  type WorkspaceFeedStore,
} from "./feed";
import type { WorkspaceMessage } from "./reducer";

const WorkspaceFeedContext = createContext<WorkspaceFeedStore | null>(null);

export function WorkspaceFeedProvider({ children }: { children: React.ReactNode }) {
  const { workspace, token } = useSession();
  const queryClient = useQueryClient();
  if (!workspace) {
    return (
      <WorkspaceFeedContext.Provider value={null}>
        {children}
      </WorkspaceFeedContext.Provider>
    );
  }
  return (
    <ActiveWorkspaceFeed
      key={`${workspace}:${token ?? "session"}`}
      workspaceID={workspace}
      queryClient={queryClient}
    >
      {children}
    </ActiveWorkspaceFeed>
  );
}

function ActiveWorkspaceFeed({
  children,
  queryClient,
  workspaceID,
}: {
  children: React.ReactNode;
  queryClient: QueryClient;
  workspaceID: string;
}) {
  const feed = useMemo(
    () => {
      const fixture = activeScenario();
      const listener = (message: WorkspaceMessage, snapshot: WorkspaceFeedSnapshot) => {
        bridgeQueryCache(queryClient, message, snapshot);
      };
      return process.env.NODE_ENV !== "production" &&
        fixture?.name === "full-schedule-forces-fresh-capacity"
        ? new FixtureEventFeed(
            workspaceID,
            import("./scenario").then(({ fullScheduleScenarioMessages }) =>
              fullScheduleScenarioMessages(workspaceID),
            ),
            listener,
            fixture.playbackDelay,
          )
        : new ConsoleEventFeed(workspaceID, listener);
    },
    [queryClient, workspaceID],
  );
  useMountEffect(() => {
    feed.start();
    return () => feed.stop();
  });
  return (
    <WorkspaceFeedContext.Provider value={feed}>
      {children}
    </WorkspaceFeedContext.Provider>
  );
}

function activeScenario(): { name: string; playbackDelay: number } | null {
  if (
    process.env.NODE_ENV === "production" ||
    typeof window === "undefined"
  ) {
    return null;
  }
  const search = new URLSearchParams(window.location.search);
  const name = search.get("scenario");
  return name
    ? { name, playbackDelay: search.get("play") === "1" ? 800 : 0 }
    : null;
}

export function useWorkspaceFeed(): WorkspaceFeedSnapshot | null {
  const feed = useContext(WorkspaceFeedContext);
  return useSyncExternalStore(
    feed?.subscribe ?? emptySubscribe,
    feed?.getSnapshot ?? nullSnapshot,
    feed?.getSnapshot ?? nullSnapshot,
  );
}

function bridgeQueryCache(
  queryClient: QueryClient,
  message: WorkspaceMessage,
  snapshot: WorkspaceFeedSnapshot,
): void {
  const workspaceID = snapshot.workspace.id;
  if (message.type === "offers_replaced") {
    queryClient.setQueryData(
      queryKeys.offers(workspaceID),
      message.catalog.offers,
    );
    return;
  }
  if (message.type === "ready") {
    void queryClient.invalidateQueries({ queryKey: queryKeys.runs(workspaceID) });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.connections(workspaceID),
    });
    return;
  }
  if (message.type !== "domain_event") return;
  const event = message.event;
  if (event.type.startsWith("compute.connection.")) {
    void queryClient.invalidateQueries({
      queryKey: queryKeys.connections(workspaceID),
    });
    return;
  }
  if (!event.type.startsWith("compute.run.")) return;
  const runID = runIDForEvent(event);
  if (!runID) return;
  queryClient.setQueryData<CloudEvent[]>(
    queryKeys.runEvents(workspaceID, runID),
    (current = []) =>
      current.some((existing) => existing.id === event.id)
        ? current
        : [...current, event],
  );
  if (event.type === "compute.run.booking_decided.v1") {
    const data = event.data as { decision?: BookingDecision };
    if (data.decision) {
      queryClient.setQueryData(
        queryKeys.runDecision(workspaceID, runID),
        data.decision,
      );
    }
  }
  if (snapshot.workspace.ready) {
    void queryClient.invalidateQueries({ queryKey: queryKeys.runs(workspaceID) });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.run(workspaceID, runID),
    });
  }
}

function runIDForEvent(event: CloudEvent): string | null {
  if (event.correlationid) return event.correlationid;
  return event.subject.startsWith("runs/")
    ? event.subject.slice("runs/".length)
    : null;
}

function emptySubscribe(): () => void {
  return () => {};
}

function nullSnapshot(): null {
  return null;
}
