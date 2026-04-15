import React, { Suspense, useState } from "react";
import { HardDrive } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAuth } from "@/context/auth-context";

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
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{t('storage.guideTitle')}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground mb-3">
            {t('storage.guideDesc')}
          </p>
          <Button size="sm" variant="outline" onClick={() => setWizardOpen(true)}>
            <HardDrive className="mr-1 size-3.5" />
            {t('storage.configureExternal')}
          </Button>
        </CardContent>
      </Card>
      <Suspense fallback={null}>
        <NasMountWizard open={wizardOpen} onOpenChange={setWizardOpen} />
      </Suspense>
    </>
  );
}
