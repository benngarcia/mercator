import * as React from "react";
import { Braces, CheckCircle2, XCircle, Plug, ShieldCheck } from "lucide-react";
import { toast } from "sonner";

import type { ConnectionRecord } from "@/lib/api/types";
import { ApiError } from "@/lib/api/client";
import { useAuthorizeConnection } from "@/lib/api/queries";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  CopyButton,
  DataTable,
  EmptyState,
  JsonViewer,
  type Column,
} from "@/components/common";

export interface ConnectionsTableProps {
  connections: ConnectionRecord[];
  isLoading?: boolean;
  className?: string;
}

/**
 * AuthorizeButton calls /authorize for a single connection. It is shown only
 * when the connection is not yet authorized. Pending state disables the button;
 * errors surface via a toast (matching the app-wide feedback pattern).
 */
function AuthorizeButton({ connectionId }: { connectionId: string }) {
  const authorize = useAuthorizeConnection();

  const handleClick = (event: React.MouseEvent) => {
    event.stopPropagation();
    authorize.mutate(connectionId, {
      onSuccess: () => {
        toast.success("Connection authorized", { description: connectionId });
      },
      onError: (error) => {
        const message =
          error instanceof ApiError
            ? error.message
            : "Failed to authorize connection";
        toast.error("Could not authorize", { description: message });
      },
    });
  };

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-7 gap-1.5 px-2 text-xs"
          onClick={handleClick}
          disabled={authorize.isPending}
          aria-label="Authorize connection"
        >
          <ShieldCheck className="size-3.5" />
          {authorize.isPending ? "Authorizing…" : "Authorize"}
        </Button>
      </TooltipTrigger>
      <TooltipContent>Verify and authorize this connection</TooltipContent>
    </Tooltip>
  );
}

/**
 * AuthorizedBadge renders the connection authorization state with the phase
 * color system — emerald for authorized, zinc for not. The connection record
 * shape is intentionally loose; this only leans on the `authorized` flag.
 */
function AuthorizedBadge({ authorized }: { authorized: boolean }) {
  return authorized ? (
    <Badge
      variant="outline"
      className="border-phase-succeeded/30 bg-phase-succeeded/10 text-phase-succeeded"
    >
      <CheckCircle2 />
      authorized
    </Badge>
  ) : (
    <Badge
      variant="outline"
      className="border-phase-cancelled/30 bg-phase-cancelled/10 text-phase-cancelled"
    >
      <XCircle />
      unauthorized
    </Badge>
  );
}

/**
 * ConnectionsTable lists adapter connections in the dense operator table:
 * a monospace id (with copy), adapter type, and an authorization badge. The
 * underlying record is a loose object, so every row also exposes a Raw JSON
 * affordance that opens the full record (including any authorization_schema or
 * adapter-specific extras the API attaches) in a side sheet.
 */
export function ConnectionsTable({
  connections,
  isLoading,
  className,
}: ConnectionsTableProps) {
  const [raw, setRaw] = React.useState<ConnectionRecord | null>(null);

  const columns = React.useMemo<Column<ConnectionRecord>[]>(
    () => [
      {
        id: "id",
        header: "Connection",
        sortable: true,
        sortValue: (row) => row.id,
        cell: (row) => (
          <div className="flex items-center gap-1">
            <span className="font-mono text-xs text-foreground">{row.id}</span>
            <CopyButton value={row.id} label="Copy connection id" />
          </div>
        ),
      },
      {
        id: "adapter_type",
        header: "Adapter",
        sortable: true,
        sortValue: (row) => row.adapter_type,
        cell: (row) => (
          <span className="inline-flex items-center gap-1.5 text-sm">
            <Plug className="size-3.5 text-muted-foreground" />
            <span className="font-mono text-xs">{row.adapter_type}</span>
          </span>
        ),
      },
      {
        id: "authorized",
        header: "Status",
        sortable: true,
        sortValue: (row) => (row.authorized ? 1 : 0),
        cell: (row) => <AuthorizedBadge authorized={row.authorized} />,
      },
      {
        id: "actions",
        header: "",
        align: "right",
        className: "w-28",
        cell: (row) =>
          row.authorized ? null : (
            <AuthorizeButton connectionId={row.id} />
          ),
      },
      {
        id: "raw",
        header: "",
        align: "right",
        className: "w-12",
        cell: (row) => (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                aria-label="View raw JSON"
                className="size-6 rounded p-0 text-muted-foreground hover:text-foreground [&_svg]:size-3.5"
                onClick={(event) => {
                  event.stopPropagation();
                  setRaw(row);
                }}
              >
                <Braces />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Raw JSON</TooltipContent>
          </Tooltip>
        ),
      },
    ],
    [],
  );

  return (
    <>
      <DataTable
        className={cn(className)}
        columns={columns}
        data={connections}
        rowKey={(row) => row.id}
        isLoading={isLoading}
        emptyState={
          <EmptyState
            icon={Plug}
            title="No connections"
            description="No adapter connections are registered for this workspace yet."
          />
        }
      />
      <Sheet open={raw !== null} onOpenChange={(open) => !open && setRaw(null)}>
        <SheetContent className="w-full gap-4 sm:max-w-lg">
          <SheetHeader>
            <SheetTitle className="font-mono text-sm">{raw?.id}</SheetTitle>
            <SheetDescription>
              Full connection record as returned by the API.
            </SheetDescription>
          </SheetHeader>
          {raw ? (
            <div className="px-4 pb-4">
              <JsonViewer value={raw} maxHeight="70vh" />
            </div>
          ) : null}
        </SheetContent>
      </Sheet>
    </>
  );
}
