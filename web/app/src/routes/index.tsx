// / — redirect to the deployment canvas.

import { createRoute, redirect } from "@tanstack/react-router";

import { rootRoute } from "./root";

export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/canvas", search });
  },
});
