import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context.hooks";
import { securityReturnPath } from "@/lib/step-up-prerequisite";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Button } from "@/components/ui/button";

export function StepUpPrerequisiteNotice({ className }: { className?: string }) {
  const { token, totpEnabled, authTransitioning } = useAuth();
  const { pathname } = useLocation();
  const { t } = useTranslation();
  if (!token || totpEnabled) return null;
  if (authTransitioning) {
    return <InlineAlert className={className}>{t("stepUp.authTransitioning")}</InlineAlert>;
  }
  const returnTo = securityReturnPath(pathname);
  return (
    <InlineAlert tone="warning" className={className}>
      <p>{t("stepUp.prerequisiteNotice")}</p>
      <Button asChild size="sm" variant="outline" className="mt-2">
        <Link to="/app/settings?tab=account" state={returnTo ? { securityReturnTo: returnTo } : undefined}>
          {t("stepUp.enableTOTP")}
        </Link>
      </Button>
    </InlineAlert>
  );
}
