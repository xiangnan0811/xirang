import { useTranslation } from "react-i18next";
import { Fingerprint, ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast-sonner";
import type { NodeHostKeyInfo, NodeHostKeyIssueCode } from "@/types/domain";

export type NodeHostKeyDialogProps = {
  open: boolean;
  issue: {
    nodeName: string;
    code: NodeHostKeyIssueCode;
    hostKey: NodeHostKeyInfo;
  } | null;
  isAdmin: boolean;
  trusting: boolean;
  error: string | null;
  onTrust: () => void;
  onOpenSettings: () => void;
  onOpenChange: (open: boolean) => void;
};

export function NodeHostKeyDialog({
  open,
  issue,
  isAdmin,
  trusting,
  error,
  onTrust,
  onOpenSettings,
  onOpenChange,
}: NodeHostKeyDialogProps) {
  const { t } = useTranslation();
  const mismatch = issue?.code === "ssh_host_key_mismatch";
  const unknown = issue?.code === "ssh_host_key_unknown";
  const canTrust = unknown && isAdmin;
  const title = mismatch ? t("nodes.hostKeyMismatchTitle") : t("nodes.hostKeyUnknownTitle");

  const copyFingerprint = async () => {
    const fingerprint = issue?.hostKey.fingerprintSha256;
    if (!fingerprint) {
      return;
    }
    try {
      await navigator.clipboard.writeText(fingerprint);
      toast.success(t("common.copiedToClipboard"));
    } catch {
      toast.error(t("common.copyFailed"));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {mismatch ? (
              <ShieldAlert className="size-4 shrink-0 text-warning" aria-hidden />
            ) : (
              <Fingerprint className="size-4 shrink-0 text-info" aria-hidden />
            )}
            <span>{issue ? `${title} — ${issue.nodeName}` : title}</span>
          </DialogTitle>
          <DialogDescription className={mismatch ? "sr-only" : "text-sm leading-relaxed text-muted-foreground"}>
            {mismatch ? t("nodes.hostKeyMismatchWarning") : t("nodes.hostKeyVerifyHint")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-3">
          {mismatch ? (
            <InlineAlert tone="warning">{t("nodes.hostKeyMismatchWarning")}</InlineAlert>
          ) : null}

          {issue ? (
            <dl className="space-y-3 rounded-lg border border-border bg-secondary/40 p-3">
              <div className="flex items-baseline justify-between gap-3">
                <dt className="text-xs text-muted-foreground">{t("nodes.colNode")}</dt>
                <dd className="min-w-0 truncate text-sm font-medium">{issue.nodeName}</dd>
              </div>
              <div className="flex items-baseline justify-between gap-3">
                <dt className="text-xs text-muted-foreground">{t("nodes.hostKeyAlgorithmLabel")}</dt>
                <dd className="font-mono text-sm" translate="no">{issue.hostKey.algorithm || "—"}</dd>
              </div>
              <div className="space-y-1.5">
                <dt className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                  <span>{t("nodes.hostKeyFingerprintLabel")}</span>
                  <Button type="button" variant="ghost" size="sm" onClick={() => void copyFingerprint()}>
                    {t("common.copy")}
                  </Button>
                </dt>
                <dd>
                  <code
                    translate="no"
                    spellCheck={false}
                    className="block select-all break-all rounded-md border border-border bg-background px-3 py-2.5 font-mono text-sm leading-6 tracking-wide text-foreground"
                  >
                    {issue.hostKey.fingerprintSha256}
                  </code>
                </dd>
              </div>
            </dl>
          ) : null}

          {canTrust ? (
            <p className="text-xs leading-relaxed text-muted-foreground">{t("nodes.hostKeyAutoAcceptHint")}</p>
          ) : null}
          {unknown && !isAdmin ? (
            <p className="text-sm font-medium">{t("nodes.hostKeyContactAdmin")}</p>
          ) : null}
        </DialogBody>

        {error ? (
          <div role="alert" className="mx-6 mb-3 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
            {error}
          </div>
        ) : null}

        <DialogFooter className="flex-wrap">
          {canTrust ? (
            <>
              <Button type="button" variant="outline" onClick={onOpenSettings} disabled={trusting}>
                {t("nodes.hostKeyOpenSettings")}
              </Button>
              <Button type="button" onClick={onTrust} loading={trusting} aria-busy={trusting}>
                {t("nodes.hostKeyTrustAndRetry")}
              </Button>
            </>
          ) : (
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.close")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
