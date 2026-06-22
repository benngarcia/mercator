import * as React from "react";
import { toast } from "sonner";

import type { Violation } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import { useCreateWorkload } from "@/lib/api/queries";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

export interface CreateWorkloadDialogProps {
  /** Controlled open state; omit for an uncontrolled dialog driven by trigger. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Optional trigger element (rendered via DialogTrigger asChild). */
  trigger?: React.ReactNode;
  /** Called with the created workload id on success. */
  onCreated?: (workloadId: string) => void;
  /** Workspace override; defaults to the session workspace (implicit). */
  workspaceId?: string;
}

// fieldFromPath maps a Violation path to one of the two form fields so server
// validation errors land on the right input.
function fieldFromPath(path: string): "workload_id" | "name" | null {
  if (!path) return null;
  const lower = path.toLowerCase();
  if (lower.includes("workload_id")) return "workload_id";
  if (lower.includes("name")) return "name";
  return null;
}

/**
 * CreateWorkloadDialog creates a workload (workspace_id is implicit from the
 * session). It collects the workload id and a display name, calls
 * useCreateWorkload, and on failure toasts the envelope message and maps any
 * Violation details onto the relevant fields.
 */
export function CreateWorkloadDialog({
  open: controlledOpen,
  onOpenChange,
  trigger,
  onCreated,
  workspaceId,
}: CreateWorkloadDialogProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = React.useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = React.useCallback(
    (next: boolean) => {
      onOpenChange?.(next);
      if (controlledOpen === undefined) setUncontrolledOpen(next);
    },
    [controlledOpen, onOpenChange],
  );

  const [workloadId, setWorkloadId] = React.useState("");
  const [name, setName] = React.useState("");
  const [fieldErrors, setFieldErrors] = React.useState<
    Partial<Record<"workload_id" | "name", string>>
  >({});
  const [otherViolations, setOtherViolations] = React.useState<Violation[]>([]);

  const createWorkload = useCreateWorkload(workspaceId);

  const reset = React.useCallback(() => {
    setWorkloadId("");
    setName("");
    setFieldErrors({});
    setOtherViolations([]);
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
      setFieldErrors({});
      setOtherViolations([]);
      createWorkload.mutate(
        { workspace_id: workspaceId ?? "", workload_id: workloadId.trim(), name: name.trim() },
        {
          onSuccess: (res) => {
            toast.success("Workload created", { description: res.workload_id });
            onCreated?.(res.workload_id);
            handleOpenChange(false);
          },
          onError: (error) => {
            const message =
              error instanceof ApiError ? error.message : "Failed to create workload";
            toast.error("Could not create workload", { description: message });
            if (error instanceof ApiError && error.details) {
              const fields: Partial<Record<"workload_id" | "name", string>> = {};
              const rest: Violation[] = [];
              for (const v of error.details) {
                const field = fieldFromPath(v.path);
                if (field) {
                  fields[field] = v.message || v.code;
                } else {
                  rest.push(v);
                }
              }
              setFieldErrors(fields);
              setOtherViolations(rest);
            }
          },
        },
      );
    },
    [createWorkload, handleOpenChange, name, onCreated, workloadId, workspaceId],
  );

  const submitting = createWorkload.isPending;
  const canSubmit = workloadId.trim().length > 0 && name.trim().length > 0 && !submitting;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {trigger ? <DialogTrigger asChild>{trigger}</DialogTrigger> : null}
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create workload</DialogTitle>
          <DialogDescription>
            Register a new workload in the current workspace. Revisions pin specs
            against it.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="cw-workload-id">Workload id</Label>
            <Input
              id="cw-workload-id"
              value={workloadId}
              onChange={(e) => setWorkloadId(e.target.value)}
              placeholder="wl_my-service"
              autoComplete="off"
              spellCheck={false}
              className={cn(
                "font-mono",
                fieldErrors.workload_id && "border-destructive",
              )}
              aria-invalid={Boolean(fieldErrors.workload_id)}
            />
            {fieldErrors.workload_id ? (
              <p className="text-xs text-destructive">{fieldErrors.workload_id}</p>
            ) : null}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="cw-name">Name</Label>
            <Input
              id="cw-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My Service"
              autoComplete="off"
              className={cn(fieldErrors.name && "border-destructive")}
              aria-invalid={Boolean(fieldErrors.name)}
            />
            {fieldErrors.name ? (
              <p className="text-xs text-destructive">{fieldErrors.name}</p>
            ) : null}
          </div>
          {otherViolations.length > 0 ? (
            <ul className="flex flex-col gap-1 rounded-md border border-destructive/40 bg-destructive/5 p-2">
              {otherViolations.map((v, i) => (
                <li key={`${v.code}-${i}`} className="text-xs text-destructive">
                  <span className="font-mono">{v.code}</span>
                  {v.message ? ` — ${v.message}` : null}
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
              {submitting ? "Creating…" : "Create workload"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
