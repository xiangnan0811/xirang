import {
  Bell,
  ClipboardList,
  FileSearch,
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
  title: string;
  path: string;
  icon: LucideIcon;
  mobileTab?: boolean;
  adminOnly?: boolean;
};

export const navItems: NavItem[] = [
  {
    title: "概览",
    path: "/app/overview",
    icon: LayoutDashboard,
    mobileTab: true
  },
  {
    title: "节点",
    path: "/app/nodes",
    icon: Server,
    mobileTab: true
  },
  {
    title: "SSH Key",
    path: "/app/ssh-keys",
    icon: KeyRound,
    mobileTab: false
  },
  {
    title: "策略",
    path: "/app/policies",
    icon: ListChecks,
    mobileTab: true
  },
  {
    title: "任务",
    path: "/app/tasks",
    icon: ClipboardList,
    mobileTab: true
  },
  {
    title: "实时日志",
    path: "/app/logs",
    icon: Monitor,
    mobileTab: true
  },
  {
    title: "通知",
    path: "/app/notifications",
    icon: Bell,
    mobileTab: true
  },
  {
    title: "审计",
    path: "/app/audit",
    icon: FileSearch,
    mobileTab: false
  },
  {
    title: "用户",
    path: "/app/users",
    icon: Users,
    mobileTab: false,
    adminOnly: true
  }
];

export function getVisibleNavItems(role: UserRecord["role"] | null): NavItem[] {
  return navItems.filter((item) => !item.adminOnly || role === "admin");
}
