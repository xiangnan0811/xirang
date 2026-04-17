import { useTranslation } from "react-i18next";
import { HardDrive, Network, Usb } from "lucide-react";

export type Protocol = "nfs" | "smb" | "usb";

interface NasMountWizardStep1Props {
  protocol: Protocol;
  onProtocolChange: (protocol: Protocol) => void;
}

export function NasMountWizardStep1({ protocol, onProtocolChange }: NasMountWizardStep1Props) {
  const { t } = useTranslation();

  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">{t("nasMountWizard.selectProtocolHint")}</p>
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
        {([
          {
            key: "nfs" as Protocol,
            icon: Network,
            title: t("nasMountWizard.protocols.nfs.title"),
            desc: t("nasMountWizard.protocols.nfs.desc"),
          },
          {
            key: "smb" as Protocol,
            icon: HardDrive,
            title: t("nasMountWizard.protocols.smb.title"),
            desc: t("nasMountWizard.protocols.smb.desc"),
          },
          {
            key: "usb" as Protocol,
            icon: Usb,
            title: t("nasMountWizard.protocols.usb.title"),
            desc: t("nasMountWizard.protocols.usb.desc"),
          },
        ] as const).map((item) => (
          <button
            key={item.key}
            type="button"
            onClick={() => onProtocolChange(item.key)}
            className={`flex flex-col items-center gap-2 rounded-xl border p-4 text-center transition-[color,background-color,border-color] ${
              protocol === item.key
                ? "border-primary bg-primary/5 ring-2 ring-primary/30"
                : "border-border/70 hover:border-primary/30 hover:bg-accent/30"
            }`}
            aria-pressed={protocol === item.key}
          >
            <item.icon
              className={`size-6 ${protocol === item.key ? "text-primary" : "text-muted-foreground"}`}
            />
            <span className="text-sm font-medium">{item.title}</span>
            <span className="text-xs text-muted-foreground leading-snug">{item.desc}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
