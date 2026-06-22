import * as React from "react";

import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { DataTable, type Column } from "@/components/common/DataTable";
import { RelativeTime } from "@/components/common/RelativeTime";
import type { OfferKind, OfferSnapshot } from "@/lib/api/types";
import { PriceTag } from "./PriceTag";
import { ResourceSummary } from "./ResourceSummary";

export interface OffersTableProps {
  offers: OfferSnapshot[];
  onSelect?: (offer: OfferSnapshot) => void;
  selectedId?: string;
  isLoading?: boolean;
  emptyState?: React.ReactNode;
}

const KIND_LABELS: Record<OfferKind, string> = {
  standing: "Standing",
  provisionable: "Provisionable",
};

// OfferKindBadge tints standing offers with the teal accent (ready capacity)
// and provisionable ones in outline (must be spun up).
function OfferKindBadge({ kind }: { kind: OfferKind }) {
  return (
    <Badge
      variant={kind === "standing" ? "default" : "outline"}
      className={cn(
        "font-mono text-[0.6875rem] uppercase tracking-wide",
        kind === "provisionable" && "text-muted-foreground",
      )}
    >
      {KIND_LABELS[kind] ?? kind}
    </Badge>
  );
}

function platformLabel(offer: OfferSnapshot): string {
  const { os, architecture } = offer.platform;
  if (!os && !architecture) return "—";
  return [os, architecture].filter(Boolean).join("/");
}

/**
 * OffersTable is the dense roster of collected offer snapshots: platform,
 * standing/provisionable kind, a resource summary, price, available capacity,
 * and time-to-expiry. Rows are clickable to open the OfferDetailSheet.
 */
export function OffersTable({
  offers,
  onSelect,
  selectedId,
  isLoading,
  emptyState,
}: OffersTableProps) {
  const columns = React.useMemo<Column<OfferSnapshot>[]>(
    () => [
      {
        id: "id",
        header: "Offer",
        sortable: true,
        sortValue: (o) => o.id,
        cell: (o) => (
          <div className="flex flex-col gap-0.5">
            <span className="font-mono text-[0.8125rem] text-foreground">
              {o.id}
            </span>
            <span className="font-mono text-[0.6875rem] text-muted-foreground">
              {o.adapter_type}
            </span>
          </div>
        ),
      },
      {
        id: "platform",
        header: "Platform",
        sortable: true,
        sortValue: platformLabel,
        cell: (o) => (
          <span className="font-mono text-[0.8125rem] text-foreground">
            {platformLabel(o)}
          </span>
        ),
      },
      {
        id: "kind",
        header: "Kind",
        sortable: true,
        sortValue: (o) => o.kind,
        cell: (o) => <OfferKindBadge kind={o.kind} />,
      },
      {
        id: "resources",
        header: "Resources",
        cell: (o) => <ResourceSummary resources={o.resources} />,
      },
      {
        id: "pricing",
        header: "Price",
        align: "right",
        sortable: true,
        sortValue: (o) =>
          o.pricing?.known ? o.pricing.rate_per_second_usd : Infinity,
        headerClassName: "text-right",
        cell: (o) => (
          <div className="flex justify-end">
            <PriceTag pricing={o.pricing} />
          </div>
        ),
      },
      {
        id: "capacity",
        header: "Capacity",
        sortable: true,
        sortValue: (o) => (o.capacity?.available ? 1 : 0),
        cell: (o) => {
          const available = o.capacity?.available;
          const confidence = o.capacity?.confidence;
          return (
            <div className="flex flex-col gap-0.5">
              <span
                className={cn(
                  "text-[0.8125rem]",
                  available
                    ? "text-phase-succeeded"
                    : "text-muted-foreground",
                )}
              >
                {available ? "Available" : "Unavailable"}
              </span>
              {typeof confidence === "number" ? (
                <span className="font-mono tabular text-[0.6875rem] text-muted-foreground">
                  conf {confidence.toFixed(2)}
                </span>
              ) : null}
            </div>
          );
        },
      },
      {
        id: "expires_at",
        header: "Expires",
        align: "right",
        sortable: true,
        sortValue: (o) => o.expires_at,
        headerClassName: "text-right",
        cell: (o) => <RelativeTime iso={o.expires_at} className="text-[0.8125rem]" />,
      },
    ],
    [],
  );

  return (
    <DataTable
      columns={columns}
      data={offers}
      rowKey={(o) => o.id}
      onRowClick={onSelect}
      selectedKey={selectedId}
      isLoading={isLoading}
      emptyState={emptyState}
    />
  );
}
