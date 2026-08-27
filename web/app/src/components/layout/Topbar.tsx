// Topbar: identity controls, live event status, and the theme toggle.

import { Loader2 } from "lucide-react";

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useDeploymentFeed } from "@/lib/deployment";

import { IdentityControls } from "./IdentityControls";
import { ThemeToggle } from "./ThemeToggle";

function HealthDot() {
  const feed = useDeploymentFeed();

  if (feed.status === "connecting") {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="flex size-7 items-center justify-center">
            <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
          </span>
        </TooltipTrigger>
        <TooltipContent>Connecting to deployment events</TooltipContent>
      </Tooltip>
    );
  }

  let tone: "ok" | "degraded" | "down" | "idle";
  let label: string;
  if (feed.status === "live") {
    tone = "ok";
    label = "Deployment events live";
  } else if (feed.status === "degraded") {
    tone = "degraded";
    label = "Deployment events live; Offers unavailable";
  } else if (feed.status === "error") {
    tone = "down";
    label = feed.error?.message ?? "Deployment event feed unavailable";
  } else {
    tone = "idle";
    label = "Deployment event feed idle";
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className="flex size-7 items-center justify-center"
          aria-label={label}
          role="status"
        >
          <span
            className={cn(
              "size-2 rounded-full",
              tone === "ok" && "bg-phase-succeeded",
              tone === "degraded" && "bg-phase-launching",
              tone === "down" && "bg-phase-failed",
              tone === "idle" && "bg-muted-foreground",
            )}
          />
        </span>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export function Topbar() {
  return (
    <header className="flex h-14 items-center gap-3 border-b bg-card/40 px-4 backdrop-blur">
      <div className="ml-auto flex items-center gap-1">
        <HealthDot />
        <IdentityControls />
        <ThemeToggle />
      </div>
    </header>
  );
}
