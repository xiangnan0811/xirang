import { useRef } from "react";
import { LayoutGrid, List } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type ViewMode = "cards" | "list";

const OPTIONS: ViewMode[] = ["cards", "list"];

type ViewModeToggleProps = {
  value: ViewMode;
  onChange: (mode: ViewMode) => void;
  groupLabel: string;
  cardsButtonLabel?: string;
  listButtonLabel?: string;
  cardsText?: string;
  listText?: string;
  className?: string;
};

export function ViewModeToggle({
  value,
  onChange,
  groupLabel,
  cardsButtonLabel,
  listButtonLabel,
  cardsText,
  listText,
  className,
}: ViewModeToggleProps) {
  const { t } = useTranslation();
  const resolvedCardsText = cardsText ?? t('viewMode.cards');
  const resolvedListText = listText ?? t('viewMode.list');
  const refs = useRef<(HTMLButtonElement | null)[]>([]);

  function handleKeyDown(e: React.KeyboardEvent, index: number) {
    let next = index;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") {
      next = (index + 1) % OPTIONS.length;
    } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
      next = (index - 1 + OPTIONS.length) % OPTIONS.length;
    } else {
      return;
    }
    e.preventDefault();
    onChange(OPTIONS[next]);
    refs.current[next]?.focus();
  }

  return (
    <div
      className={cn(
        "inline-flex items-center gap-1 rounded-lg border border-border/80 bg-background/80 p-1",
        className
      )}
      role="radiogroup"
      aria-label={groupLabel}
    >
      <Button
        ref={(el) => { refs.current[0] = el; }}
        type="button"
        size="sm"
        variant={value === "cards" ? "default" : "ghost"}
        role="radio"
        aria-checked={value === "cards"}
        aria-label={cardsButtonLabel}
        tabIndex={value === "cards" ? 0 : -1}
        onClick={() => onChange("cards")}
        onKeyDown={(e) => handleKeyDown(e, 0)}
      >
        <LayoutGrid className="mr-1 size-4" />
        {resolvedCardsText}
      </Button>
      <Button
        ref={(el) => { refs.current[1] = el; }}
        type="button"
        size="sm"
        variant={value === "list" ? "default" : "ghost"}
        role="radio"
        aria-checked={value === "list"}
        aria-label={listButtonLabel}
        tabIndex={value === "list" ? 0 : -1}
        onClick={() => onChange("list")}
        onKeyDown={(e) => handleKeyDown(e, 1)}
      >
        <List className="mr-1 size-4" />
        {resolvedListText}
      </Button>
    </div>
  );
}
