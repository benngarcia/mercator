import * as React from "react";
import { Plus, Trash2, Lock } from "lucide-react";

import type { EnvBinding } from "@/lib/api/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export interface EnvEditorProps {
  /** The env map as it appears on the wire: { KEY: { value } }. */
  value: Record<string, EnvBinding>;
  onChange: (value: Record<string, EnvBinding>) => void;
  className?: string;
}

// Internal editable representation: an ordered list of rows. We keep this local
// (rather than deriving from the unordered record on each keystroke) so editing
// a key doesn't reorder rows or drop focus.
interface Row {
  id: string;
  key: string;
  value: string;
}

let rowSeq = 0;
function newRowId(): string {
  rowSeq += 1;
  return `env-${rowSeq}`;
}

// rowsToRecord drops blank-keyed rows and emits the wire shape. Literal values
// only — there is intentionally no secret_ref path (ADR 0001).
function rowsToRecord(rows: Row[]): Record<string, EnvBinding> {
  const out: Record<string, EnvBinding> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) continue;
    out[key] = { value: row.value };
  }
  return out;
}

/**
 * EnvEditor edits container environment variables as literal key/value rows.
 *
 * Per ADR 0001, Mercator does not own secrets: there is deliberately no
 * secret_ref affordance. Sensitive values must be provided to the workload via
 * the provider/workload's own secret mechanism, not injected here.
 */
export function EnvEditor({ value, onChange, className }: EnvEditorProps) {
  // Seed rows once from the incoming value; thereafter rows are the source of
  // truth and we push changes outward via onChange.
  const [rows, setRows] = React.useState<Row[]>(() =>
    Object.entries(value).map(([key, binding]) => ({
      id: newRowId(),
      key,
      value: binding.value ?? "",
    })),
  );

  const emit = React.useCallback(
    (next: Row[]) => {
      setRows(next);
      onChange(rowsToRecord(next));
    },
    [onChange],
  );

  const updateRow = (id: string, patch: Partial<Omit<Row, "id">>) => {
    emit(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  };

  const addRow = () => {
    setRows((prev) => [...prev, { id: newRowId(), key: "", value: "" }]);
  };

  const removeRow = (id: string) => {
    emit(rows.filter((r) => r.id !== id));
  };

  // Flag duplicate keys; the last writer wins on the wire, so surface it.
  const keyCounts = React.useMemo(() => {
    const counts = new Map<string, number>();
    for (const row of rows) {
      const key = row.key.trim();
      if (!key) continue;
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
    return counts;
  }, [rows]);

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      {rows.length > 0 ? (
        <div className="flex flex-col gap-1.5">
          {rows.map((row) => {
            const trimmed = row.key.trim();
            const duplicate =
              trimmed.length > 0 && (keyCounts.get(trimmed) ?? 0) > 1;
            return (
              <div key={row.id} className="flex items-start gap-1.5">
                <div className="flex-1">
                  <Input
                    value={row.key}
                    onChange={(e) => updateRow(row.id, { key: e.target.value })}
                    placeholder="KEY"
                    spellCheck={false}
                    autoCapitalize="off"
                    autoCorrect="off"
                    className={cn(
                      "h-8 font-mono text-xs",
                      duplicate && "border-phase-failed/60",
                    )}
                    aria-label="Environment variable name"
                  />
                  {duplicate ? (
                    <p className="mt-0.5 text-[0.6875rem] text-phase-failed">
                      Duplicate key — the last row wins.
                    </p>
                  ) : null}
                </div>
                <span className="pt-1.5 text-xs text-muted-foreground">=</span>
                <Input
                  value={row.value}
                  onChange={(e) => updateRow(row.id, { value: e.target.value })}
                  placeholder="value"
                  spellCheck={false}
                  className="h-8 flex-1 font-mono text-xs"
                  aria-label="Environment variable value"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-8 shrink-0 text-muted-foreground hover:text-phase-failed"
                  onClick={() => removeRow(row.id)}
                  aria-label="Remove variable"
                >
                  <Trash2 />
                </Button>
              </div>
            );
          })}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          No environment variables.
        </p>
      )}

      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        onClick={addRow}
      >
        <Plus />
        Add variable
      </Button>

      <p className="flex items-start gap-1.5 text-[0.6875rem] leading-relaxed text-muted-foreground">
        <Lock className="mt-0.5 size-3 shrink-0" />
        <span>
          Values are stored and transmitted literally. Mercator does not manage
          secrets (ADR 0001) — provide sensitive values through your workload's
          own secret mechanism, not here.
        </span>
      </p>
    </div>
  );
}
