import { createRootRoute, Link } from "@tanstack/react-router";

import { AppShell } from "@/components/layout";
import { Button } from "@/components/ui/button";
import { useAuthSession } from "@/lib/api/queries";

export const rootRoute = createRootRoute({
  component: RootComponent,
  notFoundComponent: NotFoundPage,
});

function NotFoundPage() {
  return (
    <div className="flex min-h-full items-center justify-center p-6">
      <div className="flex max-w-md flex-col items-center gap-3 text-center">
        <h1 className="text-xl font-semibold tracking-tight">Page not found</h1>
        <p className="text-sm text-muted-foreground">
          This console destination does not exist.
        </p>
        <Button asChild size="sm">
          <Link to="/canvas">Return to deployment</Link>
        </Button>
      </div>
    </div>
  );
}

function RootComponent() {
  const auth = useAuthSession();
  if (auth.data === undefined && !auth.isError) {
    return <div className="h-screen bg-background" />;
  }
  return <AppShell />;
}
