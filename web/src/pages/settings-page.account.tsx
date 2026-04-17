import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ShieldCheck, ShieldOff } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { TOTPSetupDialog } from "@/components/totp-setup-dialog";
import { TOTPDisableDialog } from "@/components/totp-disable-dialog";
import { cn } from "@/lib/utils";

export function AccountTab() {
  const { t } = useTranslation();
  const { token, username, role, totpEnabled, setTotpEnabled } = useAuth();
  const [totpSetupOpen, setTotpSetupOpen] = useState(false);
  const [totpDisableOpen, setTotpDisableOpen] = useState(false);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordMsg, setPasswordMsg] = useState<{ type: "success" | "error"; text: string } | null>(null);
  const [loading, setLoading] = useState(false);

  const handleChangePassword = async () => {
    setPasswordMsg(null);
    if (newPassword !== confirmPassword) {
      setPasswordMsg({ type: "error", text: t("settings.account.passwordMismatch") });
      return;
    }
    if (newPassword.length < 8) {
      setPasswordMsg({ type: "error", text: t("settings.account.passwordTooShort") });
      return;
    }
    setLoading(true);
    try {
      await apiClient.changePassword(token!, currentPassword, newPassword);
      setPasswordMsg({ type: "success", text: t("settings.account.passwordChanged") });
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t("common.operationFailed");
      setPasswordMsg({ type: "error", text: message });
    } finally {
      setLoading(false);
    }
  };

  const inputClass = "h-9 w-full rounded-md border border-input bg-background px-3 text-sm";

  return (
    <div className="space-y-8 max-w-2xl">
      <h2 className="text-lg font-semibold">{t("settings.account.title")}</h2>

      {/* 会话信息 */}
      <div className="rounded-lg border border-border bg-card shadow-sm relative overflow-hidden p-5 space-y-2">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
        <h3 className="text-sm font-medium">{t("settings.account.sessionInfo")}</h3>
        <div className="flex gap-4 text-sm text-muted-foreground">
          <span>{t("settings.account.username")}: <strong className="text-foreground">{username}</strong></span>
          <span>{t("settings.account.role")}: <strong className="text-foreground">{role}</strong></span>
        </div>
      </div>

      {/* 修改密码 */}
      <div className="rounded-lg border border-border bg-card shadow-sm relative overflow-hidden p-5 space-y-4">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
        <h3 className="text-sm font-medium">{t("settings.account.changePassword")}</h3>
        <div className="space-y-3 max-w-sm">
          <input
            id="current-password"
            name="current-password"
            type="password"
            className={inputClass}
            placeholder={t("settings.account.currentPassword")}
            aria-label={t("settings.account.currentPassword")}
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            autoComplete="current-password"
          />
          <input
            id="new-password"
            name="new-password"
            type="password"
            className={inputClass}
            placeholder={t("settings.account.newPassword")}
            aria-label={t("settings.account.newPassword")}
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
          />
          <input
            id="confirm-password"
            name="confirm-password"
            type="password"
            className={inputClass}
            placeholder={t("settings.account.confirmPassword")}
            aria-label={t("settings.account.confirmPassword")}
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
          />
          {passwordMsg && (
            <p className={cn("text-xs", passwordMsg.type === "error" ? "text-destructive" : "text-success")}>
              {passwordMsg.text}
            </p>
          )}
          <Button onClick={handleChangePassword} disabled={loading || !currentPassword || !newPassword}>
            {t("settings.account.changePasswordBtn")}
          </Button>
        </div>
      </div>

      {/* 2FA section */}
      <div className="rounded-lg border border-border bg-card shadow-sm relative overflow-hidden p-5 space-y-3">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
        <h3 className="text-sm font-medium">{t("settings.account.twoFactor")}</h3>
        <div className="flex items-center gap-3">
          {totpEnabled ? (
            <>
              <span className="inline-flex items-center gap-1.5 rounded-md bg-success/15 px-2.5 py-1 text-sm font-medium text-success">
                <ShieldCheck className="size-4" />
                {t("settings.account.twoFactorEnabled")}
              </span>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setTotpDisableOpen(true)}
              >
                {t("settings.account.disableTwoFactor")}
              </Button>
            </>
          ) : (
            <>
              <span className="inline-flex items-center gap-1.5 rounded-md bg-muted px-2.5 py-1 text-sm text-muted-foreground">
                <ShieldOff className="size-4" />
                {t("settings.account.twoFactorDisabled")}
              </span>
              <Button
                size="sm"
                onClick={() => setTotpSetupOpen(true)}
              >
                {t("settings.account.enableTwoFactor")}
              </Button>
            </>
          )}
        </div>
      </div>

      {token ? (
        <>
          <TOTPSetupDialog
            open={totpSetupOpen}
            onOpenChange={setTotpSetupOpen}
            token={token}
            onSuccess={() => setTotpEnabled(true)}
          />
          <TOTPDisableDialog
            open={totpDisableOpen}
            onOpenChange={setTotpDisableOpen}
            token={token}
            onSuccess={() => setTotpEnabled(false)}
          />
        </>
      ) : null}
    </div>
  );
}
