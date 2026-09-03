import React, { Suspense, useState } from "react";
import { HardDrive } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { useAuth } from "@/context/auth-context.hooks";

const NasMountWizard = React.lazy(() =>
  import("@/components/nas-mount-wizard").then(m => ({ default: m.NasMountWizard }))
);

export function StorageGuideCard() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const [wizardOpen, setWizardOpen] = useState(false);

  if (!token || role !== "admin") return null;

  return (
    <>
      <DataSurface>
        <DataSurfaceHeader title={t("storage.guideTitle")} />
        <DataSurfaceContent>
          <p className="mb-3 text-xs text-muted-foreground">
            {t("storage.guideDesc")}
          </p>
          <Button size="lg" variant="outline" onClick={() => setWizardOpen(true)}>
            <HardDrive className="mr-1 size-3.5" />
            {t("storage.configureExternal")}
          </Button>
        </DataSurfaceContent>
      </DataSurface>
      <Suspense fallback={null}>
        <NasMountWizard open={wizardOpen} onOpenChange={setWizardOpen} />
      </Suspense>
    </>
  );
}
