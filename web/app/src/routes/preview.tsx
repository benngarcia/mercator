// /preview — dry-run placement. The operator edits a workload revision JSON in
// WorkloadSpecEditor and runs usePreviewPlacement (no run is created); the
// returned decision renders in PreviewResult (DecisionPanel + CandidateTable).
// Mutation errors with Violation details flow back into the editor inline.

import { useState } from "react";
import { createRoute } from "@tanstack/react-router";
import { Compass, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { rootRoute } from "./root";
import { EmptyState, PageHeader } from "@/components/common";
import { Button } from "@/components/ui/button";
import { WorkloadSpecEditor, PreviewResult } from "@/components/placements";
import { usePreviewPlacement } from "@/lib/api/queries";
import type { WorkloadRevision } from "@/lib/api/types";
import { useSession } from "@/hooks/useSession";

const STARTER = `{
  "spec": {
    "containers": [
      {
        "name": "main",
        "image": "busybox@sha256:1cfa4e2b09e127b9c4ed43578d3f3c18e7d44ea47b9ea98475c0cbe9086525f8",
        "platform": { "os": "linux", "architecture": "amd64" },
        "args": ["echo", "hello from mercator"]
      }
    ],
    "resources": {
      "cpu": { "min_millis": 500 },
      "memory": { "min_bytes": 134217728 },
      "ephemeral_disk": { "min_bytes": 1073741824 }
    },
    "network": { "inbound": "none" },
    "placement": { "objective": "balanced" },
    "execution": { "max_runtime_seconds": 300, "max_pre_start_attempts": 3 }
  }
}`;

function PreviewPage() {
  const { workspace } = useSession();
  const [value, setValue] = useState(STARTER);
  const preview = usePreviewPlacement();

  const handlePreview = () => {
    let workload: WorkloadRevision;
    try {
      workload = JSON.parse(value) as WorkloadRevision;
    } catch (err) {
      toast.error("Invalid JSON", {
        description: err instanceof Error ? err.message : String(err),
      });
      return;
    }
    preview.mutate(
      { workload, workspace_id: workspace ?? undefined },
      {
        onError: (err) =>
          toast.error("Preview failed", {
            description: err.message || err.code,
          }),
      },
    );
  };

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Preview placement"
        description="Evaluate where a workload would be placed without creating a run."
        actions={
          <Button size="sm" onClick={handlePreview} disabled={preview.isPending}>
            {preview.isPending ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Compass className="size-4" />
            )}
            Preview
          </Button>
        }
      />

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <WorkloadSpecEditor
          value={value}
          onChange={setValue}
          error={preview.error}
          disabled={preview.isPending}
          label="Workload revision JSON"
        />
        <div className="min-w-0">
          {preview.data ? (
            <PreviewResult decision={preview.data} />
          ) : (
            <EmptyState
              icon={Compass}
              title="No preview yet"
              description="Edit the workload and run Preview to see candidate offers and the selected placement."
            />
          )}
        </div>
      </div>
    </div>
  );
}

export const previewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/preview",
  component: PreviewPage,
});
