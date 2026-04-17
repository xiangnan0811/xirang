import * as React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { Command } from "cmdk";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { useCommandPalette } from "@/context/command-palette-context";
import { useNodesContext } from "@/context/nodes-context";
import { useTasksContext } from "@/context/tasks-context";

const routes = [
  { key: "nav.overview", path: "/app/overview" },
  { key: "nav.nodes", path: "/app/nodes" },
  { key: "nav.tasks", path: "/app/tasks" },
  { key: "nav.logs", path: "/app/logs" },
  { key: "nav.backups", path: "/app/backups" },
  { key: "nav.policies", path: "/app/policies" },
  { key: "nav.sshKeys", path: "/app/ssh-keys" },
  { key: "nav.alertCenter", path: "/app/notifications" },
  { key: "nav.audit", path: "/app/audit" },
  { key: "nav.reports", path: "/app/reports" },
  { key: "nav.settings", path: "/app/settings" },
] as const;

export function CommandPalette() {
  const { t } = useTranslation();
  const { open, setOpen } = useCommandPalette();
  const [query, setQuery] = React.useState("");
  const navigate = useNavigate();
  const { nodes } = useNodesContext();
  const { tasks } = useTasksContext();

  // Reset query when closed
  React.useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  const close = React.useCallback(() => setOpen(false), [setOpen]);

  const goTo = React.useCallback(
    (path: string) => {
      navigate(path);
      close();
    },
    [navigate, close],
  );

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent size="md" className="p-0 overflow-hidden">
        <Command
          label={t("search.placeholder")}
          className="[&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.06em] [&_[cmdk-group-heading]]:text-muted-foreground [&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-item]]:flex [&_[cmdk-item]]:items-center [&_[cmdk-item]]:gap-2 [&_[cmdk-item]]:rounded-md [&_[cmdk-item]]:px-3 [&_[cmdk-item]]:py-2 [&_[cmdk-item]]:text-sm [&_[cmdk-item]]:cursor-pointer [&_[cmdk-item]:hover]:bg-accent [&_[cmdk-item][data-selected=true]]:bg-accent [&_[cmdk-empty]]:py-6 [&_[cmdk-empty]]:text-center [&_[cmdk-empty]]:text-sm [&_[cmdk-empty]]:text-muted-foreground"
        >
          <div className="flex items-center gap-3 border-b border-border px-4 py-3">
            <Command.Input
              autoFocus
              value={query}
              onValueChange={setQuery}
              placeholder={t("search.placeholder")}
              className="flex-1 h-8 border-0 bg-transparent text-sm outline-none placeholder:text-muted-foreground focus:ring-0"
            />
            <kbd className="rounded border border-border bg-background px-1.5 py-[2px] font-mono text-[10px] text-muted-foreground">
              {t("search.kbd")}
            </kbd>
          </div>

          <Command.List className="max-h-[400px] overflow-y-auto px-2 py-2">
            <Command.Empty>{t("search.emptyResults")}</Command.Empty>

            {nodes.length > 0 && (
              <Command.Group heading={t("nav.nodes")}>
                {nodes.slice(0, 5).map((node) => (
                  <Command.Item
                    key={node.id}
                    value={`node-${node.id}-${node.name}-${node.ip}`}
                    onSelect={() =>
                      goTo(`/app/nodes?keyword=${encodeURIComponent(node.name)}`)
                    }
                  >
                    <span className="flex-1 font-medium">{node.name}</span>
                    <span className="text-xs text-muted-foreground">{node.ip}</span>
                  </Command.Item>
                ))}
              </Command.Group>
            )}

            {tasks.length > 0 && (
              <Command.Group heading={t("nav.tasks")}>
                {tasks.slice(0, 5).map((task) => (
                  <Command.Item
                    key={task.id}
                    value={`task-${task.id}-${task.name}`}
                    onSelect={() =>
                      goTo(`/app/tasks?id=${task.id}`)
                    }
                  >
                    <span className="flex-1">{task.name}</span>
                    <span className="text-xs text-muted-foreground">{task.nodeName}</span>
                  </Command.Item>
                ))}
              </Command.Group>
            )}

            <Command.Group heading={t("search.navigation")}>
              {routes.map((route) => (
                <Command.Item
                  key={route.path}
                  value={`nav-${route.key}-${t(route.key)}`}
                  onSelect={() => goTo(route.path)}
                >
                  {t(route.key)}
                </Command.Item>
              ))}
            </Command.Group>
          </Command.List>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
