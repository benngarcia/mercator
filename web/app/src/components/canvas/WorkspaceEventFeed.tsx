import { Radio } from "lucide-react";

import { EventTimeline } from "@/components/runs/EventTimeline";
import type { CloudEvent } from "@/lib/api/types";
import type { ScenarioFidelity } from "@/lib/workspace/playback";

export function WorkspaceEventFeed({
  events,
  fidelity,
}: {
  events: readonly CloudEvent[];
  fidelity: ScenarioFidelity | null;
}) {
  const latest = events[0];
  return (
    <aside
      role="region"
      aria-label="Workspace events"
      className="flex min-h-0 w-[22rem] shrink-0 flex-col border-l bg-card/30"
    >
      <div className="flex h-14 shrink-0 items-center justify-between border-b px-4">
        <div className="flex items-center gap-2">
          <Radio className="size-3.5 text-primary" />
          <h2 className="text-sm font-semibold">Events</h2>
        </div>
        <span className="font-mono text-[10px] tabular text-muted-foreground">
          {events.length} shown
        </span>
      </div>
      {fidelity ? <ScenarioEvidence fidelity={fidelity} /> : null}
      <span className="sr-only" aria-live="polite">
        {latest ? `Latest event ${latest.type}` : "No Workspace events"}
      </span>
      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        <EventTimeline events={events} dense highlightLatest />
      </div>
    </aside>
  );
}

function ScenarioEvidence({ fidelity }: { fidelity: ScenarioFidelity }) {
  const recordedOffers = fidelity.offerSource === "sanitized_recordings";
  return (
    <div className="shrink-0 border-b px-4 py-3 text-[11px] leading-4 text-muted-foreground">
      <div className="font-medium text-foreground">Scenario evidence</div>
      <div>
        {recordedOffers ? "Sanitized recorded offers" : fidelity.offerSource}
        {fidelity.provenCapabilities.length > 0
          ? ` · ${fidelity.provenCapabilities.length} production paths`
          : ""}
      </div>
      {fidelity.targetCapabilities.length > 0 ? (
        <div>Target contract: {fidelity.targetCapabilities.join(", ")}</div>
      ) : null}
    </div>
  );
}
