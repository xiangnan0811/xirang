import { useTranslation } from "react-i18next";
import {
  Copy,
  CheckCircle2,
  XCircle,
  Loader2,
  HardDrive,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import type { MountVerifyResult } from "@/lib/api/storage-guide-api";
import type { Protocol } from "./nas-mount-wizard.step1";
import type { NfsFields, SmbFields, UsbFields } from "./nas-mount-wizard.step2";

/* -------------------------------------------------------------------------- */
/*  Command generation utilities                                               */
/* -------------------------------------------------------------------------- */

export function generateMountCommand(
  protocol: Protocol,
  nfs: NfsFields,
  smb: SmbFields,
  usb: UsbFields,
): string {
  switch (protocol) {
    case "nfs":
      return `sudo mount -t nfs ${nfs.server}:${nfs.exportPath} ${nfs.mountPoint} -o ${nfs.options}`;
    case "smb":
      return `sudo mount -t cifs //${smb.server}/${smb.shareName} ${smb.mountPoint} -o username=${smb.username},password=<YOUR_PASSWORD>,${smb.options}`;
    case "usb":
      return `sudo mount${usb.fsType ? ` -t ${usb.fsType}` : ""} ${usb.devicePath} ${usb.mountPoint}`;
  }
}

export function generateFstabEntry(
  protocol: Protocol,
  nfs: NfsFields,
  smb: SmbFields,
  usb: UsbFields,
): string {
  switch (protocol) {
    case "nfs":
      return `${nfs.server}:${nfs.exportPath}  ${nfs.mountPoint}  nfs  ${nfs.options}  0  0`;
    case "smb":
      return `//${smb.server}/${smb.shareName}  ${smb.mountPoint}  cifs  credentials=/etc/samba/.smbcredentials,${smb.options}  0  0`;
    case "usb":
      return `${usb.devicePath}  ${usb.mountPoint}  ${usb.fsType || "auto"}  defaults  0  0`;
  }
}

export function getMountPoint(
  protocol: Protocol,
  nfs: NfsFields,
  smb: SmbFields,
  usb: UsbFields,
): string {
  switch (protocol) {
    case "nfs":
      return nfs.mountPoint;
    case "smb":
      return smb.mountPoint;
    case "usb":
      return usb.mountPoint;
  }
}

/* -------------------------------------------------------------------------- */
/*  Sub-step 2a: Commands                                                      */
/* -------------------------------------------------------------------------- */

interface CommandsSubStepProps {
  protocol: Protocol;
  nfs: NfsFields;
  smb: SmbFields;
  usb: UsbFields;
  onCopy: (text: string) => void;
}

export function NasMountCommandsView({
  protocol,
  nfs,
  smb,
  usb,
  onCopy,
}: CommandsSubStepProps) {
  const { t } = useTranslation();

  return (
    <div className="space-y-4">
      <div>
        <div className="flex items-center justify-between mb-1">
          <span className="text-xs font-medium">{t("nasMountWizard.mountCommand")}</span>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-xs"
            onClick={() => onCopy(generateMountCommand(protocol, nfs, smb, usb))}
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
            onClick={() => onCopy(generateFstabEntry(protocol, nfs, smb, usb))}
          >
            <Copy className="mr-1 size-3" />
            {t("nasMountWizard.copyEntry")}
          </Button>
        </div>
        <pre className="rounded-lg border border-border/70 bg-muted/30 p-3 text-xs font-mono whitespace-pre-wrap break-all select-all">
          {generateFstabEntry(protocol, nfs, smb, usb)}
        </pre>
        <p className="mt-1 text-xs text-muted-foreground">{t("nasMountWizard.fstabHint")}</p>
      </div>

      <div className="rounded-lg border border-primary/30 bg-primary/5 p-3">
        <p className="text-xs text-foreground">{t("nasMountWizard.commandHint")}</p>
      </div>

      {protocol === "smb" && (
        <div className="rounded-lg border border-warning/30 bg-warning/5 p-3">
          <p className="text-xs text-warning">{t("nasMountWizard.smbSecurityHint")}</p>
        </div>
      )}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/*  Sub-step 2b: Verify                                                        */
/* -------------------------------------------------------------------------- */

interface VerifyRowProps {
  label: string;
  ok: boolean;
}

function VerifyRow({ label, ok }: VerifyRowProps) {
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

interface VerifySubStepProps {
  protocol: Protocol;
  nfs: NfsFields;
  smb: SmbFields;
  usb: UsbFields;
  verifying: boolean;
  verifyResult: MountVerifyResult | null;
  verifyError: string | null;
  onVerify: () => void;
}

export function NasMountVerifyView({
  protocol,
  nfs,
  smb,
  usb,
  verifying,
  verifyResult,
  verifyError,
  onVerify,
}: VerifySubStepProps) {
  const { t } = useTranslation();
  const mountPoint = getMountPoint(protocol, nfs, smb, usb);

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        {t("nasMountWizard.verifyMountPointHint", { path: mountPoint })}
      </p>

      <Button variant="outline" size="sm" onClick={onVerify} disabled={verifying}>
        {verifying ? (
          <Loader2 className="mr-1.5 size-3.5 animate-spin" />
        ) : (
          <HardDrive className="mr-1.5 size-3.5" />
        )}
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
              <p>
                <span className="text-muted-foreground">{t("nasMountWizard.totalSpace")}</span>
                {verifyResult.total_gb} GB
              </p>
              <p>
                <span className="text-muted-foreground">{t("nasMountWizard.freeSpace")}</span>
                {verifyResult.free_gb} GB
              </p>
              {verifyResult.filesystem && verifyResult.filesystem !== "unknown" && (
                <p>
                  <span className="text-muted-foreground">{t("nasMountWizard.filesystem")}</span>
                  {verifyResult.filesystem}
                </p>
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
              <p className="text-xs text-warning">{t("nasMountWizard.notMountPointWarning")}</p>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/*  Step 3 composite: step index 2 = commands, step index 3 = verify          */
/* -------------------------------------------------------------------------- */

interface NasMountWizardStep3Props {
  /** Internal sub-step: 0 = commands, 1 = verify */
  subStep: number;
  protocol: Protocol;
  nfs: NfsFields;
  smb: SmbFields;
  usb: UsbFields;
  verifying: boolean;
  verifyResult: MountVerifyResult | null;
  verifyError: string | null;
  onCopy: (text: string) => void;
  onVerify: () => void;
}

export function NasMountWizardStep3({
  subStep,
  protocol,
  nfs,
  smb,
  usb,
  verifying,
  verifyResult,
  verifyError,
  onCopy,
  onVerify,
}: NasMountWizardStep3Props) {
  if (subStep === 0) {
    return (
      <NasMountCommandsView
        protocol={protocol}
        nfs={nfs}
        smb={smb}
        usb={usb}
        onCopy={onCopy}
      />
    );
  }

  return (
    <NasMountVerifyView
      protocol={protocol}
      nfs={nfs}
      smb={smb}
      usb={usb}
      verifying={verifying}
      verifyResult={verifyResult}
      verifyError={verifyError}
      onVerify={onVerify}
    />
  );
}
