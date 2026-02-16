import { useEffect, useState } from "react";
import { ServerCog } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import type {
  NewNodeInput,
  NodeRecord,
  SSHKeyRecord,
  SSHKeyType,
} from "@/types/domain";

type NodeEditorDraft = {
  id?: number;
  name: string;
  host: string;
  port: number;
  username: string;
  authType: "key" | "password";
  keyId: string;
  password: string;
  tags: string;
  basePath: string;
  inlineKeyName: string;
  inlineKeyType: SSHKeyType;
  inlinePrivateKey: string;
};

const emptyDraft: NodeEditorDraft = {
  name: "",
  host: "",
  port: 22,
  username: "root",
  authType: "key",
  keyId: "",
  password: "",
  tags: "",
  basePath: "/",
  inlineKeyName: "",
  inlineKeyType: "auto",
  inlinePrivateKey: "",
};

function toAuthType(value: string): "key" | "password" {
  if (value === "password") {
    return "password";
  }
  return "key";
}

function toKeyType(value: string): SSHKeyType {
  if (value === "rsa" || value === "ed25519" || value === "ecdsa") {
    return value;
  }
  return "auto";
}

function toPort(value: number): number {
  if (!Number.isFinite(value)) {
    return 22;
  }
  return Math.min(65535, Math.max(1, Math.trunc(value)));
}

function toDraft(node: NodeRecord): NodeEditorDraft {
  return {
    id: node.id,
    name: node.name,
    host: node.host,
    port: node.port,
    username: node.username,
    authType: node.authType,
    keyId: node.keyId ?? "",
    password: "",
    tags: node.tags.join(","),
    basePath: node.basePath || "/",
    inlineKeyName: "",
    inlineKeyType: "auto",
    inlinePrivateKey: "",
  };
}

function buildNodeInput(draft: NodeEditorDraft): NewNodeInput {
  const useInlineKey = draft.authType === "key" && draft.keyId === "__new__";
  return {
    name: draft.name.trim(),
    host: draft.host.trim(),
    port: toPort(draft.port),
    username: draft.username.trim(),
    authType: draft.authType,
    keyId:
      draft.authType === "key" && !useInlineKey ? draft.keyId || null : null,
    password: draft.authType === "password" ? draft.password : undefined,
    tags: draft.tags,
    basePath: draft.basePath || "/",
    inlineKeyName: useInlineKey ? draft.inlineKeyName : undefined,
    inlineKeyType: useInlineKey ? draft.inlineKeyType : undefined,
    inlinePrivateKey: useInlineKey ? draft.inlinePrivateKey : undefined,
  };
}

type NodeEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingNode?: NodeRecord | null;
  sshKeys: SSHKeyRecord[];
  onSave: (input: NewNodeInput, nodeId?: number) => Promise<void>;
  onTestConnection?: (nodeId: number) => Promise<void>;
};

