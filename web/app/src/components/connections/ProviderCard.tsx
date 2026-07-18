import type { AdapterManifest, ConnectionRecord } from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { ProviderLogo } from "./ProviderLogo";

// The card's quiet one-line status. Derived, in priority order, from the
// workspace's connections of this adapter type: any authorized connection
// means the provider works (verified); otherwise a connection whose last
// verify attempt failed this session outranks plain configured.
export type ProviderStatus = "none" | "configured" | "verified" | "verify_failed";

export function deriveProviderStatus(
  connections: ConnectionRecord[],
  verifyFailedIds: ReadonlySet<string>,
): ProviderStatus {
  if (connections.length === 0) return "none";
  if (connections.some((c) => c.authorized)) return "verified";
  if (connections.some((c) => verifyFailedIds.has(c.id))) return "verify_failed";
  return "configured";
}

const STATUS_TREATMENT: Record<
  ProviderStatus,
  { label: string; dot: string; text: string }
> = {
  none: {
    label: "Not connected",
    dot: "bg-border",
    text: "text-muted-foreground",
  },
  configured: {
    label: "Configured — not verified",
    dot: "bg-phase-cancelled",
    text: "text-muted-foreground",
  },
  verified: {
    label: "Verified",
    dot: "bg-phase-succeeded",
    text: "text-phase-succeeded",
  },
  verify_failed: {
    label: "Verify failed",
    dot: "bg-phase-failed",
    text: "text-phase-failed",
  },
};

export interface ProviderCardProps {
  manifest: AdapterManifest;
  connections: ConnectionRecord[];
  verifyFailedIds: ReadonlySet<string>;
  onSelect: () => void;
}

/**
 * ProviderCard is one tile in the Connections grid: logomark, provider name,
 * one-line description, and the derived connection status as a quiet dot +
 * label footer. The whole card is one button; selecting it opens setup (no
 * connection yet) or management (connection exists).
 */
export function ProviderCard({
  manifest,
  connections,
  verifyFailedIds,
  onSelect,
}: ProviderCardProps) {
  const status = deriveProviderStatus(connections, verifyFailedIds);
  const treatment = STATUS_TREATMENT[status];

  return (
    <button
      type="button"
      onClick={onSelect}
      data-testid={`provider-card-${manifest.type}`}
      data-status={status}
      className={cn(
        "group flex flex-col gap-3 rounded-2xl border bg-card p-5 text-left shadow-sm transition-colors",
        "hover:border-ring/40 hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
      )}
    >
      <div className="flex items-center gap-3">
        <ProviderLogo slug={manifest.logo} name={manifest.display_name} />
        <div className="min-w-0">
          <div className="font-semibold leading-tight tracking-tight text-foreground">
            {manifest.display_name}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            {manifest.type}
          </div>
        </div>
      </div>
      <p className="line-clamp-2 min-h-10 text-sm text-muted-foreground">
        {manifest.description}
      </p>
      <div className="flex items-center gap-2 text-xs">
        <span className={cn("size-1.5 rounded-full", treatment.dot)} />
        <span className={treatment.text}>
          {treatment.label}
          {connections.length > 1 ? ` · ${connections.length} connections` : ""}
        </span>
      </div>
    </button>
  );
}
