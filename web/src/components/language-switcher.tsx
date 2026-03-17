import { useTranslation } from "react-i18next";
import { setLanguage } from "@/i18n";
import { Button } from "@/components/ui/button";

export function LanguageSwitcher({ className }: { className?: string }) {
  const { i18n } = useTranslation();
  const isZh = i18n.language === "zh";

  return (
    <Button
      variant="ghost"
      size="icon"
      className={className}
      onClick={() => setLanguage(isZh ? "en" : "zh")}
      title={isZh ? "Switch to English" : "切换到中文"}
      aria-label={isZh ? "Switch to English" : "切换到中文"}
    >
      <span className="text-xs font-medium">{isZh ? "EN" : "中"}</span>
    </Button>
  );
}
