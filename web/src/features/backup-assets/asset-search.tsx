import { Search } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { SearchInput } from "@/components/ui/search-input";
import { Select } from "@/components/ui/select";

import type { BackupAssetsScope } from "./backup-assets-route-state";

export interface AssetSearchProps {
  draft: string;
  scope: BackupAssetsScope;
  disabled: boolean;
  onDraftChange: (value: string) => void;
  onScopeChange: (scope: BackupAssetsScope) => void;
  onSearch: (query: string, scope: BackupAssetsScope) => void;
}

export function AssetSearch({
  draft,
  scope,
  disabled,
  onDraftChange,
  onScopeChange,
  onSearch,
}: AssetSearchProps) {
  const { t } = useTranslation();
  return (
    <form
      role="search"
      className="col-span-full flex min-w-0 items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        const normalized = draft.trim();
        if (normalized !== "") onSearch(normalized, scope);
      }}
    >
      <SearchInput
        type="search"
        value={draft}
        disabled={disabled}
        aria-label={t("backupAssets.search.label")}
        placeholder={t("backupAssets.search.placeholder")}
        maxLength={512}
        autoComplete="off"
        spellCheck={false}
        containerClassName="min-w-0 flex-1"
        className="touch-target min-h-11 lg:min-h-9"
        onChange={(event) => onDraftChange(event.target.value)}
      />
      <Select
        value={scope}
        disabled={disabled}
        aria-label={t("backupAssets.search.scope")}
        className="touch-target min-h-11 lg:min-h-9"
        containerClassName="w-32 shrink-0"
        onChange={(event) => onScopeChange(event.target.value as BackupAssetsScope)}
      >
        <option value="current">{t("backupAssets.search.current")}</option>
        <option value="all_retained">{t("backupAssets.search.allRetained")}</option>
      </Select>
      <Button type="submit" size="sm" className="touch-target min-h-11 shrink-0 lg:min-h-8" disabled={disabled || draft.trim() === ""}>
        <Search className="size-4" aria-hidden />
        {t("backupAssets.actions.search")}
      </Button>
    </form>
  );
}
