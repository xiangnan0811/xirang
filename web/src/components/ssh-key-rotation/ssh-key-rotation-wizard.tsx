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
import { createSSHKeysApi, type TestConnectionResult } from "@/lib/api/ssh-keys-api";
import { ApiError, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import { getErrorMessage } from "@/lib/utils";
import { type NodeRecord, type SSHKeyPreview, type SSHKeyRecord, type SSHKeyType } from "@/types/domain";
import { RotationPreview, RotationUpload } from "./rotation-preview";
import { RotationProgress } from "./rotation-progress";
import { RotationSummary, type NodeVerifyResult, type RotationSaveStatus } from "./rotation-summary";

type Step = 1 | 2 | 3 | 4;
type SSHKeysApi = ReturnType<typeof createSSHKeysApi>;

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

function isUncertainTransport(error: unknown): boolean {
  if (isAbortError(error)) return false;
  if (error instanceof ApiError) {
    return error.status === 408 || error.status === 429 || error.status >= 500;
  }
  return true;
}

function nodeResultId(node: NodeRecord): string {
  return `node-${node.id}`;
}

function resultsFromTest(
  nodes: NodeRecord[],
  testResults: TestConnectionResult[] | null,
): NodeVerifyResult[] {
  const returned = new Map<string, TestConnectionResult>();
  for (const result of testResults ?? []) {
    if (!returned.has(result.nodeId)) returned.set(result.nodeId, result);
  }
  return nodes.map((node) => {
    const nodeId = nodeResultId(node);
    const result = testResults ? returned.get(nodeId) : undefined;
    if (!testResults || !result) {
      return { nodeId, name: node.name, status: "unknown" as const };
    }
    if (result.success) {
      return { nodeId, name: node.name, status: "verified" as const };
    }
    return {
      nodeId,
      name: node.name,
      status: "failed" as const,
      ...(result.error ? { error: result.error } : {}),
    };
  });
}

async function verifyNodes(
  apiClient: SSHKeysApi,
  token: string,
  keyId: string,
  nodes: NodeRecord[],
): Promise<NodeVerifyResult[]> {
  try {
    const testResults = await apiClient.testConnection(
      token,
      keyId,
      nodes.map((node) => nodeResultId(node)),
    );
    return resultsFromTest(nodes, testResults);
  } catch (error) {
    if (isAbortError(error)) throw error;
    return resultsFromTest(nodes, null);
  }
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
  const [verifying, setVerifying] = useState(false);
  const [results, setResults] = useState<NodeVerifyResult[]>([]);
  const [savedPublicFingerprint, setSavedPublicFingerprint] = useState("");
  const [rotationError, setRotationError] = useState<string | null>(null);
  const [showDisabledStop, setShowDisabledStop] = useState(false);
  const [rotationAcknowledgement, setRotationAcknowledgement] = useState("");

  const draftGenerationRef = useRef(0);
  const draftRef = useRef({ privateKey: "", keyType: "auto" as SSHKeyType });
  const previewAbortRef = useRef<AbortController | null>(null);
  const rotationAbortRef = useRef<AbortController | null>(null);
  const submitLockRef = useRef(false);
  const checkLockRef = useRef(false);
  const reverifyLockRef = useRef(false);
  const reverifyOpRef = useRef(0);

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
    const publicFingerprint = checkedCandidate.preview.publicKeyFingerprint;
    const draftGeneration = draftGenerationRef.current;
    const keyName = newKeyName;
    submitLockRef.current = true;
    const controller = new AbortController();
    rotationAbortRef.current = controller;
    setSaveStatus("saving");
    setRotationError(null);
    setShowDisabledStop(false);
    setResults([]);
    setSavedPublicFingerprint("");
    setStep(4);

    const still = () => isCurrent(generation, sessionGeneration) && !controller.signal.aborted
      && draftGenerationRef.current === draftGeneration;
    const apiClient = createSSHKeysApi();
    let submitted = false;
    try {
      const fresh = await apiClient.getSSHKey(token, selectedKey.id, { signal: controller.signal });
      if (!still()) return;
      if (fresh.id !== selectedKey.id) {
        setSaveStatus("failed");
        setRotationError(t("sshKeys.rotationKeyMissing"));
        return;
      }
      if (fresh.disabled) {
        setSaveStatus("failed");
        setRotationError(t("sshKeys.rotationKeyDisabledStop"));
        setShowDisabledStop(true);
        return;
      }
      submitted = true;
      await apiClient.updateSSHKey(token, fresh.id, {
        name: keyName.trim() || fresh.name,
        username: fresh.username,
        keyType,
        privateKey,
        disabled: fresh.disabled,
        allowedPurposes: fresh.allowedPurposes,
        allowedNodeIds: fresh.allowedNodeIds,
        allowedNodeTags: fresh.allowedNodeTags,
      });
      if (!still()) return;
      setSaveStatus("saved");
      setSavedPublicFingerprint(publicFingerprint);
      draftRef.current = { privateKey: "", keyType };
      setNewPrivateKey("");
      draftGenerationRef.current += 1;
      setCheckedCandidate(null);
      toast.success(t("sshKeys.rotationKeyUpdated"));
      const nodes = keyUsageMap.get(fresh.id) ?? [];
      if (nodes.length === 0) {
        setResults([]);
        return;
      }
      setVerifying(true);
      const nextResults = await verifyNodes(apiClient, token, fresh.id, nodes);
      if (!isCurrent(generation, sessionGeneration) || controller.signal.aborted) return;
      setResults(nextResults);
    } catch (error) {
      if (!isCurrent(generation, sessionGeneration) || controller.signal.aborted || isAbortError(error)) return;
      if (isUncertainTransport(error)) {
        draftRef.current = { privateKey: "", keyType: newKeyType };
        setNewPrivateKey("");
        draftGenerationRef.current += 1;
        setCheckedCandidate(null);
        setSaveStatus("unknown");
        setRotationError(t(submitted ? "sshKeys.rotationSaveUnknown" : "sshKeys.rotationReadUnknown"));
        return;
      }
      setSaveStatus("failed");
      setRotationError(
        error instanceof ApiError && error.status === 404
          ? t("sshKeys.rotationKeyMissing")
          : getErrorMessage(error),
      );
    } finally {
      if (isCurrent(generation, sessionGeneration)) {
        setVerifying(false);
        submitLockRef.current = false;
      }
    }
  }, [
    checkedCandidate,
    generationRef,
    isCurrent,
    keyUsageMap,
    newKeyName,
    newKeyType,
    newPrivateKey,
    selectedKey,
    t,
    token,
  ]);

  const reverifyFailed = useCallback(async () => {
    if (saveStatus !== "saved" || reverifyLockRef.current || !selectedKey) return;
    const targets = results.filter((result) => result.status !== "verified");
    if (targets.length === 0) return;
    const generation = generationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    if (!isCurrent(generation, sessionGeneration)) return;
    reverifyLockRef.current = true;
    const op = reverifyOpRef.current + 1;
    reverifyOpRef.current = op;
    const controller = new AbortController();
    rotationAbortRef.current = controller;
    setVerifying(true);
    const targetIds = new Set(targets.map((result) => result.nodeId));
    const still = () => isCurrent(generation, sessionGeneration)
      && reverifyOpRef.current === op
      && !controller.signal.aborted;
    try {
      const testResults = await createSSHKeysApi().testConnection(
        token,
        selectedKey.id,
        targets.map((result) => result.nodeId),
      );
      if (!still()) return;
      const returned = new Map<string, TestConnectionResult>();
      for (const result of testResults) {
        if (!returned.has(result.nodeId)) returned.set(result.nodeId, result);
      }
      setResults((current) => current.map((result) => {
        if (!targetIds.has(result.nodeId)) return result;
        const match = returned.get(result.nodeId);
        if (!match) return { nodeId: result.nodeId, name: result.name, status: "unknown" };
        if (match.success) return { nodeId: result.nodeId, name: result.name, status: "verified" };
        return {
          nodeId: result.nodeId,
          name: result.name,
          status: "failed",
          ...(match.error ? { error: match.error } : {}),
        };
      }));
    } catch (error) {
      if (!still() || isAbortError(error)) return;
      setResults((current) => current.map((result) => (
        targetIds.has(result.nodeId)
          ? { nodeId: result.nodeId, name: result.name, status: "unknown" }
          : result
      )));
    } finally {
      if (still()) {
        setVerifying(false);
        reverifyLockRef.current = false;
      }
    }
  }, [generationRef, isCurrent, results, saveStatus, selectedKey, token]);

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
    if (saveStatus !== "failed") return;
    setSaveStatus("not_submitted");
    setRotationError(null);
    setShowDisabledStop(false);
    setResults([]);
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
    return (
      <RotationSummary
        saveStatus={saveStatus}
        verifying={verifying}
        rotationError={rotationError}
        results={results}
        newPublicKeyFingerprint={savedPublicFingerprint}
        showDisabledRemediation={showDisabledStop}
        onNavigate={() => onOpenChange(false)}
        onReverify={() => void reverifyFailed()}
        onEditAgain={handleEditAgain}
        onDone={handleDone}
      />
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen && saveStatus !== "saving" && !verifying) onOpenChange(false);
      }}
    >
      <DialogContent size="md" onCloseAutoFocus={onCloseAutoFocus}>
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

        <div className="space-y-4 px-6 pb-6">{renderStep()}</div>
      </DialogContent>
    </Dialog>
  );
}
