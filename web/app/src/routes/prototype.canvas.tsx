// PROTOTYPE: three Workspace dashboard variants, switchable through ?variant=.
// Question: which visual hierarchy makes Rental schedules easiest to understand?

import { createRoute, useNavigate } from "@tanstack/react-router";

import {
  PrototypeHeader,
  PrototypeSwitcher,
  VariantA,
  VariantB,
  VariantC,
  workspaceFor,
  type PrototypeStep,
  type PrototypeVariant,
} from "@/prototypes/canvas";

import { rootRoute } from "./root";

interface PrototypeCanvasSearch {
  variant?: PrototypeVariant;
  step?: PrototypeStep;
}

function validateSearch(search: Record<string, unknown>): PrototypeCanvasSearch {
  const variant =
    search.variant === "A" || search.variant === "B" || search.variant === "C"
      ? search.variant
      : undefined;
  const step =
    search.step === "requested" ||
    search.step === "provisioning" ||
    search.step === "running"
      ? search.step
      : undefined;
  return { variant, step };
}

function PrototypeCanvasPage() {
  const navigate = useNavigate({ from: "/prototype/canvas" });
  const search = prototypeCanvasRoute.useSearch();
  const variant = search.variant ?? "A";
  const workspace = workspaceFor(search.step ?? "requested");

  const selectVariant = (next: PrototypeVariant) => {
    void navigate({
      search: (previous) => ({ ...previous, variant: next }),
      replace: true,
    });
  };

  const selectStep = (next: PrototypeStep) => {
    void navigate({
      search: (previous) => ({ ...previous, step: next }),
      replace: true,
    });
  };

  return (
    <div className="min-h-full bg-background">
      <PrototypeHeader workspace={workspace} onStep={selectStep} />
      {variant === "A" && <VariantA workspace={workspace} />}
      {variant === "B" && <VariantB workspace={workspace} />}
      {variant === "C" && <VariantC workspace={workspace} />}
      <PrototypeSwitcher current={variant} onSelect={selectVariant} />
    </div>
  );
}

export const prototypeCanvasRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/prototype/canvas",
  validateSearch,
  component: PrototypeCanvasPage,
});

