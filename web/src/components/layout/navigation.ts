import {
  Bell,
  ClipboardList,
  FileSearch,
  KeyRound,
  LayoutDashboard,
  ListChecks,
  Monitor,
  Server,
  ShieldAlert,
  type LucideIcon
} from "lucide-react";

export type NavItem = {
  title: string;
  path: string;
  icon: LucideIcon;
  mobileTab?: boolean;
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
    mobileTab: false
  },
  {
    title: "通知中心",
    path: "/app/alert-center",
    icon: ShieldAlert,
    mobileTab: true
  },
  {
    title: "审计",
    path: "/app/audit",
    icon: FileSearch,
    mobileTab: false
  }
];
