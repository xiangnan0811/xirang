import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { Upload } from "lucide-react";
import { Button } from "@/components/ui/button";
import { AppSelect } from "@/components/ui/app-select";
import { AppTextarea } from "@/components/ui/app-textarea";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import {
  parseSSHKeyType,
  type NodeRecord,
  type SSHKeyRecord,
  type SSHKeyType,
} from "@/types/domain";

// Step 1: select which key to rotate
interface RotationPreviewProps {
  rotatableKeys: SSHKeyRecord[];
  keyUsageMap: Map<string, NodeRecord[]>;
  selectedKey: SSHKeyRecord | null;
  onSelectKey: (key: SSHKeyRecord) => void;
  onNext: () => void;
}

export function RotationPreview({
  rotatableKeys,
  keyUsageMap,
  selectedKey,
  onSelectKey,
  onNext,
}: RotationPreviewProps) {
  const { t } = useTranslation();

  return (
    <>
      <p className="text-sm text-muted-foreground">
        {t("sshKeys.rotationSelectKeyDesc")}
      </p>
      <div className="max-h-64 space-y-2 overflow-y-auto thin-scrollbar">
        {rotatableKeys.map((key) => {
          const nodes = keyUsageMap.get(key.id) ?? [];
          const isSelected = selectedKey?.id === key.id;
          return (
            <label
              key={key.id}
              className={`flex cursor-pointer items-center gap-3 rounded-lg border p-3 transition-colors ${
                isSelected
                  ? "border-primary/50 bg-primary/5"
                  : "border-border/60 hover:border-border hover:bg-accent/30"
              }`}
            >
              <input
                type="radio"
                name="rotation-key"
                className="accent-primary"
                checked={isSelected}
                onChange={() => onSelectKey(key)}
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{key.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {key.username}
                  </span>
                </div>
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <span>{String(key.keyType).toUpperCase()}</span>
                  <span>&middot;</span>
                  <span>
                    {t("sshKeys.inUseNodes", { count: nodes.length })}
                  </span>
                </div>
              </div>
            </label>
          );
        })}
      </div>
      <div className="flex justify-end pt-2">
        <Button disabled={!selectedKey} onClick={onNext}>
          {t("sshKeys.rotationNext")}
        </Button>
      </div>
    </>
  );
}

// Step 2: upload new key material
interface RotationUploadProps {
  selectedKey: SSHKeyRecord | null;
  newKeyName: string;
  onNewKeyNameChange: (name: string) => void;
  newKeyType: SSHKeyType;
  onNewKeyTypeChange: (type: SSHKeyType) => void;
  newPrivateKey: string;
  onNewPrivateKeyChange: (key: string) => void;
  preselectedKey?: SSHKeyRecord | null;
  onBack: () => void;
  onNext: () => void;
}

export function RotationUpload({
  selectedKey,
  newKeyName,
  onNewKeyNameChange,
  newKeyType,
  onNewKeyTypeChange,
  newPrivateKey,
  onNewPrivateKeyChange,
  preselectedKey,
  onBack,
  onNext,
}: RotationUploadProps) {
  const { t } = useTranslation();
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
        onNewPrivateKeyChange(content);
      }
    };
    reader.onerror = () => {
      toast.error(t("sshKeys.fileReadFailed"));
    };
    reader.readAsText(file);
    event.target.value = "";
  };

  return (
    <>
      <p className="text-sm text-muted-foreground">
        {t("sshKeys.rotationUploadKeyDesc", { name: selectedKey?.name })}
      </p>

      <div>
        <label
          htmlFor="rotation-key-name"
          className="mb-1 block text-sm font-medium"
        >
          {t("sshKeys.keyName")}
        </label>
        <Input
          id="rotation-key-name"
          value={newKeyName}
          onChange={(e) => onNewKeyNameChange(e.target.value)}
        />
      </div>

      <div>
        <label
          htmlFor="rotation-key-type"
          className="mb-1 block text-sm font-medium"
        >
          {t("sshKeys.keyTypeLabel")}
        </label>
        <AppSelect
          id="rotation-key-type"
          containerClassName="w-full"
          value={newKeyType}
          onChange={(e) => onNewKeyTypeChange(parseSSHKeyType(e.target.value))}
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
            htmlFor="rotation-private-key"
            className="block text-sm font-medium"
          >
            {t("sshKeys.privateKeyLabel")}
          </label>
          <button
            type="button"
            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors duration-200 ease-out hover:bg-accent hover:text-foreground"
            onClick={() => fileInputRef.current?.click()}
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
          id="rotation-private-key"
          className="min-h-36 text-xs"
          placeholder={t("sshKeys.privateKeyPlaceholder")}
          value={newPrivateKey}
          onChange={(e) => onNewPrivateKeyChange(e.target.value)}
        />
      </div>

      <div className="flex justify-between pt-2">
        {!preselectedKey ? (
          <Button variant="outline" onClick={onBack}>
            {t("sshKeys.rotationPrev")}
          </Button>
        ) : (
          <div />
        )}
        <Button disabled={!newPrivateKey.trim()} onClick={onNext}>
          {t("sshKeys.rotationNext")}
        </Button>
      </div>
    </>
  );
}
