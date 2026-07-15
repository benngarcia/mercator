import * as React from "react";
import { Search } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { useMountEffect } from "@/hooks/useMountEffect";

/**
 * Lightweight, dependency-free command palette.
 *
 * The canonical shadcn `command` is built on `cmdk`, which is NOT a declared
 * dependency of this project. Per the spec we implement the needed surface
 * without adding a dep. This is a self-contained filterable list with keyboard
 * navigation (Arrow keys + Enter), exposing the same component names the rest
 * of the app expects: Command, CommandInput, CommandList, CommandEmpty,
 * CommandGroup, CommandItem, CommandSeparator, CommandShortcut, CommandDialog.
 *
 * Filtering: each CommandItem registers a search value (its `value` prop, else
 * its text content). The CommandInput's text filters items case-insensitively
 * via substring match. Hidden items are removed from the DOM so CommandEmpty
 * and groups react correctly.
 */

interface CommandContextValue {
  search: string;
  setSearch: (value: string) => void;
  activeId: string | null;
  setActiveId: (id: string | null) => void;
  registerItem: (id: string, value: string, onSelect?: () => void) => void;
  unregisterItem: (id: string) => void;
  matches: (value: string) => boolean;
  visibleIds: string[];
  selectActive: () => void;
  moveActive: (delta: number) => void;
}

const CommandContext = React.createContext<CommandContextValue | null>(null);

function useCommand(): CommandContextValue {
  const ctx = React.useContext(CommandContext);
  if (!ctx) {
    throw new Error("Command components must be used within <Command>");
  }
  return ctx;
}

interface ItemRecord {
  id: string;
  value: string;
  onSelect?: () => void;
}

const Command = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, children, ...props }, ref) => {
  const [search, setSearch] = React.useState("");
  const [activeId, setActiveId] = React.useState<string | null>(null);
  const itemsRef = React.useRef<ItemRecord[]>([]);
  const [, forceRender] = React.useReducer((n: number) => n + 1, 0);

  const matches = React.useCallback(
    (value: string): boolean => {
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return value.toLowerCase().includes(q);
    },
    [search],
  );

  const registerItem = React.useCallback(
    (id: string, value: string, onSelect?: () => void) => {
      const existing = itemsRef.current.find((i) => i.id === id);
      if (existing) {
        existing.value = value;
        existing.onSelect = onSelect;
      } else {
        itemsRef.current.push({ id, value, onSelect });
      }
      forceRender();
    },
    [],
  );

  const unregisterItem = React.useCallback((id: string) => {
    itemsRef.current = itemsRef.current.filter((i) => i.id !== id);
    forceRender();
  }, []);

  const visibleIds = React.useMemo(
    () => itemsRef.current.filter((i) => matches(i.value)).map((i) => i.id),
    // re-derive when search changes or items change (forceRender bumps).
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [matches, search],
  );

  const validActiveId = activeId && visibleIds.includes(activeId) ? activeId : (visibleIds[0] ?? null);
  if (validActiveId !== activeId) setActiveId(validActiveId);

  const moveActive = React.useCallback(
    (delta: number) => {
      if (visibleIds.length === 0) return;
      const idx = activeId ? visibleIds.indexOf(activeId) : -1;
      const next =
        (idx + delta + visibleIds.length) % visibleIds.length;
      setActiveId(visibleIds[next] ?? null);
    },
    [visibleIds, activeId],
  );

  const selectActive = React.useCallback(() => {
    if (!activeId) return;
    itemsRef.current.find((i) => i.id === activeId)?.onSelect?.();
  }, [activeId]);

  const value = React.useMemo<CommandContextValue>(
    () => ({
      search,
      setSearch,
      activeId,
      setActiveId,
      registerItem,
      unregisterItem,
      matches,
      visibleIds,
      selectActive,
      moveActive,
    }),
    [
      search,
      activeId,
      registerItem,
      unregisterItem,
      matches,
      visibleIds,
      selectActive,
      moveActive,
    ],
  );

  return (
    <CommandContext.Provider value={value}>
      <div
        ref={ref}
        className={cn(
          "flex size-full flex-col overflow-hidden rounded-md bg-popover text-popover-foreground",
          className,
        )}
        {...props}
      >
        {children}
      </div>
    </CommandContext.Provider>
  );
});
Command.displayName = "Command";

interface CommandDialogProps
  extends React.ComponentPropsWithoutRef<typeof Dialog> {
  title?: string;
  description?: string;
  className?: string;
  children: React.ReactNode;
}

