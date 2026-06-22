import * as React from "react";

import { bytes, duration } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { CopyButton } from "@/components/common/CopyButton";
import { JsonViewer } from "@/components/common/JsonViewer";
import { RelativeTime } from "@/components/common/RelativeTime";
import { StatBlock } from "@/components/common/StatBlock";
import type {
  NetworkFact,
  OfferSnapshot,
  ReliabilityEvidence,
} from "@/lib/api/types";
import { PriceTag } from "./PriceTag";
import { ResourceSummary } from "./ResourceSummary";

export interface OfferDetailSheetProps {
  /** The offer to inspect; `null`/`undefined` keeps the sheet closed. */
  offer: OfferSnapshot | null | undefined;
  onOpenChange?: (open: boolean) => void;
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-[0.6875rem] font-semibold uppercase tracking-wider text-muted-foreground">
        {title}
      </h3>
      {children}
    </section>
  );
}

function YesNo({ value }: { value: boolean }) {
  return (
    <span className={value ? "text-phase-succeeded" : "text-muted-foreground"}>
      {value ? "yes" : "no"}
    </span>
  );
}

function pct(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  return `${(value * 100).toFixed(1)}%`;
}

function NetworkFactRow({ fact }: { fact: NetworkFact }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border bg-card/60 p-3">
      <div className="flex items-center justify-between">
        <span className="font-mono text-[0.8125rem] text-foreground">
          {fact.scope} · {fact.statistic}
        </span>
        <span className="font-mono tabular text-[0.8125rem] text-foreground">
          {fact.value_mbps.toFixed(0)} Mbps
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[0.6875rem] text-muted-foreground">
        <span>{fact.source}</span>
        <span>n={fact.sample_count}</span>
        <span>conf {fact.confidence.toFixed(2)}</span>
        <span>
          valid until <RelativeTime iso={fact.valid_until} className="text-[0.6875rem]" />
        </span>
      </div>
    </div>
  );
}

function Reliability({ reliability }: { reliability: ReliabilityEvidence }) {
  return (
    <div className="grid grid-cols-3 gap-3">
      <StatBlock
        label="Start failure"
        value={pct(reliability.start_failure_rate)}
        mono
      />
      <StatBlock
        label="Interruption"
        value={pct(reliability.interruption_rate)}
        mono
      />
      <StatBlock
        label="Confidence"
        value={
          typeof reliability.confidence === "number"
            ? reliability.confidence.toFixed(2)
            : "—"
        }
        mono
      />
    </div>
  );
}

/**
 * OfferDetailSheet is the full evidence dossier for a single offer snapshot:
 * identity, resources, pricing, capability profile, observed network facts,
 * provisioning/queue/image-cache evidence, reliability, and the raw snapshot
 * JSON. Driven as a controlled sheet off the `offer` prop.
 */
