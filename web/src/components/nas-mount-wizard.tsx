import { useState } from "react";
import { useTranslation } from "react-i18next";
import { HardDrive, Network, Usb, Copy, CheckCircle2, XCircle, Loader2, ArrowLeft, ArrowRight } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogCloseButton,
  DialogBody,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import type { MountVerifyResult } from "@/lib/api/storage-guide-api";
import { getErrorMessage } from "@/lib/utils";
import { toast } from "sonner";

type Protocol = "nfs" | "smb" | "usb";

type NfsFields = {
  server: string;
  exportPath: string;
  mountPoint: string;
  options: string;
};

type SmbFields = {
  server: string;
  shareName: string;
  mountPoint: string;
  username: string;
  password: string;
  options: string;
};

type UsbFields = {
  devicePath: string;
  mountPoint: string;
  fsType: string;
};

function generateMountCommand(protocol: Protocol, nfs: NfsFields, smb: SmbFields, usb: UsbFields): string {
  switch (protocol) {
    case "nfs":
      return `sudo mount -t nfs ${nfs.server}:${nfs.exportPath} ${nfs.mountPoint} -o ${nfs.options}`;
    case "smb":
      return `sudo mount -t cifs //${smb.server}/${smb.shareName} ${smb.mountPoint} -o username=${smb.username},password=<YOUR_PASSWORD>,${smb.options}`;
    case "usb":
      return `sudo mount${usb.fsType ? ` -t ${usb.fsType}` : ""} ${usb.devicePath} ${usb.mountPoint}`;
  }
}

function generateFstabEntry(protocol: Protocol, nfs: NfsFields, smb: SmbFields, usb: UsbFields): string {
  switch (protocol) {
    case "nfs":
      return `${nfs.server}:${nfs.exportPath}  ${nfs.mountPoint}  nfs  ${nfs.options}  0  0`;
    case "smb":
      return `//${smb.server}/${smb.shareName}  ${smb.mountPoint}  cifs  credentials=/etc/samba/.smbcredentials,${smb.options}  0  0`;
    case "usb":
      return `${usb.devicePath}  ${usb.mountPoint}  ${usb.fsType || "auto"}  defaults  0  0`;
  }
}

function getMountPoint(protocol: Protocol, nfs: NfsFields, smb: SmbFields, usb: UsbFields): string {
  switch (protocol) {
    case "nfs":
      return nfs.mountPoint;
    case "smb":
      return smb.mountPoint;
    case "usb":
      return usb.mountPoint;
  }
}

function isStep2Valid(protocol: Protocol, nfs: NfsFields, smb: SmbFields, usb: UsbFields): boolean {
  switch (protocol) {
    case "nfs":
      return Boolean(nfs.server && nfs.exportPath && nfs.mountPoint);
    case "smb":
      return Boolean(smb.server && smb.shareName && smb.mountPoint && smb.username);
    case "usb":
      return Boolean(usb.devicePath && usb.mountPoint);
  }
}

const inputClass = "w-full rounded-lg border border-input/90 bg-background/70 px-3 py-2 text-sm placeholder:text-muted-foreground/60 focus:outline-none focus:ring-2 focus:ring-primary/40 focus:border-primary/35";
const labelClass = "block text-xs font-medium text-foreground mb-1";

