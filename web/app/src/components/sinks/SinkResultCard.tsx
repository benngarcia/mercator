import { AlertTriangle, CheckCircle2 } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { CopyButton, StatBlock } from "@/components/common";
import { cn } from "@/lib/utils";
import type { SinkResult } from "@/lib/api/types";

export interface SinkResultCardProps {
  result: SinkResult;
  className?: string;
}

/**
 * SinkResultCard renders the outcome of a deliver/replay: how many events were
 * delivered, the last global position reached, and — when delivery halted on a
 * bad event — the failing event id. A replay also carries its replay_id.
 */
export function SinkResultCard({ result, className }: SinkResultCardProps) {
  const failed = Boolean(result.failed_event_id);
  return (
    <Card
      className={cn(
        failed ? "border-phase-failed/40" : "border-phase-succeeded/40",
        className,
      )}
    >
      <CardHeader className="flex flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="text-sm font-medium">Last result</CardTitle>
        {failed ? (
          <Badge className="gap-1 bg-phase-failed/15 text-phase-failed border-phase-failed/30">
            <AlertTriangle className="size-3.5" />
            stopped on error
          </Badge>
        ) : (
          <Badge className="gap-1 bg-phase-succeeded/15 text-phase-succeeded border-phase-succeeded/30">
            <CheckCircle2 className="size-3.5" />
            delivered
          </Badge>
        )}
      </CardHeader>
      <CardContent className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <StatBlock
          label="Delivered"
          value={result.delivered.toLocaleString()}
          mono
        />
        <StatBlock
          label="Last position"
          value={result.last_position.toLocaleString()}
          mono
          trailing={
            <CopyButton
              value={String(result.last_position)}
              label="Copy position"
            />
          }
        />
        {result.failed_event_id ? (
          <StatBlock
            label="Failed event"
            value={result.failed_event_id}
            mono
            trailing={
              <CopyButton
                value={result.failed_event_id}
                label="Copy event id"
              />
            }
          />
        ) : null}
        {result.replay_id ? (
          <StatBlock
            label="Replay ID"
            value={result.replay_id}
            mono
            trailing={
              <CopyButton value={result.replay_id} label="Copy replay id" />
            }
          />
        ) : null}
      </CardContent>
    </Card>
  );
}
