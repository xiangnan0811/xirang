import { useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";

export function SetupWizardStep3() {
  const { t } = useTranslation();

  return (
    <div className="flex flex-col items-center text-center space-y-4 py-4">
      <div className="p-4 bg-success/10 rounded-full text-success">
        <CheckCircle2 className="size-10" />
      </div>
      <div className="space-y-1">
        <p className="text-sm text-muted-foreground">
          {t("setupWizard.completeHint1")}
        </p>
        <p className="text-sm text-muted-foreground">
          {t("setupWizard.completeHint2")}
        </p>
      </div>
    </div>
  );
}
