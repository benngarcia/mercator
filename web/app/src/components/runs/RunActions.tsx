import * as React from "react";
import { Ban, RefreshCw, Loader2 } from "lucide-react";
import { toast } from "sonner";

import type { Run } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import { useCancelRun, useRefreshRun } from "@/lib/api/queries";
import { useIsTerminal } from "@/hooks/useIsTerminal";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export interface RunActionsProps {
  run: Run;
  className?: string;
}

// errorMessage extracts a human-facing string from an ApiError (or any error).
function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.message || error.code;
  }
  if (error instanceof Error) return error.message;
  return "Request failed.";
}

/**
 * RunActions exposes the two terminal-safe run mutations: Refresh (re-poll the
 * provider's view of the run) and Cancel (behind a confirm dialog). Both are
 * disabled once the run is closed, since a terminal run cannot change. Errors
 * surface as toasts using the { code, message } envelope.
 */
export function RunActions({ run, className }: RunActionsProps) {
  const terminal = useIsTerminal(run);
  const cancel = useCancelRun();
  const refresh = useRefreshRun();
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  const onRefresh = () => {
    refresh.mutate(run.id, {
      onSuccess: () => toast.success("Run refreshed"),
      onError: (error) => toast.error("Refresh failed", { description: errorMessage(error) }),
    });
  };

  const onConfirmCancel = () => {
    cancel.mutate(run.id, {
      onSuccess: () => {
        toast.success("Run cancellation requested");
        setConfirmOpen(false);
      },
      onError: (error) =>
        toast.error("Cancel failed", { description: errorMessage(error) }),
    });
  };

  return (
    <div className={cn("flex items-center gap-2", className)}>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={onRefresh}
        disabled={refresh.isPending}
      >
        {refresh.isPending ? (
          <Loader2 className="animate-spin" />
        ) : (
          <RefreshCw />
        )}
        Refresh
      </Button>

      <Button
        type="button"
        variant="outline"
        size="sm"
        className={cn(
          !terminal &&
            "border-phase-failed/40 text-phase-failed hover:bg-phase-failed/10 hover:text-phase-failed",
        )}
        onClick={() => setConfirmOpen(true)}
        disabled={terminal || cancel.isPending}
        title={terminal ? "Run is already closed" : undefined}
      >
        <Ban />
        Cancel
      </Button>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Cancel this run?</DialogTitle>
            <DialogDescription>
              This requests cancellation of run{" "}
              <span className="font-mono text-foreground">{run.id}</span>. Any
              launched workload will be torn down according to its cleanup
              policy. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setConfirmOpen(false)}
              disabled={cancel.isPending}
            >
              Keep running
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={onConfirmCancel}
              disabled={cancel.isPending}
            >
              {cancel.isPending ? <Loader2 className="animate-spin" /> : <Ban />}
              Cancel run
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
