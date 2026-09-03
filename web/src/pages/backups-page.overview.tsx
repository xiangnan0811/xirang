import { BackupConfidencePanel } from "@/components/backup-confidence-panel";
import { BackupHealthPanel } from "@/components/backup-health-panel";
import { StorageGuideCard } from "@/components/storage-guide-card";
import { StorageUsagePanel } from "@/components/storage-usage-panel";
import { Reveal, Stagger } from "@/components/ui/reveal";
import { useAuth } from "@/context/auth-context.hooks";
import { GaReadinessPanel } from "@/features/backup-assets/ga-readiness-panel";
import { PrivateNetworkContentTransportPanel } from "@/features/backup-assets/private-network-content-transport-panel";
import { useTranslation } from "react-i18next";

export function BackupsOverviewPage() {
  const { t } = useTranslation();
  const { token, role } = useAuth();

  return (
    <Stagger className="flex min-h-0 flex-col space-y-5">
      <h2 className="sr-only">{t("backups.tabs.overview")}</h2>
      {role === "admin" && token ? (
        <>
          <Reveal>
            <GaReadinessPanel token={token} role={role} />
          </Reveal>
          <Reveal>
            <PrivateNetworkContentTransportPanel token={token} />
          </Reveal>
        </>
      ) : null}
      <Reveal>
        <BackupConfidencePanel />
      </Reveal>
      <Reveal>
        <BackupHealthPanel />
      </Reveal>
      <Reveal>
        <section className="grid shrink-0 grid-cols-1 items-start gap-5 xl:grid-cols-3">
          <div className="xl:col-span-2">
            <StorageUsagePanel />
          </div>
          <div className="xl:col-span-1">
            <StorageGuideCard />
          </div>
        </section>
      </Reveal>
    </Stagger>
  );
}
