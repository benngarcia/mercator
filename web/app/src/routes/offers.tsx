// /offers — the offer snapshots available to the workspace (polled every 10s).
// Selecting a row opens OfferDetailSheet with the full capability profile,
// network facts, pricing and reliability.

import { useState } from "react";
import { createRoute } from "@tanstack/react-router";
import { Tags } from "lucide-react";

import { rootRoute } from "./root";
import { EmptyState, ErrorState, PageHeader } from "@/components/common";
import { OfferDetailSheet, OffersTable } from "@/components/offers";
import { useOffers } from "@/lib/api/queries";
import type { OfferSnapshot } from "@/lib/api/types";

function OffersPage() {
  const { data, isLoading, isError, error, refetch } = useOffers();
  const [selected, setSelected] = useState<OfferSnapshot | null>(null);

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Offers"
        description="Standing and provisionable compute offers visible to this workspace."
      />
      {isError ? (
        <ErrorState error={error} onRetry={() => void refetch()} />
      ) : (
        <OffersTable
          offers={data ?? []}
          isLoading={isLoading}
          selectedId={selected?.id}
          onSelect={setSelected}
          emptyState={
            <EmptyState
              icon={Tags}
              title="No offers"
              description="No compute offers are currently visible to this workspace."
            />
          }
        />
      )}
      <OfferDetailSheet
        offer={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
      />
    </div>
  );
}

export const offersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/offers",
  component: OffersPage,
});
