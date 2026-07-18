// Topbar: workspace switcher, token field, a service-health dot (useHealth),
// and the theme toggle. The workspace is bound to the session here — the
// canonical default the data hooks read — keeping WorkspaceSwitcher a pure
// controlled component. (Per the design, workspace_id is also a route search
// param; route modules sync the param to the session, so writing the session
// here is the single source of truth the rest of the app observes.)

import { Loader2 } from "lucide-react";

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useSession } from "@/hooks/useSession";
import { useHealth } from "@/lib/api/queries";

import { IdentityControls } from "./IdentityControls";
import { ThemeToggle } from "./ThemeToggle";
import { WorkspaceSwitcher } from "./WorkspaceSwitcher";

function HealthDot() {
  const { data, isLoading, isError } = useHealth();

  if (isLoading) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="flex size-7 items-center justify-center">
            <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
          </span>
        </TooltipTrigger>
        <TooltipContent>Checking server health…</TooltipContent>
      </Tooltip>
    );
  }

  const live = !isError && Boolean(data?.live);
  const ready = !isError && Boolean(data?.ready);

  // Healthy: live + ready (emerald). Degraded: live but not ready (amber).
  // Down: not live / errored (red).
  let tone: "ok" | "degraded" | "down";
  let label: string;
  if (live && ready) {
    tone = "ok";
    label = "Server healthy (live + ready)";
  } else if (live) {
    tone = "degraded";
    label = "Server live but not ready";
  } else {
    tone = "down";
    label = "Server unreachable";
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
            )}
          />
        </span>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export function Topbar() {
  const { workspace, setWorkspace } = useSession();

  return (
    <header className="flex h-14 items-center gap-3 border-b bg-card/40 px-4 backdrop-blur">
      <WorkspaceSwitcher value={workspace} onChange={setWorkspace} />
      <div className="ml-auto flex items-center gap-1">
        <HealthDot />
        <IdentityControls />
        <ThemeToggle />
      </div>
    </header>
  );
}
