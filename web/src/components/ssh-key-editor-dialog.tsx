import { useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Upload } from "lucide-react";
import { toast } from "@/components/ui/toast-sonner";
import { FormDialog, type DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { InlineAlert } from "@/components/ui/inline-alert";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { useAuth } from "@/context/auth-context.hooks";
import {
  parseSSHKeyType,
  type NewSSHKeyInput,
  type SSHKeyRecord,
} from "@/types/domain";
import { preserveSSHPurposeScope, retiredSSHPurposesIn } from "@/lib/ssh-purpose-scope";
import { ApiError, getAuthSessionGeneration, isAuthTransitionActive, isStepUpRequiredError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/step-up-storage";
import {
  type SSHKeyReplacementOptions,
  SSHKeyReplacementProofError,
} from "@/lib/ssh-key-replacement";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";

type SSHKeyDraft = NewSSHKeyInput & {
  id?: string;
};

const emptyDraft: SSHKeyDraft = {
  name: "",
  username: "root",
  keyType: "auto",
  privateKey: "",
  disabled: false,
  expiresAt: "",
  allowedPurposes: "",
  allowedNodeIds: "",
  allowedNodeTags: "",
};

function toDraft(key: SSHKeyRecord): SSHKeyDraft {
  return {
    id: key.id,
    name: key.name,
    username: key.username,
    keyType: key.keyType,
    privateKey: "",
    disabled: key.disabled,
    expiresAt: key.expiresAt ?? "",
    allowedPurposes: key.allowedPurposes,
    allowedNodeIds: key.allowedNodeIds,
    allowedNodeTags: key.allowedNodeTags,
  };
}

type SSHKeyEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingKey?: SSHKeyRecord | null;
  onSave: (draft: SSHKeyDraft, options: SSHKeyReplacementOptions) => Promise<void>;
  onCloseAutoFocus?: DialogCloseAutoFocus;
};

export function SSHKeyEditorDialog(props: SSHKeyEditorDialogProps) {
  const { role, token, authTransitioning = false } = useAuth();
  const authGeneration = getAuthSessionGeneration();
  const sessionKey = [
    props.open ? "1" : "0",
    props.editingKey?.id ?? "",
    token ?? "",
    role ?? "",
    String(authGeneration),
    authTransitioning ? "1" : "0",
  ].join("|");
  return <SSHKeyEditorDialogSession key={sessionKey} {...props} />;
}

function SSHKeyEditorDialogSession({
  open,
  onOpenChange,
  editingKey,
  onSave,
  onCloseAutoFocus,
}: SSHKeyEditorDialogProps) {
  const { t } = useTranslation();
  const { totpEnabled, ensureStepUpProof } = useAuth();
  const [draft, setDraft] = useDialogDraft<SSHKeyDraft, SSHKeyRecord>(
    open,
    emptyDraft,
    editingKey,
    toDraft,
  );
  const [saving, setSaving] = useState(false);
  const [editorSaveDisabled, setEditorSaveDisabled] = useState(false);
  const [unknownWarning, setUnknownWarning] = useState<string | null>(null);
  const [determinateError, setDeterminateError] = useState<string | null>(null);
  const [prerequisiteError, setPrerequisiteError] = useState(false);

  const fileInputRef = useRef<HTMLInputElement>(null);
  const generationRef = useRef(0);
  const submitLockRef = useRef(false);
  const operationAbortRef = useRef<AbortController | null>(null);
  const fileReaderRef = useRef<FileReader | null>(null);
  const fileReaderGenerationRef = useRef(0);

  useLayoutEffect(() => {
    generationRef.current += 1;
    return () => {
      generationRef.current += 1;
      operationAbortRef.current?.abort();
      fileReaderRef.current?.abort();
      fileReaderRef.current = null;
    };
  }, []);

  // Synchronously revoke any pending FileReader so its completion cannot
  // mutate the draft. Bumping the reader generation invalidates the callback
  // even if abort() is not honoured before the load event fires.
  const invalidatePendingFileRead = () => {
    fileReaderGenerationRef.current += 1;
    fileReaderRef.current?.abort();
    fileReaderRef.current = null;
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      operationAbortRef.current?.abort();
      invalidatePendingFileRead();
    }
    onOpenChange(nextOpen);
  };

  const isEditing = Boolean(draft.id);
  const hasCandidatePrivateKey = draft.privateKey.trim().length > 0;
  const showEarlyPrerequisite = isEditing && hasCandidatePrivateKey && totpEnabled === false;

  const handleFileUpload = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > 128 * 1024) {
      toast.error(t("sshKeys.fileTooLarge"));
      event.target.value = "";
      return;
    }
    fileReaderRef.current?.abort();
    const reader = new FileReader();
    fileReaderRef.current = reader;
    const readerGen = ++fileReaderGenerationRef.current;
    const localGen = generationRef.current;

    reader.onload = (e) => {
      if (
        fileReaderRef.current !== reader ||
        fileReaderGenerationRef.current !== readerGen ||
        generationRef.current !== localGen
      ) {
        return;
      }
      const content = e.target?.result;
      if (typeof content === "string") {
        setDraft((prev) => ({ ...prev, privateKey: content }));
        setDeterminateError(null);
      }
    };
    reader.onerror = () => {
      if (
        fileReaderRef.current !== reader ||
        fileReaderGenerationRef.current !== readerGen ||
        generationRef.current !== localGen
      ) {
        return;
      }
      toast.error(t("sshKeys.fileReadFailed"));
    };
    reader.readAsText(file);
    // 重置 input 以便同一文件可再次选择
    event.target.value = "";
  };

  const handleSave = async () => {
    if (submitLockRef.current || saving || editorSaveDisabled) return;
    submitLockRef.current = true;
    // The submission captures the current draft; a pending file read must not
    // later replace the displayed candidate or repopulate a cleared secret.
    invalidatePendingFileRead();
    setSaving(true);
    setDeterminateError(null);
    setPrerequisiteError(false);

    operationAbortRef.current?.abort();
    const controller = new AbortController();
    operationAbortRef.current = controller;

    const localGen = generationRef.current;
    const sessionGen = getAuthSessionGeneration();

    const isCurrent = () => {
      return (
        open &&
        generationRef.current === localGen &&
        getAuthSessionGeneration() === sessionGen &&
        !isAuthTransitionActive() &&
        !controller.signal.aborted
      );
    };

    const options: SSHKeyReplacementOptions = {
      signal: controller.signal,
      isCurrent,
      requestProof: async () => {
        if (!isCurrent()) {
          throw new DOMException("SSH key replacement no longer belongs to its owner", "AbortError");
        }
        return await ensureStepUpProof(STEP_UP_ACTIONS.sshKeyReplace, {
          persist: false,
          reuseCached: false,
          signal: controller.signal,
        });
      },
    };

    try {
      await onSave(
        {
          ...draft,
          expiresAt: draft.expiresAt ?? "",
          allowedPurposes: preserveSSHPurposeScope(draft.allowedPurposes),
        },
        options,
      );
    } catch (error) {
      if (!isCurrent() || (error instanceof Error && error.name === "AbortError")) {
        return;
      }
      handleSaveError(error);
    } finally {
      if (
        isCurrent() &&
        operationAbortRef.current === controller &&
        !controller.signal.aborted
      ) {
        submitLockRef.current = false;
        setSaving(false);
      }
    }
  };

  const handleSaveError = (error: unknown) => {
    // 1. SSHKeyReplacementProofError
    if (error instanceof SSHKeyReplacementProofError) {
      const cause = error.cause;
      if (cause instanceof Error && cause.message === t("stepUp.cancelled")) {
        // OTP cancellation keeps same current draft, no success
        return;
      }
      if (
        cause instanceof StepUpPrerequisiteError ||
        (cause instanceof Error && (cause as { code?: string }).code === "TOTP_REQUIRED")
      ) {
        setPrerequisiteError(true);
        return;
      }
      // other proof failures safe localized '二次验证未完成，未继续保存'
      setDeterminateError(t("sshKeys.stepUpIncomplete"));
      return;
    }

    // 2. Second real challenge: HTTP 403 STEP_UP_REQUIRED. A 2xx failure envelope is not a determinate refusal.
    if (isStepUpRequiredError(error) && error.httpStatus === 403) {
      setDeterminateError(t("sshKeys.stepUpExpired"));
      return;
    }

    // 3. Determinate HTTP400/403/404/409/413 keep draft and safe error, allow new explicit submission
    if (error instanceof ApiError) {
      const status = error.httpStatus ?? error.status;
      if (status === 400 || status === 403 || status === 404 || status === 409 || status === 413) {
        if (status === 404) {
          setDeterminateError(t("sshKeys.editKeyMissing"));
        } else if (status === 403) {
          setDeterminateError(t("sshKeys.editForbidden"));
        } else if (status === 413) {
          setDeterminateError(t("sshKeys.editPayloadTooLarge"));
        } else if (status === 409) {
          setDeterminateError(t("sshKeys.editConflict"));
        } else {
          setDeterminateError(t("sshKeys.editInvalid"));
        }
        return;
      }
    }

    // 4. Network/408/429/5xx/decode/unknown submission failure => UNKNOWN:
    // clear private input, visible localized uncertain-result warning, permanently disable save for that form
    invalidatePendingFileRead();
    setDraft((prev) => ({ ...prev, privateKey: "" }));
    setEditorSaveDisabled(true);
    setUnknownWarning(t("sshKeys.rotationSaveUnknown"));
  };

  const historicalPurposes = retiredSSHPurposesIn(draft.allowedPurposes);
  const fieldsDisabled = saving || editorSaveDisabled;

  return (
    <FormDialog
      open={open}
      onOpenChange={handleOpenChange}
      onCloseAutoFocus={onCloseAutoFocus}
      size="sm"
      icon={<KeyRound className="size-5 text-primary" aria-hidden />}
      title={
        isEditing
          ? t("sshKeys.editKeyTitle", { name: draft.name })
          : t("sshKeys.addKey")
      }
      description={
        isEditing ? t("sshKeys.editKeyDesc") : t("sshKeys.addKeyDesc")
      }
      saving={fieldsDisabled}
      savingLabel={editorSaveDisabled ? (isEditing ? t("sshKeys.updateKey") : t("sshKeys.saveKey")) : undefined}
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
          disabled={fieldsDisabled}
          onChange={(event) => {
            setDeterminateError(null);
            setDraft((prev) => ({ ...prev, name: event.target.value }));
          }}
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
          disabled={fieldsDisabled}
          onChange={(event) => {
            setDeterminateError(null);
            setDraft((prev) => ({ ...prev, username: event.target.value }));
          }}
        />
      </div>

      <div>
        <label
          htmlFor="ssh-key-edit-type"
          className="mb-1 block text-sm font-medium"
        >
          {t("sshKeys.keyTypeLabel")}
        </label>
        <Select
          id="ssh-key-edit-type"
          containerClassName="w-full"
          value={draft.keyType}
          disabled={fieldsDisabled}
          onChange={(event) => {
            setDeterminateError(null);
            setDraft((prev) => ({
              ...prev,
              keyType: parseSSHKeyType(event.target.value),
            }));
          }}
        >
          <option value="auto">{t("sshKeys.keyTypeAuto")}</option>
          <option value="rsa">RSA</option>
          <option value="ed25519">ED25519</option>
          <option value="ecdsa">ECDSA</option>
        </Select>
        <p className="mt-1 text-xs text-muted-foreground">
          {t("sshKeys.keyTypeHint")}
        </p>
      </div>

      <div className="rounded-lg border border-border bg-secondary/40 p-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <label htmlFor="ssh-key-edit-disabled" className="text-sm font-medium">
              {t("sshKeys.scopeDisabled")}
            </label>
            <p className="mt-1 text-xs text-muted-foreground">
              {t("sshKeys.scopeDisabledHint")}
            </p>
          </div>
          <Switch
            id="ssh-key-edit-disabled"
            checked={draft.disabled}
            onCheckedChange={(checked) => {
              setDeterminateError(null);
              setDraft((prev) => ({ ...prev, disabled: checked }));
            }}
            disabled={fieldsDisabled}
            aria-label={t("sshKeys.scopeDisabled")}
          />
        </div>

        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <div>
            <label htmlFor="ssh-key-edit-expires-at" className="mb-1 block text-sm font-medium">
              {t("sshKeys.scopeExpiresAt")}
            </label>
            <Input
              id="ssh-key-edit-expires-at"
              type="datetime-local"
              value={draft.expiresAt}
              disabled={fieldsDisabled}
              onChange={(event) => {
                setDeterminateError(null);
                setDraft((prev) => ({ ...prev, expiresAt: event.target.value }));
              }}
            />
          </div>
          <div>
            <label htmlFor="ssh-key-edit-purposes" className="mb-1 block text-sm font-medium">
              {t("sshKeys.scopeAllowedPurposes")}
            </label>
            <Input
              id="ssh-key-edit-purposes"
              placeholder="terminal,task_command"
              value={draft.allowedPurposes}
              disabled={fieldsDisabled}
              onChange={(event) => {
                setDeterminateError(null);
                setDraft((prev) => ({ ...prev, allowedPurposes: event.target.value }));
              }}
            />
            {historicalPurposes.length > 0 ? (
              <p className="mt-1 text-xs text-muted-foreground">
                {t("sshKeys.scopeHistoricalNote", { purposes: historicalPurposes.join(", ") })}
              </p>
            ) : null}
          </div>
          <div>
            <label htmlFor="ssh-key-edit-node-ids" className="mb-1 block text-sm font-medium">
              {t("sshKeys.scopeAllowedNodeIds")}
            </label>
            <Input
              id="ssh-key-edit-node-ids"
              placeholder="1,2,3"
              value={draft.allowedNodeIds}
              disabled={fieldsDisabled}
              onChange={(event) => {
                setDeterminateError(null);
                setDraft((prev) => ({ ...prev, allowedNodeIds: event.target.value }));
              }}
            />
          </div>
          <div>
            <label htmlFor="ssh-key-edit-node-tags" className="mb-1 block text-sm font-medium">
              {t("sshKeys.scopeAllowedNodeTags")}
            </label>
            <Input
              id="ssh-key-edit-node-tags"
              placeholder="prod,db"
              value={draft.allowedNodeTags}
              disabled={fieldsDisabled}
              onChange={(event) => {
                setDeterminateError(null);
                setDraft((prev) => ({ ...prev, allowedNodeTags: event.target.value }));
              }}
            />
          </div>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          {t("sshKeys.scopeHint")}
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
            disabled={fieldsDisabled}
          >
            <Upload className="size-3.5" aria-hidden />
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
        {isEditing ? (
          <p
            data-testid="ssh-key-replacement-policy"
            className="mb-1 text-xs text-muted-foreground"
          >
            {t("sshKeys.privateKeyEditPolicyHint")}
          </p>
        ) : null}
        <Textarea
          id="ssh-key-edit-private-key"
          className="min-h-36 text-xs"
          placeholder={
            isEditing
              ? t("sshKeys.privateKeyPlaceholderEdit")
              : t("sshKeys.privateKeyPlaceholder")
          }
          value={draft.privateKey}
          disabled={fieldsDisabled}
          onChange={(event) => {
            setDeterminateError(null);
            setDraft((prev) => ({
              ...prev,
              privateKey: event.target.value,
            }));
          }}
        />
        {(showEarlyPrerequisite || prerequisiteError) ? (
          <StepUpPrerequisiteNotice className="mt-2" />
        ) : null}
        {determinateError ? (
          <InlineAlert tone="critical" className="mt-2">
            {determinateError}
          </InlineAlert>
        ) : null}
        {unknownWarning ? (
          <InlineAlert tone="warning" className="mt-2">
            {unknownWarning}
          </InlineAlert>
        ) : null}
      </div>
    </FormDialog>
  );
}

export type { SSHKeyDraft };