export function OfferDetailSheet({
  offer,
  onOpenChange,
}: OfferDetailSheetProps) {
  const caps = offer?.capabilities;
  const downloads = offer?.network?.download ?? [];

  return (
    <Sheet open={Boolean(offer)} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 overflow-hidden p-0 sm:max-w-xl"
      >
        {offer ? (
          <>
            <SheetHeader className="space-y-1 border-b border-border p-6 text-left">
              <SheetTitle className="flex items-center gap-2">
                <Badge
                  variant={offer.kind === "standing" ? "default" : "outline"}
                  className="font-mono text-[0.6875rem] uppercase tracking-wide"
                >
                  {offer.kind}
                </Badge>
                <span className="font-mono text-sm">{offer.adapter_type}</span>
              </SheetTitle>
              <SheetDescription className="flex items-center gap-1 font-mono text-[0.8125rem]">
                {offer.id}
                <CopyButton value={offer.id} label="Copy offer id" />
              </SheetDescription>
            </SheetHeader>

            <div className="flex-1 space-y-6 overflow-y-auto p-6">
              <Section title="Identity">
                <div className="grid grid-cols-2 gap-3">
                  <StatBlock
                    label="Connection"
                    value={offer.connection_id}
                    mono
                    trailing={<CopyButton value={offer.connection_id} />}
                  />
                  <StatBlock label="Adapter" value={offer.adapter_type} mono />
                  <StatBlock
                    label="Platform"
                    value={`${offer.platform.os}/${offer.platform.architecture}`}
                    mono
                  />
                  <StatBlock
                    label="Native ref"
                    value={offer.native_ref}
                    mono
                    trailing={
                      offer.native_ref ? (
                        <CopyButton value={offer.native_ref} />
                      ) : undefined
                    }
                  />
                  <StatBlock
                    label="Observed"
                    value={<RelativeTime iso={offer.observed_at} />}
                  />
                  <StatBlock
                    label="Expires"
                    value={<RelativeTime iso={offer.expires_at} />}
                  />
                </div>
              </Section>

              <Separator />

              <Section title="Resources">
                <ResourceSummary
                  resources={offer.resources}
                  orientation="stack"
                />
              </Section>

              <Separator />

              <Section title="Pricing">
                <div className="flex flex-col gap-3">
                  <PriceTag pricing={offer.pricing} />
                  {offer.pricing?.known ? (
                    <div className="grid grid-cols-2 gap-3">
                      <StatBlock
                        label="Currency"
                        value={offer.pricing.currency}
                        mono
                      />
                      <StatBlock
                        label="Min charge"
                        value={duration(offer.pricing.minimum_charge_seconds)}
                        mono
                      />
                      <StatBlock
                        label="Granularity"
                        value={duration(offer.pricing.granularity_seconds)}
                        mono
                      />
                    </div>
                  ) : null}
                </div>
              </Section>

              <Separator />

              {caps ? (
                <>
                  <Section title="Capability profile">
                    <div className="grid grid-cols-2 gap-3">
                      <StatBlock
                        label="Max containers"
                        value={caps.container.max_containers}
                        mono
                      />
                      <StatBlock
                        label="Digest refs"
                        value={<YesNo value={caps.container.supports_digest_refs} />}
                      />
                      <StatBlock
                        label="Max env bytes"
                        value={bytes(caps.container.max_environment_bytes)}
                        mono
                      />
                      <StatBlock
                        label="Idempotent launch"
                        value={caps.lifecycle.idempotent_launch}
                        mono
                      />
                      <StatBlock
                        label="List owned"
                        value={<YesNo value={caps.lifecycle.list_owned} />}
                      />
                      <StatBlock
                        label="Provider TTL"
                        value={<YesNo value={caps.lifecycle.provider_ttl} />}
                      />
                      <StatBlock
                        label="Cancel queued"
                        value={<YesNo value={caps.lifecycle.cancel_queued} />}
                      />
                      <StatBlock
                        label="GPU vendors"
                        value={
                          caps.resources.gpu_vendors?.length
                            ? caps.resources.gpu_vendors.join(", ")
                            : "—"
                        }
                        mono
                      />
                      <StatBlock
                        label="Inbound network"
                        value={caps.network.inbound}
                        mono
                      />
                      <StatBlock
                        label="Public IPv4"
                        value={<YesNo value={caps.network.public_ipv4} />}
                      />
                      <StatBlock
                        label="Protocols"
                        value={
                          caps.network.protocols?.length
                            ? caps.network.protocols.join(", ")
                            : "—"
                        }
                        mono
                      />
                      <StatBlock
                        label="Logs"
                        value={caps.observability.logs}
                        mono
                      />
                      <StatBlock
                        label="Metrics"
                        value={caps.observability.metrics}
                        mono
                      />
                      <StatBlock
                        label="Shell"
                        value={caps.observability.shell}
                        mono
                      />
                    </div>
                  </Section>
                  <Separator />
                </>
              ) : null}

              <Section title="Network facts">
                {downloads.length > 0 ? (
                  <div className="flex flex-col gap-2">
                    {downloads.map((fact, i) => (
                      <NetworkFactRow key={`${fact.scope}-${fact.statistic}-${i}`} fact={fact} />
                    ))}
                  </div>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No measured download facts.
                  </p>
                )}
              </Section>

              <Separator />

              <Section title="Provisioning & cache">
                <div className="grid grid-cols-2 gap-3">
                  <StatBlock
                    label="Queued work"
                    value={
                      offer.queue
                        ? duration(offer.queue.queued_work_seconds)
                        : "—"
                    }
                    mono
                  />
                  <StatBlock
                    label="Active slots"
                    value={offer.queue ? offer.queue.active_slots : "—"}
                    mono
                  />
                  <StatBlock
                    label="Provision est (exp)"
                    value={
                      typeof offer.provisioning?.expected === "number"
                        ? duration(offer.provisioning.expected)
                        : "—"
                    }
                    mono
                  />
                  <StatBlock
                    label="Manifest cached"
                    value={
                      offer.image_cache.known ? (
                        <YesNo value={offer.image_cache.manifest_cached} />
                      ) : (
                        "unknown"
                      )
                    }
                  />
                  <StatBlock
                    label="Missing bytes"
                    value={
                      offer.image_cache.known
                        ? bytes(offer.image_cache.missing_bytes)
                        : "unknown"
                    }
                    mono
                  />
                  <StatBlock
                    label="Capacity"
                    value={
                      <span
                        className={cn(
                          offer.capacity.available
                            ? "text-phase-succeeded"
                            : "text-muted-foreground",
                        )}
                      >
                        {offer.capacity.available ? "available" : "unavailable"} ·
                        conf {offer.capacity.confidence.toFixed(2)}
                      </span>
                    }
                  />
                </div>
              </Section>

              <Separator />

              <Section title="Reliability">
                {offer.reliability ? (
                  <Reliability reliability={offer.reliability} />
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No reliability evidence.
                  </p>
                )}
              </Section>

              <Separator />

              <Section title="Raw snapshot">
                <JsonViewer value={offer} collapsed />
              </Section>
            </div>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
