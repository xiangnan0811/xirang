import { useEffect, useRef, useState } from "react";
import { KeyRound, Upload } from "lucide-react";
import { toast } from "@/components/ui/toast";
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
import { AppSelect } from "@/components/ui/app-select";
import { AppTextarea } from "@/components/ui/app-textarea";
import { parseSSHKeyType, type NewSSHKeyInput, type SSHKeyRecord } from "@/types/domain";

type SSHKeyDraft = NewSSHKeyInput & {
  id?: string;
};

const emptyDraft: SSHKeyDraft = {
  name: "",
  username: "root",
  keyType: "auto",
  privateKey: "",
};

function toDraft(key: SSHKeyRecord): SSHKeyDraft {
  return {
    id: key.id,
    name: key.name,
    username: key.username,
    keyType: key.keyType,
    privateKey: "",
  };
}

type SSHKeyEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingKey?: SSHKeyRecord | null;
  onSave: (draft: SSHKeyDraft) => Promise<void>;
};

export function SSHKeyEditorDialog({
  open,
  onOpenChange,
  editingKey,
  onSave,
}: SSHKeyEditorDialogProps) {
  const [draft, setDraft] = useState<SSHKeyDraft>(emptyDraft);
  const [saving, setSaving] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleFileUpload = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > 128 * 1024) {
      toast.error("文件过大，SSH 密钥文件通常不超过 128 KB");
      event.target.value = "";
      return;
    }
    const reader = new FileReader();
    reader.onload = (e) => {
      const content = e.target?.result;
      if (typeof content === "string") {
        setDraft((prev) => ({ ...prev, privateKey: content }));
      }
    };
    reader.onerror = () => {
      toast.error("密钥文件读取失败，请检查文件是否有效");
    };
    reader.readAsText(file);
    // 重置 input 以便同一文件可再次选择
    event.target.value = "";
  };

  const isEditing = Boolean(draft.id);

  useEffect(() => {
    if (!open) {
      setDraft(emptyDraft);
      return;
    }
    setDraft(editingKey ? toDraft(editingKey) : emptyDraft);
  }, [editingKey, open]);

  const handleOpenChange = (next: boolean) => {
    onOpenChange(next);
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(draft);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent size="sm">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <KeyRound className="size-5 text-primary" />
            <DialogTitle>
              {isEditing ? `编辑 SSH Key - ${draft.name}` : "新增 SSH Key"}
            </DialogTitle>
          </div>
          <DialogDescription>
            {isEditing
              ? "修改密钥配置。私钥留空表示不修改，填写后会更新私钥。"
              : "添加新的 SSH 密钥，用于节点连接认证。"}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-3">
          <div>
            <label htmlFor="ssh-key-edit-name" className="mb-1 block text-sm font-medium">Key 名称</label>
            <Input id="ssh-key-edit-name"               placeholder="例如：prod-deploy-key"
              value={draft.name}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, name: event.target.value }))
              }
            />
          </div>

          <div>
            <label htmlFor="ssh-key-edit-username" className="mb-1 block text-sm font-medium">默认用户名</label>
            <Input id="ssh-key-edit-username"               placeholder="默认用户名"
              value={draft.username}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, username: event.target.value }))
              }
            />
          </div>

          <div>
            <label htmlFor="ssh-key-edit-type" className="mb-1 block text-sm font-medium">密钥类型</label>
            <AppSelect id="ssh-key-edit-type"               className="w-full"
              value={draft.keyType}
              onChange={(event) =>
                setDraft((prev) => ({
                  ...prev,
                  keyType: parseSSHKeyType(event.target.value),
                }))
              }
            >
              <option value="auto">自动识别（推荐）</option>
              <option value="rsa">RSA</option>
              <option value="ed25519">ED25519</option>
              <option value="ecdsa">ECDSA</option>
            </AppSelect>
            <p className="mt-1 text-xs text-muted-foreground">
              保存时会校验私钥内容与所选类型是否一致。
            </p>
          </div>

          <div>
            <div className="mb-1 flex items-center justify-between">
              <label htmlFor="ssh-key-edit-private-key" className="block text-sm font-medium">
                {isEditing ? "私钥内容（留空表示不修改）" : "私钥内容"}
              </label>
              <button
                type="button"
                className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors duration-200 ease-out hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
                onClick={() => fileInputRef.current?.click()}
                disabled={saving}
              >
                <Upload className="size-3.5" />
                上传密钥文件
              </button>
              <input
                ref={fileInputRef}
                type="file"
                className="hidden"
                accept=".pem,.key,.pub,.ppk,.openssh"
                onChange={handleFileUpload}
              />
            </div>
            <AppTextarea
              id="ssh-key-edit-private-key"
              className="min-h-36 text-xs"
              placeholder={isEditing
                ? "留空表示不修改现有私钥；如需更新请粘贴新的 OpenSSH 私钥"
                : "粘贴 OpenSSH 私钥（支持粘贴带 \\n 转义的内容）"}
              value={draft.privateKey}
              onChange={(event) =>
                setDraft((prev) => ({
                  ...prev,
                  privateKey: event.target.value,
                }))
              }
            />
          </div>
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => handleOpenChange(false)}>
            取消
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? "保存中..." : isEditing ? "更新 Key" : "保存 Key"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export type { SSHKeyDraft };
