import { useCallback, useLayoutEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Toaster as SonnerToaster } from "sonner";
import { useTheme } from "@/context/theme-context.hooks";
import { observeToastAnnouncements } from "./toast-announcements";

export function Toaster() {
  const { theme } = useTheme();
  const { t } = useTranslation();
  const visualRef = useRef<HTMLElement | null>(null);
  const announcementRef = useRef<HTMLDivElement>(null);
  const setVisualRegion = useCallback((node: HTMLElement | null) => {
    visualRef.current = node;
    node?.setAttribute("aria-live", "off");
  }, []);

  useLayoutEffect(() => {
    if (visualRef.current && announcementRef.current) {
      return observeToastAnnouncements(visualRef.current, announcementRef.current);
    }
  }, []);

  return (
    <>
    <SonnerToaster
      ref={setVisualRegion}
      position="bottom-right"
      offset={16}
      expand
      richColors
      closeButton
      visibleToasts={5}
      theme={theme}
      customAriaLabel={t("common.toastNotifications")}
      toastOptions={{
        duration: 4200,
        closeButtonAriaLabel: t("common.dismissNotification"),
        classNames: {
          toast:
            "group rounded-lg border border-l-4 border-border shadow-lg",
          title: "text-sm font-semibold tracking-wide !text-foreground",
          description: "text-xs !text-foreground",
          actionButton: "bg-primary text-primary-foreground",
          cancelButton: "bg-muted text-muted-foreground",
          default: "border-l-border",
          error: "border-l-destructive",
          success: "border-l-success",
          warning: "border-l-warning",
          info: "border-l-info",
        },
      }}
    />
      <div
        ref={announcementRef}
        role="status"
        aria-live="polite"
        aria-atomic="true"
        aria-relevant="additions text"
        className="sr-only"
        data-toast-announcer=""
      />
    </>
  );
}
