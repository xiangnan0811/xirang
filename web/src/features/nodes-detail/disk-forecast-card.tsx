import { useDiskForecast } from "./use-disk-forecast";

const confidenceCopy: Record<string, string> = {
  high: "预测置信度：高",
  medium: "预测置信度：中",
  low: "预测置信度：低（样本不足）",
  insufficient: "样本不足（< 7 天）",
};

type Props = { nodeId: number };

export default function DiskForecastCard({ nodeId }: Props) {
  const { data } = useDiskForecast(nodeId);

  if (!data) {
    return (
      <div data-testid="disk-forecast-card" className="rounded-md border border-border bg-card p-4">
        <div className="text-sm text-muted-foreground">磁盘预测加载中…</div>
      </div>
    );
  }

  const { forecast, disk_gb_total, disk_gb_used_now, daily_growth_gb } = data;
  const flatOrShrinking = daily_growth_gb !== null && daily_growth_gb <= 0;

  return (
    <div data-testid="disk-forecast-card" className="rounded-md border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h3 className="text-base font-medium">💾 磁盘增长预测</h3>
        <span data-testid="confidence" className="text-xs text-muted-foreground">
          {confidenceCopy[forecast.confidence] ?? forecast.confidence}
        </span>
      </div>
      {disk_gb_total > 0 && (
        <div className="text-sm">
          当前 <b>{disk_gb_used_now.toFixed(1)}</b> / {disk_gb_total.toFixed(0)} GB
        </div>
      )}
      {daily_growth_gb !== null && !flatOrShrinking && (
        <div className="text-sm text-muted-foreground">日均增长 {daily_growth_gb.toFixed(2)} GB</div>
      )}
      {flatOrShrinking && <div className="text-sm text-muted-foreground">磁盘用量持平或下降中</div>}
      {forecast.days_to_full !== null && forecast.days_to_full > 0 && (
        <div className="text-sm">
          预计 <b>{Math.round(forecast.days_to_full)}</b> 天后满
          {forecast.date_full && <span className="text-muted-foreground">（{forecast.date_full}）</span>}
        </div>
      )}
    </div>
  );
}
