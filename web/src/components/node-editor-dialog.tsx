import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ServerCog } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { NodeEditorSSH } from "@/components/node-editor.ssh";
import { NodeEditorBackup } from "@/components/node-editor.backup";
import { NodeEditorMeta } from "@/components/node-editor.meta";
import {
  type NewNodeInput,
  type NodeRecord,
  type SSHKeyRecord,
  type SSHKeyType,
} from "@/types/domain";

export type NodeEditorDraft = {
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
  useSudo: boolean;
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
  useSudo: false,
  inlineKeyName: "",
  inlineKeyType: "auto",
  inlinePrivateKey: "",
  maintenanceStart: "",
  maintenanceEnd: "",
};

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
  if (!Number.isFinite(value)) return 22;
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
    useSudo: node.useSudo ?? false,
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
    keyId: draft.authType === "key" && !useInlineKey ? draft.keyId || null : null,
    password: draft.authType === "password" ? draft.password : undefined,
    tags: draft.tags,
    basePath: draft.basePath || "/",
    backupDir: draft.backupDir,
    useSudo: draft.useSudo,
    inlineKeyName: useInlineKey ? draft.inlineKeyName : undefined,
    inlineKeyType: useInlineKey ? draft.inlineKeyType : undefined,
    inlinePrivateKey: useInlineKey ? draft.inlinePrivateKey : undefined,
    maintenanceStart: fromDatetimeLocal(draft.maintenanceStart),
    maintenanceEnd: fromDatetimeLocal(draft.maintenanceEnd),
  };
}

function sanitizeForBackupDir(name: string): string {
  let s = name.toLowerCase();
  s = s.replace(/[^a-z0-9\-_.]/g, "-");
  s = s.replace(/-{2,}/g, "-");
  s = s.replace(/^-+|-+$/g, "");
  return s.length < 2 ? "" : s;
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
  const [draft, setDraft] = useDialogDraft<NodeEditorDraft, NodeRecord>(
    open,
    emptyDraft,
    editingNode,
    toDraft,
  );
  const [saving, setSaving] = useState(false);
  const [backupDirManuallyEdited, setBackupDirManuallyEdited] = useState(false);

  const isEditing = Boolean(draft.id);

  // 编辑已有节点时，backupDir 已存在，视为手动编辑过
  const editingNodeId = editingNode?.id;
  if (editingNodeId && !backupDirManuallyEdited && draft.id === editingNodeId && draft.backupDir) {
    setBackupDirManuallyEdited(true);
  }
  // 对话框关闭后重置标记
  if (!open && backupDirManuallyEdited) {
    setBackupDirManuallyEdited(false);
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
      title={
        isEditing
          ? t("nodeEditor.titleEdit", { name: draft.name })
          : t("nodeEditor.titleCreate")
      }
      description={
        isEditing
          ? t("nodeEditor.descEdit", { name: draft.name })
          : t("nodeEditor.descCreate")
      }
      saving={saving}
      onSubmit={handleSave}
      submitLabel={t("nodeEditor.submitLabel")}
      extraFooter={
        isEditing ? (
          <Button variant="outline" onClick={handleTestConnection} disabled={saving}>
            {t("nodeEditor.testConnection")}
          </Button>
        ) : null
      }
    >
      {/* Node name — kept in shell since it drives backupDir auto-fill */}
      <div>
        <label htmlFor="node-edit-name" className="mb-1 block text-sm font-medium">
          {t("nodeEditor.nodeName")}
        </label>
        <Input
          id="node-edit-name"
          placeholder={t("nodeEditor.namePlaceholder")}
          value={draft.name}
          onChange={(event) => {
            const newName = event.target.value;
            if (!backupDirManuallyEdited) {
              setDraft((prev) => ({
                ...prev,
                name: newName,
                backupDir: sanitizeForBackupDir(newName),
              }));
            } else {
              setDraft((prev) => ({ ...prev, name: newName }));
            }
          }}
        />
      </div>

      {/* SSH section */}
      <NodeEditorSSH
        draft={draft}
        isEditing={isEditing}
        sshKeys={sshKeys}
        setDraft={setDraft}
      />

      {/* Backup section */}
      <NodeEditorBackup
        draft={draft}
        isEditing={isEditing}
        backupDirManuallyEdited={backupDirManuallyEdited}
        setBackupDirManuallyEdited={setBackupDirManuallyEdited}
        setDraft={setDraft}
      />

      {/* Meta section (tags + maintenance window) */}
      <NodeEditorMeta draft={draft} setDraft={setDraft} />
    </FormDialog>
  );
}
