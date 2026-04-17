import { useTranslation } from "react-i18next";
import type { Protocol } from "./nas-mount-wizard.step1";

export type { Protocol };

export type NfsFields = {
  server: string;
  exportPath: string;
  mountPoint: string;
  options: string;
};

export type SmbFields = {
  server: string;
  shareName: string;
  mountPoint: string;
  username: string;
  password: string;
  options: string;
};

export type UsbFields = {
  devicePath: string;
  mountPoint: string;
  fsType: string;
};

const inputClass =
  "w-full rounded-lg border border-input/90 bg-background/70 px-3 py-2 text-sm placeholder:text-muted-foreground/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 focus-visible:border-primary/35";
const labelClass = "block text-xs font-medium text-foreground mb-1";

interface NasMountWizardStep2Props {
  protocol: Protocol;
  nfs: NfsFields;
  smb: SmbFields;
  usb: UsbFields;
  onNfsChange: (fields: NfsFields) => void;
  onSmbChange: (fields: SmbFields) => void;
  onUsbChange: (fields: UsbFields) => void;
}

export function NasMountWizardStep2({
  protocol,
  nfs,
  smb,
  usb,
  onNfsChange,
  onSmbChange,
  onUsbChange,
}: NasMountWizardStep2Props) {
  const { t } = useTranslation();

  if (protocol === "nfs") {
    return (
      <div className="space-y-3">
        <div>
          <label htmlFor="nas-nfs-server" className={labelClass}>
            {t("nasMountWizard.nfs.serverAddress")}
          </label>
          <input
            id="nas-nfs-server"
            className={inputClass}
            placeholder="192.168.1.100"
            value={nfs.server}
            onChange={(e) => onNfsChange({ ...nfs, server: e.target.value })}
          />
        </div>
        <div>
          <label htmlFor="nas-nfs-export" className={labelClass}>
            {t("nasMountWizard.nfs.exportPath")}
          </label>
          <input
            id="nas-nfs-export"
            className={inputClass}
            placeholder="/export/backup"
            value={nfs.exportPath}
            onChange={(e) => onNfsChange({ ...nfs, exportPath: e.target.value })}
          />
        </div>
        <div>
          <label htmlFor="nas-nfs-mount" className={labelClass}>
            {t("nasMountWizard.localMountPoint")}
          </label>
          <input
            id="nas-nfs-mount"
            className={inputClass}
            placeholder="/mnt/nas-backup"
            value={nfs.mountPoint}
            onChange={(e) => onNfsChange({ ...nfs, mountPoint: e.target.value })}
          />
        </div>
        <div>
          <label htmlFor="nas-nfs-options" className={labelClass}>
            {t("nasMountWizard.mountOptions")}
          </label>
          <input
            id="nas-nfs-options"
            className={inputClass}
            value={nfs.options}
            onChange={(e) => onNfsChange({ ...nfs, options: e.target.value })}
          />
        </div>
      </div>
    );
  }

  if (protocol === "smb") {
    return (
      <div className="space-y-3">
        <div>
          <label htmlFor="nas-smb-server" className={labelClass}>
            {t("nasMountWizard.smb.serverAddress")}
          </label>
          <input
            id="nas-smb-server"
            className={inputClass}
            placeholder="192.168.1.100"
            value={smb.server}
            onChange={(e) => onSmbChange({ ...smb, server: e.target.value })}
          />
        </div>
        <div>
          <label htmlFor="nas-smb-share" className={labelClass}>
            {t("nasMountWizard.smb.shareName")}
          </label>
          <input
            id="nas-smb-share"
            className={inputClass}
            placeholder="backup"
            value={smb.shareName}
            onChange={(e) => onSmbChange({ ...smb, shareName: e.target.value })}
          />
        </div>
        <div>
          <label htmlFor="nas-smb-mount" className={labelClass}>
            {t("nasMountWizard.localMountPoint")}
          </label>
          <input
            id="nas-smb-mount"
            className={inputClass}
            placeholder="/mnt/nas-backup"
            value={smb.mountPoint}
            onChange={(e) => onSmbChange({ ...smb, mountPoint: e.target.value })}
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label htmlFor="nas-smb-user" className={labelClass}>
              {t("nasMountWizard.smb.username")}
            </label>
            <input
              id="nas-smb-user"
              className={inputClass}
              placeholder="admin"
              value={smb.username}
              onChange={(e) => onSmbChange({ ...smb, username: e.target.value })}
            />
          </div>
          <div>
            <label htmlFor="nas-smb-pass" className={labelClass}>
              {t("nasMountWizard.smb.password")}
            </label>
            <input
              id="nas-smb-pass"
              className={inputClass}
              type="password"
              placeholder="********"
              value={smb.password}
              onChange={(e) => onSmbChange({ ...smb, password: e.target.value })}
            />
          </div>
        </div>
        <div>
          <label htmlFor="nas-smb-options" className={labelClass}>
            {t("nasMountWizard.mountOptions")}
          </label>
          <input
            id="nas-smb-options"
            className={inputClass}
            value={smb.options}
            onChange={(e) => onSmbChange({ ...smb, options: e.target.value })}
          />
        </div>
      </div>
    );
  }

  // USB
  return (
    <div className="space-y-3">
      <div>
        <label htmlFor="nas-usb-device" className={labelClass}>
          {t("nasMountWizard.usb.devicePath")}
        </label>
        <input
          id="nas-usb-device"
          className={inputClass}
          placeholder="/dev/sdb1"
          value={usb.devicePath}
          onChange={(e) => onUsbChange({ ...usb, devicePath: e.target.value })}
        />
      </div>
      <div>
        <label htmlFor="nas-usb-mount" className={labelClass}>
          {t("nasMountWizard.localMountPoint")}
        </label>
        <input
          id="nas-usb-mount"
          className={inputClass}
          placeholder="/mnt/usb-backup"
          value={usb.mountPoint}
          onChange={(e) => onUsbChange({ ...usb, mountPoint: e.target.value })}
        />
      </div>
      <div>
        <label htmlFor="nas-usb-fstype" className={labelClass}>
          {t("nasMountWizard.usb.fsType")}
        </label>
        <input
          id="nas-usb-fstype"
          className={inputClass}
          placeholder="ext4"
          value={usb.fsType}
          onChange={(e) => onUsbChange({ ...usb, fsType: e.target.value })}
        />
      </div>
    </div>
  );
}
