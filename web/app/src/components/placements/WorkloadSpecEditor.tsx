import * as React from "react";
import { AlertCircle, Check, Code2 } from "lucide-react";

import { ApiError } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ViolationDetails } from "@/components/common";

export interface WorkloadSpecEditorProps {
  /** Raw JSON text for the workload (revision) document being edited. */
  value: string;
  /** Fires on every keystroke with the new raw text. */
  onChange: (value: string) => void;
  /**
   * Server-side error from a failed Create run mutation. Its
   * `details[]` Violations are surfaced inline beneath the editor.
   */
  error?: ApiError | null;
  /** Optional field label. Defaults to "Workload JSON". */
  label?: string;
  /** Disable editing (e.g. while a mutation is in flight). */
  disabled?: boolean;
  className?: string;
  id?: string;
}

interface LocalParse {
  ok: boolean;
  message?: string;
}

// parseLocal validates the buffer as JSON and returns a precise error message
// (including the line/column when the runtime exposes a position) so the
// operator can find the syntax problem without leaving the editor.
function parseLocal(text: string): LocalParse {
  const trimmed = text.trim();
  if (trimmed === "") {
    return { ok: false, message: "Spec is empty." };
  }
  try {
    JSON.parse(trimmed);
    return { ok: true };
  } catch (err) {
    const raw = err instanceof Error ? err.message : String(err);
    const posMatch = /position (\d+)/.exec(raw);
    if (posMatch) {
      const pos = Number(posMatch[1]);
      const upTo = text.slice(0, pos);
      const line = upTo.split("\n").length;
      const col = pos - upTo.lastIndexOf("\n");
      return { ok: false, message: `${raw} (line ${line}, column ${col})` };
    }
    return { ok: false, message: raw };
  }
}

/**
 * WorkloadSpecEditor is the JSON authoring surface used by Create run's spec
 * mode. It is a controlled monospace textarea with live
 * client-side JSON validation and inline display of server Violations from a
 * failed submission. It does not own a submit action — the parent owns the
 * button and reads `value`.
 */
export function WorkloadSpecEditor({
  value,
  onChange,
  error,
  label = "Workload JSON",
  disabled = false,
  className,
  id: idProp,
}: WorkloadSpecEditorProps) {
  const reactId = React.useId();
  const id = idProp ?? reactId;
  const parse = React.useMemo(() => parseLocal(value), [value]);

  const violations = error?.details ?? [];
  const hasViolations = violations.length > 0;
  // A server error without structured Violations still deserves a line.
  const serverMessage =
    error && !hasViolations ? error.message || error.code : undefined;

  const lineCount = React.useMemo(
    () => (value === "" ? 1 : value.split("\n").length),
    [value],
  );

  // Tab inserts two spaces instead of moving focus, matching editor muscle
  // memory while keeping the textarea controlled.
  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Tab" || e.shiftKey) return;
    e.preventDefault();
    const el = e.currentTarget;
    const { selectionStart, selectionEnd } = el;
    const next =
      value.slice(0, selectionStart) + "  " + value.slice(selectionEnd);
    onChange(next);
    requestAnimationFrame(() => {
      el.selectionStart = el.selectionEnd = selectionStart + 2;
    });
  };

  const handleFormat = () => {
    try {
      const parsed = JSON.parse(value);
      onChange(JSON.stringify(parsed, null, 2));
    } catch {
      // Ignore — the validity line already explains why formatting is blocked.
    }
  };

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex items-center justify-between">
        <Label htmlFor={id} className="flex items-center gap-1.5">
          <Code2 className="size-3.5 text-muted-foreground" />
          {label}
        </Label>
        <button
          type="button"
          onClick={handleFormat}
          disabled={disabled || !parse.ok}
          className="rounded px-1.5 py-0.5 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
        >
          Format
        </button>
      </div>

      <div
        className={cn(
          "overflow-hidden rounded-md border bg-card/40 focus-within:ring-1 focus-within:ring-ring",
          parse.ok && !hasViolations && "border-border",
          (!parse.ok || hasViolations) && "border-destructive/60",
        )}
      >
        <Textarea
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={handleKeyDown}
          disabled={disabled}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          aria-invalid={!parse.ok || hasViolations}
          placeholder='{ "spec": { "containers": [ … ], "resources": { … } } }'
          className="min-h-72 resize-y rounded-none border-0 bg-transparent font-mono text-xs leading-relaxed shadow-none focus-visible:ring-0"
        />
        <div className="flex items-center justify-between border-t border-border/60 bg-muted/30 px-2 py-1 font-mono text-[11px] text-muted-foreground">
          <span>
            {lineCount} {lineCount === 1 ? "line" : "lines"} · {value.length} chars
          </span>
          {parse.ok ? (
            <span className="flex items-center gap-1 text-phase-succeeded">
              <Check className="size-3" />
              valid JSON
            </span>
          ) : (
            <span className="flex items-center gap-1 text-destructive">
              <AlertCircle className="size-3" />
              invalid JSON
            </span>
          )}
        </div>
      </div>

      {!parse.ok && parse.message ? (
        <p className="flex items-start gap-1.5 text-xs text-destructive">
          <AlertCircle className="mt-0.5 size-3.5 shrink-0" />
          <span className="font-mono">{parse.message}</span>
        </p>
      ) : null}

      {serverMessage ? (
        <p className="flex items-start gap-1.5 text-xs text-destructive">
          <AlertCircle className="mt-0.5 size-3.5 shrink-0" />
          <span>{serverMessage}</span>
        </p>
      ) : null}

      {hasViolations ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-xs font-medium text-muted-foreground">
            The server rejected this spec:
          </p>
          <ViolationDetails violations={violations} />
        </div>
      ) : null}
    </div>
  );
}
