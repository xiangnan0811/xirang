import { useTranslation } from "react-i18next";
import { AlertTriangle, CheckCircle2, Loader2, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { DisabledKeyRemediation } from "./rotation-preview";

export type RotationSaveStatus = "not_submitted" | "saving" | "saved" | "unknown" | "failed";

export interface NodeVerifyResult {
  nodeId: string;
  name: string;
  status: "verified" | "failed" | "unknown";
  error?: string;
}

interface RotationSummaryProps {
  saveStatus: RotationSaveStatus;
  verifying: boolean;
  rotationError: string | null;
  results: NodeVerifyResult[];
  newPublicKeyFingerprint: string;
  showDisabledRemediation?: boolean;
  onNavigate: () => void;
  onReverify: () => void;
  onEditAgain: () => void;
  onDone: () => void;
}

function verificationTone(results: NodeVerifyResult[]): "success" | "warning" | "critical" {
  if (results.every((result) => result.status === "verified")) return "success";
  if (results.length > 0 && results.every((result) => result.status === "failed")) return "critical";
  return "warning";
}

export function RotationSummary({
  saveStatus,
  verifying,
  rotationError,
  results,
  newPublicKeyFingerprint,
  showDisabledRemediation = false,
  onNavigate,
  onReverify,
  onEditAgain,
  onDone,
}: RotationSummaryProps) {
  const { t } = useTranslation();
  const busy = saveStatus === "saving" || verifying;
  const showResults = saveStatus === "saved" && results.length > 0;
  const showNeutral = saveStatus === "saved" && results.length === 0 && !verifying;
  const canReverify = showResults && results.some((result) => result.status !== "verified");
  const tone = showResults ? verificationTone(results) : "success";

  return (
    <>
      {saveStatus === "saving" && (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          {t("sshKeys.rotationSaving")}
        </div>
      )}

      {saveStatus === "unknown" && (
        <InlineAlert tone="warning">
          {rotationError ?? t("sshKeys.rotationSaveUnknown")}
        </InlineAlert>
      )}

      {saveStatus === "failed" && rotationError ? (
        <div className="space-y-3">
          <InlineAlert tone="critical">{rotationError}</InlineAlert>
          {showDisabledRemediation ? <DisabledKeyRemediation onNavigate={onNavigate} /> : null}
        </div>
      ) : null}

      {verifying && results.length === 0 && saveStatus === "saved" ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          {t("sshKeys.testing")}
        </div>
      ) : null}

      {showNeutral && (
        <InlineAlert tone="info">{t("sshKeys.rotationNoNodeVerification")}</InlineAlert>
      )}

      {showResults && (
        <div className="space-y-3">
          <InlineAlert tone={tone}>
            {tone === "success" ? t("sshKeys.rotationOutcomeSuccess") : null}
            {tone === "warning" ? t("sshKeys.rotationOutcomePartial") : null}
            {tone === "critical" ? t("sshKeys.rotationOutcomeFailed") : null}
          </InlineAlert>

          {newPublicKeyFingerprint ? (
            <div className="flex min-w-0 flex-col gap-2 text-sm sm:flex-row sm:items-center">
              <span className="text-muted-foreground">
                {t("sshKeys.rotationNewPublicFingerprint")}:
              </span>
              <code className="min-w-0 break-all rounded bg-muted px-1.5 py-0.5 text-xs">
                {newPublicKeyFingerprint}
              </code>
            </div>
          ) : null}

          <div>
            <p className="mb-2 text-sm font-medium">
              {t("sshKeys.rotationVerifyResults")}
            </p>
            <div className="space-y-1.5">
              {results.map((result) => (
                <div
                  key={result.nodeId}
                  className="rounded-md border px-3 py-2 text-sm"
                >
                  <div className="flex items-center gap-2">
                    {result.status === "verified" && (
                      <CheckCircle2 className="size-4 shrink-0 text-success" />
                    )}
                    {result.status === "failed" && (
                      <XCircle className="size-4 shrink-0 text-destructive" />
                    )}
                    {result.status === "unknown" && (
                      <AlertTriangle className="size-4 shrink-0 text-warning" />
                    )}
                    <span className="min-w-0 truncate">{result.name}</span>
                    <span className="ml-auto text-xs text-muted-foreground">
                      {result.status === "verified" && t("sshKeys.rotationVerified")}
                      {result.status === "failed" && t("sshKeys.rotationFailed")}
                      {result.status === "unknown" && t("sshKeys.rotationVerifyUnknown")}
                    </span>
                  </div>
                  {result.status === "failed" && result.error ? (
                    <p className="mt-1 break-words pl-6 text-xs text-destructive">{result.error}</p>
                  ) : null}
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      <div className="flex justify-end gap-2 pt-2">
        {saveStatus === "failed" && (
          <Button variant="outline" onClick={onEditAgain} disabled={busy}>
            {t("sshKeys.rotationEditAgain")}
          </Button>
        )}
        {canReverify && (
          <Button variant="outline" onClick={onReverify} disabled={busy} loading={verifying}>
            {t("sshKeys.rotationReverifySubset")}
          </Button>
        )}
        <Button onClick={onDone} disabled={busy}>
          {busy ? t("sshKeys.testing") : t("sshKeys.rotationDone")}
        </Button>
      </div>
    </>
  );
}
