/* ARIA separator is intentionally focusable and interactive per the WAI-ARIA Window Splitter pattern. */
/* eslint-disable jsx-a11y/no-noninteractive-element-interactions */
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { ArrowLeft, Maximize2, Minimize2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

const INITIAL_BROWSER_RATIO = 42;
const BROWSER_MIN_REM = 20;
const PREVIEW_MIN_REM = 30;
const SEPARATOR_PX = 8;

export interface BackupFileSplitPaneProps {
  browser: ReactNode;
  preview: ReactNode;
  previewActive: boolean;
  onBack: () => void;
  onSequentialChange?: (sequential: boolean) => void;
}

export function BackupFileSplitPane({ browser, preview, previewActive, onBack, onSequentialChange }: BackupFileSplitPaneProps) {
  const { t } = useTranslation();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const separatorRef = useRef<HTMLDivElement | null>(null);
  const dragPointerRef = useRef<number | null>(null);
  const focusTriggerRef = useRef<HTMLButtonElement | null>(null);
  const exitFocusRef = useRef<HTMLButtonElement | null>(null);
  const priorRatioRef = useRef(INITIAL_BROWSER_RATIO);
  const [width, setWidth] = useState(0);
  const [browserRatio, setBrowserRatio] = useState(INITIAL_BROWSER_RATIO);
  const [dragging, setDragging] = useState(false);
  const [focused, setFocused] = useState(false);

  useLayoutEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const measure = () => {
      const measured = root.getBoundingClientRect().width;
      const nextWidth = Number.isFinite(measured) && measured > 0 ? measured : 0;
      setWidth(nextWidth);
      if (nextWidth >= minimumCombinedWidth()) {
        setBrowserRatio((current) => clamp(current, ...ratioLimits(nextWidth)));
      }
    };
    measure();
    if (typeof ResizeObserver === "undefined") {
      window.addEventListener("resize", measure);
      return () => window.removeEventListener("resize", measure);
    }
    const observer = new ResizeObserver(measure);
    observer.observe(root);
    return () => observer.disconnect();
  }, []);

  const limits = ratioLimits(width);
  const measured = width > 0;
  const sequential = measured && width < minimumCombinedWidth();

  useEffect(() => {
    if (measured) onSequentialChange?.(sequential);
  }, [measured, onSequentialChange, sequential]);

  useLayoutEffect(() => {
    if (focused) exitFocusRef.current?.focus();
  }, [focused]);

  useEffect(() => {
    if (!dragging) return;
    const move = (event: PointerEvent) => {
      if (dragPointerRef.current !== null && Number.isFinite(event.pointerId) && event.pointerId !== dragPointerRef.current) return;
      const root = rootRef.current;
      if (!root || !Number.isFinite(event.clientX)) return;
      const rect = root.getBoundingClientRect();
      const availableWidth = Math.max(1, rect.width - SEPARATOR_PX);
      setBrowserRatio(clamp(((event.clientX - rect.left - SEPARATOR_PX / 2) / availableWidth) * 100, ...ratioLimits(rect.width)));
    };
    const stop = (event: PointerEvent) => {
      if (dragPointerRef.current !== null && Number.isFinite(event.pointerId) && event.pointerId !== dragPointerRef.current) return;
      const pointerId = dragPointerRef.current;
      dragPointerRef.current = null;
      if (pointerId !== null) {
        try {
          separatorRef.current?.releasePointerCapture?.(pointerId);
        } catch {
          // Pointer capture may already be released by the browser on pointerup/cancel.
        }
      }
      setDragging(false);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop);
    window.addEventListener("pointercancel", stop);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
      window.removeEventListener("pointercancel", stop);
    };
  }, [dragging]);

  const exitFocused = () => {
    setBrowserRatio(priorRatioRef.current);
    setFocused(false);
    focusTriggerRef.current?.focus();
    requestAnimationFrame(() => focusTriggerRef.current?.focus());
  };

  return (
    <div ref={rootRef} className="flex min-h-0 flex-1 flex-col overflow-hidden" data-layout={!measured ? "pending" : sequential ? "sequential" : "split"}>
      <div className="flex min-h-11 shrink-0 items-center justify-end gap-1 border-b border-border bg-muted/20 px-2 lg:min-h-10">
        <Button
          ref={focusTriggerRef}
          type="button"
          variant="ghost"
          size="sm"
          disabled={!previewActive}
          hidden={focused}
          tabIndex={focused ? -1 : 0}
          className="touch-target min-h-11 gap-2 lg:min-h-8"
          onClick={() => {
            priorRatioRef.current = browserRatio;
            setFocused(true);
          }}
        >
          <Maximize2 className="size-4" aria-hidden />
          {t("backupAssets.splitPane.focus")}
        </Button>
        {focused ? (
          <Button ref={exitFocusRef} type="button" variant="ghost" size="sm" className="touch-target min-h-11 gap-2 lg:min-h-8" onClick={exitFocused}>
            <Minimize2 className="size-4" aria-hidden />
            {t("backupAssets.splitPane.exitFocus")}
          </Button>
        ) : null}
      </div>

      {!measured ? <div className="min-h-0 flex-1 overflow-hidden">{browser}</div> : sequential ? (
        previewActive ? (
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <div className="shrink-0 border-b border-border p-2">
              <Button type="button" variant="ghost" size="sm" className="touch-target min-h-11 gap-2 lg:min-h-8" onClick={onBack}>
                <ArrowLeft className="size-4" aria-hidden />
                {t("backupAssets.splitPane.back")}
              </Button>
            </div>
            <div className="min-h-0 flex-1 overflow-hidden">{preview}</div>
          </div>
        ) : <div className="min-h-0 flex-1 overflow-hidden">{browser}</div>
      ) : (
        <div
          className="grid min-h-0 flex-1 overflow-hidden"
          style={{
            gridTemplateColumns: focused
              ? "minmax(0, 1fr)"
              : `minmax(20rem, ${browserRatio}fr) ${SEPARATOR_PX}px minmax(30rem, ${100 - browserRatio}fr)`,
          }}
        >
          <div hidden={focused} className="min-h-0 min-w-0 overflow-hidden">{browser}</div>
          <div
            ref={separatorRef}
            hidden={focused}
            role="separator"
            aria-label={t("backupAssets.splitPane.resize")}
            aria-orientation="vertical"
            aria-valuemin={Math.round(limits[0])}
            aria-valuemax={Math.round(limits[1])}
            aria-valuenow={Math.round(browserRatio)}
            tabIndex={focused ? -1 : 0}
            className="relative cursor-col-resize bg-border outline-none after:absolute after:inset-y-0 after:left-1/2 after:w-px after:-translate-x-1/2 after:bg-muted-foreground/30 hover:bg-primary/25 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
            onPointerDown={(event) => {
              dragPointerRef.current = event.pointerId;
              event.currentTarget.setPointerCapture?.(event.pointerId);
              setDragging(true);
            }}
            onLostPointerCapture={() => {
              dragPointerRef.current = null;
              setDragging(false);
            }}
            onKeyDown={(event) => {
              if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
              event.preventDefault();
              setBrowserRatio((current) => clamp(current + (event.key === "ArrowRight" ? 2 : -2), ...limits));
            }}
          />
          <div className="min-h-0 min-w-0 overflow-hidden">{preview}</div>
        </div>
      )}
    </div>
  );
}

function minimumCombinedWidth(): number {
  const rootFontSize = typeof window === "undefined"
    ? 16
    : Number.parseFloat(window.getComputedStyle(document.documentElement).fontSize) || 16;
  return (BROWSER_MIN_REM + PREVIEW_MIN_REM) * rootFontSize + SEPARATOR_PX;
}

function ratioLimits(width: number): [number, number] {
  if (width < minimumCombinedWidth()) return [0, 100];
  const rootFontSize = typeof window === "undefined"
    ? 16
    : Number.parseFloat(window.getComputedStyle(document.documentElement).fontSize) || 16;
  const availableWidth = width - SEPARATOR_PX;
  return [
    (BROWSER_MIN_REM * rootFontSize / availableWidth) * 100,
    100 - (PREVIEW_MIN_REM * rootFontSize / availableWidth) * 100,
  ];
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value));
}
