import { useMemo, useState } from "react";
import { Clock3, LayoutGrid, List, Plus, Trash2, Wrench, X } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import type { NewPolicyInput, PolicyRecord } from "@/types/domain";

type PolicyTemplate = {
  label: string;
  cron: string;
  hint: string;
};

const policyTemplates: PolicyTemplate[] = [
  { label: "每 2 小时", cron: "0 */2 * * *", hint: "两小时整点执行" },
  { label: "每天 02:30", cron: "30 2 * * *", hint: "每日凌晨执行" },
  { label: "每周日 03:00", cron: "0 3 * * 0", hint: "周级归档策略" }
];

function cronToNatural(cron: string) {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return `按表达式 ${cron} 执行`;
  }

  const [minute, hour] = parts;
  if (minute.startsWith("*/")) {
    return `每隔 ${minute.replace("*/", "")} 分钟同步一次`;
  }
  if (hour.startsWith("*/")) {
    const hours = hour.replace("*/", "");
    return minute === "0" ? `每隔 ${hours} 小时整点同步一次` : `每隔 ${hours} 小时在 ${minute} 分执行`;
  }
  if (parts[4] !== "*") {
    return `每周 ${parts[4]} ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
  }
  return `每天 ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
}

function parseWeekday(weekday: string) {
  const map: Record<string, string> = {
    "0": "周日",
    "1": "周一",
    "2": "周二",
    "3": "周三",
    "4": "周四",
    "5": "周五",
    "6": "周六",
    "7": "周日"
  };
  return map[weekday] ?? `周${weekday}`;
}

