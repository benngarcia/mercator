import * as React from "react";
import { ExternalLink } from "lucide-react";
import { toast } from "sonner";

import type {
  AdapterConfigField,
  AdapterManifest,
  CredentialSource,
} from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import {
  useAuthorizeConnection,
  useCreateConnection,
  useDeleteConnection,
} from "@/lib/api/queries";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { ProviderLogo } from "./ProviderLogo";

export interface ProviderSetupDialogProps {
  manifest: AdapterManifest | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Reports a connection whose verify attempt failed (for card status). */
  onVerifyFailed: (connectionId: string) => void;
  /** Reports a connection whose verify concern is resolved (verified or deleted). */
  onVerifyResolved: (connectionId: string) => void;
}

function newConnectionId(adapterType: string): string {
  const suffix = crypto
    .getRandomValues(new Uint8Array(3))
    .reduce((acc, b) => acc + b.toString(16).padStart(2, "0"), "");
  return `conn_${adapterType}_${suffix}`;
}

// The guided flow: fill the form, then one action creates the connection and
// verifies it against the provider. Verify failure keeps the dialog open with
// the adapter's real error text and two ways forward: verify again (transient
// provider trouble) or discard the saved connection and edit the form
// (connection configs are immutable, so editing means recreate under a fresh
// id).
type Phase =
  | { kind: "editing" }
  | { kind: "submitting" }
  | { kind: "verify_failed"; connectionId: string; error: string }
  | { kind: "reverifying"; connectionId: string; error: string };

function SetupSteps({ manifest }: { manifest: AdapterManifest }) {
  return (
    <ol className="flex flex-col gap-3">
      {manifest.setup_steps.map((step, i) => (
        <li key={i} className="flex gap-3">
          <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-[11px] text-muted-foreground">
            {i + 1}
          </span>
          <span className="text-sm leading-relaxed text-foreground">
            {step.text}{" "}
            {step.url ? (
              <a
                href={step.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-0.5 text-primary underline-offset-2 hover:underline"
              >
                {new URL(step.url).hostname.replace(/^www\./, "")}
                <ExternalLink className="size-3" />
              </a>
            ) : null}
          </span>
        </li>
      ))}
    </ol>
  );
}

function ConfigFieldInput({
  field,
  value,
  onChange,
}: {
  field: AdapterConfigField;
  value: string;
  onChange: (value: string) => void;
}) {
  const id = `setup-cfg-${field.name}`;
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id} className="text-xs">
        {field.label}
        {field.required ? null : (
          <span className="font-normal text-muted-foreground"> (optional)</span>
        )}
      </Label>
      {field.type === "bool" ? (
        <Select value={value} onValueChange={onChange}>
          <SelectTrigger id={id} className="h-8 w-full text-xs">
            <SelectValue
              placeholder={`Default: ${field.default || "false"}`}
            />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="true">Yes</SelectItem>
            <SelectItem value="false">No</SelectItem>
          </SelectContent>
        </Select>
      ) : (
        <Input
          id={id}
          type={field.secret ? "password" : field.type === "int" ? "number" : "text"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={field.placeholder ?? field.default}
          autoComplete="off"
          spellCheck={false}
          className="h-8 font-mono text-xs"
        />
      )}
      {field.help ? (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {field.help}
        </p>
      ) : null}
    </div>
  );
}

