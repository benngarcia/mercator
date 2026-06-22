import { Loader2, Send } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { ServiceDisabled } from "@/components/common";
import { useDeliverSink } from "@/lib/api/queries";
import { ApiError } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import type { SinkResult } from "@/lib/api/types";
import { ReplayDialog } from "./ReplayDialog";

export interface SinkActionsBarProps {
  sinkId: string;
  /**
   * Called with the SinkResult of a deliver or replay so the page can render a
   * <SinkResultCard>. Optional: the bar also toasts a short summary.
   */
  onResult?: (result: SinkResult) => void;
  className?: string;
}

/**
 * SinkActionsBar exposes the operator actions for a sink: Deliver (drain from
 * the current cursor) and Replay (a bounded window via <ReplayDialog>). Deliver
 * is a mutation on useDeliverSink; both surface a SinkResult upward and toast a
 * one-line summary. A 501 degrades the whole bar to <ServiceDisabled>.
 */
export function SinkActionsBar({
  sinkId,
  onResult,
  className,
}: SinkActionsBarProps) {
  const deliver = useDeliverSink();

  const onDeliver = () => {
    deliver.mutate(sinkId, {
      onSuccess: (result) => {
        onResult?.(result);
        if (result.failed_event_id) {
          toast.error(
            `Delivery stopped at event ${result.failed_event_id}`,
            { description: `${result.delivered} delivered before the failure.` },
          );
        } else {
          toast.success(
            `Delivered ${result.delivered} event${result.delivered === 1 ? "" : "s"}`,
            { description: `Cursor now at position ${result.last_position}.` },
          );
        }
      },
      onError: (error) => {
        if (error instanceof ApiError && error.serviceDisabled) return;
        toast.error("Deliver failed", { description: error.message });
      },
    });
  };

  if (deliver.error instanceof ApiError && deliver.error.serviceDisabled) {
    return <ServiceDisabled feature="Sinks" className={className} />;
  }

  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      <Button onClick={onDeliver} disabled={deliver.isPending}>
        {deliver.isPending ? (
          <Loader2 className="animate-spin" />
        ) : (
          <Send />
        )}
        Deliver
      </Button>
      <ReplayDialog sinkId={sinkId} onResult={onResult} />
    </div>
  );
}
