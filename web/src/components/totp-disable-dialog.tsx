import { useState } from "react";
import { useTranslation } from "react-i18next";
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

interface TOTPDisableDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  token: string;
  onSuccess?: () => void;
}

export function TOTPDisableDialog({ open, onOpenChange, token, onSuccess }: TOTPDisableDialogProps) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [totpCode, setTotpCode] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleClose = (isOpen: boolean) => {
    if (!isOpen) {
      setPassword("");
      setTotpCode("");
      setError(null);
    }
    onOpenChange(isOpen);
  };

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);
    setError(null);
    try {
      await apiClient.totpDisable(token, password, totpCode);
      handleClose(false);
      onSuccess?.();
    } catch (err) {
      const msg = err instanceof ApiError
        ? (err.detail && typeof err.detail === "object"
            ? ((err.detail as { error?: string }).error ?? t("totp.disableFailed"))
            : t("totp.disableFailed"))
        : t("totp.disableFailed");
      setError(msg);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{t("totp.disableTitle")}</DialogTitle>
          <DialogDescription>
            {t("totp.disableDesc")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody>
          <form id="totp-disable-form" className="space-y-3" onSubmit={handleSubmit}>
            <div className="space-y-1.5">
              <label className="text-sm font-medium" htmlFor="totp-disable-password">
                {t("totp.accountPassword")}
              </label>
              <Input
                id="totp-disable-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                placeholder={t("totp.accountPasswordPlaceholder")}
                autoFocus
                required
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium" htmlFor="totp-disable-code">
                {t("totp.verificationCode")}
              </label>
              <Input
                id="totp-disable-code"
                value={totpCode}
                onChange={(e) => setTotpCode(e.target.value)}
                autoComplete="one-time-code"
                placeholder={t("totp.codePlaceholder")}
                required
              />
            </div>
            {error ? (
              <p role="alert" className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {error}
              </p>
            ) : null}
          </form>
        </DialogBody>

        <DialogFooter>
          <Button type="button" variant="ghost" onClick={() => handleClose(false)}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" form="totp-disable-form" variant="destructive" loading={loading}>
            {t("totp.disableButton")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
