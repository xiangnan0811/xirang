import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Edit2, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { EscalationPolicyEditor } from "@/components/escalation-policy-editor";
import {
  deleteEscalationPolicy,
  listEscalationPolicies,
  updateEscalationPolicy,
  type EscalationPolicyInput,
} from "@/lib/api/escalation";
import { getErrorMessage } from "@/lib/utils";
import { useAuth } from "@/context/auth-context";
import type { EscalationPolicy } from "@/types/domain";

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" });
}

function severityBadgeTone(sev: string): "destructive" | "warning" | "neutral" {
  if (sev === "critical") return "destructive";
  if (sev === "warning") return "warning";
  return "neutral";
}

export function SettingsPageEscalation() {
  const { t } = useTranslation();
  const { token } = useAuth();

  const [policies, setPolicies] = useState<EscalationPolicy[]>([]);
  const [loading, setLoading] = useState(true);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingPolicy, setEditingPolicy] = useState<EscalationPolicy | undefined>(undefined);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [togglingId, setTogglingId] = useState<number | null>(null);

  const refresh = useCallback(() => {
    if (!token) return;
    setLoading(true);
    listEscalationPolicies(token)
      .then(setPolicies)
      .catch((err) => toast.error(getErrorMessage(err)))
      .finally(() => setLoading(false));
  }, [token]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const handleOpenNew = () => {
    setEditingPolicy(undefined);
    setEditorOpen(true);
  };

  const handleOpenEdit = (policy: EscalationPolicy) => {
    setEditingPolicy(policy);
    setEditorOpen(true);
  };

  const handleSaved = (saved: EscalationPolicy) => {
    setPolicies((prev) => {
      const idx = prev.findIndex((p) => p.id === saved.id);
      if (idx >= 0) {
        const next = [...prev];
        next[idx] = saved;
        return next;
      }
      return [...prev, saved];
    });
  };

  const handleToggle = async (policy: EscalationPolicy, checked: boolean) => {
    if (!token) return;
    // Optimistic update
    setPolicies((prev) =>
      prev.map((p) => (p.id === policy.id ? { ...p, enabled: checked } : p))
    );
    setTogglingId(policy.id);
    try {
      const input: EscalationPolicyInput = {
        name: policy.name,
        description: policy.description,
        min_severity: policy.min_severity,
        enabled: checked,
        levels: policy.levels,
      };
      const updated = await updateEscalationPolicy(token, policy.id, input);
      setPolicies((prev) =>
        prev.map((p) => (p.id === updated.id ? updated : p))
      );
    } catch (err) {
      // Revert
      setPolicies((prev) =>
        prev.map((p) => (p.id === policy.id ? { ...p, enabled: !checked } : p))
      );
      toast.error(getErrorMessage(err));
    } finally {
      setTogglingId(null);
    }
  };

  const handleDelete = async (policy: EscalationPolicy) => {
    if (!token) return;
    const confirmed = window.confirm(t("escalation.deleteConfirm", { name: policy.name }));
    if (!confirmed) return;
    setDeletingId(policy.id);
    try {
      await deleteEscalationPolicy(token, policy.id);
      setPolicies((prev) => prev.filter((p) => p.id !== policy.id));
    } catch (err) {
      toast.error(getErrorMessage(err));
    } finally {
      setDeletingId(null);
    }
  };

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-base">{t("escalation.tabTitle")}</CardTitle>
        <Button size="sm" onClick={handleOpenNew}>
          <Plus className="mr-1 size-4" />
          {t("escalation.newButton")}
        </Button>
      </CardHeader>

      <CardContent>
        {loading ? (
          <p className="text-sm text-muted-foreground">加载中…</p>
        ) : policies.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-12 text-center">
            <p className="text-sm font-medium">{t("escalation.empty.title")}</p>
            <p className="text-xs text-muted-foreground">{t("escalation.empty.hint")}</p>
            <Button size="sm" onClick={handleOpenNew}>
              <Plus className="mr-1 size-4" />
              {t("escalation.newButton")}
            </Button>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-muted-foreground">
                  <th className="pb-2 pr-4 font-medium">{t("escalation.fields.name")}</th>
                  <th className="pb-2 pr-4 font-medium">{t("escalation.fields.minSeverity")}</th>
                  <th className="pb-2 pr-4 font-medium">{t("escalation.fields.levels")}</th>
                  <th className="pb-2 pr-4 font-medium">{t("escalation.fields.enabled")}</th>
                  <th className="pb-2 pr-4 font-medium">更新时间</th>
                  <th className="pb-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {policies.map((policy) => (
                  <tr key={policy.id} className="border-b border-border/50 last:border-0">
                    <td className="py-2 pr-4 font-medium">{policy.name}</td>
                    <td className="py-2 pr-4">
                      <Badge tone={severityBadgeTone(policy.min_severity)}>
                        {policy.min_severity}
                      </Badge>
                    </td>
                    <td className="py-2 pr-4 text-muted-foreground">{policy.levels.length}</td>
                    <td className="py-2 pr-4">
                      <Switch
                        checked={policy.enabled}
                        disabled={togglingId === policy.id}
                        onCheckedChange={(checked) => void handleToggle(policy, checked)}
                        aria-label={`启用 ${policy.name}`}
                      />
                    </td>
                    <td className="py-2 pr-4 text-muted-foreground whitespace-nowrap">
                      {formatDate(policy.updated_at)}
                    </td>
                    <td className="py-2">
                      <div className="flex items-center gap-1">
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => handleOpenEdit(policy)}
                          aria-label={`编辑 ${policy.name}`}
                        >
                          <Edit2 className="size-4" />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => void handleDelete(policy)}
                          disabled={deletingId === policy.id}
                          aria-label={`删除 ${policy.name}`}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>

      <EscalationPolicyEditor
        open={editorOpen}
        onOpenChange={setEditorOpen}
        policy={editingPolicy}
        onSaved={handleSaved}
      />
    </Card>
  );
}
