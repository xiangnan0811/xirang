import React from "react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { parseSSHKeyType, type SSHKeyRecord, type SSHKeyType } from "@/types/domain";
import type { NodeEditorDraft } from "@/components/node-editor-dialog";

type NodeEditorSSHProps = {
  draft: NodeEditorDraft;
  isEditing: boolean;
  sshKeys: SSHKeyRecord[];
  setDraft: React.Dispatch<React.SetStateAction<NodeEditorDraft>>;
};

function toAuthType(value: string): "key" | "password" {
  if (value === "password") return "password";
  return "key";
}

export function NodeEditorSSH({ draft, isEditing, sshKeys, setDraft }: NodeEditorSSHProps) {
  const { t } = useTranslation();

  return (
    <>
      <div className="grid gap-3 md:grid-cols-[2fr_1fr]">
        <div>
          <label htmlFor="node-edit-host" className="mb-1 block text-sm font-medium">{t("nodeEditor.host")}</label>
          <Input
            id="node-edit-host"
            placeholder="10.10.0.11"
            value={draft.host}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, host: event.target.value }))
            }
          />
        </div>
        <div>
          <label htmlFor="node-edit-port" className="mb-1 block text-sm font-medium">{t("nodeEditor.port")}</label>
          <Input
            id="node-edit-port"
            type="number"
            placeholder="22"
            value={draft.port}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                port: Math.min(65535, Math.max(1, Math.trunc(Number.parseInt(event.target.value, 10) || 22))),
              }))
            }
          />
        </div>
      </div>

      <div>
        <label htmlFor="node-edit-username" className="mb-1 block text-sm font-medium">{t("nodeEditor.sshUsername")}</label>
        <Input
          id="node-edit-username"
          placeholder="root"
          value={draft.username}
          onChange={(event) => {
            const newUsername = event.target.value;
            const isRoot = newUsername.trim() === "" || newUsername.trim() === "root";
            setDraft((prev) => ({ ...prev, username: newUsername, useSudo: isRoot ? false : prev.useSudo }));
          }}
        />
      </div>

      {draft.username.trim() !== "" && draft.username.trim() !== "root" && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={draft.useSudo}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, useSudo: event.target.checked }))
            }
          />
          <span>{t("nodeEditor.useSudo")}</span>
          <span className="text-xs text-muted-foreground">{t("nodeEditor.useSudoHint")}</span>
        </label>
      )}

      <div>
        <label htmlFor="node-edit-auth" className="mb-1 block text-sm font-medium">{t("nodeEditor.authMethod")}</label>
        <Select
          id="node-edit-auth"
          containerClassName="w-full"
          value={draft.authType}
          onChange={(event) =>
            setDraft((prev) => ({
              ...prev,
              authType: toAuthType(event.target.value),
            }))
          }
        >
          <option value="key">{t("nodeEditor.keyAuth")}</option>
          <option value="password">{t("nodeEditor.passwordAuth")}</option>
        </Select>
      </div>

      {draft.authType === "key" ? (
        <div>
          <label htmlFor="node-edit-ssh-key" className="mb-1 block text-sm font-medium">{t("nodeEditor.sshKey")}</label>
          <Select
            id="node-edit-ssh-key"
            containerClassName="w-full"
            value={draft.keyId}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, keyId: event.target.value }))
            }
          >
            <option value="">{t("nodeEditor.selectExistingKey")}</option>
            {sshKeys.map((key) => (
              <option key={key.id} value={key.id}>
                {key.name} ({key.username})
              </option>
            ))}
            <option value="__new__">{t("nodeEditor.newSshKey")}</option>
          </Select>
        </div>
      ) : (
        <div>
          <label htmlFor="node-edit-password" className="mb-1 block text-sm font-medium">{t("nodeEditor.sshPassword")}</label>
          <Input
            id="node-edit-password"
            type="password"
            placeholder={isEditing ? t("nodeEditor.passwordPlaceholderEdit") : t("nodeEditor.passwordPlaceholder")}
            value={draft.password}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, password: event.target.value }))
            }
          />
        </div>
      )}

      {draft.authType === "key" && draft.keyId === "__new__" ? (
        <div className="space-y-2 rounded-md border border-dashed bg-muted/20 p-3">
          <label
            htmlFor="node-edit-inline-private-key"
            className="block text-xs font-medium text-muted-foreground"
          >
            {t("nodeEditor.inlineNewKey")}
          </label>
          <div className="grid gap-2 md:grid-cols-2">
            <div>
              <label htmlFor="node-edit-inline-key-name" className="mb-1 block text-xs font-medium">
                {t("nodeEditor.keyName")}
              </label>
              <Input
                id="node-edit-inline-key-name"
                placeholder={t("nodeEditor.newKeyName")}
                value={draft.inlineKeyName}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, inlineKeyName: event.target.value }))
                }
              />
            </div>
            <div>
              <label htmlFor="node-edit-inline-key-type" className="mb-1 block text-xs font-medium">
                {t("nodeEditor.keyType")}
              </label>
              <Select
                id="node-edit-inline-key-type"
                containerClassName="w-full"
                value={draft.inlineKeyType}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    inlineKeyType: parseSSHKeyType(event.target.value) as SSHKeyType,
                  }))
                }
              >
                <option value="auto">{t("nodeEditor.autoDetect")}</option>
                <option value="rsa">RSA</option>
                <option value="ed25519">ED25519</option>
                <option value="ecdsa">ECDSA</option>
              </Select>
            </div>
          </div>
          <Textarea
            id="node-edit-inline-private-key"
            className="mt-1 min-h-28 text-xs"
            placeholder={t("nodeEditor.privateKeyPlaceholder")}
            value={draft.inlinePrivateKey}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, inlinePrivateKey: event.target.value }))
            }
          />
        </div>
      ) : null}
    </>
  );
}
