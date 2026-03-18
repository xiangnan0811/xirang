import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function AccountTab() {
  const { t } = useTranslation();
  const { token, username, role } = useAuth();
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
      <div className="rounded-lg border p-4 space-y-2">
        <h3 className="text-sm font-medium">{t("settings.account.sessionInfo")}</h3>
        <div className="flex gap-4 text-sm text-muted-foreground">
          <span>{t("settings.account.username")}: <strong className="text-foreground">{username}</strong></span>
          <span>{t("settings.account.role")}: <strong className="text-foreground">{role}</strong></span>
        </div>
      </div>

      {/* 修改密码 */}
      <div className="rounded-lg border p-4 space-y-4">
        <h3 className="text-sm font-medium">{t("settings.account.changePassword")}</h3>
        <div className="space-y-3 max-w-sm">
          <input
            type="password"
            className={inputClass}
            placeholder={t("settings.account.currentPassword")}
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            autoComplete="current-password"
          />
          <input
            type="password"
            className={inputClass}
            placeholder={t("settings.account.newPassword")}
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
          />
          <input
            type="password"
            className={inputClass}
            placeholder={t("settings.account.confirmPassword")}
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
      <div className="rounded-lg border p-4 space-y-2">
        <h3 className="text-sm font-medium">{t("settings.account.twoFactor")}</h3>
        <p className="text-sm text-muted-foreground">{t("settings.account.twoFactorDesc")}</p>
      </div>
    </div>
  );
}
