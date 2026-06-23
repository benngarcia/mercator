// WorkspaceSwitcher edits the active workspace id. It is a controlled component
// — { value; onChange } — so the parent (Topbar) owns binding to the route
// search param / session. Recents are read directly from session.ts (the
// canonical store updated whenever a workspace is committed) and offered in a
// filterable list for quick switching; any free-form id can be committed with
// Enter or the "Use" affordance, since workspaces are operator-supplied and
// need not pre-exist in recents.

import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronsUpDown, Layers, Search } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import { getRecentWorkspaces, workspaceOptions } from "@/lib/session";

export interface WorkspaceSwitcherProps {
  value: string | null;
  onChange: (workspaceID: string) => void;
}

export function WorkspaceSwitcher({ value, onChange }: WorkspaceSwitcherProps) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [recents, setRecents] = useState<string[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);

  // Refresh recents on open so newly-committed workspaces appear immediately,
  // and focus the field for quick entry.
  useEffect(() => {
    if (open) {
      setRecents(getRecentWorkspaces());
      setDraft("");
      const id = window.setTimeout(() => inputRef.current?.focus(), 0);
      return () => window.clearTimeout(id);
    }
  }, [open]);

  const select = (workspaceID: string) => {
    const next = workspaceID.trim();
    if (next === "") return;
    onChange(next);
    setOpen(false);
  };

  const trimmedDraft = draft.trim();
  const query = trimmedDraft.toLowerCase();
  // Always include the active workspace and the default, not just recents, so
  // the current selection is visible/selectable even when it was set via URL or
  // never explicitly committed.
  const options = useMemo(
    () => workspaceOptions(value, recents),
    [value, recents],
  );
  const filtered = useMemo(
    () =>
      query === ""
        ? options
        : options.filter((ws) => ws.toLowerCase().includes(query)),
    [options, query],
  );
  const draftIsNew =
    trimmedDraft !== "" && !options.some((ws) => ws === trimmedDraft);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          role="combobox"
          aria-expanded={open}
          aria-label="Switch workspace"
          className="min-w-44 justify-between gap-2 rounded-full"
        >
          <span className="flex min-w-0 items-center gap-2">
            <Layers className="text-muted-foreground" />
            <span
              className={cn(
                "truncate font-mono text-xs",
                !value && "text-muted-foreground",
              )}
            >
              {value || "Set workspace…"}
            </span>
          </span>
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-0">
        <div className="flex items-center border-b px-3">
          <Search className="mr-2 size-4 shrink-0 text-muted-foreground" />
          <Input
            ref={inputRef}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                // Commit the typed id, or the sole filtered match if no draft.
                select(trimmedDraft !== "" ? trimmedDraft : (filtered[0] ?? ""));
              }
            }}
            placeholder="Workspace id…"
            autoComplete="off"
            spellCheck={false}
            aria-label="Workspace id"
            className="h-10 border-0 bg-transparent px-0 font-mono text-xs shadow-none focus-visible:ring-0"
          />
        </div>
        <div
          role="listbox"
          className="max-h-72 overflow-y-auto p-1"
        >
          {draftIsNew ? (
            <button
              type="button"
              role="option"
              aria-selected={false}
              onClick={() => select(trimmedDraft)}
              className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-none hover:bg-accent hover:text-accent-foreground"
            >
              <Layers className="size-4 shrink-0 text-muted-foreground" />
              <span className="text-muted-foreground">Use</span>
              <span className="truncate font-mono text-xs">{trimmedDraft}</span>
            </button>
          ) : null}

          {filtered.length > 0 ? (
            <>
              {draftIsNew ? <div className="my-1 h-px bg-border" /> : null}
              <div className="px-2 py-1 text-xs font-medium text-muted-foreground">
                Workspaces
              </div>
              {filtered.map((ws) => (
                <button
                  key={ws}
                  type="button"
                  role="option"
                  aria-selected={ws === value}
                  onClick={() => select(ws)}
                  className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm outline-none hover:bg-accent hover:text-accent-foreground"
                >
                  <Check
                    className={cn(
                      "size-4 shrink-0 text-primary",
                      ws === value ? "opacity-100" : "opacity-0",
                    )}
                  />
                  <span className="truncate font-mono text-xs">{ws}</span>
                </button>
              ))}
            </>
          ) : null}

          {filtered.length === 0 && !draftIsNew ? (
            <div className="px-2 py-6 text-center text-sm text-muted-foreground">
              {trimmedDraft === ""
                ? "Type a workspace id."
                : "Press Enter to use this id."}
            </div>
          ) : null}
        </div>
      </PopoverContent>
    </Popover>
  );
}
