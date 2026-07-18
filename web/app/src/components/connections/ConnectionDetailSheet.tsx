import * as React from "react";
import { KeyRound, Plus } from "lucide-react";
import { toast } from "sonner";

import type { AdapterManifest, ConnectionRecord } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import {
  useAuthorizeConnection,
  useDeleteConnection,
} from "@/lib/api/queries";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { CopyButton } from "@/components/common";
import { cn } from "@/lib/utils";
import { ProviderLogo } from "./ProviderLogo";

export interface ConnectionDetailSheetProps {
  manifest: AdapterManifest | null;
  connections: ConnectionRecord[];
  verifyFailedIds: ReadonlySet<string>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAddAnother: () => void;
  onVerifyFailed: (connectionId: string) => void;
  onVerifyResolved: (connectionId: string) => void;
}

function StatusBadge({
  connection,
  verifyFailed,
}: {
  connection: ConnectionRecord;
  verifyFailed: boolean;
}) {
  if (connection.authorized) {
    return (
      <Badge
        variant="outline"
        className="border-phase-succeeded/30 bg-phase-succeeded/10 text-phase-succeeded"
      >
        verified
      </Badge>
    );
  }
  if (verifyFailed) {
    return (
      <Badge
        variant="outline"
        className="border-phase-failed/30 bg-phase-failed/10 text-phase-failed"
      >
        verify failed
      </Badge>
    );
  }
  return (
    <Badge
      variant="outline"
      className="border-border bg-muted/40 text-muted-foreground"
    >
      not verified
    </Badge>
  );
}

