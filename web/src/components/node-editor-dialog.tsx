import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ServerCog } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { AppTextarea } from "@/components/ui/app-textarea";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import {
  parseSSHKeyType,
  type NewNodeInput,
  type NodeRecord,
  type SSHKeyRecord,
  type SSHKeyType,
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
  backupDir: string;
  inlineKeyName: string;
  inlineKeyType: SSHKeyType;
  inlinePrivateKey: string;
  maintenanceStart: string;
  maintenanceEnd: string;
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
  backupDir: "",
  inlineKeyName: "",
  inlineKeyType: "auto",
  inlinePrivateKey: "",
  maintenanceStart: "",
  maintenanceEnd: "",
};

function toAuthType(value: string): "key" | "password" {
  if (value === "password") {
    return "password";
  }
  return "key";
}

function toDatetimeLocal(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromDatetimeLocal(value: string): string {
  if (!value) return "";
  const d = new Date(value);
  if (isNaN(d.getTime())) return "";
  return d.toISOString();
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
    backupDir: node.backupDir || "",
    inlineKeyName: "",
    inlineKeyType: "auto",
    inlinePrivateKey: "",
    maintenanceStart: toDatetimeLocal(node.maintenanceStart),
    maintenanceEnd: toDatetimeLocal(node.maintenanceEnd),
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
    backupDir: draft.backupDir,
    inlineKeyName: useInlineKey ? draft.inlineKeyName : undefined,
    inlineKeyType: useInlineKey ? draft.inlineKeyType : undefined,
    inlinePrivateKey: useInlineKey ? draft.inlinePrivateKey : undefined,
    maintenanceStart: fromDatetimeLocal(draft.maintenanceStart),
    maintenanceEnd: fromDatetimeLocal(draft.maintenanceEnd),
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
  const { t } = useTranslation();
  const [draft, setDraft] = useDialogDraft<NodeEditorDraft, NodeRecord>(open, emptyDraft, editingNode, toDraft);
  const [saving, setSaving] = useState(false);
  const [backupDirManuallyEdited, setBackupDirManuallyEdited] = useState(false);

  const isEditing = Boolean(draft.id);

  // 编辑已有节点时，backupDir 已存在，视为手动编辑过
  // useDialogDraft 在 editingNode 变化时会重建 draft，此处同步重置标记
  const editingNodeId = editingNode?.id;
  if (editingNodeId && !backupDirManuallyEdited && draft.id === editingNodeId && draft.backupDir) {
    setBackupDirManuallyEdited(true);
  }
  // 对话框关闭后重置标记
  if (!open && backupDirManuallyEdited) {
    setBackupDirManuallyEdited(false);
  }

  function sanitizeForBackupDir(name: string): string {
    let s = name.toLowerCase();
    s = s.replace(/[^a-z0-9\-_.]/g, '-');
    s = s.replace(/-{2,}/g, '-');
    s = s.replace(/^-+|-+$/g, '');
    return s.length < 2 ? '' : s;
  }

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
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      icon={<ServerCog className="size-5 text-primary" />}
      title={isEditing ? t('nodeEditor.titleEdit', { name: draft.name }) : t('nodeEditor.titleCreate')}
      description={isEditing
        ? t('nodeEditor.descEdit', { name: draft.name })
        : t('nodeEditor.descCreate')}
      saving={saving}
      onSubmit={handleSave}
      submitLabel={t('nodeEditor.submitLabel')}
      extraFooter={isEditing ? (
        <Button
          variant="outline"
          onClick={handleTestConnection}
          disabled={saving}
        >
          {t('nodeEditor.testConnection')}
        </Button>
      ) : null}
    >
      <div>
        <label htmlFor="node-edit-name" className="mb-1 block text-sm font-medium">{t('nodeEditor.nodeName')}</label>
        <Input id="node-edit-name" placeholder={t('nodeEditor.namePlaceholder')}
          value={draft.name}
          onChange={(event) => {
            const newName = event.target.value;
            if (!backupDirManuallyEdited) {
              setDraft((prev) => ({ ...prev, name: newName, backupDir: sanitizeForBackupDir(newName) }));
            } else {
              setDraft((prev) => ({ ...prev, name: newName }));
            }
          }}
        />
      </div>

      <div className="grid gap-3 md:grid-cols-[2fr_1fr]">
        <div>
          <label htmlFor="node-edit-host" className="mb-1 block text-sm font-medium">{t('nodeEditor.host')}</label>
          <Input id="node-edit-host" placeholder="10.10.0.11"
            value={draft.host}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, host: event.target.value }))
            }
          />
        </div>
        <div>
          <label htmlFor="node-edit-port" className="mb-1 block text-sm font-medium">{t('nodeEditor.port')}</label>
          <Input id="node-edit-port" type="number"
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
        <label htmlFor="node-edit-username" className="mb-1 block text-sm font-medium">{t('nodeEditor.sshUsername')}</label>
        <Input id="node-edit-username" placeholder="root"
          value={draft.username}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, username: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="node-edit-auth" className="mb-1 block text-sm font-medium">{t('nodeEditor.authMethod')}</label>
        <AppSelect id="node-edit-auth" containerClassName="w-full"
          value={draft.authType}
          onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                authType: toAuthType(event.target.value),
              }))
            }
          >
          <option value="key">{t('nodeEditor.keyAuth')}</option>
          <option value="password">{t('nodeEditor.passwordAuth')}</option>
        </AppSelect>
      </div>

      {draft.authType === "key" ? (
        <div>
          <label htmlFor="node-edit-ssh-key" className="mb-1 block text-sm font-medium">{t('nodeEditor.sshKey')}</label>
          <AppSelect id="node-edit-ssh-key" containerClassName="w-full"
            value={draft.keyId}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, keyId: event.target.value }))
            }
          >
            <option value="">{t('nodeEditor.selectExistingKey')}</option>
            {sshKeys.map((key) => (
              <option key={key.id} value={key.id}>
                {key.name} ({key.username})
              </option>
            ))}
            <option value="__new__">{t('nodeEditor.newSshKey')}</option>
          </AppSelect>
        </div>
      ) : (
        <div>
          <label htmlFor="node-edit-password" className="mb-1 block text-sm font-medium">{t('nodeEditor.sshPassword')}</label>
          <Input id="node-edit-password" type="password"
            placeholder={isEditing ? t('nodeEditor.passwordPlaceholderEdit') : t('nodeEditor.passwordPlaceholder')}
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
          <label htmlFor="node-edit-inline-private-key" className="block text-xs font-medium text-muted-foreground">
            {t('nodeEditor.inlineNewKey')}
          </label>
          <div className="grid gap-2 md:grid-cols-2">
            <div>
              <label htmlFor="node-edit-inline-key-name" className="mb-1 block text-xs font-medium">
                {t('nodeEditor.keyName')}
              </label>
              <Input
                id="node-edit-inline-key-name"
                placeholder={t('nodeEditor.newKeyName')}
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
              <label htmlFor="node-edit-inline-key-type" className="mb-1 block text-xs font-medium">
                {t('nodeEditor.keyType')}
              </label>
              <AppSelect
                id="node-edit-inline-key-type"
                containerClassName="w-full"
                value={draft.inlineKeyType}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    inlineKeyType: parseSSHKeyType(event.target.value),
                  }))
                }
              >
                <option value="auto">{t('nodeEditor.autoDetect')}</option>
                <option value="rsa">RSA</option>
                <option value="ed25519">ED25519</option>
                <option value="ecdsa">ECDSA</option>
              </AppSelect>
            </div>
          </div>
          <AppTextarea
            id="node-edit-inline-private-key"
            className="mt-1 min-h-28 text-xs"
            placeholder={t('nodeEditor.privateKeyPlaceholder')}
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
        <label htmlFor="node-edit-base-path" className="mb-1 block text-sm font-medium">{t('nodeEditor.basePath')}</label>
        <Input id="node-edit-base-path" placeholder="/"
          value={draft.basePath}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, basePath: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="node-edit-backup-dir" className="mb-1 block text-sm font-medium">
          {t('nodeEditor.backupDir')}
        </label>
        <Input
          id="node-edit-backup-dir"
          placeholder={t('nodeEditor.backupDirPlaceholder')}
          value={draft.backupDir}
          onChange={(event) => {
            setBackupDirManuallyEdited(true);
            setDraft((prev) => ({ ...prev, backupDir: event.target.value }));
          }}
        />
        <p className="mt-1 text-xs text-muted-foreground">
          {t('nodeEditor.backupDirHint')}
        </p>
        {draft.backupDir && (
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t('nodeEditor.backupDirPreview', { dir: draft.backupDir })}
          </p>
        )}
        {isEditing && draft.backupDir && (
          <p className="mt-0.5 text-xs text-yellow-600 dark:text-yellow-400">
            {t('nodeEditor.backupDirChangeWarning')}
          </p>
        )}
        {!backupDirManuallyEdited && /[^\x00-\x7F]/.test(draft.name) && (
          <p className="mt-0.5 text-xs text-yellow-600 dark:text-yellow-400">
            {t('nodeEditor.backupDirNonAsciiWarning')}
          </p>
        )}
      </div>

      <div>
        <label htmlFor="node-edit-tags" className="mb-1 block text-sm font-medium">{t('nodeEditor.tags')}</label>
        <Input id="node-edit-tags" placeholder={t('nodeEditor.tagsPlaceholder')}
          value={draft.tags}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, tags: event.target.value }))
          }
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="node-edit-maint-start" className="mb-1 block text-sm font-medium">{t('nodeEditor.maintenanceStart')}</label>
          <Input id="node-edit-maint-start" type="datetime-local"
            value={draft.maintenanceStart}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, maintenanceStart: event.target.value }))
            }
          />
        </div>
        <div>
          <label htmlFor="node-edit-maint-end" className="mb-1 block text-sm font-medium">{t('nodeEditor.maintenanceEnd')}</label>
          <Input id="node-edit-maint-end" type="datetime-local"
            value={draft.maintenanceEnd}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, maintenanceEnd: event.target.value }))
            }
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">{t('nodeEditor.maintenanceHint')}</p>
    </FormDialog>
  );
}

export type { NodeEditorDraft };
