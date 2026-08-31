import { useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/confirm-dialog";

export interface BandwidthRule {
  start: string;
  end: string;
  limitMbps: number;
}

type BandwidthRuleWire = {
  start?: unknown;
  end?: unknown;
  limit_mbps?: unknown;
  limitMbps?: unknown;
};

interface BandwidthScheduleEditorProps {
  value: string;
  onChange: (value: string) => void;
}

function parseLimitMbps(raw: unknown): number {
  const num = typeof raw === "number" ? raw : Number.parseInt(String(raw ?? ""), 10);
  return Number.isFinite(num) && num >= 0 ? num : 0;
}

function parseRules(json: string): BandwidthRule[] {
  if (!json) return [];
  try {
    const parsed: unknown = JSON.parse(json);
    if (!Array.isArray(parsed)) return [];
    return parsed.flatMap((item) => {
      if (!item || typeof item !== "object") return [];
      const row = item as BandwidthRuleWire;
      return [{
        start: typeof row.start === "string" ? row.start : "",
        end: typeof row.end === "string" ? row.end : "",
        limitMbps: parseLimitMbps(row.limitMbps ?? row.limit_mbps),
      }];
    });
  } catch {
    return [];
  }
}

function toWireRules(rules: BandwidthRule[]) {
  return rules.map((rule) => ({
    start: rule.start,
    end: rule.end,
    limit_mbps: rule.limitMbps,
  }));
}

export function BandwidthScheduleEditor({ value, onChange }: BandwidthScheduleEditorProps) {
  const { t } = useTranslation();
  const rules = parseRules(value);
  const [pendingDelete, setPendingDelete] = useState<number | null>(null);

  const emit = (next: BandwidthRule[]) => {
    onChange(next.length > 0 ? JSON.stringify(toWireRules(next)) : "");
  };

  const addRule = () => {
    emit([...rules, { start: "22:00", end: "06:00", limitMbps: 100 }]);
  };

  const confirmRemoveRule = () => {
    if (pendingDelete === null) return;
    emit(rules.filter((_, i) => i !== pendingDelete));
    setPendingDelete(null);
  };

  const pendingRule = pendingDelete !== null ? rules[pendingDelete] : null;

  const updateRule = (index: number, field: keyof BandwidthRule, val: string) => {
    const next = rules.map((r, i) => {
      if (i !== index) return r;
      if (field === "limitMbps") {
        return { ...r, limitMbps: parseLimitMbps(val) };
      }
      return { ...r, [field]: val };
    });
    emit(next);
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <label className="block text-sm font-medium">{t('bandwidth.scheduleTitle')}</label>
        <Button type="button" variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={addRule}>
          <Plus className="mr-1 size-3" aria-hidden="true" />
          {t('bandwidth.addRule')}
        </Button>
      </div>

      {rules.length === 0 && (
        <p className="text-xs text-muted-foreground">{t('bandwidth.noRules')}</p>
      )}

      {rules.map((rule, i) => (
        <div key={`rule-${rule.start}-${rule.end}-${i}`} className="flex items-center gap-2">
          <Input
            type="time"
            className="w-28 text-xs"
            value={rule.start}
            onChange={(e) => updateRule(i, "start", e.target.value)}
            aria-label={t('bandwidthEditor.startTime')}
          />
          <span className="text-xs text-muted-foreground">{t('bandwidth.to')}</span>
          <Input
            type="time"
            className="w-28 text-xs"
            value={rule.end}
            onChange={(e) => updateRule(i, "end", e.target.value)}
            aria-label={t('bandwidthEditor.endTime')}
          />
          <Input
            type="number"
            className="w-24 text-xs"
            min={0}
            value={rule.limitMbps}
            onChange={(e) => updateRule(i, "limitMbps", e.target.value)}
            aria-label={t('bandwidthEditor.limitMbps')}
          />
          <span className="text-xs text-muted-foreground shrink-0">Mbps</span>
          <Button type="button" variant="ghost" size="sm" className="size-7 p-0 shrink-0" onClick={() => setPendingDelete(i)} aria-label={t('bandwidthEditor.deleteRule')}>
            <Trash2 className="size-3.5 text-destructive" aria-hidden="true" />
          </Button>
        </div>
      ))}

      {rules.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('bandwidth.rulesHint')}
        </p>
      )}

      <AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('bandwidthEditor.deleteConfirmTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRule
                ? t('bandwidthEditor.deleteConfirmDesc', { start: pendingRule.start, end: pendingRule.end })
                : null}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('bandwidthEditor.deleteConfirmCancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={confirmRemoveRule}>{t('bandwidthEditor.deleteConfirmAction')}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
