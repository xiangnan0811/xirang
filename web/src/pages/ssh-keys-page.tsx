import { useMemo, useState } from "react";
import { KeyRound, Plus, ShieldAlert, Trash2, Wrench } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import {
  SSHKeyEditorDialog,
  type SSHKeyDraft,
} from "@/components/ssh-key-editor-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";

export function SSHKeysPage() {
  const { sshKeys, nodes, createSSHKey, updateSSHKey, deleteSSHKey } =
    useOutletContext<ConsoleOutletContext>();

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

  const openCreateDialog = () => {
    setEditingKey(null);
    setEditorOpen(true);
  };

  const openEditDialog = (key: SSHKeyRecord) => {
    setEditingKey(key);
    setEditorOpen(true);
  };

  const handleSave = async (draft: SSHKeyDraft) => {
    if (!draft.name.trim() || !draft.username.trim() || !draft.privateKey.trim()) {
      toast.error("保存失败：名称、用户名、私钥都不能为空。");
      return;
    }

    const input: NewSSHKeyInput = {
      name: draft.name.trim(),
      username: draft.username.trim(),
      keyType: draft.keyType,
      privateKey: draft.privateKey.trim(),
    };

    try {
      if (draft.id) {
        await updateSSHKey(draft.id, input);
        toast.success(`SSH Key ${draft.name} 已更新。`);
      } else {
        await createSSHKey(input);
        toast.success(`SSH Key ${draft.name} 已新增。`);
      }

      setEditorOpen(false);
      setEditingKey(null);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const onDelete = async (key: SSHKeyRecord) => {
    const ok = await confirm({
      title: "确认操作",
      description: `确认删除 SSH Key ${key.name} 吗？`,
    });
    if (!ok) {
      return;
    }

    const success = await deleteSSHKey(key.id);
    if (!success) {
      toast.error(
        `删除失败：${key.name} 仍被节点使用，请先修改节点认证信息。`
      );
      return;
    }
    toast.success(`SSH Key ${key.name} 已删除。`);
  };

  return (
    <div className="animate-fade-in space-y-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">SSH Key 管理（第 0 步）</CardTitle>
            <Button size="sm" onClick={openCreateDialog}>
              <Plus className="mr-1 size-4" />
              新增 Key
            </Button>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
            私钥仅用于演示环境。生产环境建议接入密钥管理系统（如
            Vault/KMS），并启用审计与最小权限策略。
          </div>

          <div className="grid gap-3 lg:grid-cols-2">
            {sshKeys.map((key) => {
              const usageCount = keyUsageMap.get(key.id) ?? 0;
              return (
                <div key={key.id} className="rounded-lg border p-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <span className="rounded-md bg-primary/10 p-1.5 text-primary">
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
                      使用中 {usageCount} 节点
                    </Badge>
                  </div>

                  <div className="mt-3 space-y-1 text-xs text-muted-foreground">
                    <p>类型：{String(key.keyType).toUpperCase()}</p>
                    <p>指纹：{key.fingerprint}</p>
                    <p>创建时间：{key.createdAt}</p>
                    <p>最后使用：{key.lastUsedAt ?? "未使用"}</p>
                  </div>

                  <div className="mt-3 flex gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => openEditDialog(key)}
                    >
                      <Wrench className="mr-1 size-4" />
                      编辑
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => onDelete(key)}
                    >
                      <Trash2 className="mr-1 size-4" />
                      删除
                    </Button>
                  </div>

                  {usageCount > 0 ? (
                    <p className="mt-2 text-[11px] text-amber-600 dark:text-amber-300">
                      <ShieldAlert className="mr-1 inline size-3" />
                      该密钥正在被节点使用，删除前请先切换节点认证配置。
                    </p>
                  ) : null}
                </div>
              );
            })}
          </div>

          {!sshKeys.length ? (
            <EmptyState
              title="当前还没有 SSH Key"
              description="请先新增密钥后再创建节点"
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
