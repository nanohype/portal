import {
  Boxes,
  Workflow,
  Activity,
  Gauge,
  Building2,
  Server,
  Layers,
  LayoutTemplate,
  LayoutGrid,
  UsersRound,
  User,
  ScrollText,
  Settings,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

export interface NavItem {
  label: string;
  href: string;
  icon: LucideIcon;
  adminOnly?: boolean;
  // active-state matcher against the current pathname
  match: (path: string) => boolean;
}

export interface NavCategory {
  label: string;
  items: NavItem[];
}

// Single source of truth — consumed by both the sidebar and the command palette
// so navigation never drifts between them.
export const navCategories: NavCategory[] = [
  {
    label: "Delivery",
    items: [
      { label: "Workspaces", href: "/", icon: Boxes, match: (p) => p === "/" || p.startsWith("/workspaces") },
      { label: "Pipelines", href: "/pipelines", icon: Workflow, match: (p) => p.startsWith("/pipelines") },
    ],
  },
  {
    label: "Cloud",
    items: [
      { label: "Fleet", href: "/fleet", icon: Gauge, adminOnly: true, match: (p) => p.startsWith("/fleet") },
      { label: "Operations", href: "/ops", icon: Activity, adminOnly: true, match: (p) => p.startsWith("/ops") },
      { label: "Accounts", href: "/accounts", icon: Building2, adminOnly: true, match: (p) => p.startsWith("/accounts") },
      { label: "Clusters", href: "/clusters", icon: Server, adminOnly: true, match: (p) => p.startsWith("/clusters") },
    ],
  },
  {
    label: "Platform",
    items: [
      { label: "Tenants", href: "/tenants", icon: Layers, match: (p) => p.startsWith("/tenants") },
      { label: "Templates", href: "/templates", icon: LayoutTemplate, adminOnly: true, match: (p) => p.startsWith("/templates") },
      { label: "Catalog", href: "/catalog", icon: LayoutGrid, match: (p) => p === "/catalog" },
    ],
  },
  {
    label: "Organization",
    items: [
      { label: "Teams", href: "/teams", icon: UsersRound, match: (p) => p === "/teams" },
      { label: "Users", href: "/users", icon: User, adminOnly: true, match: (p) => p === "/users" },
      { label: "Audit Logs", href: "/audit-logs", icon: ScrollText, adminOnly: true, match: (p) => p === "/audit-logs" },
      { label: "Settings", href: "/settings", icon: Settings, adminOnly: true, match: (p) => p === "/settings" },
    ],
  },
];
