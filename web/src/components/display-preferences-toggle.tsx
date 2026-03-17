import { Leaf, Minus, Plus, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useTheme } from "@/context/theme-context";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type DisplayPreferencesToggleProps = {
  className?: string;
};

export function DisplayPreferencesToggle({ className }: DisplayPreferencesToggleProps) {
  const { t } = useTranslation();
  const { density, toggleDensity, powerMode, togglePowerMode } = useTheme();

  return (
    <div className={cn("flex items-center gap-1", className)}>
      <Button
        variant="ghost"
        size="icon"
        onClick={toggleDensity}
        aria-label={density === "compact" ? t('displayPrefs.comfortDensity') : t('displayPrefs.compactDensity')}
        title={density === "compact" ? t('displayPrefs.comfortDensity') : t('displayPrefs.compactDensity')}
      >
        {density === "compact" ? <Plus className="size-4" /> : <Minus className="size-4" />}
      </Button>

      <Button
        variant="ghost"
        size="icon"
        onClick={togglePowerMode}
        aria-label={powerMode === "save" ? t('displayPrefs.powerSaveOff') : t('displayPrefs.powerSaveOn')}
        title={powerMode === "save" ? t('displayPrefs.powerSaveOff') : t('displayPrefs.powerSaveOn')}
      >
        {powerMode === "save" ? <Leaf className="size-4" /> : <Zap className="size-4" />}
      </Button>
    </div>
  );
}
