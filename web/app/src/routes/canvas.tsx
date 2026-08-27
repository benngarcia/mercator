import { createRoute } from "@tanstack/react-router";

import { DeploymentCanvas } from "@/components/canvas";
import { Skeleton } from "@/components/ui/skeleton";
import { useDeploymentFeed } from "@/lib/deployment";

import { rootRoute } from "./root";

function CanvasPage() {
  const feed = useDeploymentFeed();
  if (!feed.deployment.ready) {
    return <CanvasSkeleton />;
  }
  return (
    <DeploymentCanvas
      deployment={feed.deployment}
      events={feed.events}
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
  component: CanvasPage,
});
