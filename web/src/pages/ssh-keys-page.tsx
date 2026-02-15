import { useMemo, useState } from "react";
import { KeyRound, Plus, ShieldAlert, Trash2, Wrench } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import type { NewSSHKeyInput, SSHKeyRecord, SSHKeyType } from "@/types/domain";

type SSHKeyDraft = NewSSHKeyInput & {
  id?: string;
};

const emptyDraft: SSHKeyDraft = {
  name: "",
  username: "root",
  keyType: "auto",
  privateKey: ""
};

function toDraft(key: SSHKeyRecord): SSHKeyDraft {
  return {
    id: key.id,
    name: key.name,
    username: key.username,
    keyType: key.keyType,
    privateKey: key.privateKey
  };
}

export function SSHKeysPage() {
  const { sshKeys, nodes, createSSHKey, updateSSHKey, deleteSSHKey } =
    useOutletContext<ConsoleOutletContext>();

  const [showEditor, setShowEditor] = useState(false);
  const [draft, setDraft] = useState<SSHKeyDraft>(emptyDraft);
  const [toast, setToast] = useState<string | null>(null);

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

  const onSave = async () => {
    if (!draft.name.trim() || !draft.username.trim() || !draft.privateKey.trim()) {
      setToast("保存失败：名称、用户名、私钥都不能为空。");
      return;
    }

    const input: NewSSHKeyInput = {
      name: draft.name.trim(),
      username: draft.username.trim(),
      keyType: draft.keyType,
      privateKey: draft.privateKey.trim()
    };

    if (draft.id) {
      await updateSSHKey(draft.id, input);
      setToast(`SSH Key ${draft.name} 已更新。`);
    } else {
      await createSSHKey(input);
      setToast(`SSH Key ${draft.name} 已新增。`);
    }

    setDraft(emptyDraft);
    setShowEditor(false);
  };

  const onDelete = async (key: SSHKeyRecord) => {
    const ok = window.confirm(`确认删除 SSH Key ${key.name} 吗？`);
    if (!ok) {
      return;
    }

    const success = await deleteSSHKey(key.id);
    if (!success) {
      setToast(`删除失败：${key.name} 仍被节点使用，请先修改节点认证信息。`);
      return;
    }
    setToast(`SSH Key ${key.name} 已删除。`);
  };

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">SSH Key 管理（第 0 步）</CardTitle>
            <Button
              size="sm"
              onClick={() => {
                setDraft(emptyDraft);
                setShowEditor((prev) => !prev);
              }}
            >
              <Plus className="mr-1 size-4" />
              新增 Key
            </Button>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
            私钥仅用于演示环境。生产环境建议接入密钥管理系统（如 Vault/KMS），并启用审计与最小权限策略。
          </div>

          {showEditor ? (
            <div className="space-y-2 rounded-lg border bg-muted/30 p-3">
              <div className="grid gap-2 md:grid-cols-2">
                <Input
                  placeholder="Key 名称"
                  value={draft.name}
                  onChange={(event) => setDraft((prev) => ({ ...prev, name: event.target.value }))}
                />
                <Input
                  placeholder="默认用户名"
                  value={draft.username}
                  onChange={(event) => setDraft((prev) => ({ ...prev, username: event.target.value }))}
                />
              </div>

              <div className="grid gap-2 md:grid-cols-2">
                <select
                  className="h-10 rounded-md border bg-background px-3 text-sm"
                  value={draft.keyType}
                  onChange={(event) => setDraft((prev) => ({ ...prev, keyType: event.target.value as SSHKeyType }))}
                >
                  <option value="auto">密钥类型：自动识别（推荐）</option>
                  <option value="rsa">密钥类型：RSA</option>
                  <option value="ed25519">密钥类型：ED25519</option>
                  <option value="ecdsa">密钥类型：ECDSA</option>
                </select>
                <div className="rounded-md border bg-background/50 px-3 py-2 text-xs text-muted-foreground">
                  保存时会校验私钥内容与所选类型是否一致。
                </div>
              </div>

              <textarea
                className="min-h-36 w-full rounded-md border bg-background p-2 text-xs"
                placeholder="粘贴 OpenSSH 私钥（支持粘贴带 \n 转义的内容）"
                value={draft.privateKey}
                onChange={(event) => setDraft((prev) => ({ ...prev, privateKey: event.target.value }))}
              />

              <div className="grid grid-cols-2 gap-2">
                <Button variant="outline" onClick={() => setShowEditor(false)}>
                  取消
                </Button>
                <Button onClick={onSave}>保存 Key</Button>
              </div>
            </div>
          ) : null}

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
                        <p className="text-xs text-muted-foreground">{key.username}</p>
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
                      onClick={() => {
                        setDraft(toDraft(key));
                        setShowEditor(true);
                      }}
                    >
                      <Wrench className="mr-1 size-4" />
                      编辑
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => onDelete(key)}>
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
            <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
              当前还没有 SSH Key，请先新增密钥后再创建节点。
            </div>
          ) : null}
        </CardContent>
      </Card>

      {toast ? (
        <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-600 dark:text-emerald-300">
          {toast}
        </div>
      ) : null}
    </div>
  );
}
