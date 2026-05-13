import * as React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { Command } from "cmdk";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { getVisibleNavItems } from "@/components/layout/navigation";
import { useCommandPalette } from "@/context/command-palette-context.hooks";
import { useNodesContextOptional } from "@/context/nodes-context.hooks";
import { useTasksContextOptional } from "@/context/tasks-context.hooks";
import { useAuth } from "@/context/auth-context.hooks";

export function CommandPalette() {
  const { t } = useTranslation();
  const { open, setOpen } = useCommandPalette();
  const [query, setQuery] = React.useState("");
  const inputRef = React.useRef<HTMLInputElement | null>(null);
  const navigate = useNavigate();
  const nodesCtx = useNodesContextOptional();
  const tasksCtx = useTasksContextOptional();
  const nodes = nodesCtx?.nodes ?? [];
  const tasks = tasksCtx?.tasks ?? [];
  const { role } = useAuth();
  const visibleNavItems = React.useMemo(() => getVisibleNavItems(role), [role]);

  // Reset query when closed
  React.useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  React.useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      inputRef.current?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
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
        <DialogTitle className="sr-only">{t("search.title")}</DialogTitle>
        <DialogDescription className="sr-only">{t("search.description")}</DialogDescription>
        <Command
          label={t("search.placeholder")}
          className="[&_[cmdk-group-heading]]:text-micro [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.06em] [&_[cmdk-group-heading]]:text-muted-foreground [&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-item]]:flex [&_[cmdk-item]]:items-center [&_[cmdk-item]]:gap-2 [&_[cmdk-item]]:rounded-md [&_[cmdk-item]]:px-3 [&_[cmdk-item]]:py-2 [&_[cmdk-item]]:text-sm [&_[cmdk-item]]:cursor-pointer [&_[cmdk-item]:hover]:bg-accent [&_[cmdk-item][data-selected=true]]:bg-accent [&_[cmdk-empty]]:py-6 [&_[cmdk-empty]]:text-center [&_[cmdk-empty]]:text-sm [&_[cmdk-empty]]:text-muted-foreground"
        >
          <div className="flex items-center gap-3 border-b border-border px-4 py-3">
            <Command.Input
              ref={inputRef}
              value={query}
              onValueChange={setQuery}
              placeholder={t("search.placeholder")}
              className="flex-1 h-8 border-0 bg-transparent text-sm outline-none placeholder:text-muted-foreground focus:ring-0"
            />
            <kbd className="rounded border border-border bg-background px-1.5 py-[2px] font-mono text-micro text-muted-foreground">
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
              {visibleNavItems.map((route) => (
                <Command.Item
                  key={route.path}
                  value={`nav-${route.titleKey}-${t(route.titleKey)}`}
                  onSelect={() => goTo(route.path)}
                >
                  {t(route.titleKey)}
                </Command.Item>
              ))}
            </Command.Group>
          </Command.List>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
