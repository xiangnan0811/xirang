import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Upload } from "lucide-react";
import { toast } from "@/components/ui/toast";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { AppTextarea } from "@/components/ui/app-textarea";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import {
  parseSSHKeyType,
  type NewSSHKeyInput,
  type SSHKeyRecord,
} from "@/types/domain";

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
  const { t } = useTranslation();
  const [draft, setDraft] = useDialogDraft<SSHKeyDraft, SSHKeyRecord>(
    open,
    emptyDraft,
    editingKey,
    toDraft,
  );
  const [saving, setSaving] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleFileUpload = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > 128 * 1024) {
      toast.error(t("sshKeys.fileTooLarge"));
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
      toast.error(t("sshKeys.fileReadFailed"));
    };
    reader.readAsText(file);
    // 重置 input 以便同一文件可再次选择
    event.target.value = "";
  };

  const isEditing = Boolean(draft.id);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(draft);
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="sm"
      icon={<KeyRound className="size-5 text-primary" />}
      title={
        isEditing
          ? t("sshKeys.editKeyTitle", { name: draft.name })
          : t("sshKeys.addKey")
      }
      description={
        isEditing ? t("sshKeys.editKeyDesc") : t("sshKeys.addKeyDesc")
      }
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? t("sshKeys.updateKey") : t("sshKeys.saveKey")}
    >
      <div>
        <label
          htmlFor="ssh-key-edit-name"
          className="mb-1 block text-sm font-medium"
        >
          {t("sshKeys.keyName")}
        </label>
        <Input
          id="ssh-key-edit-name"
          placeholder="prod-deploy-key"
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
        />
      </div>

      <div>
        <label
          htmlFor="ssh-key-edit-username"
          className="mb-1 block text-sm font-medium"
        >
          {t("sshKeys.defaultUsername")}
        </label>
        <Input
          id="ssh-key-edit-username"
          placeholder={t("sshKeys.defaultUsername")}
          value={draft.username}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, username: event.target.value }))
          }
        />
      </div>

      <div>
        <label
          htmlFor="ssh-key-edit-type"
          className="mb-1 block text-sm font-medium"
        >
          {t("sshKeys.keyTypeLabel")}
        </label>
        <AppSelect
          id="ssh-key-edit-type"
          containerClassName="w-full"
          value={draft.keyType}
          onChange={(event) =>
            setDraft((prev) => ({
              ...prev,
              keyType: parseSSHKeyType(event.target.value),
            }))
          }
        >
          <option value="auto">{t("sshKeys.keyTypeAuto")}</option>
          <option value="rsa">RSA</option>
          <option value="ed25519">ED25519</option>
          <option value="ecdsa">ECDSA</option>
        </AppSelect>
        <p className="mt-1 text-xs text-muted-foreground">
          {t("sshKeys.keyTypeHint")}
        </p>
      </div>

      <div>
        <div className="mb-1 flex items-center justify-between">
          <label
            htmlFor="ssh-key-edit-private-key"
            className="block text-sm font-medium"
          >
            {isEditing
              ? t("sshKeys.privateKeyLabelEdit")
              : t("sshKeys.privateKeyLabel")}
          </label>
          <button
            type="button"
            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors duration-200 ease-out hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
            onClick={() => fileInputRef.current?.click()}
            disabled={saving}
          >
            <Upload className="size-3.5" />
            {t("sshKeys.uploadKeyFile")}
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
          placeholder={
            isEditing
              ? t("sshKeys.privateKeyPlaceholderEdit")
              : t("sshKeys.privateKeyPlaceholder")
          }
          value={draft.privateKey}
          onChange={(event) =>
            setDraft((prev) => ({
              ...prev,
              privateKey: event.target.value,
            }))
          }
        />
      </div>
    </FormDialog>
  );
}

export type { SSHKeyDraft };
