import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { AlertTriangle, CheckCircle2, Loader2, XCircle } from "lucide-react";
import type { SSHKeyRotationErrorCode, SSHKeyRotationReason } from "@/lib/api/ssh-keys-api";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";

export type RotationSaveStatus = "not_submitted" | "validating" | "saved" | "not_saved" | "unknown" | "failed";

export interface NodeVerifyResult {
  nodeId: string;
  name: string;
  status: "verified" | "failed" | "unknown";
  errorCode?: SSHKeyRotationErrorCode;
}

interface RotationSummaryProps {
  saveStatus: RotationSaveStatus;
  reason: SSHKeyRotationReason | null;
  rotationError: string | null;
  results: NodeVerifyResult[];
  newPublicKeyFingerprint: string;
  onEditAgain: () => void;
  onDone: () => void;
}

const LINK_REASONS = new Set<SSHKeyRotationReason>([
  "scope_blocked",
  "trust_unavailable",
  "validation_timeout",
  "inventory_limit",
  "busy",
]);

type Translate = ReturnType<typeof useTranslation>["t"];

function reasonText(reason: SSHKeyRotationReason | null, t: Translate): string | null {
  switch (reason) {
    case "validation_failed":
      return t("sshKeys.rotationReasonValidationFailed");
    case "validation_timeout":
      return t("sshKeys.rotationReasonValidationTimeout");
    case "conflict":
      return t("sshKeys.rotationReasonConflict");
    case "scope_blocked":
      return t("sshKeys.rotationReasonScopeBlocked");
    case "trust_unavailable":
      return t("sshKeys.rotationReasonTrustUnavailable");
    case "inventory_limit":
      return t("sshKeys.rotationReasonInventoryLimit");
    case "busy":
      return t("sshKeys.rotationReasonBusy");
    default:
      return null;
  }
}

function errorText(code: SSHKeyRotationErrorCode, t: Translate): string {
  switch (code) {
    case "scope_denied":
      return t("sshKeys.rotationErrorScopeDenied");
    case "ssh_host_key_unknown":
      return t("sshKeys.rotationErrorHostKeyUnknown");
    case "ssh_host_key_mismatch":
      return t("sshKeys.rotationErrorHostKeyMismatch");
    case "connection_failed":
      return t("sshKeys.rotationErrorConnectionFailed");
    case "timeout":
      return t("sshKeys.rotationErrorTimeout");
    case "not_checked":
      return t("sshKeys.rotationErrorNotChecked");
  }
}

