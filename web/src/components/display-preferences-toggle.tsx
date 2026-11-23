import { Leaf, Rows3, Rows4, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useTheme } from "@/context/theme-context.hooks";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type DisplayPreferencesToggleProps = {
  className?: string;
};

export function DisplayPreferencesToggle({ className }: DisplayPreferencesToggleProps) {
  const { t } = useTranslation();
  const { density, toggleDensity, powerMode, togglePowerMode } = useTheme();
  const isCompact = density === "compact";
  const isPowerSave = powerMode === "save";
  const densityLabel = isCompact
    ? t("displayPrefs.comfortDensity")
    : t("displayPrefs.compactDensity");
  const powerLabel = isPowerSave
    ? t("displayPrefs.powerSaveOff")
    : t("displayPrefs.powerSaveOn");

  return (
    <div className={cn("flex items-center gap-1", className)}>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        onClick={toggleDensity}
        aria-pressed={isCompact}
        aria-label={densityLabel}
        title={densityLabel}
        className={cn(isCompact && "bg-accent text-accent-foreground")}
      >
        {isCompact ? (
          <Rows4 className="size-4" aria-hidden />
        ) : (
          <Rows3 className="size-4" aria-hidden />
        )}
      </Button>

      <Button
        type="button"
        variant="ghost"
        size="icon"
        onClick={togglePowerMode}
        aria-pressed={isPowerSave}
        aria-label={powerLabel}
        title={powerLabel}
        className={cn(isPowerSave && "bg-accent text-accent-foreground")}
      >
        {isPowerSave ? (
          <Leaf className="size-4" aria-hidden />
        ) : (
          <Sparkles className="size-4" aria-hidden />
        )}
      </Button>
    </div>
  );
}
