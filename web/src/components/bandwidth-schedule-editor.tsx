import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export interface BandwidthRule {
  start: string;
  end: string;
  limit_mbps: number;
}

interface BandwidthScheduleEditorProps {
  value: string;
  onChange: (value: string) => void;
}

function parseRules(json: string): BandwidthRule[] {
  if (!json) return [];
  try {
    const parsed = JSON.parse(json);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

export function BandwidthScheduleEditor({ value, onChange }: BandwidthScheduleEditorProps) {
  const { t } = useTranslation();
  const rules = parseRules(value);

  const emit = (next: BandwidthRule[]) => {
    onChange(next.length > 0 ? JSON.stringify(next) : "");
  };

  const addRule = () => {
    emit([...rules, { start: "22:00", end: "06:00", limit_mbps: 100 }]);
  };

  const removeRule = (index: number) => {
    emit(rules.filter((_, i) => i !== index));
  };

  const updateRule = (index: number, field: keyof BandwidthRule, val: string) => {
    const next = rules.map((r, i) => {
      if (i !== index) return r;
      if (field === "limit_mbps") {
        const num = Number.parseInt(val, 10);
        return { ...r, limit_mbps: Number.isFinite(num) && num >= 0 ? num : 0 };
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
          <Plus className="mr-1 size-3" />
          {t('bandwidth.addRule')}
        </Button>
      </div>

      {rules.length === 0 && (
        <p className="text-xs text-muted-foreground">{t('bandwidth.noRules')}</p>
      )}

      {rules.map((rule, i) => (
        <div key={i} className="flex items-center gap-2">
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
            value={rule.limit_mbps}
            onChange={(e) => updateRule(i, "limit_mbps", e.target.value)}
            aria-label={t('bandwidthEditor.limitMbps')}
          />
          <span className="text-xs text-muted-foreground shrink-0">Mbps</span>
          <Button type="button" variant="ghost" size="sm" className="size-7 p-0 shrink-0" onClick={() => removeRule(i)}>
            <Trash2 className="size-3.5 text-destructive" />
          </Button>
        </div>
      ))}

      {rules.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('bandwidth.rulesHint')}
        </p>
      )}
    </div>
  );
}
