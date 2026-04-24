import { LineChart, Line, ResponsiveContainer } from "recharts";

type StatCardProps = {
  label: string;
  value: number;
  unit?: string;
  sparkline?: number[];
  warnAt?: number;
};

function formatValue(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (n >= 100) return n.toFixed(0);
  if (n >= 10) return n.toFixed(1);
  return n.toFixed(2);
}

export default function StatCard({ label, value, unit, sparkline, warnAt }: StatCardProps) {
  const variant = warnAt !== undefined && value >= warnAt ? "warn" : "default";
  const data = sparkline?.map((v, i) => ({ i, v })) ?? [];

  const containerClass =
    "rounded-md border p-4 flex flex-col gap-1 " +
    (variant === "warn"
      ? "border-warning/40 bg-warning/10"
      : "border-border bg-card");

  const valueClass =
    "text-3xl font-semibold leading-none " +
    (variant === "warn" ? "text-warning-foreground dark:text-warning" : "text-foreground");

  return (
    <div data-testid="stat-card" data-variant={variant} className={containerClass}>
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className={valueClass}>
        {formatValue(value)}
        {unit ? <span className="text-lg text-muted-foreground ml-1">{unit}</span> : null}
      </div>
      {sparkline && sparkline.length > 0 && (
        <div className="h-10 mt-1" aria-hidden="true">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={data}>
              <Line
                type="monotone"
                dataKey="v"
                strokeWidth={1.5}
                dot={false}
                stroke="currentColor"
                isAnimationActive={false}
              />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  );
}