function CommandDialog({
  title = "Command Palette",
  description = "Search for a command to run...",
  children,
  className,
  ...props
}: CommandDialogProps) {
  return (
    <Dialog {...props}>
      <DialogHeader className="sr-only">
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <DialogContent className={cn("overflow-hidden p-0", className)}>
        <Command className="[&_[data-command-group-heading]]:px-2 [&_[data-command-group-heading]]:py-1.5 [&_[data-command-group-heading]]:text-xs [&_[data-command-group-heading]]:font-medium [&_[data-command-group-heading]]:text-muted-foreground">
          {children}
        </Command>
      </DialogContent>
    </Dialog>
  );
}

const CommandInput = React.forwardRef<
  HTMLInputElement,
  Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">
>(({ className, ...props }, ref) => {
  const { search, setSearch, moveActive, selectActive } = useCommand();
  return (
    <div className="flex items-center border-b px-3" data-command-input-wrapper>
      <Search className="mr-2 size-4 shrink-0 opacity-50" />
      <input
        ref={ref}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            moveActive(1);
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            moveActive(-1);
          } else if (e.key === "Enter") {
            e.preventDefault();
            selectActive();
          }
        }}
        className={cn(
          "flex h-10 w-full rounded-md bg-transparent py-3 text-sm outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...props}
      />
    </div>
  );
});
CommandInput.displayName = "CommandInput";

const CommandList = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn(
      "max-h-75 scroll-py-1 overflow-y-auto overflow-x-hidden",
      className,
    )}
    role="listbox"
    {...props}
  />
));
CommandList.displayName = "CommandList";

const CommandEmpty = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => {
  const { visibleIds } = useCommand();
  if (visibleIds.length > 0) return null;
  return (
    <div
      ref={ref}
      className={cn("py-6 text-center text-sm", className)}
      {...props}
    />
  );
});
CommandEmpty.displayName = "CommandEmpty";

const CommandGroup = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement> & { heading?: React.ReactNode }
>(({ className, heading, children, ...props }, ref) => (
  <div
    ref={ref}
    className={cn("overflow-hidden p-1 text-foreground", className)}
    role="group"
    {...props}
  >
    {heading ? (
      <div data-command-group-heading aria-hidden>
        {heading}
      </div>
    ) : null}
    {children}
  </div>
));
CommandGroup.displayName = "CommandGroup";

const CommandSeparator = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn("-mx-1 h-px bg-border", className)}
    {...props}
  />
));
CommandSeparator.displayName = "CommandSeparator";

interface CommandItemProps
  extends Omit<React.HTMLAttributes<HTMLDivElement>, "onSelect"> {
  value?: string;
  disabled?: boolean;
  onSelect?: (value: string) => void;
}

function CommandItemRegistration({ ctx, id, onSelect, value }: {
  ctx: CommandContextValue;
  id: string;
  onSelect: () => void;
  value: string;
}) {
  const selectRef = React.useRef(onSelect);
  selectRef.current = onSelect;
  useMountEffect(() => {
    ctx.registerItem(id, value, () => selectRef.current());
    return () => ctx.unregisterItem(id);
  });
  return null;
}

const CommandItem = React.forwardRef<HTMLDivElement, CommandItemProps>(
  ({ className, value, disabled, onSelect, children, ...props }, _ref) => {
    const ctx = useCommand();
    const id = React.useId();
    const innerRef = React.useRef<HTMLDivElement>(null);

    const searchValue =
      value ??
      (typeof children === "string" ? children : innerRef.current?.textContent ?? id);

    const handleSelect = React.useCallback(() => {
      if (disabled) return;
      onSelect?.(searchValue);
    }, [disabled, onSelect, searchValue]);

    const registration = (
      <CommandItemRegistration
        key={`${id}:${searchValue}`}
        ctx={ctx}
        id={id}
        onSelect={handleSelect}
        value={searchValue}
      />
    );

    if (!ctx.matches(searchValue)) return registration;

    const active = ctx.activeId === id;

    return (
      <>
        {registration}
        <div
          ref={innerRef}
          role="option"
          aria-selected={active}
          aria-disabled={disabled || undefined}
          data-selected={active ? "" : undefined}
          data-disabled={disabled ? "" : undefined}
          onPointerMove={() => !disabled && ctx.setActiveId(id)}
          onClick={handleSelect}
          className={cn(
            "relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none data-[selected]:bg-accent data-[selected]:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0",
            className,
          )}
          {...props}
        >
          {children}
        </div>
      </>
    );
  },
);
CommandItem.displayName = "CommandItem";

function CommandShortcut({
  className,
  ...props
}: React.HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn(
        "ml-auto text-xs tracking-widest text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}
CommandShortcut.displayName = "CommandShortcut";

export {
  Command,
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandShortcut,
  CommandSeparator,
};
