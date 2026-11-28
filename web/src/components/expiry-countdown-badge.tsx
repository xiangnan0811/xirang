import { useTranslation } from "react-i18next";

interface ExpiryCountdownBadgeProps {
  expiryDate?: string;
  archived?: boolean;
}

export function ExpiryCountdownBadge({ expiryDate, archived }: ExpiryCountdownBadgeProps) {
  const { t } = useTranslation();

  if (archived) {
    return (
      <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-micro font-medium text-muted-foreground">
        {t('expiry.archived')}
      </span>
    );
  }

  if (!expiryDate) {
    return null;
  }

  // eslint-disable-next-line react-hooks/purity -- Date.now() is intentional for expiry calculation
  const now = Date.now();
  const expiry = new Date(expiryDate).getTime();
  if (Number.isNaN(expiry)) {
    return null;
  }

  const msRemaining = expiry - now;

  // Compare instants before rounding: Math.ceil of a negative fraction is -0,
  // which would render "0 days left" for an expiry that already passed.
  if (msRemaining <= 0) {
    return (
      <span className="inline-flex items-center rounded-full bg-destructive/10 px-2 py-0.5 text-micro font-medium text-destructive">
        {t('expiry.expired')}
      </span>
    );
  }

  const daysRemaining = Math.ceil(msRemaining / (1000 * 60 * 60 * 24));

  if (daysRemaining <= 3) {
    return (
      <span className="inline-flex items-center rounded-full bg-destructive/10 px-2 py-0.5 text-micro font-medium text-destructive">
        {t('expiry.daysLeft', { days: daysRemaining })}
      </span>
    );
  }

  if (daysRemaining <= 7) {
    return (
      <span className="inline-flex items-center rounded-full bg-warning/10 px-2 py-0.5 text-micro font-medium text-warning">
        {t('expiry.daysLeft', { days: daysRemaining })}
      </span>
    );
  }

  return (
    <span className="inline-flex items-center rounded-full bg-success/10 px-2 py-0.5 text-micro font-medium text-success">
      {t('expiry.daysLeft', { days: daysRemaining })}
    </span>
  );
}
