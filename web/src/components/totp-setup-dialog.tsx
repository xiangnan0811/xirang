import { useState } from "react";
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
  const [step, setStep] = useState<Step>("setup");
  const [secret, setSecret] = useState("");
  const [qrUrl, setQrUrl] = useState("");
  const [verifyCode, setVerifyCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const handleOpen = async (isOpen: boolean) => {
    if (isOpen && step === "setup") {
      setLoading(true);
      setError(null);
      try {
        const data = await apiClient.totpSetup(token);
        setSecret(data.secret);
        setQrUrl(data.qr_url);
      } catch (err) {
        const msg = err instanceof ApiError
          ? (err.detail && typeof err.detail === "object"
              ? ((err.detail as { error?: string }).error ?? "生成密钥失败")
              : "生成密钥失败")
          : "生成密钥失败";
        setError(msg);
      } finally {
        setLoading(false);
      }
    }
    if (!isOpen) {
      const wasCompleted = step === "recovery";
      // 关闭时重置状态
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
      const data = await apiClient.totpVerify(token, secret, verifyCode);
      setRecoveryCodes(data.recovery_codes);
      setStep("recovery");
    } catch (err) {
      const msg = err instanceof ApiError
        ? (err.detail && typeof err.detail === "object"
            ? ((err.detail as { error?: string }).error ?? "验证码错误")
            : "验证码错误")
        : "验证失败";
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
    <Dialog open={open} onOpenChange={handleOpen}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>设置两步验证</DialogTitle>
          <DialogDescription>
            {step === "setup" && "使用 Google Authenticator 或其他 TOTP 应用扫描二维码。"}
            {step === "verify" && "输入验证器 App 显示的 6 位验证码以完成绑定。"}
            {step === "recovery" && "请保存以下恢复码，每个恢复码只能使用一次。"}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-4">
          {step === "setup" && (
            <>
              {loading ? (
                <p className="text-center text-sm text-muted-foreground">正在生成密钥…</p>
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
                    <p className="text-xs text-muted-foreground">无法扫码？手动输入密钥：</p>
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
                  验证码
                </label>
                <Input
                  id="totp-verify-code"
                  value={verifyCode}
                  onChange={(e) => setVerifyCode(e.target.value)}
                  autoComplete="one-time-code"
                  placeholder="请输入 6 位验证码"
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
                两步验证已成功开启。请将以下恢复码保存在安全位置，忘记验证器时可用于登录。
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
                {copied ? "已复制" : "复制恢复码"}
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
              下一步
            </Button>
          )}
          {step === "verify" && (
            <>
              <Button type="button" variant="ghost" onClick={() => { setStep("setup"); setError(null); }}>
                上一步
              </Button>
              <Button type="submit" form="totp-verify-form" loading={loading}>
                验证并开启
              </Button>
            </>
          )}
          {step === "recovery" && (
            <Button type="button" onClick={() => handleOpen(false)}>
              完成
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
