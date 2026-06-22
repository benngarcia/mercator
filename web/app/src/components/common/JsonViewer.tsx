import * as React from "react";
import { ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";
import { CopyButton } from "./CopyButton";

export interface JsonViewerProps {
  value: unknown;
  /** Start collapsed (root toggle hidden until expanded). Defaults to false. */
  collapsed?: boolean;
  className?: string;
  /** Max height before the body scrolls. Defaults to a roomy 24rem. */
  maxHeight?: string;
}

function stringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/**
 * JsonViewer pretty-prints arbitrary JSON (CloudEvent.data, raw specs,
 * decisions) in the mono stack with a copy affordance and an optional
 * collapse. Syntax is lightly tokenized for legibility against the dark theme.
 */
export function JsonViewer({
  value,
  collapsed = false,
  className,
  maxHeight = "24rem",
}: JsonViewerProps) {
  const [open, setOpen] = React.useState(!collapsed);
  const text = React.useMemo(() => stringify(value), [value]);

  return (
    <div
      className={cn(
        "overflow-hidden rounded-md border border-border bg-card/60",
        className,
      )}
    >
      <div className="flex items-center justify-between border-b border-border bg-muted/30 px-2 py-1">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="flex items-center gap-1 rounded px-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronRight
            className={cn(
              "size-3.5 transition-transform",
              open && "rotate-90",
            )}
          />
          json
        </button>
        <CopyButton value={text} label="Copy JSON" />
      </div>
      {open ? (
        <pre
          className="overflow-auto p-3 text-xs leading-relaxed"
          style={{ maxHeight }}
        >
          <code className="font-mono text-foreground">{highlight(text)}</code>
        </pre>
      ) : null}
    </div>
  );
}

// highlight applies minimal token coloring (keys, strings, numbers, literals)
// without a dependency, returning React nodes keyed by index.
const TOKEN =
  /("(?:\\.|[^"\\])*"(?=\s*:)|"(?:\\.|[^"\\])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|\b(?:true|false|null)\b)/g;

function highlight(text: string): React.ReactNode[] {
  const nodes: React.ReactNode[] = [];
  let last = 0;
  let key = 0;
  let match: RegExpExecArray | null;
  TOKEN.lastIndex = 0;
  while ((match = TOKEN.exec(text)) !== null) {
    if (match.index > last) {
      nodes.push(text.slice(last, match.index));
    }
    const token = match[0];
    let cls = "text-foreground";
    if (token.endsWith('"') && text[TOKEN.lastIndex] === ":") {
      cls = "text-primary";
    } else if (token.startsWith('"')) {
      cls = "text-phase-succeeded";
    } else if (token === "true" || token === "false" || token === "null") {
      cls = "text-phase-launching";
    } else {
      cls = "text-phase-running";
    }
    nodes.push(
      <span key={key++} className={cls}>
        {token}
      </span>,
    );
    last = TOKEN.lastIndex;
  }
  if (last < text.length) {
    nodes.push(text.slice(last));
  }
  return nodes;
}