function SetupForm({
  manifest,
  onDone,
  onVerifyFailed,
  onVerifyResolved,
}: {
  manifest: AdapterManifest;
  onDone: () => void;
  onVerifyFailed: (connectionId: string) => void;
  onVerifyResolved: (connectionId: string) => void;
}) {
  const [connectionId, setConnectionId] = React.useState(() =>
    newConnectionId(manifest.type),
  );
  const [config, setConfig] = React.useState<Record<string, string>>({});
  const [credentialSource, setCredentialSource] =
    React.useState<CredentialSource>("mercator");
  const [secret, setSecret] = React.useState("");
  const [envRef, setEnvRef] = React.useState("");
  const [phase, setPhase] = React.useState<Phase>({ kind: "editing" });

  const createConnection = useCreateConnection();
  const authorizeConnection = useAuthorizeConnection();
  const deleteConnection = useDeleteConnection();

  const showsCredential =
    manifest.credential.required || Boolean(manifest.credential.label);
  const credentialLabel = manifest.credential.label ?? "API key";
  const credentialProvided =
    credentialSource === "mercator" ? Boolean(secret) : Boolean(envRef.trim());

  const missingRequired =
    manifest.config_fields.some(
      (f) => f.required && !(config[f.name] ?? "").trim(),
    ) ||
    (manifest.credential.required && !credentialProvided);

  const verify = (id: string, previousError: string) => {
    setPhase({ kind: "reverifying", connectionId: id, error: previousError });
    authorizeConnection.mutate(id, {
      onSuccess: () => {
        onVerifyResolved(id);
        toast.success(`${manifest.display_name} connected`, {
          description: id,
        });
        onDone();
      },
      onError: (error) => {
        const message =
          error instanceof ApiError ? error.message : "Verification failed";
        onVerifyFailed(id);
        setPhase({ kind: "verify_failed", connectionId: id, error: message });
      },
    });
  };

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (missingRequired) return;
    setPhase({ kind: "submitting" });

    const trimmedConfig: Record<string, string> = {};
    for (const [k, v] of Object.entries(config)) {
      if (v.trim()) trimmedConfig[k] = v.trim();
    }
    createConnection.mutate(
      {
		connection_id: connectionId.trim(),
        adapter_type: manifest.type,
        config: Object.keys(trimmedConfig).length ? trimmedConfig : undefined,
        credential: credentialProvided
          ? credentialSource === "mercator"
            ? { source: "mercator", ref: "" }
            : { source: "env", ref: envRef.trim() }
          : undefined,
        secret:
          credentialProvided && credentialSource === "mercator"
            ? secret
            : undefined,
      },
      {
        onSuccess: (record) => verify(record.id, ""),
        onError: (error) => {
          const message =
            error instanceof ApiError
              ? error.message
              : "Failed to save connection";
          toast.error("Could not save connection", { description: message });
          setPhase({ kind: "editing" });
        },
      },
    );
  };

  const discardAndEdit = (id: string) => {
    deleteConnection.mutate(id, {
      onSuccess: () => {
        onVerifyResolved(id);
        setConnectionId(newConnectionId(manifest.type));
        setPhase({ kind: "editing" });
      },
      onError: (error) => {
        const message =
          error instanceof ApiError ? error.message : "Failed to delete";
        toast.error("Could not discard connection", { description: message });
      },
    });
  };

  const busy = phase.kind === "submitting" || phase.kind === "reverifying";
  const failed = phase.kind === "verify_failed" || phase.kind === "reverifying";

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      {manifest.config_fields.map((field) => (
        <ConfigFieldInput
          key={field.name}
          field={field}
          value={config[field.name] ?? ""}
          onChange={(value) =>
            setConfig((prev) => ({ ...prev, [field.name]: value }))
          }
        />
      ))}

      {showsCredential ? (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="setup-secret" className="text-xs">
            {credentialLabel}
            {manifest.credential.required ? null : " (optional)"}
          </Label>
          {credentialSource === "mercator" ? (
            <Input
              id="setup-secret"
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder="••••••••••••"
              autoComplete="new-password"
              className="h-8 font-mono text-xs"
              data-testid="setup-secret"
            />
          ) : (
            <Input
              id="setup-secret"
              value={envRef}
              onChange={(e) => setEnvRef(e.target.value)}
              placeholder="PROVIDER_API_KEY"
              autoComplete="off"
              spellCheck={false}
              className="h-8 font-mono text-xs"
              data-testid="setup-env-ref"
            />
          )}
          {manifest.credential.format ? (
            <p className="text-xs leading-relaxed text-muted-foreground">
              {manifest.credential.format}
            </p>
          ) : null}
          <button
            type="button"
            className="w-fit text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            onClick={() =>
              setCredentialSource((s) => (s === "mercator" ? "env" : "mercator"))
            }
          >
            {credentialSource === "mercator"
              ? "Read from a server environment variable instead"
              : "Paste the key directly instead (stored encrypted)"}
          </button>
          {credentialSource === "mercator" ? (
            <p className="text-xs text-muted-foreground">
              Stored encrypted; shown as “configured” after save, never the
              value. Editing later means re-entering it.
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">
              The named variable is read from the Mercator server's environment
              at use time.
            </p>
          )}
        </div>
      ) : null}

      <div className="flex flex-col gap-1.5">
        <Label htmlFor="setup-connection-id" className="text-xs">
          Connection id
        </Label>
        <Input
          id="setup-connection-id"
          value={connectionId}
          onChange={(e) => setConnectionId(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          className="h-8 font-mono text-xs"
          disabled={failed || busy}
        />
      </div>

      {failed ? (
        <div
          className="flex flex-col gap-2 rounded-md border border-phase-failed/40 bg-phase-failed/5 p-3"
          data-testid="verify-error"
        >
          <p className="text-xs font-medium text-phase-failed">
            Verification failed
          </p>
          <p className="max-h-40 overflow-y-auto break-all font-mono text-xs leading-relaxed text-foreground">
            {phase.error}
          </p>
          <p className="text-xs text-muted-foreground">
            The connection is saved but not verified. Fix the account or key on
            the provider side and verify again, or discard it to edit the form
            (saved connections are immutable).
          </p>
        </div>
      ) : null}

      <DialogFooter className="gap-2 sm:justify-end">
        {failed ? (
          <>
            <Button
              type="button"
              variant="ghost"
              disabled={busy || deleteConnection.isPending}
              onClick={() => discardAndEdit(phase.connectionId)}
            >
              {deleteConnection.isPending ? "Discarding…" : "Discard & edit"}
            </Button>
            <Button
              type="button"
              disabled={busy || deleteConnection.isPending}
              onClick={() => verify(phase.connectionId, phase.error)}
              data-testid="verify-again"
            >
              {phase.kind === "reverifying" ? "Verifying…" : "Verify again"}
            </Button>
          </>
        ) : (
          <Button
            type="submit"
            disabled={busy || missingRequired}
            data-testid="save-and-verify"
          >
            {phase.kind === "submitting" ? "Saving…" : "Save & verify"}
          </Button>
        )}
      </DialogFooter>
    </form>
  );
}

