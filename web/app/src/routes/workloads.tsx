// /workloads — there is no list endpoint; the operator enters (or creates) a
// workload id to inspect its revisions. A free-form id field navigates to the
// detail route; CreateWorkloadDialog mints a new workload and jumps to it.

import { useState } from "react";
import { createRoute, useNavigate } from "@tanstack/react-router";
import { ArrowRight, Boxes, Plus } from "lucide-react";

import { rootRoute } from "./root";
import { EmptyState, PageHeader } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { CreateWorkloadDialog } from "@/components/workloads";

function WorkloadsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState("");

  const open = (workloadId: string) => {
    const id = workloadId.trim();
    if (id === "") return;
    void navigate({ to: "/workloads/$workloadId", params: { workloadId: id } });
  };

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Workloads"
        description="Inspect a workload's revisions, or create a new workload."
        actions={
          <CreateWorkloadDialog
            trigger={
              <Button size="sm">
                <Plus className="size-4" />
                New workload
              </Button>
            }
            onCreated={open}
          />
        }
      />

      <form
        className="flex max-w-lg items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          open(draft);
        }}
      >
        <div className="flex-1">
          <Label htmlFor="workload-id">Workload id</Label>
          <Input
            id="workload-id"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="workload id…"
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

      <EmptyState
        icon={Boxes}
        title="Enter a workload id"
        description="Workloads are addressed by id; type one above to view its revisions."
      />
    </div>
  );
}

export const workloadsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workloads",
  component: WorkloadsPage,
});