export function NodeEditorDialog({
  open,
  onOpenChange,
  editingNode,
  sshKeys,
  onSave,
  onTestConnection,
}: NodeEditorDialogProps) {
  const [draft, setDraft] = useState<NodeEditorDraft>(emptyDraft);
  const [saving, setSaving] = useState(false);

  const isEditing = Boolean(draft.id);

  useEffect(() => {
    if (!open) {
      setDraft(emptyDraft);
      return;
    }
    setDraft(editingNode ? toDraft(editingNode) : emptyDraft);
  }, [editingNode, open]);

  const handleOpenChange = (next: boolean) => {
    onOpenChange(next);
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(buildNodeInput(draft), draft.id);
    } finally {
      setSaving(false);
    }
  };

  const handleTestConnection = async () => {
    if (draft.id && onTestConnection) {
      await onTestConnection(draft.id);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent size="lg">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <ServerCog className="size-5 text-primary" />
            <DialogTitle>
              {isEditing ? `编辑节点 - ${draft.name}` : "新增节点"}
            </DialogTitle>
          </div>
          <DialogDescription>
            {isEditing
              ? `修改 ${draft.name} 的连接配置。`
              : "添加新的远程服务器节点，保存后自动探测连接状态。"}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-3">
          <div>
            <label className="mb-1 block text-sm font-medium">节点名称</label>
            <Input
              placeholder="例如：prod-app-01"
              value={draft.name}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, name: event.target.value }))
              }
            />
          </div>

          <div className="grid gap-3 md:grid-cols-[2fr_1fr]">
            <div>
              <label className="mb-1 block text-sm font-medium">主机 / IP</label>
              <Input
                placeholder="10.10.0.11"
                value={draft.host}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, host: event.target.value }))
                }
              />
            </div>
            <div>
              <label className="mb-1 block text-sm font-medium">端口</label>
              <Input
                type="number"
                placeholder="22"
                value={draft.port}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    port: toPort(Number.parseInt(event.target.value, 10)),
                  }))
                }
              />
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">SSH 用户名</label>
            <Input
              placeholder="root"
              value={draft.username}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, username: event.target.value }))
              }
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">认证方式</label>
            <select
              className="h-10 w-full rounded-md border bg-background px-3 text-sm"
              value={draft.authType}
              onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    authType: toAuthType(event.target.value),
                  }))
                }
              >
              <option value="key">密钥认证</option>
              <option value="password">密码认证</option>
            </select>
          </div>

          {draft.authType === "key" ? (
            <div>
              <label className="mb-1 block text-sm font-medium">SSH Key</label>
              <select
                className="h-10 w-full rounded-md border bg-background px-3 text-sm"
                value={draft.keyId}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, keyId: event.target.value }))
                }
              >
                <option value="">选择已有 SSH Key</option>
                {sshKeys.map((key) => (
                  <option key={key.id} value={key.id}>
                    {key.name} ({key.username})
                  </option>
                ))}
                <option value="__new__">+ 新增 SSH Key</option>
              </select>
            </div>
          ) : (
            <div>
              <label className="mb-1 block text-sm font-medium">SSH 密码</label>
              <Input
                type="password"
                placeholder={isEditing ? "留空则不修改" : "请输入 SSH 密码"}
                value={draft.password}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    password: event.target.value,
                  }))
                }
              />
            </div>
          )}

          {draft.authType === "key" && draft.keyId === "__new__" ? (
            <div className="space-y-2 rounded-md border border-dashed bg-muted/20 p-3">
              <p className="text-xs font-medium text-muted-foreground">
                内联新增 SSH Key
              </p>
              <div className="grid gap-2 md:grid-cols-2">
                <div>
                  <label className="mb-1 block text-xs font-medium">
                    Key 名称
                  </label>
                  <Input
                    placeholder="新 Key 名称"
                    value={draft.inlineKeyName}
                    onChange={(event) =>
                      setDraft((prev) => ({
                        ...prev,
                        inlineKeyName: event.target.value,
                      }))
                    }
                  />
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium">
                    密钥类型
                  </label>
                  <select
                    className="h-10 w-full rounded-md border bg-background px-3 text-sm"
                    value={draft.inlineKeyType}
                    onChange={(event) =>
                      setDraft((prev) => ({
                        ...prev,
                        inlineKeyType: toKeyType(event.target.value),
                      }))
                    }
                  >
                    <option value="auto">自动识别（推荐）</option>
                    <option value="rsa">RSA</option>
                    <option value="ed25519">ED25519</option>
                    <option value="ecdsa">ECDSA</option>
                  </select>
                </div>
              </div>
              <textarea
                className="mt-1 min-h-28 w-full rounded-md border bg-background p-2 text-xs"
                placeholder="粘贴 OpenSSH 私钥（支持粘贴带 \n 转义的内容）"
                value={draft.inlinePrivateKey}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    inlinePrivateKey: event.target.value,
                  }))
                }
              />
            </div>
          ) : null}

          <div>
            <label className="mb-1 block text-sm font-medium">基础路径</label>
            <Input
              placeholder="/"
              value={draft.basePath}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, basePath: event.target.value }))
              }
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">标签</label>
            <Input
              placeholder="逗号分隔，例如：prod,app"
              value={draft.tags}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, tags: event.target.value }))
              }
            />
          </div>
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => handleOpenChange(false)}>
            取消
          </Button>
          {isEditing ? (
            <Button
              variant="outline"
              onClick={handleTestConnection}
              disabled={saving}
            >
              测试连接
            </Button>
          ) : null}
          <Button onClick={handleSave} disabled={saving}>
            {saving ? "保存中..." : "保存并探测"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export type { NodeEditorDraft };
