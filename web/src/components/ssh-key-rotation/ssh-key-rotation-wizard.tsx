import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound } from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast-sonner";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import {
  createSSHKeysApi,
  rotateSSHKey,
  SSHKeyRotationDecodeError,
  type SSHKeyRotationReason,
} from "@/lib/api/ssh-keys-api";
import { ApiError, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import { getErrorMessage } from "@/lib/utils";
import { type NodeRecord, type SSHKeyPreview, type SSHKeyRecord, type SSHKeyType } from "@/types/domain";
import { RotationPreview, RotationUpload } from "./rotation-preview";
import { RotationProgress } from "./rotation-progress";
import { RotationSummary, type NodeVerifyResult, type RotationSaveStatus } from "./rotation-summary";

type Step = 1 | 2 | 3 | 4;

const stepLabels = [
  "rotationStep1",
  "rotationStep2",
  "rotationStep3",
  "rotationStep4",
] as const;

interface CheckedCandidate {
  preview: SSHKeyPreview;
  privateKey: string;
  keyType: SSHKeyType;
  draftGeneration: number;
}

function useCommittedDialogScope(
  role: AuthRole | null,
  token: string,
  open: boolean,
  authGeneration: number,
  authTransitioning: boolean,
) {
  const mountedRef = useRef(false);
  const openRef = useRef(open);
  const generationRef = useRef(0);

  useLayoutEffect(() => {
    openRef.current = open;
    generationRef.current += 1;
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      openRef.current = false;
      generationRef.current += 1;
    };
  }, [open, role, token, authGeneration, authTransitioning]);

  const isCurrent = useCallback((generation: number, sessionGeneration: number) => (
    mountedRef.current
    && openRef.current
    && generationRef.current === generation
    && getAuthSessionGeneration() === sessionGeneration
    && !isAuthTransitionActive()
  ), []);

  return { generationRef, isCurrent };
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

function isUncertainRotation(error: unknown): boolean {
  if (isAbortError(error)) return false;
  if (error instanceof SSHKeyRotationDecodeError) return true;
  if (error instanceof ApiError) {
    return error.status === 408 || error.status === 429 || error.status >= 500;
  }
  return true;
}

function definiteFailureMessage(error: unknown, t: (key: "sshKeys.rotationKeyMissing" | "sshKeys.rotationForbidden" | "sshKeys.rotationPayloadTooLarge" | "sshKeys.rotationRequestRejected") => string): string {
  if (error instanceof ApiError) {
    if (error.status === 404) return t("sshKeys.rotationKeyMissing");
    if (error.status === 403) return t("sshKeys.rotationForbidden");
    if (error.status === 413) return t("sshKeys.rotationPayloadTooLarge");
  }
  return t("sshKeys.rotationRequestRejected");
}

export interface SSHKeyRotationWizardProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sshKeys: SSHKeyRecord[];
  keyUsageMap: Map<string, NodeRecord[]>;
  preselectedKey?: SSHKeyRecord | null;
  token: string;
  onComplete: () => void;
  onCloseAutoFocus?: DialogCloseAutoFocus;
}

export function SSHKeyRotationWizard(props: SSHKeyRotationWizardProps) {
  const { role, authTransitioning } = useAuth();
  const authGeneration = getAuthSessionGeneration();
  const sessionKey = [
    props.open ? "1" : "0",
    props.preselectedKey?.id ?? "",
    props.preselectedKey?.disabled ? "1" : "0",
    props.token,
    role ?? "",
    String(authGeneration),
    authTransitioning ? "1" : "0",
  ].join("|");
  return <SSHKeyRotationSession key={sessionKey} {...props} />;
}

