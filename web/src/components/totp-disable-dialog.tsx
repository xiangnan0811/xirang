import { useState } from "react";
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
            ? ((err.detail as { error?: string }).error ?? "禁用失败")
            : "禁用失败")
        : "禁用失败";
      setError(msg);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>禁用两步验证</DialogTitle>
          <DialogDescription>
            输入账号密码和当前验证码以关闭两步验证。
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody>
          <form id="totp-disable-form" className="space-y-3" onSubmit={handleSubmit}>
            <div className="space-y-1.5">
              <label className="text-sm font-medium" htmlFor="totp-disable-password">
                账号密码
              </label>
              <Input
                id="totp-disable-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                placeholder="请输入账号密码"
                autoFocus
                required
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium" htmlFor="totp-disable-code">
                验证码
              </label>
              <Input
                id="totp-disable-code"
                value={totpCode}
                onChange={(e) => setTotpCode(e.target.value)}
                autoComplete="one-time-code"
                placeholder="请输入 6 位验证码"
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
            取消
          </Button>
          <Button type="submit" form="totp-disable-form" variant="destructive" loading={loading}>
            禁用两步验证
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
