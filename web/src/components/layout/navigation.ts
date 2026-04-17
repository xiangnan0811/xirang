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
  Settings,
  type LucideIcon
} from "lucide-react";
import type { UserRecord } from "@/types/domain";

export type NavGroup = "operate" | "automate" | "observe" | "pinned";

export type NavItem = {
  titleKey: string;
  path: string;
  icon: LucideIcon;
  group: NavGroup;
  mobileTab?: boolean;
  adminOnly?: boolean;
};

export const navGroups: { key: Exclude<NavGroup, "pinned">; labelKey: string }[] = [
  { key: "operate", labelKey: "nav.group.operate" },
  { key: "automate", labelKey: "nav.group.automate" },
  { key: "observe", labelKey: "nav.group.observe" },
];

export const navItems: NavItem[] = [
  {
    titleKey: "nav.overview",
    path: "/app/overview",
    icon: LayoutDashboard,
    group: "operate",
    mobileTab: true
  },
  {
    titleKey: "nav.nodes",
    path: "/app/nodes",
    icon: Server,
    group: "operate",
    mobileTab: true
  },
  {
    titleKey: "nav.sshKeys",
    path: "/app/ssh-keys",
    icon: KeyRound,
    group: "operate",
    mobileTab: false
  },
  {
    titleKey: "nav.policies",
    path: "/app/policies",
    icon: ListChecks,
    group: "automate",
    mobileTab: false
  },
  {
    titleKey: "nav.backups",
    path: "/app/backups",
    icon: HardDrive,
    group: "automate",
    mobileTab: false
  },
  {
    titleKey: "nav.tasks",
    path: "/app/tasks",
    icon: ClipboardList,
    group: "automate",
    mobileTab: true
  },
  {
    titleKey: "nav.logs",
    path: "/app/logs",
    icon: Monitor,
    group: "observe",
    mobileTab: true
  },
  {
    titleKey: "nav.alertCenter",
    path: "/app/notifications",
    icon: Bell,
    group: "observe",
    mobileTab: false
  },
  {
    titleKey: "nav.audit",
    path: "/app/audit",
    icon: FileSearch,
    group: "observe",
    mobileTab: false
  },
  {
    titleKey: "nav.reports",
    path: "/app/reports",
    icon: FileText,
    group: "observe",
    mobileTab: false
  },
  {
    titleKey: "nav.settings",
    path: "/app/settings",
    icon: Settings,
    group: "pinned",
    mobileTab: false
  },
];

export function getVisibleNavItems(role: UserRecord["role"] | null): NavItem[] {
  return navItems.filter((item) => !item.adminOnly || role === "admin");
}
