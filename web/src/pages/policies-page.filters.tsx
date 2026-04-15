import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export type PoliciesFiltersProps = {
  keyword: string;
  setKeyword: (value: string) => void;
  activeCount: number;
  totalCount: number;
  resetFilters: () => void;
};

export function PoliciesFilters({
  keyword,
  setKeyword,
  activeCount,
  totalCount,
  resetFilters,
}: PoliciesFiltersProps) {
  const { t } = useTranslation();

  return (
    <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-[1fr_auto] lg:grid-cols-[1fr_auto_auto]">
      <Input
        placeholder={t('policies.searchPlaceholder')}
        aria-label={t('policies.searchAriaLabel')}
        value={keyword}
        onChange={(event) => setKeyword(event.target.value)}
      />
      <Badge variant="secondary" className="hidden lg:inline-flex">
        {t('policies.enabledRatio', { active: activeCount, total: totalCount })}
      </Badge>
      <Button size="sm" variant="outline" onClick={resetFilters}>
        {t('common.resetFilter')}
      </Button>
    </div>
  );
}
