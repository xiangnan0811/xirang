import { useState, type ButtonHTMLAttributes, type FormEvent } from "react";
import {
  Clock3,
  Eye,
  HeartOff,
  Pencil,
  Play,
  Plus,
  Save,
  Tag as TagIcon,
  Trash2,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import type {
  AssetRef,
  BackupAssetFavorite,
  BackupAssetRecentAccess,
  BackupAssetTag,
  SavedAssetSearch,
} from "@/types/domain";

import type { BackupAssetsUIError } from "@/lib/api/backup-assets-error";
import type { BackupAssetsCollectionResource, BackupAssetsOverlaySection } from "./use-backup-assets-state";

export interface AssetOverlaysProps {
  section: BackupAssetsOverlaySection | null;
  savedSearches: BackupAssetsCollectionResource<SavedAssetSearch>;
  favorites: BackupAssetsCollectionResource<BackupAssetFavorite>;
  tags: BackupAssetsCollectionResource<BackupAssetTag>;
  recent: BackupAssetsCollectionResource<BackupAssetRecentAccess>;
  pending: boolean;
  error?: BackupAssetsUIError;
  canSaveCurrent: boolean;
  selectedRef: AssetRef | null;
  onClose: () => void;
  onCreateSaved: () => void;
  onUpdateSaved: (savedSearch: SavedAssetSearch) => void;
  onDeleteSaved: (savedSearch: SavedAssetSearch) => void;
  onExecuteSaved: (savedSearchId: string) => void;
  onToggleFavorite: (ref: AssetRef, label: string) => void;
  onCreateTag: (name: string) => void;
  onUpdateTag: (tag: BackupAssetTag, name: string) => void;
  onDeleteTag: (tag: BackupAssetTag) => void;
  onAssignTag: (tagId: string, ref: AssetRef) => void;
  onClearRecent: () => void;
  onOpenRef: (ref: AssetRef) => void;
}

export function AssetOverlays(props: AssetOverlaysProps) {
  const { t } = useTranslation();
  const title = props.section ? t(`backupAssets.context.${sectionTitleKey(props.section)}`) : "";
  return (
    <Dialog open={props.section !== null} onOpenChange={(open) => !open && props.onClose()}>
      <DialogContent size="md" aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogCloseButton aria-label={t("backupAssets.actions.closeOverlay")} />
        </DialogHeader>
        <DialogBody className="max-h-[70dvh] overflow-y-auto px-0 py-0">
          {props.error ? (
            <InlineAlert tone={props.error.code === "conflict" ? "warning" : "critical"} className="m-3">
              {t(props.error.translationKey)}
            </InlineAlert>
          ) : null}
          {props.section === "saved" ? <SavedSearchesPanel {...props} /> : null}
          {props.section === "favorites" ? <FavoritesPanel {...props} /> : null}
          {props.section === "tags" ? <TagsPanel {...props} /> : null}
          {props.section === "recent" ? <RecentPanel {...props} /> : null}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

function SavedSearchesPanel(props: AssetOverlaysProps) {
  const { t } = useTranslation();
  if (props.savedSearches.status === "loading" || props.savedSearches.status === "idle") {
    return <LoadingState title={t("backupAssets.overlays.loading")} rows={3} />;
  }
  if (props.savedSearches.status === "blocked" || props.savedSearches.status === "error") {
    return <ResourceError resource={props.savedSearches} />;
  }
  return (
    <div>
      <div className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 py-2">
        <span className="text-xs text-muted-foreground">{t("backupAssets.overlays.savedCount", { count: props.savedSearches.items.length })}</span>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={props.pending || !props.canSaveCurrent}
          onClick={props.onCreateSaved}
        >
          <Plus className="size-4" aria-hidden />
          {t("backupAssets.actions.saveSearch")}
        </Button>
      </div>
      {props.savedSearches.items.length === 0 ? <OverlayEmpty /> : null}
      {props.savedSearches.items.map((savedSearch, index) => (
        <div key={savedSearch.id} className="flex min-h-14 items-center gap-3 border-b border-border/70 px-4 py-2">
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">
              {t("backupAssets.overlays.savedItem", { index: index + 1 })}
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
              <Badge tone={savedSearch.state === "active" ? "success" : "warning"}>
                {t(`backupAssets.overlays.savedState.${savedSearch.state}`)}
              </Badge>
              <span>{t(`backupAssets.overlays.scope.${savedSearch.query.scope.mode}`)}</span>
              <time dateTime={savedSearch.updatedAt} className="tabular-nums">
                {formatOverlayTimestamp(savedSearch.updatedAt)}
              </time>
              {savedSearch.stateReason ? (
                <span>{t(`backupAssets.overlays.savedReason.${savedSearch.stateReason}`)}</span>
              ) : null}
            </div>
          </div>
          <IconButton
            label={t("backupAssets.actions.runSaved")}
            disabled={props.pending || savedSearch.state !== "active"}
            onClick={() => props.onExecuteSaved(savedSearch.id)}
          >
            <Play className="size-4" aria-hidden />
          </IconButton>
          <IconButton
            label={t("backupAssets.actions.updateSaved")}
            disabled={props.pending || !props.canSaveCurrent}
            onClick={() => props.onUpdateSaved(savedSearch)}
          >
            <Save className="size-4" aria-hidden />
          </IconButton>
          <IconButton
            label={t("backupAssets.actions.deleteSaved")}
            disabled={props.pending}
            onClick={() => props.onDeleteSaved(savedSearch)}
          >
            <Trash2 className="size-4" aria-hidden />
          </IconButton>
        </div>
      ))}
    </div>
  );
}

function FavoritesPanel(props: AssetOverlaysProps) {
  const { t } = useTranslation();
  if (props.favorites.status === "loading" || props.favorites.status === "idle") {
    return <LoadingState title={t("backupAssets.overlays.loading")} rows={3} />;
  }
  if (props.favorites.status === "blocked" || props.favorites.status === "error") {
    return <ResourceError resource={props.favorites} />;
  }
  if (props.favorites.items.length === 0) return <OverlayEmpty />;
  return (
    <div>
      {props.favorites.items.map((favorite) => (
        <div key={favorite.id} className="flex min-h-14 items-center gap-3 border-b border-border/70 px-4 py-2">
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">
              {favorite.state === "active" ? favorite.label || shortRef(favorite.ref) : shortRef(favorite.ref)}
            </div>
            <div className="mt-1 text-[11px] text-muted-foreground">
              {favorite.tombstoneReason
                ? t(`backupAssets.overlays.favoriteReason.${favorite.tombstoneReason}`)
                : t(`backupAssets.overlays.favoriteState.${favorite.state}`)}
            </div>
          </div>
          {favorite.state === "active" ? (
            <IconButton label={t("backupAssets.actions.openAsset")} onClick={() => props.onOpenRef(favorite.ref)}>
              <Eye className="size-4" aria-hidden />
            </IconButton>
          ) : null}
          <IconButton
            label={t("backupAssets.actions.removeFavorite")}
            disabled={props.pending}
            onClick={() => props.onToggleFavorite(favorite.ref, favorite.label)}
          >
            <HeartOff className="size-4" aria-hidden />
          </IconButton>
        </div>
      ))}
    </div>
  );
}

function TagsPanel(props: AssetOverlaysProps) {
  const { t } = useTranslation();
  const [tagDraft, setTagDraft] = useState("");
  const [editingTag, setEditingTag] = useState<BackupAssetTag | null>(null);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (editingTag) props.onUpdateTag(editingTag, tagDraft);
    else props.onCreateTag(tagDraft);
    setTagDraft("");
    setEditingTag(null);
  };
  if (props.tags.status === "loading" || props.tags.status === "idle") {
    return <LoadingState title={t("backupAssets.overlays.loading")} rows={3} />;
  }
  if (props.tags.status === "blocked" || props.tags.status === "error") {
    return <ResourceError resource={props.tags} />;
  }
  return (
    <div>
      <form className="flex min-h-14 items-center gap-2 border-b border-border px-4 py-2" onSubmit={submit}>
        <Input
          value={tagDraft}
          maxLength={128}
          disabled={props.pending}
          aria-label={t("backupAssets.overlays.tagName")}
          placeholder={t("backupAssets.overlays.tagName")}
          onChange={(event) => setTagDraft(event.target.value)}
        />
        <Button type="submit" size="sm" disabled={props.pending || tagDraft.trim() === ""}>
          {editingTag ? <Save className="size-4" aria-hidden /> : <Plus className="size-4" aria-hidden />}
          {editingTag ? t("backupAssets.actions.saveTag") : t("backupAssets.actions.createTag")}
        </Button>
      </form>
      {props.tags.items.length === 0 ? <OverlayEmpty /> : null}
      {props.tags.items.map((tag) => (
        <div key={tag.id} className="flex min-h-12 items-center gap-2 border-b border-border/70 px-4 py-2">
          <TagIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1 truncate text-sm font-medium">{tag.name}</span>
          {props.selectedRef ? (
            <IconButton
              label={t("backupAssets.actions.assignTag", { name: tag.name })}
              disabled={props.pending}
              onClick={() => props.onAssignTag(tag.id, props.selectedRef!)}
            >
              <TagIcon className="size-4" aria-hidden />
            </IconButton>
          ) : null}
          <IconButton
            label={t("backupAssets.actions.editTag", { name: tag.name })}
            disabled={props.pending}
            onClick={() => {
              setEditingTag(tag);
              setTagDraft(tag.name);
            }}
          >
            <Pencil className="size-4" aria-hidden />
          </IconButton>
          <IconButton
            label={t("backupAssets.actions.deleteTag", { name: tag.name })}
            disabled={props.pending}
            onClick={() => props.onDeleteTag(tag)}
          >
            <Trash2 className="size-4" aria-hidden />
          </IconButton>
        </div>
      ))}
    </div>
  );
}

function RecentPanel(props: AssetOverlaysProps) {
  const { t } = useTranslation();
  if (props.recent.status === "loading" || props.recent.status === "idle") {
    return <LoadingState title={t("backupAssets.overlays.loading")} rows={3} />;
  }
  if (props.recent.status === "blocked" || props.recent.status === "error") {
    return <ResourceError resource={props.recent} />;
  }
  return (
    <div>
      <div className="flex min-h-12 items-center justify-end border-b border-border px-4 py-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={props.pending || props.recent.items.length === 0}
          onClick={props.onClearRecent}
        >
          <Trash2 className="size-4" aria-hidden />
          {t("backupAssets.actions.clearRecent")}
        </Button>
      </div>
      {props.recent.items.length === 0 ? <OverlayEmpty /> : null}
      {props.recent.items.map((recent) => (
        <div key={recent.id} className="flex min-h-12 items-center gap-3 border-b border-border/70 px-4 py-2">
          <Clock3 className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1 truncate text-xs tabular-nums">{shortRef(recent.ref)}</span>
          <span className="text-[11px] tabular-nums text-muted-foreground">{recent.lastAccessedAt.slice(0, 16).replace("T", " ")}</span>
          <IconButton label={t("backupAssets.actions.openAsset")} onClick={() => props.onOpenRef(recent.ref)}>
            <Eye className="size-4" aria-hidden />
          </IconButton>
        </div>
      ))}
    </div>
  );
}

function ResourceError<T>({ resource }: { resource: BackupAssetsCollectionResource<T> }) {
  const { t } = useTranslation();
  return (
    <div className="p-3">
      <InlineAlert tone="critical">{t(resource.error?.translationKey ?? "backupAssets.errors.unknown")}</InlineAlert>
    </div>
  );
}

function OverlayEmpty() {
  const { t } = useTranslation();
  return <div className="px-4 py-10 text-center text-sm text-muted-foreground">{t("backupAssets.overlays.empty")}</div>;
}

function IconButton({ label, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <Button type="button" size="sm" variant="ghost" aria-label={label} title={label} {...props}>
      {children}
    </Button>
  );
}

function sectionTitleKey(section: BackupAssetsOverlaySection): "savedSearches" | "favorites" | "tags" | "recent" {
  return section === "saved" ? "savedSearches" : section;
}

function shortRef(ref: AssetRef): string {
  return `${ref.recoveryPointId.slice(0, 8)} · ${ref.entryId.slice(0, 12)}`;
}

function formatOverlayTimestamp(value: string): string {
  return value.slice(0, 16).replace("T", " ");
}
