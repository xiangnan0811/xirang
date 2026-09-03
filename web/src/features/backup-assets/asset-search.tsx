import { Search, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { SearchInput } from "@/components/ui/search-input";
import { Select } from "@/components/ui/select";

import type { BackupAssetsScope } from "./backup-assets-route-state";

export interface AssetSearchProps {
  draft: string;
  submittedQuery?: string | null;
  scope: BackupAssetsScope;
  disabled: boolean;
  locked?: boolean;
  searchActive?: boolean;
  onDraftChange: (value: string) => void;
  onSearch: (query: string, scope: BackupAssetsScope) => void;
}

export function AssetSearch({
  draft,
  submittedQuery = null,
  scope,
  disabled,
  locked = false,
  searchActive = false,
  onDraftChange,
  onSearch,
}: AssetSearchProps) {
  const { t } = useTranslation();
  const [draftScope, setDraftScope] = useState(scope);
  useEffect(() => {
    setDraftScope(scope);
  }, [scope]);
  const editingLocked = disabled || locked;
  const canClear = draft.trim() !== "" || submittedQuery !== null || searchActive;
  return (
    <form
      role="search"
      className="col-span-full flex min-w-0 flex-wrap items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        if (editingLocked) return;
        onSearch(draft.trim(), draftScope);
      }}
    >
      <SearchInput
        type="search"
        value={draft}
        disabled={editingLocked}
        aria-label={t("backupAssets.search.label")}
        placeholder={t("backupAssets.search.placeholder")}
        maxLength={512}
        autoComplete="off"
        spellCheck={false}
        containerClassName="min-w-0 w-full flex-1 basis-full sm:basis-0"
        className="touch-target min-h-11 lg:min-h-9"
        onChange={(event) => onDraftChange(event.target.value)}
      />
      <Select
        value={draftScope}
        disabled={editingLocked}
        aria-label={t("backupAssets.search.scope")}
        className="touch-target min-h-11 lg:min-h-9"
        containerClassName="min-w-0 flex-1 sm:flex-none sm:w-72 lg:w-80"
        onChange={(event) => setDraftScope(event.target.value as BackupAssetsScope)}
      >
        <option value="current">{t("backupAssets.search.current")}</option>
        <option value="all_retained">{t("backupAssets.search.allRetained")}</option>
      </Select>
      {canClear ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="touch-target min-h-11 shrink-0 lg:min-h-8"
          aria-label={t("backupAssets.search.clear")}
          onClick={() => {
            onDraftChange("");
            onSearch("", draftScope);
          }}
        >
          <X className="size-4" aria-hidden />
          {t("backupAssets.search.clear")}
        </Button>
      ) : null}
      <Button type="submit" size="sm" className="touch-target min-h-11 shrink-0 lg:min-h-8" disabled={editingLocked || draft.trim() === ""}>
        <Search className="size-4" aria-hidden />
        {t("backupAssets.actions.search")}
      </Button>
    </form>
  );
}
