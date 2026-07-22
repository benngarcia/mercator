// Application entry: mounts one Effect Atom registry for session, remote
// resources, mutations, clocks, and the live Workspace projection.
// The dark-first theme class is applied before first paint via
// applyInitialTheme() so there is no light flash.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RegistryProvider } from "@effect/atom-react";
import { RouterProvider } from "@tanstack/react-router";

import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { applyInitialTheme } from "@/components/layout";
import { router } from "@/routes/router";

import "@fontsource-variable/figtree";
import "@fontsource-variable/jetbrains-mono";
import "./index.css";

// Apply the persisted (or dark-first default) theme before React mounts.
applyInitialTheme();

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("Root element #root not found");
}

createRoot(rootElement).render(
  <StrictMode>
    <RegistryProvider defaultIdleTTL={400}>
      <TooltipProvider delayDuration={200}>
        <RouterProvider router={router} />
        <Toaster richColors closeButton position="bottom-right" />
      </TooltipProvider>
    </RegistryProvider>
  </StrictMode>,
);
