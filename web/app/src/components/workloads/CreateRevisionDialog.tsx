import * as React from "react";
import { toast } from "sonner";

import type { Violation, WorkloadRevision, WorkloadSpec } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import { useCreateRevision } from "@/lib/api/queries";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

export interface CreateRevisionDialogProps {
  /** The workload the new revision is created under. */
  workloadId: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  trigger?: React.ReactNode;
  /** Called with the created revision on success. */
  onCreated?: (revision: WorkloadRevision) => void;
  /** Workspace override; defaults to the session workspace. */
  workspaceId?: string;
}

// looksLikeRevision narrows a parsed object to a full WorkloadRevision (vs a
// bare WorkloadSpec) by the presence of revision-envelope fields.
function looksLikeRevision(value: Record<string, unknown>): boolean {
  return "spec" in value || "workload_id" in value || "digest" in value;
}

// buildRevision normalizes pasted JSON into a WorkloadRevision: either the
// caller pasted a full revision envelope (we backfill ids) or a bare spec
// (we wrap it). Throws on shapes that are neither.
function buildRevision(
  parsed: unknown,
  workloadId: string,
  workspaceId: string,
): WorkloadRevision {
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Expected a JSON object");
  }
  const obj = parsed as Record<string, unknown>;
  if (looksLikeRevision(obj)) {
    const rev = obj as Partial<WorkloadRevision>;
    if (!rev.spec || typeof rev.spec !== "object") {
      throw new Error('Revision is missing a "spec" object');
    }
    return {
      id: rev.id ?? "",
      workspace_id: rev.workspace_id ?? workspaceId,
      workload_id: rev.workload_id ?? workloadId,
      digest: rev.digest ?? "",
      spec: rev.spec as WorkloadSpec,
    };
  }
  // Treat the object as a bare spec.
  return {
    id: "",
    workspace_id: workspaceId,
    workload_id: workloadId,
    digest: "",
    spec: obj as unknown as WorkloadSpec,
  };
}

/**
 * CreateRevisionDialog lets an operator paste a revision spec (either a full
 * WorkloadRevision envelope or a bare WorkloadSpec) as JSON and submit it via
 * useCreateRevision. Parse errors are shown inline; server validation
 * Violations are toasted and listed.
 */
export function CreateRevisionDialog({
  workloadId,
  open: controlledOpen,
  onOpenChange,
  trigger,
  onCreated,
  workspaceId,
}: CreateRevisionDialogProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = React.useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = React.useCallback(
    (next: boolean) => {
      onOpenChange?.(next);
      if (controlledOpen === undefined) setUncontrolledOpen(next);
    },
    [controlledOpen, onOpenChange],
  );

  const [text, setText] = React.useState("");
  const [parseError, setParseError] = React.useState<string | null>(null);
  const [violations, setViolations] = React.useState<Violation[]>([]);

  const createRevision = useCreateRevision(workspaceId);

  const reset = React.useCallback(() => {
    setText("");
    setParseError(null);
    setViolations([]);
  }, []);

  const handleOpenChange = React.useCallback(
    (next: boolean) => {
      if (!next) reset();
      setOpen(next);
    },
    [reset, setOpen],
  );

  const onSubmit = React.useCallback(
    (event: React.FormEvent) => {
      event.preventDefault();
      setParseError(null);
      setViolations([]);

      let revision: WorkloadRevision;
      try {
        const parsed: unknown = JSON.parse(text);
        revision = buildRevision(parsed, workloadId, workspaceId ?? "");
      } catch (error) {
        setParseError(
          error instanceof Error ? error.message : "Invalid JSON",
        );
        return;
      }

      createRevision.mutate(
        { workloadID: workloadId, body: { revision } },
        {
          onSuccess: (created) => {
            toast.success("Revision created", { description: created.id });
            onCreated?.(created);
            handleOpenChange(false);
          },
          onError: (error) => {
            const message =
              error instanceof ApiError ? error.message : "Failed to create revision";
            toast.error("Could not create revision", { description: message });
            if (error instanceof ApiError && error.details) {
              setViolations(error.details);
            }
          },
        },
      );
    },
    [createRevision, handleOpenChange, onCreated, text, workloadId, workspaceId],
  );

  const submitting = createRevision.isPending;
  const canSubmit = text.trim().length > 0 && !submitting;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {trigger ? <DialogTrigger asChild>{trigger}</DialogTrigger> : null}
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Create revision</DialogTitle>
          <DialogDescription>
            Paste a revision spec for{" "}
            <span className="font-mono text-foreground">{workloadId}</span>. Accepts
            a full revision envelope or a bare workload spec.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="cr-spec">Revision spec (JSON)</Label>
            <Textarea
              id="cr-spec"
              value={text}
              onChange={(e) => setText(e.target.value)}
              spellCheck={false}
              rows={16}
              placeholder={'{\n  "spec": {\n    "containers": [ ... ]\n  }\n}'}
              className={cn(
                "min-h-64 resize-y font-mono text-xs leading-relaxed",
                parseError && "border-destructive",
              )}
              aria-invalid={Boolean(parseError)}
            />
            {parseError ? (
              <p className="text-xs text-destructive">{parseError}</p>
            ) : null}
          </div>
          {violations.length > 0 ? (
            <ul className="flex flex-col gap-1.5 rounded-md border border-destructive/40 bg-destructive/5 p-2">
              {violations.map((v, i) => (
                <li key={`${v.code}-${v.path}-${i}`} className="text-xs">
                  <span className="font-mono font-medium text-destructive">
                    {v.code}
                  </span>
                  {v.path ? (
                    <span className="ml-2 font-mono text-muted-foreground">
                      {v.path}
                    </span>
                  ) : null}
                  {v.message ? (
                    <span className="ml-2 text-foreground">{v.message}</span>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => handleOpenChange(false)}
              disabled={submitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {submitting ? "Creating…" : "Create revision"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
