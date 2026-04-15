import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound } from "lucide-react";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
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

export interface SSHKeyRotationWizardProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sshKeys: SSHKeyRecord[];
  keyUsageMap: Map<string, NodeRecord[]>;
  preselectedKey?: SSHKeyRecord | null;
  token: string;
  onComplete: () => void;
}

export function SSHKeyRotationWizard({
  open,
  onOpenChange,
  sshKeys,
  keyUsageMap,
  preselectedKey,
  token,
  onComplete,
}: SSHKeyRotationWizardProps) {
  const { t } = useTranslation();

  // wizard state
  const [step, setStep] = useState<Step>(1);
  const [selectedKey, setSelectedKey] = useState<SSHKeyRecord | null>(null);
  const [newKeyName, setNewKeyName] = useState("");
  const [newKeyType, setNewKeyType] = useState<SSHKeyType>("auto");
  const [newPrivateKey, setNewPrivateKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [results, setResults] = useState<NodeVerifyResult[]>([]);
  const [newFingerprint, setNewFingerprint] = useState("");
  const [rotationError, setRotationError] = useState<string | null>(null);

  // keys that have at least one associated node
  const rotatableKeys = sshKeys.filter(
    (key) => (keyUsageMap.get(key.id)?.length ?? 0) > 0,
  );

  // reset on open
  useEffect(() => {
    if (!open) return;
    setNewKeyName("");
    setNewKeyType("auto");
    setNewPrivateKey("");
    setLoading(false);
    setResults([]);
    setNewFingerprint("");
    setRotationError(null);

    if (preselectedKey) {
      setSelectedKey(preselectedKey);
      setNewKeyName(preselectedKey.name);
      setStep(2);
    } else {
      setSelectedKey(null);
      setStep(1);
    }
  }, [open, preselectedKey]);

  const affectedNodes = selectedKey
    ? keyUsageMap.get(selectedKey.id) ?? []
    : [];
  const onlineNodes = affectedNodes.filter((n) => n.status === "online");
  const offlineNodes = affectedNodes.filter((n) => n.status !== "online");

  const executeRotation = useCallback(async () => {
    if (!selectedKey) return;
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
      });

      setNewFingerprint(updatedKey.fingerprint);
      toast.success(t("sshKeys.rotationSuccess", { name: updatedKey.name }));

      const verifyResults: NodeVerifyResult[] = [];

      for (const node of offlineNodes) {
        verifyResults.push({
          nodeId: `node-${node.id}`,
          name: node.name,
          status: "skipped",
        });
      }

      if (onlineNodes.length > 0) {
        const nodeIds = onlineNodes.map((n) => `node-${n.id}`);
        try {
          const testResults = await apiClient.testConnection(
            token,
            selectedKey.id,
            nodeIds,
          );

          for (const tr of testResults) {
            const node = onlineNodes.find((n) => `node-${n.id}` === tr.nodeId);
            verifyResults.push({
              nodeId: tr.nodeId,
              name: node?.name ?? tr.name,
              status: tr.success ? "verified" : "failed",
              error: tr.error,
            });
          }
        } catch {
          for (const node of onlineNodes) {
            verifyResults.push({
              nodeId: `node-${node.id}`,
              name: node.name,
              status: "failed",
              error: t("sshKeys.connectionFailed"),
            });
          }
        }
      }

      setResults(verifyResults);
    } catch (err) {
      setRotationError(getErrorMessage(err));
    } finally {
      setLoading(false);
    }
  }, [
    selectedKey,
    newKeyName,
    newKeyType,
    newPrivateKey,
    token,
    onlineNodes,
    offlineNodes,
    t,
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
    } else if (step === 3) {
      setStep(4);
      void executeRotation();
    }
  };

  const handleBack = () => {
    if (step === 2 && !preselectedKey) {
      setStep(1);
    } else if (step === 3) {
      setStep(2);
    }
  };

  const handleDone = () => {
    onOpenChange(false);
    onComplete();
  };

  const handleRetry = () => {
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
      <DialogContent size="md">
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