function SSHKeyRotationSession({
  open,
  onOpenChange,
  sshKeys,
  keyUsageMap,
  preselectedKey,
  token,
  onComplete,
  onCloseAutoFocus,
}: SSHKeyRotationWizardProps) {
  const { t } = useTranslation();
  const { role, authTransitioning = false } = useAuth();
  const authGeneration = getAuthSessionGeneration();
  const { generationRef, isCurrent } = useCommittedDialogScope(
    role,
    token,
    open,
    authGeneration,
    authTransitioning,
  );
  const preselectedBlocked = Boolean(preselectedKey?.disabled);

  const [step, setStep] = useState<Step>(preselectedKey && !preselectedBlocked ? 2 : 1);
  const [selectedKey, setSelectedKey] = useState<SSHKeyRecord | null>(
    preselectedKey && !preselectedBlocked ? preselectedKey : null,
  );
  const [newKeyName, setNewKeyName] = useState(preselectedKey?.name ?? "");
  const [newKeyType, setNewKeyType] = useState<SSHKeyType>("auto");
  const [newPrivateKey, setNewPrivateKey] = useState("");
  const [checkedCandidate, setCheckedCandidate] = useState<CheckedCandidate | null>(null);
  const [checkingCandidate, setCheckingCandidate] = useState(false);
  const [checkError, setCheckError] = useState<string | null>(null);
  const [saveStatus, setSaveStatus] = useState<RotationSaveStatus>("not_submitted");
  const [rotationReason, setRotationReason] = useState<SSHKeyRotationReason | null>(null);
  const [results, setResults] = useState<NodeVerifyResult[]>([]);
  const [savedPublicFingerprint, setSavedPublicFingerprint] = useState("");
  const [rotationError, setRotationError] = useState<string | null>(null);
  const [rotationAcknowledgement, setRotationAcknowledgement] = useState("");

  const draftGenerationRef = useRef(0);
  const draftRef = useRef({ privateKey: "", keyType: "auto" as SSHKeyType });
  const previewAbortRef = useRef<AbortController | null>(null);
  const rotationAbortRef = useRef<AbortController | null>(null);
  const submitLockRef = useRef(false);
  const checkLockRef = useRef(false);

  useLayoutEffect(() => {
    draftRef.current = { privateKey: newPrivateKey, keyType: newKeyType };
  }, [newKeyType, newPrivateKey]);

  useLayoutEffect(() => () => {
    previewAbortRef.current?.abort();
    rotationAbortRef.current?.abort();
  }, [authGeneration, authTransitioning, open, role, token]);

  const showDisabledRemediation = sshKeys.some((key) => key.disabled) || preselectedBlocked;
  const candidateReady = Boolean(
    checkedCandidate
    && checkedCandidate.privateKey === newPrivateKey
    && checkedCandidate.keyType === newKeyType,
  );

  const affectedNodes = useMemo(
    () => (selectedKey ? keyUsageMap.get(selectedKey.id) ?? [] : []),
    [keyUsageMap, selectedKey],
  );

  const invalidateDraft = useCallback(() => {
    draftGenerationRef.current += 1;
    setCheckedCandidate(null);
    setCheckError(null);
    previewAbortRef.current?.abort();
    previewAbortRef.current = null;
    checkLockRef.current = false;
    setCheckingCandidate(false);
  }, []);

  const clearDraft = useCallback(() => {
    draftRef.current = { privateKey: "", keyType: draftRef.current.keyType };
    setNewPrivateKey("");
    invalidateDraft();
  }, [invalidateDraft]);

  const handlePrivateKeyChange = (value: string) => {
    if (value === newPrivateKey) return;
    draftRef.current = { privateKey: value, keyType: newKeyType };
    setNewPrivateKey(value);
    invalidateDraft();
  };

  const handleKeyTypeChange = (nextType: SSHKeyType) => {
    if (nextType === newKeyType) return;
    draftRef.current = { privateKey: newPrivateKey, keyType: nextType };
    setNewKeyType(nextType);
    invalidateDraft();
  };

  const checkCandidate = useCallback(async () => {
    if (checkLockRef.current || !newPrivateKey.trim()) return;
    const generation = generationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    if (!isCurrent(generation, sessionGeneration)) return;
    const privateKey = newPrivateKey;
    const keyType = newKeyType;
    const draftGeneration = draftGenerationRef.current;
    checkLockRef.current = true;
    previewAbortRef.current?.abort();
    const controller = new AbortController();
    previewAbortRef.current = controller;
    setCheckingCandidate(true);
    setCheckError(null);
    setCheckedCandidate(null);
    try {
      const preview = await createSSHKeysApi().previewSSHKey(
        token,
        { privateKey, keyType },
        { signal: controller.signal },
      );
      if (!isCurrent(generation, sessionGeneration) || previewAbortRef.current !== controller) return;
      if (draftGenerationRef.current !== draftGeneration) return;
      if (draftRef.current.privateKey !== privateKey || draftRef.current.keyType !== keyType) return;
      setCheckedCandidate({ preview, privateKey, keyType, draftGeneration });
    } catch (error) {
      if (!isCurrent(generation, sessionGeneration) || isAbortError(error)) return;
      if (draftGenerationRef.current !== draftGeneration || previewAbortRef.current !== controller) return;
      setCheckError(getErrorMessage(error));
    } finally {
      if (
        isCurrent(generation, sessionGeneration)
        && previewAbortRef.current === controller
        && draftGenerationRef.current === draftGeneration
      ) {
        setCheckingCandidate(false);
        checkLockRef.current = false;
      }
    }
  }, [generationRef, isCurrent, newKeyType, newPrivateKey, token]);

  const executeRotation = useCallback(async () => {
    if (!selectedKey || !checkedCandidate || submitLockRef.current) return;
    if (checkedCandidate.privateKey !== newPrivateKey || checkedCandidate.keyType !== newKeyType) return;
    const generation = generationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    if (!isCurrent(generation, sessionGeneration)) return;
    const privateKey = checkedCandidate.privateKey;
    const keyType = checkedCandidate.keyType;
    const draftGeneration = draftGenerationRef.current;
    const keyName = newKeyName;
    const keyId = selectedKey.id;
    submitLockRef.current = true;
    rotationAbortRef.current?.abort();
    const controller = new AbortController();
    rotationAbortRef.current = controller;
    setSaveStatus("validating");
    setRotationReason(null);
    setRotationError(null);
    setResults([]);
    setSavedPublicFingerprint("");
    setStep(4);

    const still = () => isCurrent(generation, sessionGeneration)
      && !controller.signal.aborted
      && draftGenerationRef.current === draftGeneration
      && rotationAbortRef.current === controller;
    try {
      const outcome = await rotateSSHKey(token, keyId, {
        privateKey,
        keyType,
        name: keyName,
      }, { signal: controller.signal });
      if (!still()) return;
      if (outcome.status === "saved") {
        setSaveStatus("saved");
        setRotationReason("");
        setSavedPublicFingerprint(outcome.publicKeyFingerprint);
        setResults(outcome.results);
        clearDraft();
        toast.success(t("sshKeys.rotationKeyUpdated"));
        onComplete();
        return;
      }
      setSaveStatus("not_saved");
      setRotationReason(outcome.reason);
      setRotationError(null);
      setResults(outcome.results);
      setSavedPublicFingerprint("");
    } catch (error) {
      if (!still() || isAbortError(error)) return;
      if (isUncertainRotation(error)) {
        clearDraft();
        setSaveStatus("unknown");
        setRotationReason(null);
        setResults([]);
        setSavedPublicFingerprint("");
        setRotationError(t("sshKeys.rotationSaveUnknown"));
        return;
      }
      setSaveStatus("failed");
      setRotationReason(null);
      setResults([]);
      setSavedPublicFingerprint("");
      setRotationError(definiteFailureMessage(error, t));
    } finally {
      if (isCurrent(generation, sessionGeneration) && rotationAbortRef.current === controller && !controller.signal.aborted) {
        submitLockRef.current = false;
      }
    }
  }, [
    checkedCandidate,
    clearDraft,
    generationRef,
    isCurrent,
    newKeyName,
    newKeyType,
    newPrivateKey,
    onComplete,
    selectedKey,
    t,
    token,
  ]);

  const copyCandidatePublicKey = useCallback(async () => {
    const generation = generationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    if (!isCurrent(generation, sessionGeneration)) return;
    const value = checkedCandidate?.preview.publicKey;
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      if (!isCurrent(generation, sessionGeneration)) return;
      toast.success(t("sshKeys.publicKeyCopied"));
    } catch {
      if (!isCurrent(generation, sessionGeneration)) return;
      toast.error(t("sshKeys.copyFailed"));
    }
  }, [checkedCandidate, generationRef, isCurrent, t]);

  const handleSelectKey = (key: SSHKeyRecord) => {
    if (key.disabled) return;
    setSelectedKey(key);
    setNewKeyName(key.name);
  };

  const handleNext = () => {
    if (step === 1 && selectedKey && !selectedKey.disabled) {
      setStep(2);
    } else if (step === 2 && candidateReady) {
      setRotationAcknowledgement("");
      setStep(3);
    } else if (step === 3 && rotationAcknowledgement.trim() === String(affectedNodes.length)) {
      void executeRotation();
    }
  };

  const handleBack = () => {
    if (step === 2 && !preselectedKey) {
      invalidateDraft();
      setStep(1);
    } else if (step === 3) {
      setRotationAcknowledgement("");
      invalidateDraft();
      setStep(2);
    }
  };

  const handleEditAgain = () => {
    if (saveStatus !== "failed" && saveStatus !== "not_saved") return;
    setSaveStatus("not_submitted");
    setRotationReason(null);
    setRotationError(null);
    setResults([]);
    setSavedPublicFingerprint("");
    invalidateDraft();
    setStep(2);
  };

  const handleDone = () => {
    onOpenChange(false);
    onComplete();
  };

  const renderStepIndicator = () => (
    <div className="flex items-center justify-center gap-1 px-6 pb-2 text-xs text-muted-foreground">
      {([1, 2, 3, 4] as Step[]).map((s) => (
        <div key={s} className="flex items-center gap-1">
          <span
            role="presentation"
            aria-label={t(`sshKeys.${stepLabels[s - 1]}`)}
            className={`flex size-6 items-center justify-center rounded-full text-xs font-medium ${
              s === step
                ? "bg-primary text-primary-foreground"
                : s < step
                  ? "bg-primary/20 text-primary"
                  : "bg-muted text-muted-foreground"
            }`}
          >
            {s}
          </span>
          {s < 4 && (
            <span className="mx-0.5 text-muted-foreground/40">&mdash;</span>
          )}
        </div>
      ))}
    </div>
  );

  const renderStep = () => {
    if (step === 1) {
      return (
        <RotationPreview
          rotatableKeys={sshKeys}
          keyUsageMap={keyUsageMap}
          selectedKey={selectedKey}
          showDisabledRemediation={showDisabledRemediation}
          onSelectKey={handleSelectKey}
          onNavigate={() => onOpenChange(false)}
          onNext={handleNext}
        />
      );
    }
    if (step === 2) {
      return (
        <RotationUpload
          selectedKey={selectedKey}
          newKeyName={newKeyName}
          onNewKeyNameChange={setNewKeyName}
          newKeyType={newKeyType}
          onNewKeyTypeChange={handleKeyTypeChange}
          newPrivateKey={newPrivateKey}
          onNewPrivateKeyChange={handlePrivateKeyChange}
          preselectedKey={preselectedBlocked ? null : preselectedKey}
          token={token}
          role={role}
          candidateReady={candidateReady}
          checkingCandidate={checkingCandidate}
          checkError={checkError}
          onCheckCandidate={() => void checkCandidate()}
          onBack={handleBack}
          onNext={handleNext}
        />
      );
    }
    if (step === 3 && checkedCandidate) {
      return (
        <RotationProgress
          affectedNodes={affectedNodes}
          acknowledgement={rotationAcknowledgement}
          onAcknowledgementChange={setRotationAcknowledgement}
          candidatePublicKey={checkedCandidate.preview.publicKey}
          oldPublicKeyFingerprint={selectedKey?.publicKeyFingerprint}
          newPublicKeyFingerprint={checkedCandidate.preview.publicKeyFingerprint}
          onCopyPublicKey={() => void copyCandidatePublicKey()}
          onBack={handleBack}
          onNext={handleNext}
        />
      );
    }
    if (step === 4) {
      return (
        <RotationSummary
          saveStatus={saveStatus}
          reason={rotationReason}
          rotationError={rotationError}
          results={results}
          newPublicKeyFingerprint={savedPublicFingerprint}
          onEditAgain={handleEditAgain}
          onDone={handleDone}
        />
      );
    }
    return null;
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (nextOpen) return;
        if (saveStatus === "validating") {
          // Drop the draft and ignore a late result. Closing does not claim a finished server commit was rolled back.
          rotationAbortRef.current?.abort();
          clearDraft();
        }
        onOpenChange(false);
      }}
    >
      <DialogContent
        size="md"
        onCloseAutoFocus={onCloseAutoFocus}
        data-rotation-draft={newPrivateKey.trim() ? "present" : "cleared"}
        data-rotation-candidate={candidateReady ? "ready" : "absent"}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-5 text-primary" />
            {t("sshKeys.rotationTitle")}
          </DialogTitle>
          <DialogDescription>
            {selectedKey
              ? t("sshKeys.rotationUploadKeyDesc", { name: selectedKey.name })
              : t("sshKeys.rotationSelectAnyKeyDesc")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        {renderStepIndicator()}

        <div className="min-w-0 space-y-4 px-6 pb-6">{renderStep()}</div>
      </DialogContent>
    </Dialog>
  );
}
