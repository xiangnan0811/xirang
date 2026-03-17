import {
  Bell,
  ClipboardList,
  FileSearch,
  FileText,
  KeyRound,
  LayoutDashboard,
  ListChecks,
  Monitor,
  Server,
  Users,
  type LucideIcon
} from "lucide-react";
import type { UserRecord } from "@/types/domain";

export type NavItem = {
  titleKey: string;
  path: string;
  icon: LucideIcon;
  mobileTab?: boolean;
  adminOnly?: boolean;
};

export const navItems: NavItem[] = [
  {
    titleKey: "nav.overview",
    path: "/app/overview",
    icon: LayoutDashboard,
    mobileTab: true
  },
  {
    titleKey: "nav.nodes",
    path: "/app/nodes",
    icon: Server,
    mobileTab: true
  },
  {
    titleKey: "nav.sshKeys",
    path: "/app/ssh-keys",
    icon: KeyRound,
    mobileTab: false
  },
  {
    titleKey: "nav.policies",
    path: "/app/policies",
    icon: ListChecks,
    mobileTab: true
  },
  {
    titleKey: "nav.tasks",
    path: "/app/tasks",
    icon: ClipboardList,
    mobileTab: true
  },
  {
    titleKey: "nav.logs",
    path: "/app/logs",
    icon: Monitor,
    mobileTab: true
  },
  {
    titleKey: "nav.notifications",
    path: "/app/notifications",
    icon: Bell,
    mobileTab: true
  },
  {
    titleKey: "nav.audit",
    path: "/app/audit",
    icon: FileSearch,
    mobileTab: false
  },
  {
    titleKey: "nav.users",
    path: "/app/users",
    icon: Users,
    mobileTab: false,
    adminOnly: true
  },
  {
    titleKey: "nav.reports",
    path: "/app/reports",
    icon: FileText,
    mobileTab: false
  }
];

export function getVisibleNavItems(role: UserRecord["role"] | null): NavItem[] {
  return navItems.filter((item) => !item.adminOnly || role === "admin");
}
