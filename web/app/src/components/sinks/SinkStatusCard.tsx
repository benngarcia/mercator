import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CopyButton, StatBlock } from "@/components/common";
import type { SinkStatus } from "@/lib/api/types";

export interface SinkStatusCardProps {
  status: SinkStatus;
  className?: string;
}

/**
 * SinkStatusCard surfaces the delivery cursor for a sink: its id, the current
 * global position cursor (mono, tabular), and whether a cursor has been
 * established yet. A sink with no cursor has never delivered.
 */
export function SinkStatusCard({ status, className }: SinkStatusCardProps) {
  return (
    <Card className={className}>
      <CardHeader className="flex flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="text-sm font-medium">Sink status</CardTitle>
        {status.has_cursor ? (
          <Badge className="bg-phase-running/15 text-phase-running border-phase-running/30">
            tracking
          </Badge>
        ) : (
          <Badge variant="outline" className="text-muted-foreground">
            no cursor
          </Badge>
        )}
      </CardHeader>
      <CardContent className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <StatBlock
          label="Sink ID"
          value={status.sink_id}
          mono
          trailing={<CopyButton value={status.sink_id} label="Copy sink id" />}
        />
        <StatBlock
          label="Cursor"
          value={
            status.has_cursor ? status.cursor.toLocaleString() : "not started"
          }
          mono={status.has_cursor}
          trailing={
            status.has_cursor ? (
              <CopyButton
                value={String(status.cursor)}
                label="Copy cursor"
              />
            ) : undefined
          }
        />
      </CardContent>
    </Card>
  );
}
