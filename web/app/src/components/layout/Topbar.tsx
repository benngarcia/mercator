// Topbar: workspace switcher, identity controls, live event status,
// and the theme toggle. WorkspaceSwitcher remains controlled; a selection
// writes the shareable URL, and the root route projects that value into the
// session consumed by data hooks.

import { Loader2 } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useSession } from "@/hooks/useSession";
import { useWorkspaceFeed } from "@/lib/workspace";

import { IdentityControls } from "./IdentityControls";
import { ThemeToggle } from "./ThemeToggle";
import { WorkspaceSwitcher } from "./WorkspaceSwitcher";

function HealthDot() {
  const feed = useWorkspaceFeed();

  if (feed?.status === "connecting") {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="flex size-7 items-center justify-center">
            <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
          </span>
        </TooltipTrigger>
        <TooltipContent>Connecting to Workspace events</TooltipContent>
      </Tooltip>
    );
  }

  let tone: "ok" | "degraded" | "down" | "idle";
  let label: string;
  if (feed?.status === "live") {
    tone = "ok";
    label = "Workspace events live";
  } else if (feed?.status === "degraded") {
    tone = "degraded";
    label = "Workspace events live; Offers unavailable";
  } else if (feed?.status === "error") {
    tone = "down";
    label = feed.error?.message ?? "Workspace event feed unavailable";
  } else {
    tone = "idle";
    label = "Select a Workspace";
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
  const navigate = useNavigate();
  const { workspace } = useSession();
  const selectWorkspace = (workspaceID: string) => {
    void navigate({
      to: ".",
      search: (previous) => ({ ...previous, workspace_id: workspaceID }),
    });
  };

  return (
    <header className="flex h-14 items-center gap-3 border-b bg-card/40 px-4 backdrop-blur">
      <WorkspaceSwitcher value={workspace} onChange={selectWorkspace} />
      <div className="ml-auto flex items-center gap-1">
        <HealthDot />
        <IdentityControls />
        <ThemeToggle />
      </div>
    </header>
  );
}
