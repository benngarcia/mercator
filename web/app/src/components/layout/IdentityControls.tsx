// IdentityControls renders the identity mode the server reports. Local
// development has a fixed loopback identity, OIDC sessions can sign out, and
// token-only servers retain the machine-token fallback.

import { LogIn, LogOut } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { signInUrl } from "@/lib/auth";
import { useAuthSession, useLogout } from "@/lib/api/queries";

import { TokenField } from "./TokenField";

export function IdentityControls() {
  const auth = useAuthSession();
  const logout = useLogout();

  // Until the mode is known, render nothing rather than flashing the token
  // field at a signed-in operator. Treat an unreachable /auth/session like
  // token mode so a plain dev server still gets the fallback.
  if (auth.isLoading) {
    return null;
  }
  if (!auth.data || auth.data.mode === "token") {
    return <TokenField />;
  }

  if (auth.data.mode === "oidc" && !auth.data.email) {
    return (
      <Button
        variant="outline"
        size="sm"
        onClick={() => window.location.assign(signInUrl())}
      >
        <LogIn />
        Sign in
      </Button>
    );
  }

  if (auth.data.mode === "local") {
    return (
      <span className="max-w-48 truncate text-xs text-muted-foreground">
        {auth.data.email}
      </span>
    );
  }

  return (
    <div className="flex items-center gap-1">
      <span
        className="max-w-48 truncate text-xs text-muted-foreground"
        title={auth.data.email}
      >
        {auth.data.email}
      </span>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Sign out"
            disabled={logout.isPending}
            onClick={() =>
              logout.mutate(undefined, {
                onSuccess: () => window.location.assign("/"),
                onError: (error) => toast.error(error.message),
              })
            }
          >
            <LogOut />
          </Button>
        </TooltipTrigger>
        <TooltipContent>Sign out</TooltipContent>
      </Tooltip>
    </div>
  );
}
