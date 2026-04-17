import * as React from "react";
import { useTranslation } from "react-i18next";
import { Search } from "lucide-react";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useCommandPalette } from "@/context/command-palette-context";

export function CommandPalette() {
  const { t } = useTranslation();
  const { open, setOpen } = useCommandPalette();
  const [query, setQuery] = React.useState("");
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent size="md" className="p-0">
        <div className="flex items-center gap-3 border-b border-border px-4 py-3">
          <Search className="size-4 text-muted-foreground" aria-hidden />
          <Input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("search.placeholder", "Search nodes, tasks, logs…")}
            className="h-8 border-0 px-0 focus-visible:ring-0 focus-visible:border-transparent"
          />
          <kbd className="rounded border border-border bg-background px-1.5 py-[2px] font-mono text-[10px] text-muted-foreground">
            {t("search.kbd", "⌘K")}
          </kbd>
        </div>
        <div className="max-h-[400px] overflow-y-auto px-4 py-4 text-sm text-muted-foreground">
          {t("search.placeholderEmpty", "Type to search…")}
        </div>
      </DialogContent>
    </Dialog>
  );
}