/**
 * ProviderSetupDialog is the guided connect flow for one provider: the
 * manifest's numbered setup steps (with working links) beside the actual form.
 * Secrets are write-only; verify surfaces the adapter's real error text.
 */
export function ProviderSetupDialog({
  manifest,
  open,
  onOpenChange,
  onVerifyFailed,
  onVerifyResolved,
}: ProviderSetupDialogProps) {
  if (!manifest) return null;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto p-0 sm:max-w-3xl">
        <div className="flex flex-col gap-0 p-6 pb-4">
          <DialogHeader>
            <div className="flex items-center gap-3">
              <ProviderLogo slug={manifest.logo} name={manifest.display_name} />
              <div>
                <DialogTitle>Connect {manifest.display_name}</DialogTitle>
                <DialogDescription className="mt-0.5">
                  {manifest.description}
                </DialogDescription>
              </div>
            </div>
          </DialogHeader>
        </div>
        <div
          className={cn(
            "grid gap-6 px-6 pb-6",
            manifest.setup_steps.length > 0 ? "md:grid-cols-2" : "",
          )}
        >
          {manifest.setup_steps.length > 0 ? (
            <div className="flex flex-col gap-3 md:border-r md:border-border md:pr-6">
              <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                Setup
              </p>
              <SetupSteps manifest={manifest} />
            </div>
          ) : null}
          {/* key resets the form whenever the dialog targets a new provider */}
          <SetupForm
            key={manifest.type}
            manifest={manifest}
            onDone={() => onOpenChange(false)}
            onVerifyFailed={onVerifyFailed}
            onVerifyResolved={onVerifyResolved}
          />
        </div>
      </DialogContent>
    </Dialog>
  );
}
