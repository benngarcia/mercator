// AppShell: the console frame. A fixed-width sidebar column and a content
// column split into a sticky Topbar and a scrollable main region. Routes
// render into the main region via <Outlet/>; an explicit `children` override is
// accepted for non-router usages (tests, storybook-style harnesses). The shell
// owns no data — it is pure layout.

import { Outlet } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";

export interface AppShellProps {
  children?: ReactNode;
}

export function AppShell({ children }: AppShellProps) {
  return (
    <div className="grid h-screen grid-cols-[15rem_minmax(0,1fr)] grid-rows-[minmax(0,1fr)] overflow-hidden bg-background text-foreground">
      <Sidebar />
      <div className="flex min-w-0 flex-col">
        <Topbar />
        <main className="min-h-0 flex-1 overflow-auto">
          {children ?? <Outlet />}
        </main>
      </div>
    </div>
  );
}
