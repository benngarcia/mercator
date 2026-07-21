import { useMemo, useRef, useState } from "react";
import {
  Archive,
  Check,
  ChevronsUpDown,
  Layers,
  Loader2,
  Plus,
  Search,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { useCreateWorkspace, useWorkspaces } from "@/lib/api/queries";
import { cn } from "@/lib/utils";

export interface WorkspaceSwitcherProps {
  value: string | null;
  onChange: (workspaceID: string) => void;
}

export function WorkspaceSwitcher({ value, onChange }: WorkspaceSwitcherProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [creating, setCreating] = useState(false);
  const [displayName, setDisplayName] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);
  const workspaces = useWorkspaces(showArchived);
  const createWorkspace = useCreateWorkspace();

  const changeOpen = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) return;
    setQuery("");
    setCreating(false);
    setDisplayName("");
    createWorkspace.reset();
    requestAnimationFrame(() => searchRef.current?.focus());
  };

  const select = (workspaceID: string) => {
    onChange(workspaceID);
    setOpen(false);
  };

  const create = () => {
    const name = displayName.trim();
    if (name === "") return;
    createWorkspace.mutate(name, {
      onSuccess: (workspace) => select(workspace.id),
    });
  };

  const normalizedQuery = query.trim().toLowerCase();
  const filtered = useMemo(
    () =>
      (workspaces.data ?? []).filter((workspace) =>
        normalizedQuery === "" ||
        workspace.display_name.toLowerCase().includes(normalizedQuery) ||
        workspace.id.toLowerCase().includes(normalizedQuery),
      ),
    [normalizedQuery, workspaces.data],
  );
  const selected = workspaces.data?.find((workspace) => workspace.id === value);

  return (
    <Popover open={open} onOpenChange={changeOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          role="combobox"
          aria-expanded={open}
          aria-label="Switch workspace"
          className="min-w-52 justify-between gap-2 rounded-full"
        >
          <span className="flex min-w-0 items-center gap-2">
            <Layers className="text-muted-foreground" />
            <span className={cn("truncate text-xs", !value && "text-muted-foreground")}>
              {selected?.display_name ?? value ?? "Select workspace"}
            </span>
          </span>
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 p-0">
        {creating ? (
          <form
            className="space-y-3 p-3"
            onSubmit={(event) => {
              event.preventDefault();
              create();
            }}
          >
            <div>
              <div className="text-sm font-medium">Create workspace</div>
              <p className="mt-1 text-xs text-muted-foreground">
                Mercator assigns a stable ID and records your authenticated identity.
              </p>
            </div>
            <Input
              autoFocus
              value={displayName}
              onChange={(event) => setDisplayName(event.target.value)}
              placeholder="Workspace name"
              aria-label="Workspace name"
              disabled={createWorkspace.isPending}
            />
            {createWorkspace.error ? (
              <p role="alert" className="text-xs text-destructive">
                {createWorkspace.error.message}
              </p>
            ) : null}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" size="sm" onClick={() => setCreating(false)}>
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={displayName.trim() === "" || createWorkspace.isPending}>
                {createWorkspace.isPending ? <Loader2 className="animate-spin" /> : <Plus />}
                Create
              </Button>
            </div>
          </form>
        ) : (
          <>
            <div className="flex items-center border-b px-3">
              <Search className="mr-2 size-4 shrink-0 text-muted-foreground" />
              <Input
                ref={searchRef}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search workspaces"
                autoComplete="off"
                aria-label="Search workspaces"
                className="h-10 border-0 bg-transparent px-0 text-xs shadow-none focus-visible:ring-0"
              />
            </div>
            <div role="listbox" className="max-h-72 overflow-y-auto p-1">
              {workspaces.isLoading ? (
                <div className="flex items-center justify-center gap-2 px-2 py-8 text-sm text-muted-foreground">
                  <Loader2 className="animate-spin" /> Loading workspaces
                </div>
              ) : workspaces.isError ? (
                <div className="px-3 py-6 text-center text-sm text-destructive" role="alert">
                  {workspaces.error.message}
                </div>
              ) : filtered.length === 0 ? (
                <div className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {normalizedQuery === "" ? "No saved workspaces." : "No matching workspaces."}
                </div>
              ) : (
                filtered.map((workspace) => (
                  <button
                    key={workspace.id}
                    type="button"
                    role="option"
                    aria-selected={workspace.id === value}
                    onClick={() => select(workspace.id)}
                    className="flex w-full items-center gap-2 rounded-sm px-2 py-2 text-left outline-none hover:bg-accent hover:text-accent-foreground"
                  >
                    <Check className={cn("size-4 shrink-0 text-primary", workspace.id === value ? "opacity-100" : "opacity-0")} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm">{workspace.display_name}</span>
                      <span className="block truncate font-mono text-[11px] text-muted-foreground">{workspace.id}</span>
                    </span>
                    {workspace.archived_at ? (
                      <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
                        <Archive className="size-3" /> Archived
                      </span>
                    ) : null}
                  </button>
                ))
              )}
            </div>
            <div className="flex items-center justify-between border-t p-1.5">
              <Button variant="ghost" size="sm" onClick={() => setShowArchived((shown) => !shown)}>
                <Archive /> {showArchived ? "Hide archived" : "Show archived"}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setCreating(true)}>
                <Plus /> New workspace
              </Button>
            </div>
          </>
        )}
      </PopoverContent>
    </Popover>
  );
}
