import * as React from "react";
import { Rocket, Loader2, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import type {
  CreateRunRequest,
  EnvBinding,
  WorkloadRevision,
} from "@/lib/api/types";
import { useCreateRun } from "@/lib/api/queries";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { WorkloadSpecEditor } from "@/components/placements";
import { EnvEditor } from "./EnvEditor";

export interface CreateRunFormProps {
  mode: "image" | "spec";
  onCreated: (runId: string) => void;
  onPendingChange: (pending: boolean) => void;
  className?: string;
}

// A starter document for spec mode so the operator has a valid shape to edit
// rather than a blank buffer. Kept minimal but structurally complete.
const SPEC_TEMPLATE = JSON.stringify(
  {
    workload_id: "",
    spec: {
      containers: [
        {
          name: "main",
          image: "",
          platform: { os: "linux", architecture: "amd64" },
          args: [],
        },
      ],
      resources: {
        cpu: { min_millis: 1000 },
        memory: { min_bytes: 536870912 },
        ephemeral_disk: { min_bytes: 1073741824 },
      },
      network: { inbound: "none" },
      placement: { objective: "balanced" },
      execution: { max_runtime_seconds: 3600, max_pre_start_attempts: 3 },
    },
  },
  null,
  2,
);

/**
 * CreateRunForm submits a new run in one of two modes:
 *
 *  - "image": the shorthand — image ref, args, and literal env (EnvEditor).
 *    The server materializes a single-container workload.
 *  - "spec": full control via a JSON workload revision document
 *    (WorkloadSpecEditor), sent verbatim as the run's `workload`.
 *
 * The form owns drafts, validation, and the public create-run mutation. The
 * containing run-intake workflow owns navigation after receiving the run ID.
 */
export function CreateRunForm({
  mode,
  onCreated,
  onPendingChange,
  className,
}: CreateRunFormProps) {
  const createRun = useCreateRun();
  // --- image mode state ---
  const [image, setImage] = React.useState("");
  const [args, setArgs] = React.useState<string[]>([]);
  const [env, setEnv] = React.useState<Record<string, EnvBinding>>({});
  // --- spec mode state ---
  const [specText, setSpecText] = React.useState(SPEC_TEMPLATE);

  const submit = (request: CreateRunRequest) => {
    onPendingChange(true);
    createRun.mutate(request, {
      onSuccess: (response) => {
        toast.success("Run created", { description: response.run_id });
        onCreated(response.run_id);
      },
      onError: (error) => {
        toast.error("Could not create run", {
          description: error.message || error.code,
        });
      },
      onSettled: () => onPendingChange(false),
    });
  };

  const buildImageBody = (): CreateRunRequest => {
    const cleanedArgs = args.map((a) => a).filter((a) => a.trim() !== "");
    const body: CreateRunRequest = { image: image.trim() };
    if (cleanedArgs.length > 0) body.args = cleanedArgs;
    if (Object.keys(env).length > 0) body.env = env;
    return body;
  };

  const submitImage = (e: React.FormEvent) => {
    e.preventDefault();
    if (!image.trim()) {
      toast.error("Image is required");
      return;
    }
    submit(buildImageBody());
  };

  const submitSpec = (e: React.FormEvent) => {
    e.preventDefault();
    let workload: WorkloadRevision;
    try {
      workload = JSON.parse(specText) as WorkloadRevision;
    } catch {
      toast.error("Workload JSON is not valid", {
        description: "Fix the syntax error before submitting.",
      });
      return;
    }
    submit({ workload });
  };

  if (mode === "spec") {
    return (
      <form
        onSubmit={submitSpec}
        className={cn("flex flex-col gap-4", className)}
      >
        <WorkloadSpecEditor
          value={specText}
          onChange={setSpecText}
          error={createRun.error ?? undefined}
          disabled={createRun.isPending}
          label="Workload revision JSON"
        />
        <div className="flex justify-end">
          <Button type="submit" disabled={createRun.isPending}>
            {createRun.isPending ? (
              <Loader2 className="animate-spin" />
            ) : (
              <Rocket />
            )}
            Create run
          </Button>
        </div>
      </form>
    );
  }

  return (
    <form
      onSubmit={submitImage}
      className={cn("flex flex-col gap-5", className)}
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="run-image">Image</Label>
        <Input
          id="run-image"
          value={image}
          onChange={(e) => setImage(e.target.value)}
          placeholder="ghcr.io/org/app:tag"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          className="font-mono text-sm"
          disabled={createRun.isPending}
        />
        <p className="text-xs text-muted-foreground">
          A container image reference. Tags are resolved to a digest at launch.
        </p>
      </div>

      <ArgsEditor
        value={args}
        onChange={setArgs}
        disabled={createRun.isPending}
      />

      <div className="flex flex-col gap-1.5">
        <Label>Environment</Label>
        <EnvEditor value={env} onChange={setEnv} />
      </div>

      <div className="flex justify-end">
        <Button type="submit" disabled={createRun.isPending || !image.trim()}>
          {createRun.isPending ? (
            <Loader2 className="animate-spin" />
          ) : (
            <Rocket />
          )}
          Create run
        </Button>
      </div>
    </form>
  );
}

// ArgsEditor is a small ordered list of command argument rows for image mode.
function ArgsEditor({
  value,
  onChange,
  disabled,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  disabled?: boolean;
}) {
  const update = (index: number, next: string) => {
    onChange(value.map((a, i) => (i === index ? next : a)));
  };
  const remove = (index: number) => {
    onChange(value.filter((_, i) => i !== index));
  };
  const add = () => onChange([...value, ""]);

  return (
    <div className="flex flex-col gap-1.5">
      <Label>Arguments</Label>
      {value.length > 0 ? (
        <div className="flex flex-col gap-1.5">
          {value.map((arg, i) => (
            <div key={i} className="flex items-center gap-1.5">
              <span className="w-6 shrink-0 text-right font-mono text-xs text-muted-foreground">
                {i}
              </span>
              <Input
                value={arg}
                onChange={(e) => update(i, e.target.value)}
                placeholder="argument"
                spellCheck={false}
                className="h-8 flex-1 font-mono text-xs"
                disabled={disabled}
                aria-label={`Argument ${i}`}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="size-8 shrink-0 text-muted-foreground hover:text-phase-failed"
                onClick={() => remove(i)}
                disabled={disabled}
                aria-label="Remove argument"
              >
                <Trash2 />
              </Button>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          No arguments. The image entrypoint runs as-is.
        </p>
      )}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        onClick={add}
        disabled={disabled}
      >
        <Plus />
        Add argument
      </Button>
    </div>
  );
}
