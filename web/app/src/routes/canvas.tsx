import { createRoute } from "@tanstack/react-router";

import { WorkspaceCanvas } from "@/components/canvas";
import { Skeleton } from "@/components/ui/skeleton";
import { useSession } from "@/hooks/useSession";
import { useWorkspaceFeed } from "@/lib/workspace";

import { rootRoute } from "./root";

interface CanvasSearch {
  scenario?: string;
  play?: "1";
}

function validateSearch(search: Record<string, unknown>): CanvasSearch {
  return {
    scenario: typeof search.scenario === "string" ? search.scenario : undefined,
    play: search.play === "1" || search.play === 1 ? "1" : undefined,
  };
}

function CanvasPage() {
  const { workspace } = useSession();
  const feed = useWorkspaceFeed();
  if (!workspace) {
    return (
      <div className="flex min-h-full items-center justify-center p-8 text-sm text-muted-foreground">
        Select a Workspace
      </div>
    );
  }
  if (!feed || !feed.workspace.ready) {
    return <CanvasSkeleton />;
  }
  return (
    <WorkspaceCanvas
      workspace={feed.workspace}
      events={feed.events}
      playback={feed.playback}
      controls={feed.controls}
    />
  );
}

function CanvasSkeleton() {
  return (
    <div className="flex flex-col">
      <div className="flex h-16 items-center justify-between border-b px-5">
        <Skeleton className="h-5 w-24" />
        <Skeleton className="h-4 w-40" />
      </div>
      {Array.from({ length: 4 }, (_, index) => (
        <div key={index} className="flex h-28 border-b">
          <div className="w-56 border-r p-5">
            <Skeleton className="h-4 w-28" />
            <Skeleton className="mt-3 h-3 w-36" />
          </div>
          <div className="flex flex-1 items-center px-4">
            <Skeleton className="h-12 w-56" />
          </div>
        </div>
      ))}
    </div>
  );
}

export const canvasRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/canvas",
  validateSearch,
  component: CanvasPage,
});
