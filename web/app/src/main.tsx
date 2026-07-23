// Application entry: mounts the React 19 root with the console's providers in
// the order they depend on each other —
//   QueryClientProvider (server state) → RouterProvider (routes + loaders that
//   call queryClient.ensureQueryData) → <Toaster/> for mutation feedback.
// The dark-first theme class is applied before first paint via
// applyInitialTheme() so there is no light flash.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";

import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { applyInitialTheme } from "@/components/layout";
import { ApiError } from "@/lib/api/client";
import { router } from "@/routes/router";
import { WorkspaceFeedProvider } from "@/lib/workspace";

import "@fontsource-variable/figtree";
import "@fontsource-variable/jetbrains-mono";
import "./index.css";

// Apply the persisted (or dark-first default) theme before React mounts.
applyInitialTheme();

// A single QueryClient for the app. The Workspace event feed refreshes live
// resources; here we set sensible global defaults and a retry policy that never
// retries non-transient API errors (auth / not-found / disabled-service).
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => {
        if (error instanceof ApiError) {
          if ([401, 403, 404, 501].includes(error.status)) {
            return false;
          }
        }
        return failureCount < 1;
      },
    },
  },
});

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("Root element #root not found");
}

createRoot(rootElement).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={200}>
        <WorkspaceFeedProvider>
          <RouterProvider router={router} />
        </WorkspaceFeedProvider>
        <Toaster richColors closeButton position="bottom-right" />
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
);
