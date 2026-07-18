import { AlertTriangle, KeyRound, RotateCw, SearchX } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { ApiError } from "@/lib/api/client";
import { useAuthSession } from "@/lib/api/queries";
import { signInUrl } from "@/lib/auth";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { ServiceDisabled } from "./ServiceDisabled";
import { ViolationDetails } from "./ViolationDetails";

export interface ErrorStateProps {
  error: ApiError | Error;
  onRetry?: () => void;
  /** Friendlier feature label used when the error is a 501 service-disabled. */
  feature?: string;
  className?: string;
}

interface Presentation {
  icon: LucideIcon;
  title: string;
  message: string;
}

function present(
  error: ErrorStateProps["error"],
  oidcEnabled: boolean,
): Presentation {
  if (error instanceof ApiError) {
    if (error.unauthorized) {
      if (oidcEnabled) {
        return {
          icon: KeyRound,
          title: "Session expired",
          message: "Sign in again to keep using the console.",
        };
      }
      return {
        icon: KeyRound,
        title: "Authentication required",
        message:
          "Set a valid bearer token in the topbar to access this workspace.",
      };
    }
    if (error.notFound) {
      return {
        icon: SearchX,
        title: "Not found",
        message: error.message || "The requested resource does not exist.",
      };
    }
    return {
      icon: AlertTriangle,
      title: error.code || `Request failed (${error.status})`,
      message: error.message || "The request could not be completed.",
    };
  }
  return {
    icon: AlertTriangle,
    title: "Something went wrong",
    message: error.message || "An unexpected error occurred.",
  };
}

/**
 * ErrorState renders a query error using the { code, message, details }
 * envelope. 501 service-disabled errors degrade to <ServiceDisabled>; 401
 * surfaces a friendly "set a token" prompt; Violation details (if present)
 * render in a compact list. An optional retry re-runs the failing query.
 */
export function ErrorState({
  error,
  onRetry,
  feature,
  className,
}: ErrorStateProps) {
  const auth = useAuthSession();
  if (error instanceof ApiError && error.serviceDisabled) {
    return (
      <ServiceDisabled
        feature={feature ?? "This service"}
        className={className}
      />
    );
  }

  const oidcEnabled = Boolean(auth.data?.enabled);
  const sessionExpired =
    error instanceof ApiError && error.unauthorized && oidcEnabled;
  const { icon: Icon, title, message } = present(error, oidcEnabled);
  const details = error instanceof ApiError ? error.details : undefined;

  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-center gap-4 rounded-lg border border-destructive/30 bg-destructive/5 px-6 py-12 text-center",
        className,
      )}
    >
      <div className="flex size-12 items-center justify-center rounded-full bg-destructive/10 text-destructive [&_svg]:size-5">
        <Icon />
      </div>
      <div className="flex flex-col gap-1">
        <p className="font-mono text-sm font-medium text-foreground">{title}</p>
        <p className="mx-auto max-w-md text-sm text-muted-foreground">
          {message}
        </p>
      </div>
      {details && details.length > 0 ? (
        <ViolationDetails violations={details} className="w-full max-w-md" />
      ) : null}
      {sessionExpired ? (
        <Button
          variant="outline"
          size="sm"
          onClick={() => window.location.assign(signInUrl())}
        >
          <KeyRound />
          Sign in
        </Button>
      ) : onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RotateCw />
          Retry
        </Button>
      ) : null}
    </div>
  );
}
