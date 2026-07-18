// IdentityControls picks the human-auth surface for the topbar from the
// server's /auth/session answer:
//   - OIDC configured: the signed-in email and a sign-out action (humans never
//     paste tokens). An expired session mid-flight shows a sign-in action.
//   - OIDC not configured (dev / token-only): the TokenField fallback, exactly
//     the pre-OIDC console behavior.

import { LogIn, LogOut } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { signInUrl, signOut } from "@/lib/auth";
import { useAuthSession } from "@/lib/api/queries";

import { TokenField } from "./TokenField";

export function IdentityControls() {
  const auth = useAuthSession();

  // Until the mode is known, render nothing rather than flashing the token
  // field at a signed-in operator. Treat an unreachable /auth/session like
  // token mode so a plain dev server still gets the fallback.
  if (auth.isLoading) {
    return null;
  }
  if (!auth.data?.enabled) {
    return <TokenField />;
  }

  if (!auth.data.email) {
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
            onClick={() => void signOut()}
          >
            <LogOut />
          </Button>
        </TooltipTrigger>
        <TooltipContent>Sign out</TooltipContent>
      </Tooltip>
    </div>
  );
}