// ConfigList renders the connection's config with manifest-declared secret
// fields redacted. Values never editable: connection configs are immutable.
function ConfigList({
  connection,
  manifest,
}: {
  connection: ConnectionRecord;
  manifest: AdapterManifest;
}) {
  const entries = Object.entries(connection.config ?? {});
  if (entries.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        No config — the adapter's defaults apply.
      </p>
    );
  }
  const secretFields = new Set(
    manifest.config_fields.filter((f) => f.secret).map((f) => f.name),
  );
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
      {entries.map(([key, value]) => (
        <React.Fragment key={key}>
          <dt className="font-mono text-xs text-muted-foreground">{key}</dt>
          <dd className="break-all font-mono text-xs text-foreground">
            {secretFields.has(key) ? "••••••••" : value}
          </dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

function CredentialLine({ connection }: { connection: ConnectionRecord }) {
  const cred = connection.credential;
  if (!cred || !cred.source) {
    return (
      <p className="text-xs text-muted-foreground">No credential attached.</p>
    );
  }
  return (
    <p className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <KeyRound className="size-3.5" />
      {cred.source === "mercator" ? (
        <>Credential configured — stored encrypted, never shown.</>
      ) : (
        <>
          Read from server env var{" "}
          <span className="font-mono text-foreground">{cred.ref}</span>
        </>
      )}
    </p>
  );
}

// ConnectionRow is one existing connection: identity, status, redacted
// config, audit principals, and the re-verify / delete actions. Delete uses
// an inline two-step confirmation.
function ConnectionRow({
  connection,
  manifest,
  verifyFailed,
  onVerifyFailed,
  onVerifyResolved,
}: {
  connection: ConnectionRecord;
  manifest: AdapterManifest;
  verifyFailed: boolean;
  onVerifyFailed: (id: string) => void;
  onVerifyResolved: (id: string) => void;
}) {
  const [confirmingDelete, setConfirmingDelete] = React.useState(false);
  const [verifyError, setVerifyError] = React.useState<string | null>(null);
  const authorize = useAuthorizeConnection();
  const remove = useDeleteConnection();

  const reverify = () => {
    authorize.mutate(connection.id, {
      onSuccess: () => {
        setVerifyError(null);
        onVerifyResolved(connection.id);
        toast.success("Connection verified", { description: connection.id });
      },
      onError: (error) => {
        const message =
          error instanceof ApiError ? error.message : "Verification failed";
        setVerifyError(message);
        onVerifyFailed(connection.id);
      },
    });
  };

  const destroy = () => {
    remove.mutate(connection.id, {
      onSuccess: () => {
        onVerifyResolved(connection.id);
        toast.success("Connection deleted", { description: connection.id });
      },
      onError: (error) => {
        const message =
          error instanceof ApiError ? error.message : "Delete failed";
        toast.error("Could not delete connection", { description: message });
        setConfirmingDelete(false);
      },
    });
  };

  return (
    <div
      className="flex flex-col gap-3 rounded-lg border border-border p-4"
      data-testid={`connection-row-${connection.id}`}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-1">
          <span className="truncate font-mono text-xs text-foreground">
            {connection.id}
          </span>
          <CopyButton value={connection.id} label="Copy connection id" />
        </div>
        <StatusBadge connection={connection} verifyFailed={verifyFailed} />
      </div>

      <ConfigList connection={connection} manifest={manifest} />
      <CredentialLine connection={connection} />

      {connection.created_by || connection.authorized_by ? (
        <p className="text-xs text-muted-foreground">
          {connection.created_by ? `Created by ${connection.created_by}` : null}
          {connection.created_by && connection.authorized_by ? " · " : null}
          {connection.authorized_by
            ? `Verified by ${connection.authorized_by}`
            : null}
        </p>
      ) : null}

      {verifyError ? (
        <div className="rounded-md border border-phase-failed/40 bg-phase-failed/5 p-2.5">
          <p className="max-h-40 overflow-y-auto break-all font-mono text-xs leading-relaxed text-foreground">
            {verifyError}
          </p>
        </div>
      ) : null}

      <div className="flex items-center justify-between gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-7 px-2 text-xs"
          onClick={reverify}
          disabled={authorize.isPending || remove.isPending}
          data-testid="reverify"
        >
          {authorize.isPending
            ? "Verifying…"
            : connection.authorized
              ? "Re-verify"
              : "Verify"}
        </Button>
        {confirmingDelete ? (
          <div className="flex items-center gap-1.5">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => setConfirmingDelete(false)}
              disabled={remove.isPending}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={destroy}
              disabled={remove.isPending}
              data-testid="confirm-delete"
            >
              {remove.isPending ? "Deleting…" : "Confirm delete"}
            </Button>
          </div>
        ) : (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className={cn(
              "h-7 px-2 text-xs text-muted-foreground hover:text-phase-failed",
            )}
            onClick={() => setConfirmingDelete(true)}
            disabled={remove.isPending}
            data-testid="delete"
          >
            Delete
          </Button>
        )}
      </div>
    </div>
  );
}

/**
 * ConnectionDetailSheet manages a provider's existing connections: view
 * config (secrets redacted), re-verify against the provider, and delete with
 * an inline confirmation. Deleted ids cannot be reused; adding again mints a
 * fresh id.
 */
export function ConnectionDetailSheet({
  manifest,
  connections,
  verifyFailedIds,
  open,
  onOpenChange,
  onAddAnother,
  onVerifyFailed,
  onVerifyResolved,
}: ConnectionDetailSheetProps) {
  if (!manifest) return null;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <div className="flex items-center gap-3">
            <ProviderLogo slug={manifest.logo} name={manifest.display_name} />
            <div>
              <SheetTitle>{manifest.display_name}</SheetTitle>
              <SheetDescription className="mt-0.5">
                {connections.length === 1
                  ? "1 connection in this workspace."
                  : `${connections.length} connections in this workspace.`}
              </SheetDescription>
            </div>
          </div>
        </SheetHeader>
        <div className="flex flex-col gap-3 px-4 pb-4">
          {connections.map((connection) => (
            <ConnectionRow
              key={connection.id}
              connection={connection}
              manifest={manifest}
              verifyFailed={verifyFailedIds.has(connection.id)}
              onVerifyFailed={onVerifyFailed}
              onVerifyResolved={onVerifyResolved}
            />
          ))}
          <Separator className="my-1" />
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={onAddAnother}
          >
            <Plus className="size-4" />
            Add another {manifest.display_name} connection
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}
