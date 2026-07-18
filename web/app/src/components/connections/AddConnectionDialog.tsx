import * as React from "react";
import { Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import type { CredentialSource, Violation } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import { useCreateConnection } from "@/lib/api/queries";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

export interface AddConnectionDialogProps {
  /** Controlled open state; omit for an uncontrolled dialog driven by trigger. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Optional trigger element (rendered via DialogTrigger asChild). */
  trigger?: React.ReactNode;
  /** Called with the created connection id on success. */
  onCreated?: (connectionId: string) => void;
  /** Workspace override; defaults to the session workspace (implicit). */
  workspaceId?: string;
}

// Internal key/value row for config entries.
interface ConfigRow {
  id: string;
  key: string;
  value: string;
}

let rowSeq = 0;
function newRowId(): string {
  rowSeq += 1;
  return `cfg-${rowSeq}`;
}

function rowsToRecord(rows: ConfigRow[]): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  let hasAny = false;
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) continue;
    out[key] = row.value;
    hasAny = true;
  }
  return hasAny ? out : undefined;
}

// Map a Violation path to a known field name for inline error rendering.
type KnownField =
  | "adapter_type"
  | "connection_id"
  | "credential_source"
  | "credential_ref"
  | "secret";

function fieldFromPath(path: string): KnownField | null {
  if (!path) return null;
  const lower = path.toLowerCase();
  if (lower.includes("adapter_type")) return "adapter_type";
  if (lower.includes("connection_id")) return "connection_id";
  if (lower.includes("credential") && lower.includes("ref")) return "credential_ref";
  if (lower.includes("credential") && lower.includes("source")) return "credential_source";
  if (lower.includes("secret")) return "secret";
  return null;
}

/**
 * AddConnectionDialog creates a new adapter connection. Fields: adapter type
 * (docker | modal | runpod), optional connection id, optional config key/value rows,
 * and a credential (env var ref or mercator-stored secret). Submits via
 * useCreateConnection and surfaces ApiError.details inline.
 */
