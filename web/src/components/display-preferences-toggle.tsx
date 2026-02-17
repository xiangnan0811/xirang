import { Leaf, Minus, Plus, Zap } from "lucide-react";
import { useTheme } from "@/context/theme-context";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type DisplayPreferencesToggleProps = {
  className?: string;
};

export function DisplayPreferencesToggle({ className }: DisplayPreferencesToggleProps) {
  const { density, toggleDensity, powerMode, togglePowerMode } = useTheme();

  return (
    <div className={cn("flex items-center gap-1", className)}>
      <Button
        variant="ghost"
        size="icon"
        onClick={toggleDensity}
        aria-label={density === "compact" ? "切换舒适密度" : "切换紧凑密度"}
        title={density === "compact" ? "切换舒适密度" : "切换紧凑密度"}
      >
        {density === "compact" ? <Plus className="size-4" /> : <Minus className="size-4" />}
      </Button>

      <Button
        variant="ghost"
        size="icon"
        onClick={togglePowerMode}
        aria-label={powerMode === "save" ? "关闭节能模式" : "开启节能模式"}
        title={powerMode === "save" ? "关闭节能模式" : "开启节能模式"}
      >
        {powerMode === "save" ? <Leaf className="size-4" /> : <Zap className="size-4" />}
      </Button>
    </div>
  );
}
