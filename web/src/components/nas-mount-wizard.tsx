import { useState } from "react";
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

const STEP_LABELS = ["选择协议", "填写参数", "生成命令", "验证挂载"];

function generateMountCommand(protocol: Protocol, nfs: NfsFields, smb: SmbFields, usb: UsbFields): string {
  switch (protocol) {
    case "nfs":
      return `sudo mount -t nfs ${nfs.server}:${nfs.exportPath} ${nfs.mountPoint} -o ${nfs.options}`;
    case "smb":
      return `sudo mount -t cifs //${smb.server}/${smb.shareName} ${smb.mountPoint} -o username=${smb.username},password=${smb.password},${smb.options}`;
    case "usb":
      return `sudo mount${usb.fsType ? ` -t ${usb.fsType}` : ""} ${usb.devicePath} ${usb.mountPoint}`;
  }
}

function generateFstabEntry(protocol: Protocol, nfs: NfsFields, smb: SmbFields, usb: UsbFields): string {
  switch (protocol) {
    case "nfs":
      return `${nfs.server}:${nfs.exportPath}  ${nfs.mountPoint}  nfs  ${nfs.options}  0  0`;
    case "smb":
      return `//${smb.server}/${smb.shareName}  ${smb.mountPoint}  cifs  username=${smb.username},password=${smb.password},${smb.options}  0  0`;
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
      toast.success("已复制到剪贴板");
    } catch {
      toast.error("复制失败，请手动复制");
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
      setVerifyError(getErrorMessage(err, "验证失败"));
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

  return (
    <Dialog open={open} onOpenChange={(v) => { onOpenChange(v); if (!v) handleReset(); }}>
      <DialogContent size="lg" className="max-h-[90vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>外部存储挂载引导</DialogTitle>
          <DialogDescription>
            生成 NFS/SMB/USB 挂载命令，并验证挂载是否成功。
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        {/* 步骤指示器 */}
        <div className="flex items-center gap-1 px-6 pt-2 pb-1" role="navigation" aria-label="向导步骤">
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
              <p className="text-sm text-muted-foreground">选择外部存储的连接协议：</p>
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                {([
                  { key: "nfs" as Protocol, icon: Network, title: "NFS", desc: "Linux/Unix 网络文件系统，适合局域网高速备份" },
                  { key: "smb" as Protocol, icon: HardDrive, title: "SMB/CIFS", desc: "Windows 共享协议，兼容群晖/威联通等 NAS" },
                  { key: "usb" as Protocol, icon: Usb, title: "USB/本地", desc: "USB 外置硬盘或本地挂载设备" },
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
                <label className={labelClass}>NFS 服务器地址</label>
                <input className={inputClass} placeholder="192.168.1.100" value={nfs.server} onChange={(e) => setNfs({ ...nfs, server: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>导出路径</label>
                <input className={inputClass} placeholder="/export/backup" value={nfs.exportPath} onChange={(e) => setNfs({ ...nfs, exportPath: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>本地挂载点</label>
                <input className={inputClass} placeholder="/mnt/nas-backup" value={nfs.mountPoint} onChange={(e) => setNfs({ ...nfs, mountPoint: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>挂载选项</label>
                <input className={inputClass} value={nfs.options} onChange={(e) => setNfs({ ...nfs, options: e.target.value })} />
              </div>
            </div>
          )}

          {step === 1 && protocol === "smb" && (
            <div className="space-y-3">
              <div>
                <label className={labelClass}>SMB 服务器地址</label>
                <input className={inputClass} placeholder="192.168.1.100" value={smb.server} onChange={(e) => setSmb({ ...smb, server: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>共享名称</label>
                <input className={inputClass} placeholder="backup" value={smb.shareName} onChange={(e) => setSmb({ ...smb, shareName: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>本地挂载点</label>
                <input className={inputClass} placeholder="/mnt/nas-backup" value={smb.mountPoint} onChange={(e) => setSmb({ ...smb, mountPoint: e.target.value })} />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className={labelClass}>用户名</label>
                  <input className={inputClass} placeholder="admin" value={smb.username} onChange={(e) => setSmb({ ...smb, username: e.target.value })} />
                </div>
                <div>
                  <label className={labelClass}>密码</label>
                  <input className={inputClass} type="password" placeholder="********" value={smb.password} onChange={(e) => setSmb({ ...smb, password: e.target.value })} />
                </div>
              </div>
              <div>
                <label className={labelClass}>挂载选项</label>
                <input className={inputClass} value={smb.options} onChange={(e) => setSmb({ ...smb, options: e.target.value })} />
              </div>
            </div>
          )}

          {step === 1 && protocol === "usb" && (
            <div className="space-y-3">
              <div>
                <label className={labelClass}>设备路径</label>
                <input className={inputClass} placeholder="/dev/sdb1" value={usb.devicePath} onChange={(e) => setUsb({ ...usb, devicePath: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>本地挂载点</label>
                <input className={inputClass} placeholder="/mnt/usb-backup" value={usb.mountPoint} onChange={(e) => setUsb({ ...usb, mountPoint: e.target.value })} />
              </div>
              <div>
                <label className={labelClass}>文件系统类型</label>
                <input className={inputClass} placeholder="ext4" value={usb.fsType} onChange={(e) => setUsb({ ...usb, fsType: e.target.value })} />
              </div>
            </div>
          )}

          {/* Step 2: 生成命令 */}
          {step === 2 && (
            <div className="space-y-4">
              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-xs font-medium">挂载命令</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => handleCopy(generateMountCommand(protocol, nfs, smb, usb))}
                  >
                    <Copy className="mr-1 size-3" />
                    复制命令
                  </Button>
                </div>
                <pre className="rounded-lg border border-border/70 bg-muted/30 p-3 text-xs font-mono whitespace-pre-wrap break-all select-all">
                  {generateMountCommand(protocol, nfs, smb, usb)}
                </pre>
              </div>

              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-xs font-medium">fstab 持久化条目</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => handleCopy(generateFstabEntry(protocol, nfs, smb, usb))}
                  >
                    <Copy className="mr-1 size-3" />
                    复制条目
                  </Button>
                </div>
                <pre className="rounded-lg border border-border/70 bg-muted/30 p-3 text-xs font-mono whitespace-pre-wrap break-all select-all">
                  {generateFstabEntry(protocol, nfs, smb, usb)}
                </pre>
                <p className="mt-1 text-xs text-muted-foreground">
                  将上述条目追加到服务器的 <code className="rounded bg-muted px-1">/etc/fstab</code> 文件末尾，可实现开机自动挂载。
                </p>
              </div>

              <div className="rounded-lg border border-primary/30 bg-primary/5 p-3">
                <p className="text-xs text-foreground">
                  请在目标服务器上执行以上挂载命令，然后点击"下一步"验证挂载是否成功。
                </p>
              </div>
            </div>
          )}

          {/* Step 3: 验证 */}
          {step === 3 && (
            <div className="space-y-4">
              <p className="text-sm text-muted-foreground">
                验证挂载点 <code className="rounded bg-muted px-1.5 py-0.5 text-xs font-mono">{getMountPoint(protocol, nfs, smb, usb)}</code> 的状态：
              </p>

              <Button variant="outline" size="sm" onClick={handleVerify} disabled={verifying}>
                {verifying ? <Loader2 className="mr-1.5 size-3.5 animate-spin" /> : <HardDrive className="mr-1.5 size-3.5" />}
                验证挂载点
              </Button>

              {verifyError && (
                <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-3">
                  <p className="text-sm text-destructive">{verifyError}</p>
                </div>
              )}

              {verifyResult && (
                <div className="space-y-2">
                  <div className="grid gap-2">
                    <VerifyRow label="路径存在" ok={verifyResult.exists} />
                    <VerifyRow label="挂载点" ok={verifyResult.is_mount_point} />
                    <VerifyRow label="可写" ok={verifyResult.writable} />
                  </div>

                  {verifyResult.exists && (
                    <div className="rounded-lg border border-border/70 bg-muted/20 p-3 text-xs space-y-1">
                      <p><span className="text-muted-foreground">总空间：</span>{verifyResult.total_gb} GB</p>
                      <p><span className="text-muted-foreground">可用空间：</span>{verifyResult.free_gb} GB</p>
                      {verifyResult.filesystem && verifyResult.filesystem !== "unknown" && (
                        <p><span className="text-muted-foreground">文件系统：</span>{verifyResult.filesystem}</p>
                      )}
                    </div>
                  )}

                  {verifyResult.exists && verifyResult.is_mount_point && verifyResult.writable && (
                    <div className="rounded-lg border border-success/30 bg-success/5 p-3">
                      <p className="text-sm text-success font-medium">
                        挂载成功！您现在可以在备份策略中使用此路径作为目标路径。
                      </p>
                    </div>
                  )}

                  {verifyResult.exists && !verifyResult.is_mount_point && (
                    <div className="rounded-lg border border-warning/30 bg-warning/5 p-3">
                      <p className="text-xs text-warning">
                        路径存在但不是挂载点。请确认已执行挂载命令，或该目录本身就是本地目录。
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
              上一步
            </Button>
          )}
          <div className="flex-1" />
          {step < 3 && (
            <Button size="sm" onClick={() => setStep(step + 1)} disabled={!canNext()}>
              下一步
              <ArrowRight className="ml-1 size-3.5" />
            </Button>
          )}
          {step === 3 && (
            <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>
              完成
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
