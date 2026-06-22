// Route tree assembly + the TanStack Router instance. Code-based routes (one
// module per page) are wired under the root route here, and the resulting
// router type is registered so <Link>/useNavigate are type-safe across the app
// (the layout/Sidebar and forms navigate against these paths).

import { createRouter } from "@tanstack/react-router";

import { rootRoute } from "./root";
import { indexRoute } from "./index";
import { runsRoute } from "./runs";
import { runsNewRoute } from "./runs.new";
import { runsDetailRoute } from "./runs.detail";
import { previewRoute } from "./preview";
import { workloadsRoute } from "./workloads";
import { workloadsDetailRoute } from "./workloads.detail";
import { offersRoute } from "./offers";
import { connectionsRoute } from "./connections";
import { sinksRoute } from "./sinks";

const routeTree = rootRoute.addChildren([
  indexRoute,
  runsRoute,
  runsNewRoute,
  runsDetailRoute,
  previewRoute,
  workloadsRoute,
  workloadsDetailRoute,
  offersRoute,
  connectionsRoute,
  sinksRoute,
]);

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
