import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { type NodeRecord, type SSHKeyRecord } from "@/types/domain";

interface RotationProgressProps {
  selectedKey: SSHKeyRecord | null;
  affectedNodes: NodeRecord[];
  onBack: () => void;
  onNext: () => void;
}

export function RotationProgress({
  selectedKey,
  affectedNodes,
  onBack,
  onNext,
}: RotationProgressProps) {
  const { t } = useTranslation();

  return (
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
        <Button variant="outline" onClick={onBack}>
          {t("sshKeys.rotationPrev")}
        </Button>
        <Button
          className="border-warning/45 bg-warning/10 text-warning hover:border-warning/65 hover:bg-warning/15"
          onClick={onNext}
        >
          {t("sshKeys.rotationConfirm")}
        </Button>
      </div>
    </>
  );
}
