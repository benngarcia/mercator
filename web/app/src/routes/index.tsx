// / — redirect to /canvas (the Workspace dashboard). Preserves the workspace_id
// search param so a deep link like /?workspace_id=ws_x lands on
// /canvas?workspace_id=ws_x.

import { createRoute, redirect } from "@tanstack/react-router";

import { rootRoute } from "./root";

export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/canvas", search });
  },
});