export function RotationSummary({
  saveStatus,
  reason,
  rotationError,
  results,
  newPublicKeyFingerprint,
  onEditAgain,
  onDone,
}: RotationSummaryProps) {
  const { t } = useTranslation();
  const busy = saveStatus === "validating";
  const showResults = (saveStatus === "saved" || saveStatus === "not_saved") && results.length > 0;
  const showNeutral = saveStatus === "saved" && results.length === 0;
  const allVerified = showResults && results.every((result) => result.status === "verified");
  const showEdit = saveStatus === "failed" || saveStatus === "not_saved";
  const showInventoryLinks = saveStatus === "not_saved" && reason !== null && LINK_REASONS.has(reason);
  const detail = reasonText(reason, t);

  return (
    <section
      className="min-w-0 space-y-3"
      aria-busy={busy || undefined}
      data-testid="rotation-summary"
      data-rotation-status={saveStatus}
      data-rotation-reason={reason ?? ""}
    >
      {saveStatus === "validating" && (
        <div
          role="status"
          id="ssh-key-rotation-status"
          className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground"
        >
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          {t("sshKeys.rotationValidating")}
        </div>
      )}

      {saveStatus === "unknown" && (
        <InlineAlert tone="warning">
          {rotationError ?? t("sshKeys.rotationSaveUnknown")}
        </InlineAlert>
      )}

      {saveStatus === "failed" && rotationError ? (
        <InlineAlert tone="critical">{rotationError}</InlineAlert>
      ) : null}

      {saveStatus === "not_saved" && (
        <InlineAlert tone="warning">
          <p>{t("sshKeys.rotationNotReplaced")}</p>
          {detail ? <p className="mt-1">{detail}</p> : null}
        </InlineAlert>
      )}

      {showNeutral && (
        <InlineAlert tone="info">{t("sshKeys.rotationNoNodeVerification")}</InlineAlert>
      )}

      {saveStatus === "saved" && allVerified ? (
        <InlineAlert tone="success">{t("sshKeys.rotationOutcomeSuccess")}</InlineAlert>
      ) : null}

      {saveStatus === "saved" && newPublicKeyFingerprint ? (
        <div className="flex min-w-0 flex-col gap-2 text-sm sm:flex-row sm:items-center">
          <span className="text-muted-foreground">
            {t("sshKeys.rotationNewFingerprint")}:
          </span>
          <code
            data-testid="rotation-saved-fingerprint"
            className="min-w-0 break-all rounded bg-muted px-1.5 py-0.5 text-xs"
          >
            {newPublicKeyFingerprint}
          </code>
        </div>
      ) : null}

      {showResults && (
        <div className="min-w-0">
          <p className="mb-2 text-sm font-medium">
            {t("sshKeys.rotationVerifyResults")}
          </p>
          <ul className="max-h-40 space-y-1.5 overflow-y-auto thin-scrollbar" aria-label={t("sshKeys.rotationVerifyResults")}>
            {results.map((result) => (
              <li
                key={result.nodeId}
                data-testid="rotation-node-result"
                data-node-id={result.nodeId}
                data-node-status={result.status}
                className="min-w-0 rounded-md border px-3 py-2 text-sm"
              >
                <div className="flex min-w-0 items-center gap-2">
                  {result.status === "verified" && (
                    <CheckCircle2 className="size-4 shrink-0 text-success" aria-hidden="true" />
                  )}
                  {result.status === "failed" && (
                    <XCircle className="size-4 shrink-0 text-destructive" aria-hidden="true" />
                  )}
                  {result.status === "unknown" && (
                    <AlertTriangle className="size-4 shrink-0 text-warning" aria-hidden="true" />
                  )}
                  <span className="min-w-0 truncate">{result.name}</span>
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                    {result.status === "verified" && t("sshKeys.rotationVerified")}
                    {result.status === "failed" && t("sshKeys.rotationFailed")}
                    {result.status === "unknown" && t("sshKeys.rotationVerifyUnknown")}
                  </span>
                </div>
                {result.errorCode ? (
                  <p className="mt-1 break-words pl-6 text-xs text-destructive">{errorText(result.errorCode, t)}</p>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      )}

      {showInventoryLinks ? (
        <div className="flex flex-wrap gap-2">
          <Button asChild size="sm" variant="outline">
            <Link to="/app/ssh-keys" onClick={onDone}>{t("sshKeys.rotationDisabledOpenKeys")}</Link>
          </Button>
          <Button asChild size="sm" variant="outline">
            <Link to="/app/nodes" onClick={onDone}>{t("sshKeys.rotationOpenNodes")}</Link>
          </Button>
        </div>
      ) : null}

      {saveStatus === "unknown" ? (
        <div className="flex flex-wrap gap-2">
          <Button asChild size="sm" variant="outline">
            <Link to="/app/ssh-keys" onClick={onDone}>{t("sshKeys.rotationDisabledOpenKeys")}</Link>
          </Button>
        </div>
      ) : null}

      <div className="flex flex-wrap justify-end gap-2 pt-2">
        {showEdit && (
          <Button variant="outline" onClick={onEditAgain} disabled={busy}>
            {t("sshKeys.rotationEditAgain")}
          </Button>
        )}
        <Button onClick={onDone} disabled={busy}>
          {t("sshKeys.rotationDone")}
        </Button>
      </div>
    </section>
  );
}
