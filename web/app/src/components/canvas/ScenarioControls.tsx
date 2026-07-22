import { ChevronLeft, ChevronRight, Pause, Play, RotateCcw } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type {
  ScenarioPlaybackSnapshot,
  ScenarioPlaybackSpeed,
} from "@/lib/workspace/playback";
import type { WorkspacePlaybackControls } from "@/lib/workspace/react";

const SPEEDS: readonly ScenarioPlaybackSpeed[] = [1, 2, 4];

export function ScenarioControls({
  controls,
  playback,
}: {
  controls: WorkspacePlaybackControls;
  playback: ScenarioPlaybackSnapshot;
}) {
  const playing = playback.status === "playing";
  const progress =
    playback.cueCount === 0
      ? 0
      : (playback.cursor / playback.cueCount) * 100;
  return (
    <div className="flex min-w-0 items-center gap-3" aria-busy={controls.busy}>
      <div className="flex items-center gap-0.5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label="Previous event"
          disabled={controls.busy || playback.cursor === 0}
          onClick={() => void controls.previous()}
        >
          <ChevronLeft />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label={playing ? "Pause scenario" : "Play scenario"}
          disabled={controls.busy}
          onClick={() => void (playing ? controls.pause() : controls.play())}
        >
          {playing ? <Pause /> : <Play />}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label="Next event"
          disabled={controls.busy || playback.cursor === playback.cueCount}
          onClick={() => void controls.next()}
        >
          <ChevronRight />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label="Restart scenario"
          disabled={controls.busy}
          onClick={() => void controls.restart()}
        >
          <RotateCcw />
        </Button>
      </div>
      <div className="w-32 sm:w-44">
        <div
          role="progressbar"
          aria-label="Scenario progress"
          aria-valuemin={0}
          aria-valuemax={playback.cueCount}
          aria-valuenow={playback.cursor}
          className="h-1 overflow-hidden rounded-full bg-surface-3"
        >
          <div
            className="h-full rounded-full bg-primary transition-[width] duration-200"
            style={{ width: `${progress}%` }}
          />
        </div>
        <div className="mt-1 flex justify-between font-mono text-[10px] tabular text-muted-foreground">
          <span>
            Event {playback.cursor} of {playback.cueCount}
          </span>
          <span>{playbackTime(playback.elapsedMillis)}</span>
        </div>
      </div>
      <div className="flex rounded-md bg-muted p-0.5">
        {SPEEDS.map((speed) => (
          <button
            key={speed}
            type="button"
            aria-label={`${speed}× playback speed`}
            aria-pressed={playback.speed === speed}
            disabled={controls.busy}
            onClick={() => void controls.setSpeed(speed)}
            className={cn(
              "h-6 min-w-7 rounded px-1.5 font-mono text-[10px] text-muted-foreground transition-colors",
              playback.speed === speed &&
                "bg-background text-foreground shadow-sm",
            )}
          >
            {speed}×
          </button>
        ))}
      </div>
    </div>
  );
}

function playbackTime(milliseconds: number): string {
  const seconds = Math.floor(milliseconds / 1_000);
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(
    seconds % 60,
  ).padStart(2, "0")}`;
}
