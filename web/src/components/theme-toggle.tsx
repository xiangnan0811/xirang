import { Moon, SunMedium } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useTheme } from "@/context/theme-context";
import { Button } from "@/components/ui/button";

export function ThemeToggle() {
  const { t } = useTranslation();
  const { theme, toggleTheme } = useTheme();
  const nextLabel = theme === "dark" ? t('theme.toggleLight') : t('theme.toggleDark');

  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={toggleTheme}
      aria-label={nextLabel}
      title={nextLabel}
    >
      {theme === "dark" ? <SunMedium className="size-5" /> : <Moon className="size-5" />}
    </Button>
  );
}
