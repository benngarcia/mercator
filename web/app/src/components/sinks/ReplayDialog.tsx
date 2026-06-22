import * as React from "react";
import { History, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ViolationDetails } from "@/components/common";
import { useReplaySink } from "@/lib/api/queries";
import { ApiError } from "@/lib/api/client";
import type { ReplaySinkRequest, SinkResult, Violation } from "@/lib/api/types";

export interface ReplayDialogProps {
  sinkId: string;
  /** Called with the SinkResult of a successful replay. */
  onResult?: (result: SinkResult) => void;
}

// parseOptionalInt returns undefined for blank input and a number otherwise.
// NaN is returned for non-numeric text so the caller can flag it.
function parseOptionalInt(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (trimmed === "") return undefined;
  return Number(trimmed);
}

/**
 * ReplayDialog drives a bounded sink replay: an exclusive lower bound
 * (from_exclusive), an optional limit, and an optional caller-supplied
 * replay_id for traceability. It calls useReplaySink, maps any Violation
 * details from the error envelope inline, toasts the summary, and hands the
 * SinkResult back to the parent for a <SinkResultCard>.
 */
export function ReplayDialog({ sinkId, onResult }: ReplayDialogProps) {
  const [open, setOpen] = React.useState(false);
  const [fromExclusive, setFromExclusive] = React.useState("");
  const [limit, setLimit] = React.useState("");
  const [replayId, setReplayId] = React.useState("");
  const [violations, setViolations] = React.useState<Violation[]>([]);
  const [fieldError, setFieldError] = React.useState<string | null>(null);

  const replay = useReplaySink();

  const reset = () => {
    setFromExclusive("");
    setLimit("");
    setReplayId("");
    setViolations([]);
    setFieldError(null);
  };

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (!next) reset();
  };

  const onSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setViolations([]);
    setFieldError(null);

    const from = parseOptionalInt(fromExclusive);
    const lim = parseOptionalInt(limit);
    if (
      (from !== undefined && (!Number.isInteger(from) || from < 0)) ||
      (lim !== undefined && (!Number.isInteger(lim) || lim <= 0))
    ) {
      setFieldError(
        "From position must be a non-negative integer and limit a positive integer.",
      );
      return;
    }

    const body: ReplaySinkRequest = {};
    if (from !== undefined) body.from_exclusive = from;
    if (lim !== undefined) body.limit = lim;
    const trimmedReplayId = replayId.trim();
    if (trimmedReplayId !== "") body.replay_id = trimmedReplayId;

    replay.mutate(
      { sinkID: sinkId, body },
      {
        onSuccess: (result) => {
          onResult?.(result);
          if (result.failed_event_id) {
            toast.error(`Replay stopped at event ${result.failed_event_id}`, {
              description: `${result.delivered} replayed before the failure.`,
            });
          } else {
            toast.success(
              `Replayed ${result.delivered} event${result.delivered === 1 ? "" : "s"}`,
              { description: `Reached position ${result.last_position}.` },
            );
          }
          onOpenChange(false);
        },
        onError: (error) => {
          if (error instanceof ApiError) {
            if (error.serviceDisabled) {
              toast.error("Sinks are not enabled on this deployment.");
              onOpenChange(false);
              return;
            }
            if (error.details && error.details.length > 0) {
              setViolations(error.details);
              return;
            }
          }
          toast.error("Replay failed", { description: error.message });
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button variant="outline">
          <History />
          Replay
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={onSubmit}>
          <DialogHeader>
            <DialogTitle>Replay sink</DialogTitle>
            <DialogDescription>
              Re-deliver a bounded window of events. Leave a field blank to use
              the server default.
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4 py-4">
            <div className="grid gap-2">
              <Label htmlFor="replay-from-exclusive">
                From position (exclusive)
              </Label>
              <Input
                id="replay-from-exclusive"
                inputMode="numeric"
                placeholder="from current cursor"
                className="font-mono tabular"
                value={fromExclusive}
                onChange={(e) => setFromExclusive(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Global position to resume after. Events at or before this are
                skipped.
              </p>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="replay-limit">Limit</Label>
              <Input
                id="replay-limit"
                inputMode="numeric"
                placeholder="no limit"
                className="font-mono tabular"
                value={limit}
                onChange={(e) => setLimit(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Maximum number of events to replay in this pass.
              </p>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="replay-id">Replay ID</Label>
              <Input
                id="replay-id"
                placeholder="auto-generated"
                className="font-mono"
                value={replayId}
                onChange={(e) => setReplayId(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Optional identifier echoed back on the result for traceability.
              </p>
            </div>

            {fieldError ? (
              <p className="text-xs text-destructive">{fieldError}</p>
            ) : null}
            {violations.length > 0 ? (
              <ViolationDetails violations={violations} />
            ) : null}
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={replay.isPending}>
              {replay.isPending ? <Loader2 className="animate-spin" /> : null}
              Replay
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
