import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { PageHero } from "@/components/ui/page-hero";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function NotFoundPage() {
  const { t } = useTranslation();
  const title = t("notFound.title");
  useDocumentTitle(title);

  return (
    <div className="animate-fade-in space-y-5">
      <PageHero title={title} subtitle={t("notFound.description")} />
      <Button asChild>
        <Link to="/app/overview">{t("notFound.goHome")}</Link>
      </Button>
    </div>
  );
}
