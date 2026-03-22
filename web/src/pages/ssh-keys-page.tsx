import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Plus, ShieldAlert, Trash2, Wrench } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import {
  SSHKeyEditorDialog,
  type SSHKeyDraft,
} from "@/components/ssh-key-editor-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { getErrorMessage } from "@/lib/utils";
import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";

export function SSHKeysPage() {
  const { t } = useTranslation();
  const {
    sshKeys,
    nodes,
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
    refreshSSHKeys,
    refreshNodes,
  } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshSSHKeys();
    void refreshNodes();
  }, [refreshSSHKeys, refreshNodes]);

  const { confirm, dialog } = useConfirm();

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingKey, setEditingKey] = useState<SSHKeyRecord | null>(null);

  const keyUsageMap = useMemo(() => {
    const map = new Map<string, number>();
    nodes.forEach((node) => {
      if (!node.keyId) {
        return;
      }
      map.set(node.keyId, (map.get(node.keyId) ?? 0) + 1);
    });
    return map;
  }, [nodes]);

  const keyStats = useMemo(() => {
    let inUse = 0;
    let unused = 0;
    let bindingCount = 0;
    for (const key of sshKeys) {
      const usageCount = keyUsageMap.get(key.id) ?? 0;
      if (usageCount > 0) {
        inUse += 1;
        bindingCount += usageCount;
      } else {
        unused += 1;
      }
    }
    return { inUse, unused, bindingCount };
  }, [keyUsageMap, sshKeys]);

  const openCreateDialog = () => {
    setEditingKey(null);
    setEditorOpen(true);
  };

  const openEditDialog = (key: SSHKeyRecord) => {
    setEditingKey(key);
    setEditorOpen(true);
  };

  const handleSave = async (draft: SSHKeyDraft) => {
    const name = draft.name.trim();
    const username = draft.username.trim();
    const privateKey = draft.privateKey.trim();

    if (!name || !username) {
      toast.error(t("sshKeys.errorNameRequired"));
      return;
    }

    if (!draft.id && !privateKey) {
      toast.error(t("sshKeys.errorPrivateKeyRequired"));
      return;
    }

    const input: NewSSHKeyInput = {
      name,
      username,
      keyType: draft.keyType,
      privateKey,
    };

    try {
      if (draft.id) {
        await updateSSHKey(draft.id, input);
        toast.success(t("sshKeys.keyUpdated", { name: draft.name }));
      } else {
        await createSSHKey(input);
        toast.success(t("sshKeys.keyCreated", { name: draft.name }));
      }

      setEditorOpen(false);
      setEditingKey(null);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onDelete = async (key: SSHKeyRecord) => {
    const ok = await confirm({
      title: t("common.confirmAction"),
      description: t("sshKeys.confirmDeleteDesc", { name: key.name }),
    });
    if (!ok) {
      return;
    }

    const success = await deleteSSHKey(key.id);
    if (!success) {
      toast.error(t("sshKeys.deleteFailedInUse", { name: key.name }));
      return;
    }
    toast.success(t("sshKeys.keyDeleted", { name: key.name }));
  };

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="glass-panel border-border/70">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={openCreateDialog}>
                <Plus className="mr-1 size-3.5" />
                {t("sshKeys.addKey")}
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="success">
                {t("sshKeys.inUse", { count: keyStats.inUse })}
              </Badge>
              <Badge variant="outline">
                {t("sshKeys.unused", { count: keyStats.unused })}
              </Badge>
              <Badge variant="secondary">
                {t("sshKeys.boundNodes", { count: keyStats.bindingCount })}
              </Badge>
            </div>
          </div>
          <InlineAlert tone="warning" className="shadow-sm">
            {t("sshKeys.securityWarning")}
          </InlineAlert>

          <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
            {sshKeys.map((key) => {
              const usageCount = keyUsageMap.get(key.id) ?? 0;
              return (
                <div key={key.id} className="interactive-surface p-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <span className="rounded-md border border-primary/20 bg-primary/10 p-1.5 text-primary">
                        <KeyRound className="size-4" />
                      </span>
                      <div>
                        <p className="font-medium">{key.name}</p>
                        <p className="text-xs text-muted-foreground">
                          {key.username}
                        </p>
                      </div>
                    </div>
                    <Badge variant={usageCount > 0 ? "warning" : "outline"}>
                      {t("sshKeys.inUseNodes", { count: usageCount })}
                    </Badge>
                  </div>

                  <div className="mt-3 space-y-1 text-xs text-muted-foreground">
                    <p>
                      {t("sshKeys.keyType", {
                        type: String(key.keyType).toUpperCase(),
                      })}
                    </p>
                    <p className="break-all">
                      {t("sshKeys.fingerprint", { fp: key.fingerprint })}
                    </p>
                    <p>{t("sshKeys.createdAt", { time: key.createdAt })}</p>
                    <p>
                      {t("sshKeys.lastUsed", {
                        time: key.lastUsedAt ?? t("common.neverUsed"),
                      })}
                    </p>
                  </div>

                  <div className="mt-4 flex flex-wrap items-center justify-end gap-2 border-t border-border/40 pt-3">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                      onClick={() => openEditDialog(key)}
                      aria-label={t("common.edit")}
                    >
                      <Wrench className="size-4" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                      onClick={() => onDelete(key)}
                      aria-label={t("common.delete")}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>

                  {usageCount > 0 ? (
                    <p className="mt-2 text-[11px] text-warning">
                      <ShieldAlert className="mr-1 inline size-3" />
                      {t("sshKeys.inUseWarning")}
                    </p>
                  ) : null}
                </div>
              );
            })}
          </div>

          {!sshKeys.length ? (
            <EmptyState
              title={t("sshKeys.emptyTitle")}
              description={t("sshKeys.emptyDesc")}
            />
          ) : null}
        </CardContent>
      </Card>

      <SSHKeyEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        editingKey={editingKey}
        onSave={handleSave}
      />

      {dialog}
    </div>
  );
}
