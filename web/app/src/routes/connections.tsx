// /connections — adapter connections for the workspace (polled every 10s).
// ConnectionsTable shows id / adapter_type / authorized.

import { createRoute } from "@tanstack/react-router";

import { rootRoute } from "./root";
import { ErrorState, PageHeader } from "@/components/common";
import { ConnectionsTable } from "@/components/connections";
import { useConnections } from "@/lib/api/queries";

function ConnectionsPage() {
  const { data, isLoading, isError, error, refetch } = useConnections();

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Connections"
        description="Adapter connections authorized for this workspace."
      />
      {isError ? (
        <ErrorState error={error} onRetry={() => void refetch()} />
      ) : (
        <ConnectionsTable connections={data ?? []} isLoading={isLoading} />
      )}
    </div>
  );
}

export const connectionsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/connections",
  component: ConnectionsPage,
});
