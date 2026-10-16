import { lazy, Suspense, type FormEvent, type Ref } from "react";
import { useTranslation } from "react-i18next";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

// The login route is in the main entry; keep the QR encoder in its own chunk so only the rare
// recovery-enrollment mode pays for it. Only the QR image suspends: the secret, code input and
// actions render immediately, so the login page's focus of `codeInputRef` is unaffected.
const LazyQRCodeSVG = lazy(() =>
  import("qrcode.react").then((module) => ({ default: module.QRCodeSVG }))
);
const QR_SIZE = 160;

// Page-private presentation for the login page's offline-recovery modes. These components hold
// no state: the login page owns the restricted token, secret and codes in memory only, so a
// single owner clears them on back, expiry, identity change or unmount.

const errorClassName = "rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive";

type RecoveryEnrollmentPanelProps = {
  qrUrl: string;
  secret: string;
  expiresInMinutes: number;
  code: string;
  onCodeChange: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  onBack: () => void;
  submitting: boolean;
  error: string | null;
  codeInputRef: Ref<HTMLInputElement>;
};

export function RecoveryEnrollmentPanel({
  qrUrl,
  secret,
  expiresInMinutes,
  code,
  onCodeChange,
  onSubmit,
  onBack,
  submitting,
  error,
  codeInputRef,
}: RecoveryEnrollmentPanelProps) {
  const { t } = useTranslation();
  const errorId = "recovery-enrollment-error";

  return (
    <form className="space-y-4" onSubmit={onSubmit} data-testid="recovery-enrollment-panel" noValidate>
      <ol className="space-y-4">
        <li className="space-y-3">
          <p className="text-sm font-medium">
            <span className="mr-2 inline-flex size-5 items-center justify-center rounded-full bg-primary/10 text-xs text-primary" aria-hidden>1</span>
            {t("login.recoveryEnrollmentStepScan")}
          </p>
          <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-start">
            <div className="shrink-0 rounded-xl border border-border/50 bg-white p-3 shadow-sm">
              <Suspense
                fallback={(
                  <div
                    role="status"
                    className="flex items-center justify-center text-xs text-muted-foreground"
                    style={{ width: QR_SIZE, height: QR_SIZE }}
                  >
                    {t("common.loading")}
                  </div>
                )}
              >
                <LazyQRCodeSVG
                  value={qrUrl}
                  size={QR_SIZE}
                  role="img"
                  aria-label={t("login.recoveryEnrollmentQrAlt")}
                  data-testid="recovery-enrollment-qr"
                />
              </Suspense>
            </div>
            <div className="w-full min-w-0 space-y-1">
              <p id="recovery-enrollment-secret-label" className="text-xs text-muted-foreground">
                {t("login.recoveryEnrollmentSecretLabel")}
              </p>
              <p
                className="break-all rounded-lg bg-muted px-3 py-2 font-mono text-xs select-all"
                aria-labelledby="recovery-enrollment-secret-label"
                data-testid="recovery-enrollment-secret"
              >
                {secret}
              </p>
            </div>
          </div>
        </li>
        <li className="space-y-2">
          <div className="flex items-center">
            <span className="mr-2 inline-flex size-5 items-center justify-center rounded-full bg-primary/10 text-xs text-primary" aria-hidden>2</span>
            <label className="text-sm font-medium" htmlFor="recovery-enrollment-code">
              {t("login.recoveryEnrollmentCodeLabel")}
            </label>
          </div>
          <Input
            ref={codeInputRef}
            id="recovery-enrollment-code"
            type="text"
            value={code}
            onChange={(event) => onCodeChange(event.target.value)}
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder={t("login.totpPlaceholder")}
            maxLength={12}
            aria-invalid={Boolean(error)}
            aria-errormessage={error ? errorId : undefined}
            aria-describedby="recovery-enrollment-expiry"
            disabled={submitting}
            required
          />
        </li>
      </ol>

      <p id="recovery-enrollment-expiry" className="text-xs leading-relaxed text-muted-foreground">
        {t("login.recoveryEnrollmentExpiresHint", { minutes: expiresInMinutes })}
      </p>

      {submitting ? (
        <p role="status" className="text-sm text-muted-foreground">{t("login.recoveryEnrollmentVerifying")}</p>
      ) : null}
      {error ? (
        <p id={errorId} role="alert" className={errorClassName}>{error}</p>
      ) : null}

      <Button className="w-full" type="submit" loading={submitting} data-testid="recovery-enrollment-verify">
        {t("login.recoveryEnrollmentVerify")}
      </Button>
      <Button
        className="w-full"
        type="button"
        variant="ghost"
        disabled={submitting}
        onClick={onBack}
        data-testid="recovery-enrollment-back"
      >
        {t("login.recoveryEnrollmentBack")}
      </Button>
    </form>
  );
}

export type RecoveryCodesCopyState = "idle" | "copied" | "failed";

type RecoveryCodesPanelProps = {
  codes: readonly string[];
  copyState: RecoveryCodesCopyState;
  onCopy: () => void;
  saved: boolean;
  onSavedChange: (saved: boolean) => void;
  onReturn: () => void;
};

export function RecoveryCodesPanel({ codes, copyState, onCopy, saved, onSavedChange, onReturn }: RecoveryCodesPanelProps) {
  const { t } = useTranslation();

  return (
    <div className="space-y-4" data-testid="recovery-codes-panel">
      <ol
        aria-label={t("login.recoveryEnrollmentCodesListLabel")}
        className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-lg border border-border bg-muted p-3 font-mono text-sm select-all"
        data-testid="recovery-enrollment-codes"
      >
        {codes.map((code) => (
          <li key={code} className="tracking-wide" data-testid="recovery-enrollment-code-item">{code}</li>
        ))}
      </ol>

      <Button
        type="button"
        variant="outline"
        className="w-full gap-2"
        onClick={onCopy}
        data-testid="recovery-enrollment-copy"
      >
        {copyState === "copied" ? <Check className="size-4" aria-hidden /> : <Copy className="size-4" aria-hidden />}
        {copyState === "copied" ? t("login.recoveryEnrollmentCopied") : t("login.recoveryEnrollmentCopy")}
      </Button>
      {copyState === "failed" ? (
        <p role="alert" className={errorClassName}>{t("login.recoveryEnrollmentCopyFailed")}</p>
      ) : null}

      <label className="flex items-start gap-2 text-sm" htmlFor="recovery-enrollment-saved">
        <input
          id="recovery-enrollment-saved"
          type="checkbox"
          className="mt-1"
          checked={saved}
          onChange={(event) => onSavedChange(event.target.checked)}
        />
        <span>{t("login.recoveryEnrollmentSaved")}</span>
      </label>

      <Button
        className="w-full"
        type="button"
        disabled={!saved}
        onClick={onReturn}
        aria-describedby="recovery-enrollment-return-hint"
        data-testid="recovery-enrollment-return"
      >
        {t("login.recoveryEnrollmentReturn")}
      </Button>
      <p id="recovery-enrollment-return-hint" className="text-xs leading-relaxed text-muted-foreground">
        {t("login.recoveryEnrollmentReturnHint")}
      </p>
    </div>
  );
}
