// /connections — the guided provider onboarding surface. A card per
// registered adapter (from GET /v1/adapters) opens either the setup modal
// (manifest steps + form) or the management sheet for existing connections.
// A raw key/value dialog remains for adapters without manifests, and the
// dense operator table below keeps the full-record view.

import * as React from "react";
import { createRoute } from "@tanstack/react-router";
import { SquareDashed } from "lucide-react";

import { rootRoute } from "./root";
import { ErrorState, PageHeader } from "@/components/common";
import {
  AddConnectionDialog,
  ConnectionDetailSheet,
  ConnectionsTable,
  ProviderCard,
  ProviderSetupDialog,
} from "@/components/connections";
import { Skeleton } from "@/components/ui/skeleton";
import type { AdapterManifest } from "@/lib/api/types";
import { useAdapters, useConnections } from "@/lib/api/queries";

function CustomConnectionCard({ onSelect }: { onSelect: () => void }) {
  return (
    <button
      type="button"
      onClick={onSelect}
      data-testid="provider-card-custom"
      className="group flex min-h-36 flex-col items-start justify-between gap-3 rounded-2xl border border-dashed p-5 text-left text-muted-foreground transition-colors hover:border-ring/40 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="flex items-center gap-3">
        <div className="flex size-10 items-center justify-center rounded-lg border border-dashed border-border">
          <SquareDashed className="size-5" />
        </div>
        <div className="font-semibold leading-tight tracking-tight">
          Custom connection
        </div>
      </div>
      <p className="text-sm">
        Raw adapter type and key/value config, for adapters without a guided
        setup.
      </p>
    </button>
  );
}

function CardGridSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: 4 }, (_, i) => (
        <Skeleton key={i} className="h-36 rounded-2xl" />
      ))}
    </div>
  );
}

function ConnectionsPage() {
  const adapters = useAdapters();
  const connections = useConnections();

  const [setupFor, setSetupFor] = React.useState<AdapterManifest | null>(null);
  const [manageFor, setManageFor] = React.useState<AdapterManifest | null>(null);
  const [customOpen, setCustomOpen] = React.useState(false);
  // Connection ids whose most recent verify attempt failed this session. The
  // API only stores the authorized bit, so "verify failed" is session state.
  const [verifyFailedIds, setVerifyFailedIds] = React.useState<
    ReadonlySet<string>
  >(new Set());

  const markVerifyFailed = React.useCallback((id: string) => {
    setVerifyFailedIds((prev) => new Set(prev).add(id));
  }, []);
  const markVerifyResolved = React.useCallback((id: string) => {
    setVerifyFailedIds((prev) => {
      if (!prev.has(id)) return prev;
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  }, []);

  const connectionList = connections.data ?? [];
  const connectionsByType = new Map<string, typeof connectionList>();
  for (const record of connectionList) {
    const bucket = connectionsByType.get(record.adapter_type);
    if (bucket) {
      bucket.push(record);
    } else {
      connectionsByType.set(record.adapter_type, [record]);
    }
  }

  const selectProvider = (manifest: AdapterManifest) => {
    if ((connectionsByType.get(manifest.type) ?? []).length > 0) {
      setManageFor(manifest);
    } else {
      setSetupFor(manifest);
    }
  };

  return (
    <div className="flex flex-col gap-6 p-4">
      <PageHeader
        title="Connections"
        description="Connect compute providers to this workspace. Each card walks through creating an account, getting an API token, and verifying it."
      />

      {adapters.isError ? (
        <ErrorState
          error={adapters.error}
          onRetry={() => void adapters.refetch()}
        />
      ) : adapters.isLoading ? (
        <CardGridSkeleton />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {(adapters.data ?? []).map((manifest) => (
            <ProviderCard
              key={manifest.type}
              manifest={manifest}
              connections={connectionsByType.get(manifest.type) ?? []}
              verifyFailedIds={verifyFailedIds}
              onSelect={() => selectProvider(manifest)}
            />
          ))}
          <CustomConnectionCard onSelect={() => setCustomOpen(true)} />
        </div>
      )}

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-medium text-muted-foreground">
          All connections
        </h2>
        {connections.isError ? (
          <ErrorState
            error={connections.error}
            onRetry={() => void connections.refetch()}
          />
        ) : (
          <ConnectionsTable
            connections={connectionList}
            isLoading={connections.isLoading}
          />
        )}
      </section>

      <ProviderSetupDialog
        manifest={setupFor}
        open={setupFor !== null}
        onOpenChange={(open) => {
          if (!open) setSetupFor(null);
        }}
        onVerifyFailed={markVerifyFailed}
        onVerifyResolved={markVerifyResolved}
      />
      <ConnectionDetailSheet
        manifest={manageFor}
        connections={
          manageFor ? (connectionsByType.get(manageFor.type) ?? []) : []
        }
        verifyFailedIds={verifyFailedIds}
        open={manageFor !== null}
        onOpenChange={(open) => {
          if (!open) setManageFor(null);
        }}
        onAddAnother={() => {
          setSetupFor(manageFor);
          setManageFor(null);
        }}
        onVerifyFailed={markVerifyFailed}
        onVerifyResolved={markVerifyResolved}
      />
      <AddConnectionDialog open={customOpen} onOpenChange={setCustomOpen} />
    </div>
  );
}

export const connectionsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/connections",
  component: ConnectionsPage,
});
