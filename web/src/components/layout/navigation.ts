import {
  Bell,
  ClipboardList,
  FileSearch,
  FileText,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  ListChecks,
  Monitor,
  Server,
  type LucideIcon
} from "lucide-react";
import type { UserRecord } from "@/types/domain";

export type NavGroup = "core" | "backup" | "monitor";

export type NavItem = {
  titleKey: string;
  path: string;
  icon: LucideIcon;
  group: NavGroup;
  mobileTab?: boolean;
  adminOnly?: boolean;
};

export const navGroups: { key: NavGroup; labelKey: string }[] = [
  { key: "core", labelKey: "nav.group.core" },
  { key: "backup", labelKey: "nav.group.backup" },
  { key: "monitor", labelKey: "nav.group.monitor" },
];

export const navItems: NavItem[] = [
  {
    titleKey: "nav.overview",
    path: "/app/overview",
    icon: LayoutDashboard,
    group: "core",
    mobileTab: true
  },
  {
    titleKey: "nav.nodes",
    path: "/app/nodes",
    icon: Server,
    group: "core",
    mobileTab: true
  },
  {
    titleKey: "nav.sshKeys",
    path: "/app/ssh-keys",
    icon: KeyRound,
    group: "core",
    mobileTab: false
  },
  {
    titleKey: "nav.policies",
    path: "/app/policies",
    icon: ListChecks,
    group: "backup",
    mobileTab: true
  },
  {
    titleKey: "nav.backups",
    path: "/app/backups",
    icon: HardDrive,
    group: "backup",
    mobileTab: false
  },
  {
    titleKey: "nav.tasks",
    path: "/app/tasks",
    icon: ClipboardList,
    group: "backup",
    mobileTab: true
  },
  {
    titleKey: "nav.logs",
    path: "/app/logs",
    icon: Monitor,
    group: "monitor",
    mobileTab: true
  },
  {
    titleKey: "nav.alertCenter",
    path: "/app/notifications",
    icon: Bell,
    group: "monitor",
    mobileTab: true
  },
  {
    titleKey: "nav.audit",
    path: "/app/audit",
    icon: FileSearch,
    group: "monitor",
    mobileTab: false
  },
  {
    titleKey: "nav.reports",
    path: "/app/reports",
    icon: FileText,
    group: "monitor",
    mobileTab: false
  }
];

export function getVisibleNavItems(role: UserRecord["role"] | null): NavItem[] {
  return navItems.filter((item) => !item.adminOnly || role === "admin");
}