export function AddConnectionDialog({
  open: controlledOpen,
  onOpenChange,
  trigger,
  onCreated,
  workspaceId,
}: AddConnectionDialogProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = React.useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = React.useCallback(
    (next: boolean) => {
      onOpenChange?.(next);
      if (controlledOpen === undefined) setUncontrolledOpen(next);
    },
    [controlledOpen, onOpenChange],
  );

  // Form state
  const [adapterType, setAdapterType] = React.useState<string>("");
  const [connectionId, setConnectionId] = React.useState("");
  const [configRows, setConfigRows] = React.useState<ConfigRow[]>([]);
  const [credentialSource, setCredentialSource] =
    React.useState<CredentialSource>("env");
  const [credentialRef, setCredentialRef] = React.useState(""); // env var name
  const [secret, setSecret] = React.useState(""); // mercator secret value

  // Error state
  const [fieldErrors, setFieldErrors] = React.useState<
    Partial<Record<KnownField, string>>
  >({});
  const [otherViolations, setOtherViolations] = React.useState<Violation[]>([]);

  const createConnection = useCreateConnection(workspaceId);

  const reset = React.useCallback(() => {
    setAdapterType("");
    setConnectionId("");
    setConfigRows([]);
    setCredentialSource("env");
    setCredentialRef("");
    setSecret("");
    setFieldErrors({});
    setOtherViolations([]);
  }, []);

  const handleOpenChange = React.useCallback(
    (next: boolean) => {
      if (!next) reset();
      setOpen(next);
    },
    [reset, setOpen],
  );

  // Config row helpers
  const addConfigRow = () => {
    setConfigRows((prev) => [...prev, { id: newRowId(), key: "", value: "" }]);
  };

  const updateConfigRow = (
    id: string,
    patch: Partial<Omit<ConfigRow, "id">>,
  ) => {
    setConfigRows((rows) =>
      rows.map((r) => (r.id === id ? { ...r, ...patch } : r)),
    );
  };

  const removeConfigRow = (id: string) => {
    setConfigRows((rows) => rows.filter((r) => r.id !== id));
  };

  const onSubmit = React.useCallback(
    (event: React.FormEvent) => {
      event.preventDefault();
      setFieldErrors({});
      setOtherViolations([]);

      // Build credential
      const credential =
        credentialSource === "env"
          ? credentialRef.trim()
            ? { source: "env" as CredentialSource, ref: credentialRef.trim() }
            : undefined
          : { source: "mercator" as CredentialSource, ref: "" };

      createConnection.mutate(
        {
          workspace_id: workspaceId ?? "",
          connection_id: connectionId.trim() || undefined,
          adapter_type: adapterType,
          config: rowsToRecord(configRows),
          credential,
          secret:
            credentialSource === "mercator" && secret ? secret : undefined,
        },
        {
          onSuccess: (rec) => {
            toast.success("Connection created", { description: rec.id });
            onCreated?.(rec.id);
            handleOpenChange(false);
          },
          onError: (error) => {
            const message =
              error instanceof ApiError
                ? error.message
                : "Failed to create connection";
            toast.error("Could not create connection", { description: message });
            if (error instanceof ApiError && error.details) {
              const fields: Partial<Record<KnownField, string>> = {};
              const rest: Violation[] = [];
              for (const v of error.details) {
                const field = fieldFromPath(v.path);
                if (field) {
                  fields[field] = v.message || v.code;
                } else {
                  rest.push(v);
                }
              }
              setFieldErrors(fields);
              setOtherViolations(rest);
            }
          },
        },
      );
    },
    [
      adapterType,
      configRows,
      connectionId,
      createConnection,
      credentialRef,
      credentialSource,
      handleOpenChange,
      onCreated,
      secret,
      workspaceId,
    ],
  );

  const submitting = createConnection.isPending;
  const canSubmit = adapterType.length > 0 && !submitting;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {trigger ? <DialogTrigger asChild>{trigger}</DialogTrigger> : null}
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add connection</DialogTitle>
          <DialogDescription>
            Register a new adapter connection for this workspace. Credentials
            are stored out-of-band and never appear in the event log.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          {/* Adapter type */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ac-adapter-type">Adapter type</Label>
            <Select
              value={adapterType}
              onValueChange={setAdapterType}
            >
              <SelectTrigger
                id="ac-adapter-type"
                className={cn(
                  fieldErrors.adapter_type && "border-destructive",
                )}
                aria-invalid={Boolean(fieldErrors.adapter_type)}
              >
                <SelectValue placeholder="Select adapter…" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="docker">docker</SelectItem>
                <SelectItem value="modal">modal</SelectItem>
                <SelectItem value="runpod">runpod</SelectItem>
              </SelectContent>
            </Select>
            {fieldErrors.adapter_type ? (
              <p className="text-xs text-destructive">
                {fieldErrors.adapter_type}
              </p>
            ) : null}
          </div>

          {/* Connection id (optional) */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ac-connection-id">
              Connection id{" "}
              <span className="text-muted-foreground">(optional)</span>
            </Label>
            <Input
              id="ac-connection-id"
              value={connectionId}
              onChange={(e) => setConnectionId(e.target.value)}
              placeholder="conn_my-adapter"
              autoComplete="off"
              spellCheck={false}
              className={cn(
                "font-mono",
                fieldErrors.connection_id && "border-destructive",
              )}
              aria-invalid={Boolean(fieldErrors.connection_id)}
            />
            {fieldErrors.connection_id ? (
              <p className="text-xs text-destructive">
                {fieldErrors.connection_id}
              </p>
            ) : null}
          </div>

          {/* Config key/value rows */}
          <div className="flex flex-col gap-1.5">
            <Label>
              Config{" "}
              <span className="text-muted-foreground">(optional)</span>
            </Label>
            {configRows.length > 0 ? (
              <div className="flex flex-col gap-1.5">
                {configRows.map((row) => (
                  <div key={row.id} className="flex items-start gap-1.5">
                    <Input
                      value={row.key}
                      onChange={(e) =>
                        updateConfigRow(row.id, { key: e.target.value })
                      }
                      placeholder="key"
                      spellCheck={false}
                      autoCapitalize="off"
                      autoCorrect="off"
                      className="h-8 flex-1 font-mono text-xs"
                      aria-label="Config key"
                    />
                    <span className="pt-1.5 text-xs text-muted-foreground">
                      =
                    </span>
                    <Input
                      value={row.value}
                      onChange={(e) =>
                        updateConfigRow(row.id, { value: e.target.value })
                      }
                      placeholder="value"
                      spellCheck={false}
                      className="h-8 flex-1 font-mono text-xs"
                      aria-label="Config value"
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="size-8 shrink-0 text-muted-foreground hover:text-phase-failed"
                      onClick={() => removeConfigRow(row.id)}
                      aria-label="Remove config entry"
                    >
                      <Trash2 />
                    </Button>
                  </div>
                ))}
              </div>
            ) : (
              <p className="text-xs text-muted-foreground">
                No config entries.
              </p>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="w-fit"
              onClick={addConfigRow}
            >
              <Plus />
              Add entry
            </Button>
          </div>

          {/* Credential */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ac-credential-source">Credential source</Label>
            <Select
              value={credentialSource}
              onValueChange={(v) => setCredentialSource(v as CredentialSource)}
            >
              <SelectTrigger
                id="ac-credential-source"
                className={cn(
                  fieldErrors.credential_source && "border-destructive",
                )}
                aria-invalid={Boolean(fieldErrors.credential_source)}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="env">env — read from environment variable</SelectItem>
                <SelectItem value="mercator">mercator — store in Mercator</SelectItem>
              </SelectContent>
            </Select>
            {fieldErrors.credential_source ? (
              <p className="text-xs text-destructive">
                {fieldErrors.credential_source}
              </p>
            ) : null}
          </div>

          {credentialSource === "env" ? (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="ac-credential-ref">Environment variable name</Label>
              <Input
                id="ac-credential-ref"
                value={credentialRef}
                onChange={(e) => setCredentialRef(e.target.value)}
                placeholder="MY_API_KEY"
                autoComplete="off"
                spellCheck={false}
                autoCapitalize="off"
                className={cn(
                  "font-mono",
                  fieldErrors.credential_ref && "border-destructive",
                )}
                aria-invalid={Boolean(fieldErrors.credential_ref)}
              />
              <p className="text-xs text-muted-foreground">
                The adapter reads this variable from the Mercator server's
                environment at runtime.
              </p>
              {fieldErrors.credential_ref ? (
                <p className="text-xs text-destructive">
                  {fieldErrors.credential_ref}
                </p>
              ) : null}
            </div>
          ) : (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="ac-secret">Secret value</Label>
              <Input
                id="ac-secret"
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder="••••••••"
                autoComplete="new-password"
                className={cn(
                  "font-mono",
                  fieldErrors.secret && "border-destructive",
                )}
                aria-invalid={Boolean(fieldErrors.secret)}
              />
              <p className="text-xs text-muted-foreground">
                Stored encrypted; never returned by the API.
              </p>
              {fieldErrors.secret ? (
                <p className="text-xs text-destructive">
                  {fieldErrors.secret}
                </p>
              ) : null}
            </div>
          )}

          {/* Other (non-field) violations */}
          {otherViolations.length > 0 ? (
            <ul className="flex flex-col gap-1 rounded-md border border-destructive/40 bg-destructive/5 p-2">
              {otherViolations.map((v, i) => (
                <li key={`${v.code}-${i}`} className="text-xs text-destructive">
                  <span className="font-mono">{v.code}</span>
                  {v.message ? ` — ${v.message}` : null}
                </li>
              ))}
            </ul>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => handleOpenChange(false)}
              disabled={submitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {submitting ? "Adding…" : "Add connection"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
