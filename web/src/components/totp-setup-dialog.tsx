import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { QRCodeSVG } from "qrcode.react";
import { Copy, Check } from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { ApiError, apiClient } from "@/lib/api/client";

type Step = "setup" | "verify" | "recovery";

interface TOTPSetupDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  token: string;
  onSuccess?: () => void;
}

export function TOTPSetupDialog({ open, onOpenChange, token, onSuccess }: TOTPSetupDialogProps) {
  const { t } = useTranslation();
  const [step, setStep] = useState<Step>("setup");
  const [secret, setSecret] = useState("");
  const [qrUrl, setQrUrl] = useState("");
  const [verifyCode, setVerifyCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // 对话框打开时获取 TOTP 密钥（useEffect 保证受控模式下也能触发）
  useEffect(() => {
    if (!open || step !== "setup" || secret) return;
    let cancelled = false;
    setLoading(true);
    setError(null);

    apiClient
      .totpSetup(token)
      .then((data) => {
        if (cancelled) return;
        setSecret(data.secret);
        setQrUrl(data.qr_url);
      })
      .catch((err) => {
        if (cancelled) return;
        const msg =
          err instanceof ApiError
            ? (err.detail && typeof err.detail === "object"
                ? ((err.detail as { error?: string }).error ?? t("totp.generateKeyFailed"))
                : t("totp.generateKeyFailed"))
            : t("totp.generateKeyFailed");
        setError(msg);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => { cancelled = true; };
  }, [open, step, secret, token, t]);

  const handleClose = (isOpen: boolean) => {
    if (!isOpen) {
      const wasCompleted = step === "recovery";
      setStep("setup");
      setSecret("");
      setQrUrl("");
      setVerifyCode("");
      setRecoveryCodes([]);
      setError(null);
      setCopied(false);
      if (wasCompleted) {
        onSuccess?.();
      }
    }
    onOpenChange(isOpen);
  };

  const handleVerify = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);
    setError(null);
    try {
      const data = await apiClient.totpVerify(token, verifyCode);
      setRecoveryCodes(data.recovery_codes);
      setStep("recovery");
    } catch (err) {
      const msg = err instanceof ApiError
        ? (err.detail && typeof err.detail === "object"
            ? ((err.detail as { error?: string }).error ?? t("totp.codeError"))
            : t("totp.codeError"))
        : t("totp.verifyFailed");
      setError(msg);
    } finally {
      setLoading(false);
    }
  };

  const handleCopyCodes = async () => {
    await navigator.clipboard.writeText(recoveryCodes.join("\n"));
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{t("totp.setupTitle")}</DialogTitle>
          <DialogDescription>
            {step === "setup" && t("totp.setupStepScan")}
            {step === "verify" && t("totp.setupStepVerify")}
            {step === "recovery" && t("totp.setupStepRecovery")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-4">
          {step === "setup" && (
            <>
              {loading ? (
                <p className="text-center text-sm text-muted-foreground">{t("totp.generatingKey")}</p>
              ) : error ? (
                <p role="alert" className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {error}
                </p>
              ) : (
                <>
                  {qrUrl && (
                    <div className="flex justify-center">
                      <div className="rounded-lg border border-border bg-white p-3">
                        <QRCodeSVG value={qrUrl} size={176} />
                      </div>
                    </div>
                  )}
                  <div className="space-y-1">
                    <p className="text-xs text-muted-foreground">{t("totp.cannotScanHint")}</p>
                    <p className="break-all rounded-lg bg-muted px-3 py-2 font-mono text-xs select-all">
                      {secret}
                    </p>
                  </div>
                </>
              )}
            </>
          )}

          {step === "verify" && (
            <form id="totp-verify-form" className="space-y-3" onSubmit={handleVerify}>
              <div className="space-y-1.5">
                <label className="text-sm font-medium" htmlFor="totp-verify-code">
                  {t("totp.verificationCode")}
                </label>
                <Input
                  id="totp-verify-code"
                  value={verifyCode}
                  onChange={(e) => setVerifyCode(e.target.value)}
                  autoComplete="one-time-code"
                  placeholder={t("totp.codePlaceholder")}
                  autoFocus
                  required
                />
              </div>
              {error ? (
                <p role="alert" className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {error}
                </p>
              ) : null}
            </form>
          )}

          {step === "recovery" && (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">
                {t("totp.recoverySuccess")}
              </p>
              <div className="rounded-lg bg-muted p-3 font-mono text-sm">
                {recoveryCodes.map((code) => (
                  <div key={code} className="py-0.5">{code}</div>
                ))}
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="w-full gap-2"
                onClick={handleCopyCodes}
              >
                {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                {copied ? t("totp.copiedRecovery") : t("totp.copyRecovery")}
              </Button>
            </div>
          )}
        </DialogBody>

        <DialogFooter>
          {step === "setup" && (
            <Button
              type="button"
              disabled={loading || !secret}
              onClick={() => { setStep("verify"); setError(null); }}
            >
              {t("common.next")}
            </Button>
          )}
          {step === "verify" && (
            <>
              <Button type="button" variant="ghost" onClick={() => { setStep("setup"); setError(null); }}>
                {t("common.prev")}
              </Button>
              <Button type="submit" form="totp-verify-form" loading={loading}>
                {t("totp.verifyAndEnable")}
              </Button>
            </>
          )}
          {step === "recovery" && (
            <Button type="button" onClick={() => handleClose(false)}>
              {t("common.finish")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
