// Sidebar: primary console navigation. Active-route highlighting uses TanStack
// Router's <Link> active state (activeProps + activeOptions) rather than a
// hand-rolled pathname compare, so it stays correct under nested routes (e.g.
// /runs/:runId keeps "Runs" active while /runs/new is its own entry). The
// Mercator wordmark sits at the top; nav items carry a lucide glyph + label.

import {
  Boxes,
  GitBranch,
  Compass,
  PlugZap,
  Plus,
  Radio,
  ScrollText,
  Tags,
  type LucideIcon,
} from "lucide-react";
import { Link } from "@tanstack/react-router";

import { cn } from "@/lib/utils";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  // exact pins active state to this path only (used for index/sibling routes
  // like /runs vs /runs/new that share a prefix).
  exact?: boolean;
}

const NAV: NavItem[] = [
  { to: "/runs", label: "Runs", icon: ScrollText, exact: true },
  { to: "/runs/new", label: "Create Run", icon: Plus },
  { to: "/preview", label: "Preview", icon: Compass },
  { to: "/workloads", label: "Workloads", icon: Boxes },
  { to: "/offers", label: "Offers", icon: Tags },
  { to: "/connections", label: "Connections", icon: PlugZap },
  { to: "/sinks", label: "Sinks", icon: Radio },
];

function NavLink({ item }: { item: NavItem }) {
  const Icon = item.icon;
  return (
    <Link
      // The typed route tree is registered by the routes/ modules (a separate
      // concern); cast the literal path so this layout component stays
      // decoupled from the generated route union without resorting to `any`.
      to={item.to as never}
      activeOptions={{ exact: item.exact ?? false }}
      className={cn(
        "group flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm font-medium text-muted-foreground transition-colors",
        "hover:bg-foreground/[0.04] hover:text-foreground",
      )}
      activeProps={{
        // HIG selection: a soft accent-tinted pill with accent text + icon.
        className: cn("bg-accent-soft text-primary hover:bg-accent-soft"),
      }}
    >
      <Icon className="size-[1.05rem] shrink-0" />
      <span className="truncate">{item.label}</span>
    </Link>
  );
}

export function Sidebar() {
  return (
    <aside className="flex h-full flex-col gap-4 border-r bg-card/30 px-3 py-4">
      <div className="flex items-center gap-2 px-1.5">
        <GitBranch className="size-5 text-primary" aria-hidden />
        <div className="flex flex-col leading-none">
          <span className="text-sm font-semibold tracking-tight">Mercator</span>
          <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
            Operator Console
          </span>
        </div>
      </div>

      <nav className="flex flex-col gap-0.5" aria-label="Primary">
        {NAV.map((item) => (
          <NavLink key={item.to} item={item} />
        ))}
      </nav>
    </aside>
  );
}
