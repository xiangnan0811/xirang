interface ExpiryCountdownBadgeProps {
  expiryDate?: string;
  archived?: boolean;
}

export function ExpiryCountdownBadge({ expiryDate, archived }: ExpiryCountdownBadgeProps) {
  if (archived) {
    return (
      <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium text-muted-foreground">
        已归档
      </span>
    );
  }

  if (!expiryDate) {
    return null;
  }

  const now = Date.now();
  const expiry = new Date(expiryDate).getTime();
  if (Number.isNaN(expiry)) {
    return null;
  }

  const daysRemaining = Math.ceil((expiry - now) / (1000 * 60 * 60 * 24));

  if (daysRemaining < 0) {
    return (
      <span className="inline-flex items-center rounded-full bg-destructive/10 px-2 py-0.5 text-[10px] font-medium text-destructive">
        已到期
      </span>
    );
  }

  if (daysRemaining <= 3) {
    return (
      <span className="inline-flex items-center rounded-full bg-destructive/10 px-2 py-0.5 text-[10px] font-medium text-destructive">
        还剩 {daysRemaining}d
      </span>
    );
  }

  if (daysRemaining <= 7) {
    return (
      <span className="inline-flex items-center rounded-full bg-warning/10 px-2 py-0.5 text-[10px] font-medium text-warning">
        还剩 {daysRemaining}d
      </span>
    );
  }

  return (
    <span className="inline-flex items-center rounded-full bg-success/10 px-2 py-0.5 text-[10px] font-medium text-success">
      还剩 {daysRemaining}d
    </span>
  );
}
