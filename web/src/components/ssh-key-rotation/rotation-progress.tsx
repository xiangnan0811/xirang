import { useTranslation } from "react-i18next";
import { Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { type NodeRecord } from "@/types/domain";

interface RotationProgressProps {
  affectedNodes: NodeRecord[];
  acknowledgement: string;
  onAcknowledgementChange: (value: string) => void;
  candidatePublicKey: string;
  oldPublicKeyFingerprint?: string;
  newPublicKeyFingerprint: string;
  onCopyPublicKey: () => void;
  onBack: () => void;
  onNext: () => void;
}

export function RotationProgress({
  affectedNodes,
  acknowledgement,
  onAcknowledgementChange,
  candidatePublicKey,
  oldPublicKeyFingerprint,
  newPublicKeyFingerprint,
  onCopyPublicKey,
  onBack,
  onNext,
}: RotationProgressProps) {
  const { t } = useTranslation();
  const onlineCount = affectedNodes.filter((node) => node.status === "online").length;
  const offlineCount = affectedNodes.length - onlineCount;
  const expectedAcknowledgement = String(affectedNodes.length);
  const normalizedAcknowledgement = acknowledgement.trim();
  const canConfirm = normalizedAcknowledgement === expectedAcknowledgement;
  const showAcknowledgementError = normalizedAcknowledgement.length > 0 && !canConfirm;
  const acknowledgementDescriptionId = showAcknowledgementError
    ? "ssh-key-rotation-ack-hint ssh-key-rotation-ack-error"
    : "ssh-key-rotation-ack-hint";
  const oldFingerprint = oldPublicKeyFingerprint?.trim()
    ? oldPublicKeyFingerprint
    : t("sshKeys.publicKeyFingerprintUnknown");
  const newFingerprint = newPublicKeyFingerprint.trim()
    ? newPublicKeyFingerprint
    : t("sshKeys.publicKeyFingerprintUnknown");

  return (
    <>
      <InlineAlert tone="warning">
        {t("sshKeys.rotationWarning", { count: affectedNodes.length })}
      </InlineAlert>
      <InlineAlert tone="info">{t("sshKeys.rotationAdminDeployNotice")}</InlineAlert>

      <p className="text-xs text-muted-foreground">{t("sshKeys.rotationStatusHint")}</p>

      <div className="grid gap-2 rounded-lg border border-border/60 bg-muted/30 p-3 text-sm sm:grid-cols-3">
        <div>
          <p className="text-xs text-muted-foreground">{t("sshKeys.rotationAffectedTotal")}</p>
          <p className="font-semibold">{affectedNodes.length}</p>
        </div>
        <div>
          <p className="text-xs text-muted-foreground">{t("sshKeys.rotationAffectedOnline")}</p>
          <p className="font-semibold">{onlineCount}</p>
        </div>
        <div>
          <p className="text-xs text-muted-foreground">{t("sshKeys.rotationAffectedOffline")}</p>
          <p className="font-semibold">{offlineCount}</p>
        </div>
      </div>

      <div>
        <p className="mb-2 text-sm font-medium">
          {t("sshKeys.rotationAffectedNodes")}
        </p>
        <div className="max-h-40 space-y-1.5 overflow-y-auto rounded-lg border border-border/60 p-2 thin-scrollbar">
          {affectedNodes.length === 0 ? (
            <p className="px-2 py-1.5 text-sm text-muted-foreground">
              {t("sshKeys.rotationNoNodeVerification")}
            </p>
          ) : (
            affectedNodes.map((node) => (
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
            ))
          )}
        </div>
      </div>

      <div className="space-y-2 text-sm">
        <div className="flex items-start justify-between gap-2">
          <p className="font-medium">{t("sshKeys.rotationCandidatePublicKey")}</p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onCopyPublicKey}
            disabled={!candidatePublicKey}
          >
            <Copy className="size-3.5" />
            {t("sshKeys.copyPublicKey")}
          </Button>
        </div>
        <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border/60 bg-muted/30 p-2 text-xs thin-scrollbar">
          {candidatePublicKey}
        </pre>
      </div>

      <dl className="space-y-1 text-sm">
        <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
          <dt className="text-muted-foreground">
            {t("sshKeys.rotationOldPublicFingerprint")}:
          </dt>
          <dd className="min-w-0 break-all">
            <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{oldFingerprint}</code>
          </dd>
        </div>
        <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
          <dt className="text-muted-foreground">
            {t("sshKeys.rotationNewPublicFingerprint")}:
          </dt>
          <dd className="min-w-0 break-all">
            <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{newFingerprint}</code>
          </dd>
        </div>
      </dl>

      <div>
        <label htmlFor="ssh-key-rotation-ack" className="mb-1.5 block text-sm font-medium">
          {t("sshKeys.rotationAckLabel", { count: affectedNodes.length })}
        </label>
        <input
          id="ssh-key-rotation-ack"
          type="text"
          inputMode="numeric"
          className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
          value={acknowledgement}
          onChange={(event) => onAcknowledgementChange(event.target.value)}
          placeholder={expectedAcknowledgement}
          autoComplete="off"
          aria-describedby={acknowledgementDescriptionId}
          aria-invalid={showAcknowledgementError}
        />
        <p id="ssh-key-rotation-ack-hint" className="mt-1 text-xs text-muted-foreground">
          {t("sshKeys.rotationAckHint")}
        </p>
        {showAcknowledgementError ? (
          <p id="ssh-key-rotation-ack-error" className="mt-1 text-xs text-destructive">
            {t("sshKeys.rotationAckMismatch", { count: affectedNodes.length })}
          </p>
        ) : null}
      </div>

      <div className="flex justify-between pt-2">
        <Button variant="outline" onClick={onBack}>
          {t("sshKeys.rotationPrev")}
        </Button>
        <Button
          className="border-warning/45 bg-warning/10 text-warning hover:border-warning/65 hover:bg-warning/15"
          onClick={onNext}
          disabled={!canConfirm}
        >
          {t("sshKeys.rotationConfirm")}
        </Button>
      </div>
    </>
  );
}
