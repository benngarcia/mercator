// /sinks — there is no list endpoint; the operator enters a sink id to inspect
// its status (cursor) and act on it. SinkActionsBar / ReplayDialog deliver or
// replay events; the resulting SinkResult renders in SinkResultCard. A 501
// degrades to <ServiceDisabled> via ErrorState's feature handling.

import { useState } from "react";
import { createRoute } from "@tanstack/react-router";
import { ArrowRight, Radio } from "lucide-react";

import { rootRoute } from "./root";
import { EmptyState, ErrorState, PageHeader } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  SinkActionsBar,
  SinkResultCard,
  SinkStatusCard,
} from "@/components/sinks";
import { useSinkStatus } from "@/lib/api/queries";
import type { SinkResult } from "@/lib/api/types";

function SinksPage() {
  const [draft, setDraft] = useState("");
  const [sinkId, setSinkId] = useState<string | null>(null);
  const [result, setResult] = useState<SinkResult | null>(null);

  const status = useSinkStatus(sinkId ?? undefined);

  const open = (id: string) => {
    const next = id.trim();
    if (next === "") return;
    setResult(null);
    setSinkId(next);
  };

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Sinks"
        description="Inspect a sink's cursor and deliver or replay its events."
      />

      <form
        className="flex max-w-lg items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          open(draft);
        }}
      >
        <div className="flex-1">
          <Label htmlFor="sink-id">Sink id</Label>
          <Input
            id="sink-id"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="sink id…"
            autoComplete="off"
            spellCheck={false}
            className="font-mono text-xs"
          />
        </div>
        <Button type="submit" variant="outline" disabled={draft.trim() === ""}>
          Open
          <ArrowRight className="size-4" />
        </Button>
      </form>

      {!sinkId ? (
        <EmptyState
          icon={Radio}
          title="Enter a sink id"
          description="Sinks are addressed by id; type one above to view its status."
        />
      ) : status.isLoading ? (
        <Skeleton className="h-32 w-full max-w-lg" />
      ) : status.isError ? (
        <ErrorState
          error={status.error}
          feature="Sinks"
          onRetry={() => void status.refetch()}
        />
      ) : status.data ? (
        <div className="flex flex-col gap-4">
          <div className="max-w-lg">
            <SinkStatusCard status={status.data} />
          </div>
          <SinkActionsBar sinkId={sinkId} onResult={setResult} />
          {result ? (
            <div className="max-w-lg">
              <SinkResultCard result={result} />
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

export const sinksRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/sinks",
  component: SinksPage,
});