export function NasMountWizard({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [step, setStep] = useState(0);
  const [protocol, setProtocol] = useState<Protocol>("nfs");

  const [nfs, setNfs] = useState<NfsFields>({
    server: "",
    exportPath: "/export/backup",
    mountPoint: "/mnt/nas-backup",
    options: "rw,hard,intr",
  });

  const [smb, setSmb] = useState<SmbFields>({
    server: "",
    shareName: "",
    mountPoint: "/mnt/nas-backup",
    username: "",
    password: "",
    options: "uid=1000,gid=1000,vers=3.0",
  });

  const [usb, setUsb] = useState<UsbFields>({
    devicePath: "/dev/sdb1",
    mountPoint: "/mnt/usb-backup",
    fsType: "ext4",
  });

  const [verifying, setVerifying] = useState(false);
  const [verifyResult, setVerifyResult] = useState<MountVerifyResult | null>(null);
  const [verifyError, setVerifyError] = useState<string | null>(null);

  const handleCopy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t("common.copiedToClipboard"));
    } catch {
      toast.error(t("common.copyFailed"));
    }
  };

  const handleVerify = async () => {
    if (!token) return;
    const mountPoint = getMountPoint(protocol, nfs, smb, usb);
    setVerifying(true);
    setVerifyResult(null);
    setVerifyError(null);
    try {
      const result = await apiClient.verifyMount(token, mountPoint);
      setVerifyResult(result);
    } catch (err) {
      setVerifyError(getErrorMessage(err, t("nasMountWizard.verifyFailed")));
    } finally {
      setVerifying(false);
    }
  };

  const handleReset = () => {
    setStep(0);
    setVerifyResult(null);
    setVerifyError(null);
  };

  const canNext = (): boolean => {
    if (step === 1) return isStep2Valid(protocol, nfs, smb, usb);
    return true;
  };

  const STEP_LABELS = t("nasMountWizard.stepLabels", { returnObjects: true }) as string[];

  return (
    <Dialog open={open} onOpenChange={(v) => { onOpenChange(v); if (!v) handleReset(); }}>
      <DialogContent size="lg" className="max-h-[90vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{t("nasMountWizard.title")}</DialogTitle>
          <DialogDescription>
            {t("nasMountWizard.description")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        {/* 步骤指示器 */}
        <div className="flex items-center gap-1 px-6 pt-2 pb-1" role="navigation" aria-label={t("nasMountWizard.stepsAriaLabel")}>
          {STEP_LABELS.map((label, i) => (
            <div key={label} className="flex items-center gap-1 flex-1">
              <div className={`flex items-center justify-center size-6 rounded-full text-xs font-medium shrink-0 ${
                i <= step ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"
              }`}>
                {i + 1}
              </div>
              <span className={`text-xs truncate hidden sm:inline ${i <= step ? "text-foreground" : "text-muted-foreground"}`}>
                {label}
              </span>
              {i < STEP_LABELS.length - 1 && (
                <div className={`flex-1 h-px mx-1 ${i < step ? "bg-primary" : "bg-border"}`} />
              )}
            </div>
          ))}
        </div>

        <DialogBody>
          {/* Step 0: 选择协议 */}
          {step === 0 && (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">{t("nasMountWizard.selectProtocolHint")}</p>
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                {([
                  { key: "nfs" as Protocol, icon: Network, title: t("nasMountWizard.protocols.nfs.title"), desc: t("nasMountWizard.protocols.nfs.desc") },
                  { key: "smb" as Protocol, icon: HardDrive, title: t("nasMountWizard.protocols.smb.title"), desc: t("nasMountWizard.protocols.smb.desc") },
                  { key: "usb" as Protocol, icon: Usb, title: t("nasMountWizard.protocols.usb.title"), desc: t("nasMountWizard.protocols.usb.desc") },
                ]).map((item) => (
                  <button
                    key={item.key}
                    type="button"
                    onClick={() => setProtocol(item.key)}
                    className={`flex flex-col items-center gap-2 rounded-xl border p-4 text-center transition-all ${
                      protocol === item.key
                        ? "border-primary bg-primary/5 ring-2 ring-primary/30"
                        : "border-border/70 hover:border-primary/30 hover:bg-accent/30"
                    }`}
                    aria-pressed={protocol === item.key}
                  >
                    <item.icon className={`size-6 ${protocol === item.key ? "text-primary" : "text-muted-foreground"}`} />
                    <span className="text-sm font-medium">{item.title}</span>
                    <span className="text-xs text-muted-foreground leading-snug">{item.desc}</span>
                  </button>
                ))}
              </div>
            </div>
          )}

          {/* Step 1: 填写参数 */}
          {step === 1 && protocol === "nfs" && (
            <div className="space-y-3">
              <div>
                <label htmlFor="nas-nfs-server" className={labelClass}>{t("nasMountWizard.nfs.serverAddress")}</label>
                <input id="nas-nfs-server" className={inputClass} placeholder="192.168.1.100" value={nfs.server} onChange={(e) => setNfs({ ...nfs, server: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-nfs-export" className={labelClass}>{t("nasMountWizard.nfs.exportPath")}</label>
                <input id="nas-nfs-export" className={inputClass} placeholder="/export/backup" value={nfs.exportPath} onChange={(e) => setNfs({ ...nfs, exportPath: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-nfs-mount" className={labelClass}>{t("nasMountWizard.localMountPoint")}</label>
                <input id="nas-nfs-mount" className={inputClass} placeholder="/mnt/nas-backup" value={nfs.mountPoint} onChange={(e) => setNfs({ ...nfs, mountPoint: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-nfs-options" className={labelClass}>{t("nasMountWizard.mountOptions")}</label>
                <input id="nas-nfs-options" className={inputClass} value={nfs.options} onChange={(e) => setNfs({ ...nfs, options: e.target.value })} />
              </div>
            </div>
          )}

          {step === 1 && protocol === "smb" && (
            <div className="space-y-3">
              <div>
                <label htmlFor="nas-smb-server" className={labelClass}>{t("nasMountWizard.smb.serverAddress")}</label>
                <input id="nas-smb-server" className={inputClass} placeholder="192.168.1.100" value={smb.server} onChange={(e) => setSmb({ ...smb, server: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-smb-share" className={labelClass}>{t("nasMountWizard.smb.shareName")}</label>
                <input id="nas-smb-share" className={inputClass} placeholder="backup" value={smb.shareName} onChange={(e) => setSmb({ ...smb, shareName: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-smb-mount" className={labelClass}>{t("nasMountWizard.localMountPoint")}</label>
                <input id="nas-smb-mount" className={inputClass} placeholder="/mnt/nas-backup" value={smb.mountPoint} onChange={(e) => setSmb({ ...smb, mountPoint: e.target.value })} />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label htmlFor="nas-smb-user" className={labelClass}>{t("nasMountWizard.smb.username")}</label>
                  <input id="nas-smb-user" className={inputClass} placeholder="admin" value={smb.username} onChange={(e) => setSmb({ ...smb, username: e.target.value })} />
                </div>
                <div>
                  <label htmlFor="nas-smb-pass" className={labelClass}>{t("nasMountWizard.smb.password")}</label>
                  <input id="nas-smb-pass" className={inputClass} type="password" placeholder="********" value={smb.password} onChange={(e) => setSmb({ ...smb, password: e.target.value })} />
                </div>
              </div>
              <div>
                <label htmlFor="nas-smb-options" className={labelClass}>{t("nasMountWizard.mountOptions")}</label>
                <input id="nas-smb-options" className={inputClass} value={smb.options} onChange={(e) => setSmb({ ...smb, options: e.target.value })} />
              </div>
            </div>
          )}

          {step === 1 && protocol === "usb" && (
            <div className="space-y-3">
              <div>
                <label htmlFor="nas-usb-device" className={labelClass}>{t("nasMountWizard.usb.devicePath")}</label>
                <input id="nas-usb-device" className={inputClass} placeholder="/dev/sdb1" value={usb.devicePath} onChange={(e) => setUsb({ ...usb, devicePath: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-usb-mount" className={labelClass}>{t("nasMountWizard.localMountPoint")}</label>
                <input id="nas-usb-mount" className={inputClass} placeholder="/mnt/usb-backup" value={usb.mountPoint} onChange={(e) => setUsb({ ...usb, mountPoint: e.target.value })} />
              </div>
              <div>
                <label htmlFor="nas-usb-fstype" className={labelClass}>{t("nasMountWizard.usb.fsType")}</label>
                <input id="nas-usb-fstype" className={inputClass} placeholder="ext4" value={usb.fsType} onChange={(e) => setUsb({ ...usb, fsType: e.target.value })} />
              </div>
            </div>
          )}

          {/* Step 2: 生成命令 */}
          {step === 2 && (
            <div className="space-y-4">
              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-xs font-medium">{t("nasMountWizard.mountCommand")}</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => handleCopy(generateMountCommand(protocol, nfs, smb, usb))}
                  >
                    <Copy className="mr-1 size-3" />
                    {t("nasMountWizard.copyCommand")}
                  </Button>
                </div>
                <pre className="rounded-lg border border-border/70 bg-muted/30 p-3 text-xs font-mono whitespace-pre-wrap break-all select-all">
                  {generateMountCommand(protocol, nfs, smb, usb)}
                </pre>
              </div>

              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-xs font-medium">{t("nasMountWizard.fstabEntry")}</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => handleCopy(generateFstabEntry(protocol, nfs, smb, usb))}
                  >
                    <Copy className="mr-1 size-3" />
                    {t("nasMountWizard.copyEntry")}
                  </Button>
                </div>
                <pre className="rounded-lg border border-border/70 bg-muted/30 p-3 text-xs font-mono whitespace-pre-wrap break-all select-all">
                  {generateFstabEntry(protocol, nfs, smb, usb)}
                </pre>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("nasMountWizard.fstabHint")}
                </p>
              </div>

              <div className="rounded-lg border border-primary/30 bg-primary/5 p-3">
                <p className="text-xs text-foreground">
                  {t("nasMountWizard.commandHint")}
                </p>
              </div>

              {protocol === "smb" && (
                <div className="rounded-lg border border-warning/30 bg-warning/5 p-3">
                  <p className="text-xs text-warning">
                    {t("nasMountWizard.smbSecurityHint")}
                  </p>
                </div>
              )}
            </div>
          )}

          {/* Step 3: 验证 */}
          {step === 3 && (
            <div className="space-y-4">
              <p className="text-sm text-muted-foreground">
                {t("nasMountWizard.verifyMountPointHint", { path: getMountPoint(protocol, nfs, smb, usb) })}
              </p>

              <Button variant="outline" size="sm" onClick={handleVerify} disabled={verifying}>
                {verifying ? <Loader2 className="mr-1.5 size-3.5 animate-spin" /> : <HardDrive className="mr-1.5 size-3.5" />}
                {t("nasMountWizard.verifyMountPoint")}
              </Button>

              {verifyError && (
                <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-3">
                  <p className="text-sm text-destructive">{verifyError}</p>
                </div>
              )}

              {verifyResult && (
                <div className="space-y-2">
                  <div className="grid gap-2">
                    <VerifyRow label={t("nasMountWizard.verifyLabels.pathExists")} ok={verifyResult.exists} />
                    <VerifyRow label={t("nasMountWizard.verifyLabels.mountPoint")} ok={verifyResult.is_mount_point} />
                    <VerifyRow label={t("nasMountWizard.verifyLabels.writable")} ok={verifyResult.writable} />
                  </div>

                  {verifyResult.exists && (
                    <div className="rounded-lg border border-border/70 bg-muted/20 p-3 text-xs space-y-1">
                      <p><span className="text-muted-foreground">{t("nasMountWizard.totalSpace")}</span>{verifyResult.total_gb} GB</p>
                      <p><span className="text-muted-foreground">{t("nasMountWizard.freeSpace")}</span>{verifyResult.free_gb} GB</p>
                      {verifyResult.filesystem && verifyResult.filesystem !== "unknown" && (
                        <p><span className="text-muted-foreground">{t("nasMountWizard.filesystem")}</span>{verifyResult.filesystem}</p>
                      )}
                    </div>
                  )}

                  {verifyResult.exists && verifyResult.is_mount_point && verifyResult.writable && (
                    <div className="rounded-lg border border-success/30 bg-success/5 p-3">
                      <p className="text-sm text-success font-medium">
                        {t("nasMountWizard.mountSuccess")}
                      </p>
                    </div>
                  )}

                  {verifyResult.exists && !verifyResult.is_mount_point && (
                    <div className="rounded-lg border border-warning/30 bg-warning/5 p-3">
                      <p className="text-xs text-warning">
                        {t("nasMountWizard.notMountPointWarning")}
                      </p>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </DialogBody>

        <DialogFooter>
          {step > 0 && (
            <Button variant="outline" size="sm" onClick={() => setStep(step - 1)}>
              <ArrowLeft className="mr-1 size-3.5" />
              {t("common.prev")}
            </Button>
          )}
          <div className="flex-1" />
          {step < 3 && (
            <Button size="sm" onClick={() => setStep(step + 1)} disabled={!canNext()}>
              {t("common.next")}
              <ArrowRight className="ml-1 size-3.5" />
            </Button>
          )}
          {step === 3 && (
            <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.finish")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function VerifyRow({ label, ok }: { label: string; ok: boolean }) {
  return (
    <div className="flex items-center gap-2 text-sm">
      {ok ? (
        <CheckCircle2 className="size-4 text-success shrink-0" />
      ) : (
        <XCircle className="size-4 text-destructive shrink-0" />
      )}
      <span className={ok ? "text-foreground" : "text-muted-foreground"}>{label}</span>
    </div>
  );
}
