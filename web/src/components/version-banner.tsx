import { useEffect, useState } from "react";
import { X, ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";

const DISMISSED_VERSION_KEY = "xirang.dismissed-version";

export function VersionBanner() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const [updateAvailable, setUpdateAvailable] = useState(false);
  const [currentVersion, setCurrentVersion] = useState("");
  const [latestVersion, setLatestVersion] = useState("");
  const [releaseUrl, setReleaseUrl] = useState("");
  const [dismissed, setDismissed] = useState(false);

  useEffect(() => {
    if (!token || role !== "admin") return;

    const controller = new AbortController();

    void apiClient.checkVersion(token, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      setCurrentVersion(result.current_version);
      setLatestVersion(result.latest_version);
      setReleaseUrl(result.release_url);

      if (result.update_available) {
        try {
          const dismissedVersion = localStorage.getItem(DISMISSED_VERSION_KEY);
          if (dismissedVersion === result.latest_version) {
            setDismissed(true);
          } else {
            setUpdateAvailable(true);
          }
        } catch {
          setUpdateAvailable(true);
        }
      }
    }).catch(() => {
      // 版本检查失败时静默忽略，不影响用户使用
    });

    return () => controller.abort();
  }, [token, role]);

  if (!updateAvailable || dismissed) return null;

  const handleDismiss = () => {
    setDismissed(true);
    try {
      localStorage.setItem(DISMISSED_VERSION_KEY, latestVersion);
    } catch {
      // ignore
    }
  };

  return (
    <div
      role="status"
      className="bg-primary/10 text-primary text-xs py-1.5 px-4 flex items-center justify-center gap-3"
    >
      <span>
        {t('versionBanner.newVersion', { version: latestVersion })}
        <span className="ml-2 text-primary/70">{t('versionBanner.currentVersion', { version: currentVersion })}</span>
      </span>
      {releaseUrl ? (
        <a
          href={releaseUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 underline underline-offset-2 hover:text-primary/80"
        >
          {t('versionBanner.viewDetails')}
          <ExternalLink className="size-3" />
        </a>
      ) : null}
      <button
        type="button"
        onClick={handleDismiss}
        className="ml-auto shrink-0 rounded p-0.5 hover:bg-primary/10 transition-colors"
        aria-label={t('versionBanner.closeAriaLabel')}
      >
        <X className="size-3.5" />
      </button>
    </div>
  );
}
