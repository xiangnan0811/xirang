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
import { createSSHKeysApi } from "@/lib/api/ssh-keys-api";
import { getErrorMessage } from "@/lib/utils";
import { type NodeRecord, type SSHKeyRecord, type SSHKeyType } from "@/types/domain";
import { RotationPreview, RotationUpload } from "./rotation-preview";
import { RotationProgress } from "./rotation-progress";
import { RotationSummary, type NodeVerifyResult } from "./rotation-summary";

type Step = 1 | 2 | 3 | 4;

const stepLabels = [
  "rotationStep1",
  "rotationStep2",
  "rotationStep3",
  "rotationStep4",
] as const;

function useCommittedDialogScope(role: AuthRole | null, token: string, open: boolean) {
  const mountedRef = useRef(false);
  const openRef = useRef(open);
  const generationRef = useRef(0);
  const identityRef = useRef({ role, token, open });

  useLayoutEffect(() => {
    identityRef.current = { role, token, open };
    openRef.current = open;
    generationRef.current += 1;
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      openRef.current = false;
      generationRef.current += 1;
    };
  }, [open, role, token]);

  const isCurrent = useCallback((generation: number) => (
    mountedRef.current &&
    openRef.current &&
    generationRef.current === generation
  ), []);

  return { generationRef, isCurrent, mountedRef };
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
  return <SSHKeyRotationSession key={`${props.open}:${props.preselectedKey?.id}`} {...props} />;
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
  const { role } = useAuth();
  const { generationRef, isCurrent, mountedRef } = useCommittedDialogScope(role, token, open);

  // wizard state
  const [step, setStep] = useState<Step>(preselectedKey ? 2 : 1);
  const [selectedKey, setSelectedKey] = useState<SSHKeyRecord | null>(preselectedKey ?? null);
  const [newKeyName, setNewKeyName] = useState(preselectedKey?.name ?? "");
  const [newKeyType, setNewKeyType] = useState<SSHKeyType>("auto");
  const [newPrivateKey, setNewPrivateKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [results, setResults] = useState<NodeVerifyResult[]>([]);
  const [newFingerprint, setNewFingerprint] = useState("");
  const [rotationError, setRotationError] = useState<string | null>(null);
  const [rotationAcknowledgement, setRotationAcknowledgement] = useState("");

  // keys that have at least one associated node
  const rotatableKeys = sshKeys.filter(
    (key) => (keyUsageMap.get(key.id)?.length ?? 0) > 0,
  );

  const affectedNodes = useMemo(
    () => selectedKey ? keyUsageMap.get(selectedKey.id) ?? [] : [],
    [keyUsageMap, selectedKey],
  );

  const executeRotation = useCallback(async () => {
    if (!selectedKey) return;
    const generation = generationRef.current;
    if (!isCurrent(generation)) return;
    setLoading(true);
    setRotationError(null);
    setResults([]);
    setNewFingerprint("");

    const apiClient = createSSHKeysApi();

    try {
      const updatedKey = await apiClient.updateSSHKey(token, selectedKey.id, {
        name: newKeyName.trim() || selectedKey.name,
        username: selectedKey.username,
        keyType: newKeyType,
        privateKey: newPrivateKey,
        disabled: selectedKey.disabled,
        expiresAt: selectedKey.expiresAt ?? "",
        allowedPurposes: selectedKey.allowedPurposes,
        allowedNodeIds: selectedKey.allowedNodeIds,
        allowedNodeTags: selectedKey.allowedNodeTags,
      });
      if (!isCurrent(generation)) return;

      setNewFingerprint(updatedKey.fingerprint);
      toast.success(t("sshKeys.rotationSuccess", { name: updatedKey.name }));

      const verifyResults: NodeVerifyResult[] = [];

      if (affectedNodes.length > 0) {
        const nodeIds = affectedNodes.map((node) => `node-${node.id}`);
        try {
          const testResults = await apiClient.testConnection(
            token,
            selectedKey.id,
            nodeIds,
          );
          if (!isCurrent(generation)) return;
          const returned = new Set(testResults.map((result) => result.nodeId));

          for (const tr of testResults) {
            const node = affectedNodes.find((item) => `node-${item.id}` === tr.nodeId);
            verifyResults.push({
              nodeId: tr.nodeId,
              name: node?.name ?? tr.name,
              status: tr.success ? "verified" : "failed",
              error: tr.error,
            });
          }
          for (const node of affectedNodes) {
            const nodeId = `node-${node.id}`;
            if (!returned.has(nodeId)) {
              verifyResults.push({
                nodeId,
                name: node.name,
                status: "failed",
                error: t("sshKeys.connectionFailed"),
              });
            }
          }
        } catch {
          if (!isCurrent(generation)) return;
          for (const node of affectedNodes) {
            verifyResults.push({
              nodeId: `node-${node.id}`,
              name: node.name,
              status: "failed",
              error: t("sshKeys.connectionFailed"),
            });
          }
        }
      }

      if (!isCurrent(generation)) return;
      setResults(verifyResults);
    } catch (err) {
      if (!isCurrent(generation)) return;
      setRotationError(getErrorMessage(err));
    } finally {
      if (mountedRef.current) {
        setLoading(false);
      }
    }
  }, [
    affectedNodes,
    generationRef,
    isCurrent,
    mountedRef,
    newKeyName,
    newKeyType,
    newPrivateKey,
    selectedKey,
    t,
    token,
  ]);

  const handleSelectKey = (key: SSHKeyRecord) => {
    setSelectedKey(key);
    setNewKeyName(key.name);
  };

  const handleNext = () => {
    if (step === 1 && selectedKey) {
      setStep(2);
    } else if (step === 2 && newPrivateKey.trim()) {
      setStep(3);
    } else if (step === 3 && rotationAcknowledgement.trim() === String(affectedNodes.length)) {
      setStep(4);
      void executeRotation();
    }
  };

  const handleBack = () => {
    if (step === 2 && !preselectedKey) {
      setStep(1);
    } else if (step === 3) {
      setRotationAcknowledgement("");
      setStep(2);
    }
  };

  const handleDone = () => {
    onOpenChange(false);
    onComplete();
  };

  const handleRetry = () => {
    setRotationAcknowledgement("");
    setStep(3);
    setRotationError(null);
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
          rotatableKeys={rotatableKeys}
          keyUsageMap={keyUsageMap}
          selectedKey={selectedKey}
          onSelectKey={handleSelectKey}
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
          onNewKeyTypeChange={setNewKeyType}
          newPrivateKey={newPrivateKey}
          onNewPrivateKeyChange={setNewPrivateKey}
          preselectedKey={preselectedKey}
          token={token}
          role={role}
          onBack={handleBack}
          onNext={handleNext}
        />
      );
    }
    if (step === 3) {
      return (
        <RotationProgress
          selectedKey={selectedKey}
          affectedNodes={affectedNodes}
          acknowledgement={rotationAcknowledgement}
          onAcknowledgementChange={setRotationAcknowledgement}
          onBack={handleBack}
          onNext={handleNext}
        />
      );
    }
    return (
      <RotationSummary
        loading={loading}
        rotationError={rotationError}
        results={results}
        newFingerprint={newFingerprint}
        newKeyName={newKeyName}
        selectedKeyName={selectedKey?.name}
        onRetry={handleRetry}
        onDone={handleDone}
      />
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && !loading) onOpenChange(false);
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
              : t("sshKeys.rotationSelectKeyDesc")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        {renderStepIndicator()}

        <div className="space-y-4 px-6 pb-6">{renderStep()}</div>
      </DialogContent>
    </Dialog>
  );
}