function nextRunPreview(cron: string) {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return "无法预估下次执行时间";
  }

  const now = new Date();
  const minute = parts[0];
  const hour = parts[1];
  const weekday = parts[4];

  if (minute.startsWith("*/") && hour === "*") {
    const interval = Number(minute.replace("*/", ""));
    if (Number.isFinite(interval) && interval > 0) {
      const next = new Date(now.getTime() + interval * 60 * 1000);
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }
  }

  if (hour.startsWith("*/")) {
    const interval = Number(hour.replace("*/", ""));
    const minuteValue = Number(minute);
    if (Number.isFinite(interval) && interval > 0 && Number.isFinite(minuteValue)) {
      const next = new Date(now);
      next.setMinutes(minuteValue, 0, 0);
      while (next <= now) {
        next.setHours(next.getHours() + interval);
      }
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }
  }

  const minuteValue = Number(minute);
  const hourValue = Number(hour);
  if (Number.isFinite(minuteValue) && Number.isFinite(hourValue)) {
    const next = new Date(now);
    next.setHours(hourValue, minuteValue, 0, 0);
    if (weekday === "*") {
      if (next <= now) {
        next.setDate(next.getDate() + 1);
      }
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }

    const targetWeekday = Number(weekday);
    if (Number.isFinite(targetWeekday)) {
      let dayOffset = targetWeekday - next.getDay();
      if (dayOffset < 0 || (dayOffset === 0 && next <= now)) {
        dayOffset += 7;
      }
      next.setDate(next.getDate() + dayOffset);
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}（${parseWeekday(weekday)}）`;
    }
  }

  return "无法预估下次执行时间";
}

type MobileCronMode = "hourly" | "daily" | "weekly" | "custom";

type MobileCronDraft = {
  mode: MobileCronMode;
  minute: number;
  hour: number;
  weekday: number;
  intervalHours: number;
};

function clampNumber(value: number, min: number, max: number) {
  if (!Number.isFinite(value)) {
    return min;
  }
  return Math.min(max, Math.max(min, Math.floor(value)));
}

function parseMobileCron(cron: string): MobileCronDraft {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return { mode: "custom", minute: 0, hour: 2, weekday: 1, intervalHours: 2 };
  }

  const minute = clampNumber(Number(parts[0]), 0, 59);
  const hourRaw = parts[1];
  const weekdayRaw = parts[4];

  if (hourRaw.startsWith("*/")) {
    return {
      mode: "hourly",
      minute,
      hour: 0,
      weekday: 1,
      intervalHours: clampNumber(Number(hourRaw.replace("*/", "")), 1, 12)
    };
  }

  if (weekdayRaw !== "*") {
    return {
      mode: "weekly",
      minute,
      hour: clampNumber(Number(hourRaw), 0, 23),
      weekday: clampNumber(Number(weekdayRaw), 0, 6),
      intervalHours: 2
    };
  }

  if (!Number.isNaN(Number(hourRaw))) {
    return {
      mode: "daily",
      minute,
      hour: clampNumber(Number(hourRaw), 0, 23),
      weekday: 1,
      intervalHours: 2
    };
  }

  return { mode: "custom", minute: 0, hour: 2, weekday: 1, intervalHours: 2 };
}

function composeMobileCron(config: MobileCronDraft): string {
  const minute = clampNumber(config.minute, 0, 59);
  const hour = clampNumber(config.hour, 0, 23);
  const weekday = clampNumber(config.weekday, 0, 6);
  const intervalHours = clampNumber(config.intervalHours, 1, 12);

  switch (config.mode) {
    case "hourly":
      return `${minute} */${intervalHours} * * *`;
    case "daily":
      return `${minute} ${hour} * * *`;
    case "weekly":
      return `${minute} ${hour} * * ${weekday}`;
    default:
      return "";
  }
}

type PolicyDraft = NewPolicyInput & {
  id?: number;
};

const emptyDraft: PolicyDraft = {
  name: "",
  sourcePath: "",
  targetPath: "",
  cron: "0 */2 * * *",
  criticalThreshold: 2,
  enabled: true
};

function toDraft(policy: PolicyRecord): PolicyDraft {
  return {
    id: policy.id,
    name: policy.name,
    sourcePath: policy.sourcePath,
    targetPath: policy.targetPath,
    cron: policy.cron,
    criticalThreshold: policy.criticalThreshold,
    enabled: policy.enabled
  };
}

export function PoliciesPage() {
  const {
    policies,
    loading,
    globalSearch,
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy
  } = useOutletContext<ConsoleOutletContext>();

  const [keyword, setKeyword] = useState(globalSearch);
  const [viewMode, setViewMode] = useState<"cards" | "list">("cards");
  const [showEditor, setShowEditor] = useState(false);
  const [draft, setDraft] = useState<PolicyDraft>(emptyDraft);
  const [mobileCron, setMobileCron] = useState<MobileCronDraft>(() => parseMobileCron(emptyDraft.cron));
  const { confirm, dialog } = useConfirm();

  const filteredPolicies = useMemo(() => {
    const effectiveKeyword = (keyword || globalSearch).trim().toLowerCase();
    if (!effectiveKeyword) {
      return policies;
    }
    return policies.filter((policy) =>
      `${policy.name} ${policy.sourcePath} ${policy.targetPath} ${policy.cron}`
        .toLowerCase()
        .includes(effectiveKeyword)
    );
  }, [globalSearch, keyword, policies]);

  const activeCount = policies.filter((policy) => policy.enabled).length;

  const onSavePolicy = async () => {
    if (!draft.name.trim() || !draft.sourcePath.trim() || !draft.targetPath.trim() || !draft.cron.trim()) {
      toast.error("保存失败：策略名称、源路径、目标路径、Cron 必填。");
      return;
    }

    const input: NewPolicyInput = {
      name: draft.name.trim(),
      sourcePath: draft.sourcePath.trim(),
      targetPath: draft.targetPath.trim(),
      cron: draft.cron.trim(),
      criticalThreshold: Math.max(1, Number(draft.criticalThreshold || 1)),
      enabled: draft.enabled
    };

    try {
      if (draft.id) {
        await updatePolicy(draft.id, input);
        toast.success(`策略 ${draft.name} 已更新。`);
      } else {
        await createPolicy(input);
        toast.success(`策略 ${draft.name} 已新增。`);
      }

      setShowEditor(false);
      setDraft(emptyDraft);
      setMobileCron(parseMobileCron(emptyDraft.cron));
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const openEditorForCreate = () => {
    setDraft(emptyDraft);
    setMobileCron(parseMobileCron(emptyDraft.cron));
    setShowEditor(true);
  };

  const onEdit = (policy: PolicyRecord) => {
    const nextDraft = toDraft(policy);
    setDraft(nextDraft);
    setMobileCron(parseMobileCron(nextDraft.cron));
    setShowEditor(true);
  };

  const onDelete = async (policy: PolicyRecord) => {
    const ok = await confirm({ title: "确认删除", description: `确认删除策略 ${policy.name} 吗？` });
    if (!ok) {
      return;
    }
    try {
      await deletePolicy(policy.id);
      toast.success(`策略 ${policy.name} 已删除。`);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const onTogglePolicy = async (policy: PolicyRecord) => {
    try {
      await togglePolicy(policy.id);
      toast.success(`策略 ${policy.name} 已${policy.enabled ? "停用" : "启用"}。`);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const applyMobileCron = (next: MobileCronDraft) => {
    setMobileCron(next);
    const generated = composeMobileCron(next);
    if (next.mode !== "custom" && generated) {
      setDraft((prev) => ({
        ...prev,
        cron: generated
      }));
    }
  };

  return (
    <div className="animate-fade-in space-y-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">策略配置中枢（新增 / 编辑 / 删除 + 卡片视图）</CardTitle>
            <div className="flex items-center gap-2">
              <div className="hidden items-center gap-1 rounded-md border p-1 md:flex">
                <Button
                  size="sm"
                  variant={viewMode === "cards" ? "default" : "outline"}
                  onClick={() => setViewMode("cards")}
                >
                  <LayoutGrid className="mr-1 size-4" />
                  卡片
                </Button>
                <Button
                  size="sm"
                  variant={viewMode === "list" ? "default" : "outline"}
                  onClick={() => setViewMode("list")}
                >
                  <List className="mr-1 size-4" />
                  列表
                </Button>
              </div>
              <Button size="sm" onClick={openEditorForCreate}>
                <Plus className="mr-1 size-4" />
                新增策略
              </Button>
            </div>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="grid gap-2 md:grid-cols-[1fr_auto]">
            <Input
              placeholder="搜索策略 / 路径 / cron"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />
            <Badge variant="secondary">启用 {activeCount}/{policies.length}</Badge>
          </div>

          {showEditor ? (
            <div className="hidden space-y-3 rounded-lg border bg-muted/30 p-3 md:block">
              <div className="grid gap-2 md:grid-cols-2">
                <Input
                  placeholder="策略名称"
                  value={draft.name}
                  onChange={(event) => setDraft((prev) => ({ ...prev, name: event.target.value }))}
                />
                <Input
                  placeholder="Cron（例如：0 */2 * * *）"
                  value={draft.cron}
                  onChange={(event) => setDraft((prev) => ({ ...prev, cron: event.target.value }))}
                />
              </div>

              <div className="flex flex-wrap gap-2 rounded-md border bg-background p-2">
                {policyTemplates.map((template) => (
                  <Button
                    key={template.label}
                    size="sm"
                    variant={draft.cron === template.cron ? "default" : "outline"}
                    onClick={() => {
                      setDraft((prev) => ({ ...prev, cron: template.cron }));
                      setMobileCron(parseMobileCron(template.cron));
                    }}
                    title={template.hint}
                  >
                    {template.label}
                  </Button>
                ))}
              </div>

              <div className="grid gap-2 md:grid-cols-2">
                <Input
                  placeholder="源路径"
                  value={draft.sourcePath}
                  onChange={(event) => setDraft((prev) => ({ ...prev, sourcePath: event.target.value }))}
                />
                <Input
                  placeholder="目标路径"
                  value={draft.targetPath}
                  onChange={(event) => setDraft((prev) => ({ ...prev, targetPath: event.target.value }))}
                />
              </div>

              <div className="grid gap-2 md:grid-cols-2">
                <Input
                  type="number"
                  min={1}
                  max={10}
                  placeholder="失败阈值"
                  value={draft.criticalThreshold}
                  onChange={(event) =>
                    setDraft((prev) => ({
                      ...prev,
                      criticalThreshold: Number(event.target.value || 1)
                    }))
                  }
                />
                <label className="flex h-10 items-center gap-2 rounded-md border px-3 text-sm">
                  <Switch
                    checked={draft.enabled}
                    onCheckedChange={(checked) => setDraft((prev) => ({ ...prev, enabled: checked }))}
                  />
                  启用策略
                </label>
              </div>

              <div className="rounded-md border bg-background p-3 text-sm">
                <p className="text-xs text-muted-foreground">自然语言</p>
                <p className="mt-1">{cronToNatural(draft.cron)}</p>
                <p className="mt-1 text-xs text-cyan-600 dark:text-cyan-300">{nextRunPreview(draft.cron)}</p>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <Button variant="outline" onClick={() => setShowEditor(false)}>
                  取消
                </Button>
                <Button onClick={onSavePolicy}>保存策略</Button>
              </div>
            </div>
          ) : null}

          {loading ? <p className="text-sm text-muted-foreground">策略数据加载中...</p> : null}

          {viewMode === "cards" ? (
            <div className="grid gap-3 lg:grid-cols-2">
              {filteredPolicies.map((policy) => (
                <div key={policy.id} className="rounded-lg border p-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <h3 className="font-medium">{policy.name}</h3>
                      <p className="text-xs text-muted-foreground">{policy.naturalLanguage}</p>
                    </div>
                    <div className="flex items-center gap-2">
                      <Switch checked={policy.enabled} onCheckedChange={() => void onTogglePolicy(policy)} />
                      <Button variant="outline" size="sm" onClick={() => onEdit(policy)}>
                        <Wrench className="mr-1 size-4" />
                        编辑
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => onDelete(policy)}>
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </div>

                  <div className="mt-3 grid gap-2 text-sm text-muted-foreground">
                    <p>源路径：{policy.sourcePath}</p>
                    <p>目标路径：{policy.targetPath}</p>
                  </div>

                  <div className="mt-3 flex flex-wrap gap-2">
                    <Badge variant="outline">Cron: {policy.cron}</Badge>
                    <Badge variant="outline">失败阈值: {policy.criticalThreshold}</Badge>
                    <Badge variant={policy.enabled ? "success" : "outline"}>
                      {policy.enabled ? "自动执行中" : "已停用"}
                    </Badge>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <div className="overflow-x-auto rounded-lg border">
              <table className="min-w-[980px] text-left text-sm">
                <thead>
                  <tr className="border-b bg-muted/40 text-muted-foreground">
                    <th className="px-3 py-3">策略名</th>
                    <th className="px-3 py-3">Cron</th>
                    <th className="px-3 py-3">源路径</th>
                    <th className="px-3 py-3">目标路径</th>
                    <th className="px-3 py-3">状态</th>
                    <th className="px-3 py-3 text-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredPolicies.map((policy) => (
                    <tr key={policy.id} className="border-b">
                      <td className="px-3 py-3">
                        <p className="font-medium">{policy.name}</p>
                        <p className="text-xs text-muted-foreground">阈值 {policy.criticalThreshold}</p>
                      </td>
                      <td className="px-3 py-3">
                        <p className="font-mono text-xs">{policy.cron}</p>
                        <p className="text-xs text-muted-foreground">{policy.naturalLanguage}</p>
                      </td>
                      <td className="px-3 py-3 text-muted-foreground">{policy.sourcePath}</td>
                      <td className="px-3 py-3 text-muted-foreground">{policy.targetPath}</td>
                      <td className="px-3 py-3">
                        <Switch checked={policy.enabled} onCheckedChange={() => void onTogglePolicy(policy)} />
                      </td>
                      <td className="px-3 py-3 text-right">
                        <div className="flex justify-end gap-2">
                          <Button size="sm" variant="outline" onClick={() => onEdit(policy)}>
                            <Clock3 className="mr-1 size-4" />
                            编辑
                          </Button>
                          <Button size="sm" variant="outline" onClick={() => onDelete(policy)}>
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
      </Card>

      {showEditor ? (
        <div className="fixed inset-0 z-50 md:hidden">
          <button className="absolute inset-0 bg-black/45" onClick={() => setShowEditor(false)} />
          <section className="mobile-sheet absolute bottom-0 left-0 right-0 max-h-[92vh] overflow-auto rounded-t-2xl border-t bg-background p-4">
            <div className="mb-3 flex items-center justify-between">
              <p className="text-sm font-semibold">移动端策略编辑</p>
              <Button variant="ghost" size="icon" onClick={() => setShowEditor(false)}>
                <X className="size-4" />
              </Button>
            </div>

            <div className="space-y-3">
              <Input
                placeholder="策略名称"
                value={draft.name}
                onChange={(event) => setDraft((prev) => ({ ...prev, name: event.target.value }))}
              />

              <div className="grid grid-cols-2 gap-2">
                <Input
                  placeholder="源路径"
                  value={draft.sourcePath}
                  onChange={(event) => setDraft((prev) => ({ ...prev, sourcePath: event.target.value }))}
                />
                <Input
                  placeholder="目标路径"
                  value={draft.targetPath}
                  onChange={(event) => setDraft((prev) => ({ ...prev, targetPath: event.target.value }))}
                />
              </div>

              <div className="rounded-lg border bg-muted/30 p-3">
                <p className="text-xs text-muted-foreground">调度模式（触屏滚轮优化）</p>
                <div className="mt-2 grid grid-cols-2 gap-2">
                  <select
                    className="h-10 rounded-md border bg-background px-3 text-sm"
                    value={mobileCron.mode}
                    onChange={(event) => {
                      const mode = event.target.value as MobileCronMode;
                      const next = {
                        ...mobileCron,
                        mode
                      };
                      applyMobileCron(next);
                    }}
                  >
                    <option value="hourly">每隔 N 小时</option>
                    <option value="daily">每天</option>
                    <option value="weekly">每周</option>
                    <option value="custom">自定义 Cron</option>
                  </select>

                  <Input
                    type="number"
                    min={1}
                    max={10}
                    placeholder="失败阈值"
                    value={draft.criticalThreshold}
                    onChange={(event) =>
                      setDraft((prev) => ({
                        ...prev,
                        criticalThreshold: Number(event.target.value || 1)
                      }))
                    }
                  />
                </div>

                {mobileCron.mode === "hourly" ? (
                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <select
                      className="h-10 rounded-md border bg-background px-3 text-sm"
                      value={mobileCron.intervalHours}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          intervalHours: Number(event.target.value || 2)
                        })
                      }
                    >
                      {Array.from({ length: 12 }).map((_, index) => {
                        const value = index + 1;
                        return (
                          <option key={`interval-${value}`} value={value}>
                            每 {value} 小时
                          </option>
                        );
                      })}
                    </select>

                    <select
                      className="h-10 rounded-md border bg-background px-3 text-sm"
                      value={mobileCron.minute}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          minute: Number(event.target.value || 0)
                        })
                      }
                    >
                      {Array.from({ length: 60 }).map((_, index) => (
                        <option key={`minute-hourly-${index}`} value={index}>
                          第 {index.toString().padStart(2, "0")} 分
                        </option>
                      ))}
                    </select>
                  </div>
                ) : null}

                {mobileCron.mode === "daily" ? (
                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <select
                      className="h-10 rounded-md border bg-background px-3 text-sm"
                      value={mobileCron.hour}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          hour: Number(event.target.value || 0)
                        })
                      }
                    >
                      {Array.from({ length: 24 }).map((_, index) => (
                        <option key={`hour-daily-${index}`} value={index}>
                          {index.toString().padStart(2, "0")} 时
                        </option>
                      ))}
                    </select>

                    <select
                      className="h-10 rounded-md border bg-background px-3 text-sm"
                      value={mobileCron.minute}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          minute: Number(event.target.value || 0)
                        })
                      }
                    >
                      {Array.from({ length: 60 }).map((_, index) => (
                        <option key={`minute-daily-${index}`} value={index}>
                          {index.toString().padStart(2, "0")} 分
                        </option>
                      ))}
                    </select>
                  </div>
                ) : null}

                {mobileCron.mode === "weekly" ? (
                  <div className="mt-2 grid grid-cols-3 gap-2">
                    <select
                      className="h-10 rounded-md border bg-background px-2 text-sm"
                      value={mobileCron.weekday}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          weekday: Number(event.target.value || 0)
                        })
                      }
                    >
                      {Array.from({ length: 7 }).map((_, index) => (
                        <option key={`weekday-${index}`} value={index}>
                          {parseWeekday(String(index))}
                        </option>
                      ))}
                    </select>

                    <select
                      className="h-10 rounded-md border bg-background px-2 text-sm"
                      value={mobileCron.hour}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          hour: Number(event.target.value || 0)
                        })
                      }
                    >
                      {Array.from({ length: 24 }).map((_, index) => (
                        <option key={`hour-weekly-${index}`} value={index}>
                          {index.toString().padStart(2, "0")} 时
                        </option>
                      ))}
                    </select>

                    <select
                      className="h-10 rounded-md border bg-background px-2 text-sm"
                      value={mobileCron.minute}
                      onChange={(event) =>
                        applyMobileCron({
                          ...mobileCron,
                          minute: Number(event.target.value || 0)
                        })
                      }
                    >
                      {Array.from({ length: 60 }).map((_, index) => (
                        <option key={`minute-weekly-${index}`} value={index}>
                          {index.toString().padStart(2, "0")} 分
                        </option>
                      ))}
                    </select>
                  </div>
                ) : null}

                {mobileCron.mode === "custom" ? (
                  <Input
                    className="mt-2"
                    placeholder="Cron（例如：0 */2 * * *）"
                    value={draft.cron}
                    onChange={(event) => setDraft((prev) => ({ ...prev, cron: event.target.value }))}
                  />
                ) : null}

                <p className="mt-2 text-xs text-muted-foreground">当前 Cron：{draft.cron}</p>
                <p className="text-xs text-cyan-600 dark:text-cyan-300">{cronToNatural(draft.cron)}</p>
              </div>

              <label className="flex h-10 items-center gap-2 rounded-md border px-3 text-sm">
                <Switch
                  checked={draft.enabled}
                  onCheckedChange={(checked) => setDraft((prev) => ({ ...prev, enabled: checked }))}
                />
                启用策略
              </label>

              <div className="grid grid-cols-2 gap-2">
                <Button variant="outline" onClick={() => setShowEditor(false)}>
                  取消
                </Button>
                <Button onClick={onSavePolicy}>保存策略</Button>
              </div>
            </div>
          </section>
        </div>
      ) : null}

      {dialog}
    </div>
  );
}
