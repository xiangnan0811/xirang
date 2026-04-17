import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft, ArrowRight } from "lucide-react";
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
import { Stepper } from "@/components/ui/stepper";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import type { MountVerifyResult } from "@/lib/api/storage-guide-api";
import { getErrorMessage } from "@/lib/utils";
import { toast } from "sonner";
import { NasMountWizardStep1 } from "./nas-mount-wizard.step1";
import type { Protocol } from "./nas-mount-wizard.step1";
import { NasMountWizardStep2 } from "./nas-mount-wizard.step2";
import type { NfsFields, SmbFields, UsbFields } from "./nas-mount-wizard.step2";
import { NasMountWizardStep3 } from "./nas-mount-wizard.step3";

/* -------------------------------------------------------------------------- */
/*  Helpers                                                                    */
/* -------------------------------------------------------------------------- */

function isStep2Valid(
  protocol: Protocol,
  nfs: NfsFields,
  smb: SmbFields,
  usb: UsbFields,
): boolean {
  switch (protocol) {
    case "nfs":
      return Boolean(nfs.server && nfs.exportPath && nfs.mountPoint);
    case "smb":
      return Boolean(smb.server && smb.shareName && smb.mountPoint && smb.username);
    case "usb":
      return Boolean(usb.devicePath && usb.mountPoint);
  }
}

function getMountPoint(
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
/*  Component                                                                  */
/* -------------------------------------------------------------------------- */

/**
 * Wizard steps mapping:
 *   step 0 → Step1: protocol selection
 *   step 1 → Step2: parameter form
 *   step 2 → Step3 (subStep 0): generated commands
 *   step 3 → Step3 (subStep 1): mount verification
 */
export function NasMountWizard({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
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

  const TOTAL_STEPS = 4; // 0-3

  // Stepper labels for the 4 steps
  const stepperLabels = t("nasMountWizard.stepLabels", {
    returnObjects: true,
  }) as string[];

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        onOpenChange(v);
        if (!v) handleReset();
      }}
    >
      <DialogContent size="lg" className="max-h-[90vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{t("nasMountWizard.title")}</DialogTitle>
          <DialogDescription>{t("nasMountWizard.description")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        {/* Stepper progress indicator */}
        <div
          className="px-6 pt-2 pb-1"
          role="navigation"
          aria-label={t("nasMountWizard.stepsAriaLabel")}
        >
          <Stepper steps={stepperLabels} current={step} />
        </div>

        <DialogBody>
          {/* Step 0: Protocol selection */}
          {step === 0 && (
            <NasMountWizardStep1 protocol={protocol} onProtocolChange={setProtocol} />
          )}

          {/* Step 1: Parameter form */}
          {step === 1 && (
            <NasMountWizardStep2
              protocol={protocol}
              nfs={nfs}
              smb={smb}
              usb={usb}
              onNfsChange={setNfs}
              onSmbChange={setSmb}
              onUsbChange={setUsb}
            />
          )}

          {/* Steps 2–3: Commands + Verify */}
          {step >= 2 && (
            <NasMountWizardStep3
              subStep={step - 2}
              protocol={protocol}
              nfs={nfs}
              smb={smb}
              usb={usb}
              verifying={verifying}
              verifyResult={verifyResult}
              verifyError={verifyError}
              onCopy={handleCopy}
              onVerify={handleVerify}
            />
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
          {step < TOTAL_STEPS - 1 && (
            <Button size="sm" onClick={() => setStep(step + 1)} disabled={!canNext()}>
              {t("common.next")}
              <ArrowRight className="ml-1 size-3.5" />
            </Button>
          )}
          {step === TOTAL_STEPS - 1 && (
            <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.finish")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
