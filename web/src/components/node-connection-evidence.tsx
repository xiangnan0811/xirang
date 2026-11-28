import { useTranslation } from "react-i18next";
import { History } from "lucide-react";
import type { NodeConnectionEvidence } from "@/lib/status";
import { cn } from "@/lib/utils";

/**
 * Visible qualifier for evidence that does not describe the current connection settings; renders nothing
 * otherwise. The outcome itself is shown by the status badge or result row, never repeated here.
 */
export function NodeConnectionEvidenceNote({
  evidence,
  className,
}: {
  evidence: NodeConnectionEvidence;
  className?: string;
}) {
  const { t } = useTranslation();
  if (evidence.kind !== "previous" && evidence.kind !== "unavailable") {
    return null;
  }
  return (
    <p className={cn("flex items-start gap-1 text-xs text-[hsl(var(--warning-text))]", className)}>
      <History className="mt-0.5 size-3 shrink-0" aria-hidden />
      <span className="min-w-0">
        {evidence.kind === "previous" ? t("nodes.connectionEvidencePrevious") : t("nodes.connectionEvidenceUnavailable")}
      </span>
    </p>
  );
}
