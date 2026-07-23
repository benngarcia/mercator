// TokenField binds the bearer token to the session (localStorage via
// useSession). The token is treated as a secret: rendered in a password field,
// never logged, never placed in the URL. A show/hide toggle and a clear action
// are offered for operator convenience. Api reads the token centrally, so
// this component only persists it.

import { useRef, useState } from "react";
import { Check, Eye, EyeOff, KeyRound, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useSession } from "@/hooks/useSession";

export function TokenField() {
  const { token, hasToken, setToken } = useSession();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(token ?? "");
  const [draftSource, setDraftSource] = useState(token);
  const [reveal, setReveal] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  if (open && draftSource !== token) {
    setDraftSource(token);
    setDraft(token ?? "");
    setReveal(false);
  }

  const changeOpen = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) return;
    setDraftSource(token);
    setDraft(token ?? "");
    setReveal(false);
    requestAnimationFrame(() => inputRef.current?.focus());
  };

  const commit = () => {
    const next = draft.trim();
    setToken(next === "" ? null : next);
    setOpen(false);
  };

  const clear = () => {
    setToken(null);
    setDraft("");
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={changeOpen}>
      <Tooltip>
        <TooltipTrigger asChild>
          <PopoverTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={hasToken ? "API token set" : "Set API token"}
              className="relative"
            >
              <KeyRound />
              <span
                className={cn(
                  "absolute right-1.5 top-1.5 size-1.5 rounded-full",
                  hasToken ? "bg-primary" : "bg-muted-foreground/40",
                )}
                aria-hidden
              />
            </Button>
          </PopoverTrigger>
        </TooltipTrigger>
        <TooltipContent>
          {hasToken ? "API token set" : "No API token"}
        </TooltipContent>
      </Tooltip>

      <PopoverContent align="end" className="w-80">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            commit();
          }}
          className="grid gap-3"
        >
          <div className="grid gap-1">
            <div className="text-sm font-medium">API token</div>
            <p className="text-xs text-muted-foreground">
              Sent as a bearer token on every request. Stored locally in this
              browser only; never placed in the URL.
            </p>
          </div>

          <div className="relative">
            <Input
              ref={inputRef}
              type={reveal ? "text" : "password"}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Paste token…"
              autoComplete="off"
              spellCheck={false}
              className="pr-9 font-mono text-xs"
              aria-label="API token"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="absolute right-0 top-0 size-9 text-muted-foreground"
              aria-label={reveal ? "Hide token" : "Show token"}
              onClick={() => setReveal((v) => !v)}
            >
              {reveal ? <EyeOff /> : <Eye />}
            </Button>
          </div>

          <div className="flex items-center justify-between gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={clear}
              disabled={!hasToken && draft.trim() === ""}
            >
              <X />
              Clear
            </Button>
            <Button type="submit" size="sm">
              <Check />
              Save
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}
