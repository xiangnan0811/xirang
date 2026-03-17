import { Plus, Trash2 } from "lucide-react";
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
        <label className="block text-sm font-medium">带宽时段调度</label>
        <Button type="button" variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={addRule}>
          <Plus className="mr-1 size-3" />
          添加规则
        </Button>
      </div>

      {rules.length === 0 && (
        <p className="text-xs text-muted-foreground">未配置带宽调度规则，将使用策略的静态限速或不限速。</p>
      )}

      {rules.map((rule, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            type="time"
            className="w-28 text-xs"
            value={rule.start}
            onChange={(e) => updateRule(i, "start", e.target.value)}
            aria-label="开始时间"
          />
          <span className="text-xs text-muted-foreground">至</span>
          <Input
            type="time"
            className="w-28 text-xs"
            value={rule.end}
            onChange={(e) => updateRule(i, "end", e.target.value)}
            aria-label="结束时间"
          />
          <Input
            type="number"
            className="w-24 text-xs"
            min={0}
            value={rule.limit_mbps}
            onChange={(e) => updateRule(i, "limit_mbps", e.target.value)}
            aria-label="限速 (Mbps)"
          />
          <span className="text-xs text-muted-foreground shrink-0">Mbps</span>
          <Button type="button" variant="ghost" size="sm" className="size-7 p-0 shrink-0" onClick={() => removeRule(i)}>
            <Trash2 className="size-3.5 text-destructive" />
          </Button>
        </div>
      ))}

      {rules.length > 0 && (
        <p className="text-xs text-muted-foreground">
          在指定时段内使用对应限速，未匹配时段不限速。支持跨午夜（如 22:00 至 06:00）。
        </p>
      )}
    </div>
  );
}
