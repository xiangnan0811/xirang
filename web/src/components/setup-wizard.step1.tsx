import { useTranslation } from "react-i18next";
import { ShieldCheck, MonitorCheck, ClipboardList } from "lucide-react";

export function SetupWizardStep1() {
  const { t } = useTranslation();

  return (
    <div className="grid grid-cols-3 gap-3">
      {[
        {
          icon: <ShieldCheck className="size-5" />,
          label: t("setupWizard.capabilities.backupMgmt"),
          desc: t("setupWizard.capabilities.backupMgmtDesc"),
        },
        {
          icon: <MonitorCheck className="size-5" />,
          label: t("setupWizard.capabilities.nodeMonitor"),
          desc: t("setupWizard.capabilities.nodeMonitorDesc"),
        },
        {
          icon: <ClipboardList className="size-5" />,
          label: t("setupWizard.capabilities.policySchedule"),
          desc: t("setupWizard.capabilities.policyScheduleDesc"),
        },
      ].map((item) => (
        <div
          key={item.label}
          className="glass-panel p-4 flex flex-col items-center text-center space-y-2 border-border/40"
        >
          <div className="p-2.5 bg-primary/10 rounded-full text-primary">
            {item.icon}
          </div>
          <h3 className="font-medium text-sm">{item.label}</h3>
          <p className="text-xs text-muted-foreground">{item.desc}</p>
        </div>
      ))}
    </div>
  );
}
