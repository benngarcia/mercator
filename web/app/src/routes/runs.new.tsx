// /runs/new — create a run. CreateRunForm owns the image/spec modes, the
// Idempotency-Key, and (on success) navigation to the new run's detail page;
// this route just frames it and offers the mode toggle.

import { useState } from "react";
import { createRoute } from "@tanstack/react-router";

import { rootRoute } from "./root";
import { PageHeader } from "@/components/common";
import { CreateRunForm } from "@/components/runs";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

type Mode = "image" | "spec";

function CreateRunPage() {
  const [mode, setMode] = useState<Mode>("image");

  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        title="Create run"
        description="Place a workload on an offer. Choose a quick image shorthand or a full workload spec."
        actions={
          <Tabs value={mode} onValueChange={(v) => setMode(v as Mode)}>
            <TabsList>
              <TabsTrigger value="image">Image</TabsTrigger>
              <TabsTrigger value="spec">Spec</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />
      <div className="max-w-3xl">
        <CreateRunForm mode={mode} />
      </div>
    </div>
  );
}

export const runsNewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/new",
  component: CreateRunPage,
});
