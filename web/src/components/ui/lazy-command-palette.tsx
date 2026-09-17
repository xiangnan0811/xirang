import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCommandPalette } from "@/context/command-palette-context.hooks";
import { Button } from "@/components/ui/button";

type PaletteModule = typeof import("./command-palette");

export function LazyCommandPalette() {
  const { t } = useTranslation();
  const { open, setOpen } = useCommandPalette();
  const [loaded, setLoaded] = useState<PaletteModule | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!open || loaded || failed) return;
    let active = true;
    void import("./command-palette").then(
      (module) => { if (active) setLoaded(module); },
      () => { if (active) setFailed(true); },
    );
    return () => { active = false; };
  }, [open, loaded, failed]);

  useEffect(() => {
    if (!open || loaded) return;
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [open, loaded, setOpen]);

  // Keep the dialog mounted after its first load so Radix owns closing and focus.
  if (loaded) return <loaded.CommandPalette />;
  if (!open) return null;

  return (
    <div className="fixed bottom-4 right-4 z-[70] rounded-md border border-border bg-background p-3 shadow-lg">
      {failed ? (
        <div role="alert" className="flex items-center gap-3">
          <span>{t("common.operationFailed")}</span>
          <Button variant="outline" size="sm" onClick={() => {
            setFailed(false);
          }}>{t("common.retry")}</Button>
        </div>
      ) : <span role="status" aria-busy="true">{t("common.loading")}</span>}
    </div>
  );
}
