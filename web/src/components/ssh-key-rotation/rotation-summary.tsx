import { useTranslation } from "react-i18next";
import { CheckCircle2, Loader2, SkipForward, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";

export interface NodeVerifyResult {
  nodeId: string;
  name: string;
  status: "verified" | "skipped" | "failed";
  error?: string;
}

interface RotationSummaryProps {
  loading: boolean;
  rotationError: string | null;
  results: NodeVerifyResult[];
  newFingerprint: string;
  newKeyName: string;
  selectedKeyName?: string;
  onRetry: () => void;
  onDone: () => void;
}

export function RotationSummary({
  loading,
  rotationError,
  results,
  newFingerprint,
  newKeyName,
  selectedKeyName,
  onRetry,
  onDone,
}: RotationSummaryProps) {
  const { t } = useTranslation();
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
              name: newKeyName.trim() || selectedKeyName,
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
          <Button variant="outline" onClick={onRetry}>
            {t("sshKeys.rotationPrev")}
          </Button>
        )}
        <Button onClick={onDone} disabled={loading}>
          {loading ? t("sshKeys.testing") : t("sshKeys.rotationDone")}
        </Button>
      </div>
    </>
  );
}
