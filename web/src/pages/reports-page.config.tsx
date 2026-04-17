import { useTranslation } from "react-i18next";
import { Pencil, RefreshCw, Trash2, Zap } from "lucide-react";
import { useState } from "react";
import { type ReportConfig } from "@/lib/api/reports-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { ReportHistory } from "@/pages/reports-page.history";

export function ConfigCard({
  cfg,
  isAdmin,
  token,
  onEdit,
  onDelete,
  onGenerate,
}: {
  cfg: ReportConfig;
  isAdmin: boolean;
  token: string;
  onEdit: (cfg: ReportConfig) => void;
  onDelete: (id: number) => void;
  onGenerate: (id: number) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [generating, setGenerating] = useState(false);

  const handleGenerate = async () => {
    setGenerating(true);
    try {
      await onGenerate(cfg.id);
    } finally {
      setGenerating(false);
    }
  };

  const scopeLabel =
    cfg.scope_type === "all"
      ? t("reports.scopeLabels.all")
      : cfg.scope_type === "tag"
        ? t("reports.scopeTagValue", { value: cfg.scope_value })
        : t("reports.scopeLabels.node_ids");

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-2 pb-2">
        <div>
          <p className="font-semibold">{cfg.name}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {cfg.period === "weekly"
              ? t("reports.periodLabels.weekly")
              : t("reports.periodLabels.monthly")}{" "}
            · {scopeLabel} · {cfg.cron}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Badge tone={cfg.enabled ? "success" : "neutral"}>
            {cfg.enabled ? t("common.enabled") : t("common.disabled")}
          </Badge>
          {isAdmin && (
            <>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-muted-foreground hover:text-foreground"
                title={t("reports.generateNow")}
                aria-label={t("reports.generateNow")}
                disabled={generating}
                onClick={() => void handleGenerate()}
              >
                {generating ? (
                  <RefreshCw className="size-4 animate-spin" />
                ) : (
                  <Zap className="size-4" />
                )}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-muted-foreground hover:text-foreground"
                title={t("common.edit")}
                aria-label={t("common.edit")}
                onClick={() => onEdit(cfg)}
              >
                <Pencil className="size-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-destructive/80 hover:text-destructive"
                title={t("reports.deleteConfig")}
                aria-label={t("reports.deleteConfig")}
                onClick={() => onDelete(cfg.id)}
              >
                <Trash2 className="size-4" />
              </Button>
            </>
          )}
        </div>
      </CardHeader>

      <CardContent className="pt-0">
        <ReportHistory cfg={cfg} token={token} />
      </CardContent>
    </Card>
  );
}
