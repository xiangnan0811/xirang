import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { type NodeRecord, type SSHKeyRecord } from "@/types/domain";

interface RotationProgressProps {
  selectedKey: SSHKeyRecord | null;
  affectedNodes: NodeRecord[];
  acknowledgement: string;
  onAcknowledgementChange: (value: string) => void;
  onBack: () => void;
  onNext: () => void;
}

export function RotationProgress({
  selectedKey,
  affectedNodes,
  acknowledgement,
  onAcknowledgementChange,
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

  return (
    <>
      <InlineAlert tone="warning">
        {t("sshKeys.rotationWarning", { count: affectedNodes.length })}
      </InlineAlert>

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
