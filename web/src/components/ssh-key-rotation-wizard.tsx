import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CheckCircle2,
  KeyRound,
  Loader2,
  SkipForward,
  Upload,
  XCircle,
} from "lucide-react";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { AppTextarea } from "@/components/ui/app-textarea";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast";
import { createSSHKeysApi } from "@/lib/api/ssh-keys-api";
import { getErrorMessage } from "@/lib/utils";
import {
  parseSSHKeyType,
  type NodeRecord,
  type SSHKeyRecord,
  type SSHKeyType,
} from "@/types/domain";

type Step = 1 | 2 | 3 | 4;

interface NodeVerifyResult {
  nodeId: string;
  name: string;
  status: "verified" | "skipped" | "failed";
  error?: string;
}

interface SSHKeyRotationWizardProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sshKeys: SSHKeyRecord[];
  keyUsageMap: Map<string, NodeRecord[]>;
  preselectedKey?: SSHKeyRecord | null;
  token: string;
  onComplete: () => void;
}

const stepLabels = [
  "rotationStep1",
  "rotationStep2",
  "rotationStep3",
  "rotationStep4",
] as const;

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
  const fileInputRef = useRef<HTMLInputElement>(null);

  // 向导状态
  const [step, setStep] = useState<Step>(1);
  const [selectedKey, setSelectedKey] = useState<SSHKeyRecord | null>(null);
  const [newKeyName, setNewKeyName] = useState("");
  const [newKeyType, setNewKeyType] = useState<SSHKeyType>("auto");
  const [newPrivateKey, setNewPrivateKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [results, setResults] = useState<NodeVerifyResult[]>([]);
  const [newFingerprint, setNewFingerprint] = useState("");
  const [rotationError, setRotationError] = useState<string | null>(null);

  // 可轮换的密钥：仅包含有关联节点的
  const rotatableKeys = sshKeys.filter(
    (key) => (keyUsageMap.get(key.id)?.length ?? 0) > 0,
  );

  // 当对话框打开时重置状态
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

  // 关联节点列表
  const affectedNodes = selectedKey
    ? keyUsageMap.get(selectedKey.id) ?? []
    : [];

  const onlineNodes = affectedNodes.filter((n) => n.status === "online");
  const offlineNodes = affectedNodes.filter((n) => n.status !== "online");

  // 文件上传处理
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
        setNewPrivateKey(content);
      }
    };
    reader.onerror = () => {
      toast.error(t("sshKeys.fileReadFailed"));
    };
    reader.readAsText(file);
    event.target.value = "";
  };

  // 执行轮换
  const executeRotation = useCallback(async () => {
    if (!selectedKey) return;
    setLoading(true);
    setRotationError(null);
    setResults([]);
    setNewFingerprint("");

    const apiClient = createSSHKeysApi();

    try {
      // 1. 更新密钥
      const updatedKey = await apiClient.updateSSHKey(token, selectedKey.id, {
        name: newKeyName.trim() || selectedKey.name,
        username: selectedKey.username,
        keyType: newKeyType,
        privateKey: newPrivateKey,
      });

      setNewFingerprint(updatedKey.fingerprint);
      toast.success(t("sshKeys.rotationSuccess", { name: updatedKey.name }));

      // 2. 对在线节点进行连通性验证
      const verifyResults: NodeVerifyResult[] = [];

      // 先添加离线节点为 skipped
      for (const node of offlineNodes) {
        verifyResults.push({
          nodeId: `node-${node.id}`,
          name: node.name,
          status: "skipped",
        });
      }

      // 测试在线节点
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
          // 如果批量测试失败，将所有在线节点标记为 failed
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

  // 步骤导航
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

  // 步骤指示器
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

  // 步骤 1：选择密钥
  const renderStep1 = () => (
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
                onChange={() => handleSelectKey(key)}
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
        <Button disabled={!selectedKey} onClick={handleNext}>
          {t("sshKeys.rotationNext")}
        </Button>
      </div>
    </>
  );

  // 步骤 2：上传新密钥
  const renderStep2 = () => (
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
          onChange={(e) => setNewKeyName(e.target.value)}
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
          onChange={(e) => setNewKeyType(parseSSHKeyType(e.target.value))}
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
          onChange={(e) => setNewPrivateKey(e.target.value)}
        />
      </div>

      <div className="flex justify-between pt-2">
        {!preselectedKey ? (
          <Button variant="outline" onClick={handleBack}>
            {t("sshKeys.rotationPrev")}
          </Button>
        ) : (
          <div />
        )}
        <Button disabled={!newPrivateKey.trim()} onClick={handleNext}>
          {t("sshKeys.rotationNext")}
        </Button>
      </div>
    </>
  );

  // 步骤 3：确认影响
  const renderStep3 = () => (
    <>
      <InlineAlert tone="warning">
        {t("sshKeys.rotationWarning", { count: affectedNodes.length })}
      </InlineAlert>

      <div>
        <p className="mb-2 text-sm font-medium">
          {t("sshKeys.rotationAffectedNodes")}
        </p>
        <div className="max-h-40 space-y-1.5 overflow-y-auto rounded-lg border border-border/60 p-2 thin-scrollbar">
          {affectedNodes.map((node) => (
            <div
              key={node.id}
              className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm"
            >
              <span
                className={`size-2 shrink-0 rounded-full ${
                  node.status === "online" ? "bg-success" : "bg-destructive"
                }`}
                aria-label={node.status}
              />
              <span className="min-w-0 truncate">{node.name}</span>
              <span className="ml-auto text-xs text-muted-foreground">
                {node.host}
              </span>
            </div>
          ))}
        </div>
      </div>

      <div className="space-y-1 text-sm">
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground">
            {t("sshKeys.rotationOldFingerprint")}:
          </span>
          <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
            {selectedKey?.fingerprint}
          </code>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground">
            {t("sshKeys.rotationNewFingerprint")}:
          </span>
          <span className="text-xs italic text-muted-foreground/70">
            {t("sshKeys.rotationStep4")}
          </span>
        </div>
      </div>

      <div className="flex justify-between pt-2">
        <Button variant="outline" onClick={handleBack}>
          {t("sshKeys.rotationPrev")}
        </Button>
        <Button
          className="border-warning/45 bg-warning/10 text-warning hover:border-warning/65 hover:bg-warning/15"
          onClick={handleNext}
        >
          {t("sshKeys.rotationConfirm")}
        </Button>
      </div>
    </>
  );

  // 步骤 4：执行结果
  const renderStep4 = () => {
    const skippedCount = results.filter((r) => r.status === "skipped").length;

    return (
      <>
        {loading && (
          <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {t("sshKeys.testing")}
          </div>
        )}

        {rotationError && (
          <InlineAlert tone="critical">{rotationError}</InlineAlert>
        )}

        {!loading && !rotationError && results.length > 0 && (
          <div className="space-y-3">
            <InlineAlert tone="success" title={t("sshKeys.rotationComplete")}>
              {t("sshKeys.rotationSuccess", {
                name: newKeyName.trim() || selectedKey?.name,
              })}
            </InlineAlert>

            {newFingerprint && (
              <div className="flex items-center gap-2 text-sm">
                <span className="text-muted-foreground">
                  {t("sshKeys.rotationNewFingerprint")}:
                </span>
                <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                  {newFingerprint}
                </code>
              </div>
            )}

            <div>
              <p className="mb-2 text-sm font-medium">
                {t("sshKeys.rotationVerifyResults")}
              </p>
              <div className="space-y-1.5">
                {results.map((r) => (
                  <div
                    key={r.nodeId}
                    className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm"
                  >
                    {r.status === "verified" && (
                      <CheckCircle2 className="size-4 shrink-0 text-success" />
                    )}
                    {r.status === "skipped" && (
                      <SkipForward className="size-4 shrink-0 text-muted-foreground" />
                    )}
                    {r.status === "failed" && (
                      <XCircle className="size-4 shrink-0 text-destructive" />
                    )}
                    <span className="min-w-0 truncate">{r.name}</span>
                    <span className="ml-auto text-xs text-muted-foreground">
                      {r.status === "verified" && t("sshKeys.rotationVerified")}
                      {r.status === "skipped" && t("sshKeys.rotationSkipped")}
                      {r.status === "failed" && t("sshKeys.rotationFailed")}
                    </span>
                  </div>
                ))}
              </div>
            </div>

            {skippedCount > 0 && (
              <InlineAlert tone="warning">
                {t("sshKeys.rotationOfflineHint", { count: skippedCount })}
              </InlineAlert>
            )}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          {rotationError && (
            <Button
              variant="outline"
              onClick={() => {
                setStep(3);
                setRotationError(null);
              }}
            >
              {t("sshKeys.rotationPrev")}
            </Button>
          )}
          <Button onClick={handleDone} disabled={loading}>
            {loading ? t("sshKeys.testing") : t("sshKeys.rotationDone")}
          </Button>
        </div>
      </>
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

        <div className="space-y-4 px-6 pb-6">{
          step === 1
            ? renderStep1()
            : step === 2
              ? renderStep2()
              : step === 3
                ? renderStep3()
                : renderStep4()
        }</div>
      </DialogContent>
    </Dialog>
  );
}
