import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";
import type { ComponentType, RefObject } from "react";
// react-grid-layout v2 exports ResponsiveGridLayout + useContainerWidth.
// v2 removed the auto WidthProvider — callers must measure the container
// and pass `width` explicitly, so we use the official useContainerWidth hook.
// @types/react-grid-layout is v1 and has a conflicting namespace; cast to
// local types below.
import {
  ResponsiveGridLayout as RawResponsiveGridLayout,
  useContainerWidth as rawUseContainerWidth,
} from "react-grid-layout";
import type { Panel } from "@/types/domain";

const ResponsiveGridLayout =
  RawResponsiveGridLayout as unknown as ComponentType<ResponsiveProps>;

const useContainerWidth = rawUseContainerWidth as unknown as (
  options?: { debounceMs?: number },
) => {
  width: number;
  containerRef: RefObject<HTMLDivElement | null>;
  mounted: boolean;
};

// ─── 本地类型定义（避免引用 @types/react-grid-layout v1 命名空间） ──

type RGLLayout = {
  i: string;
  x: number;
  y: number;
  w: number;
  h: number;
  minW?: number;
  minH?: number;
  static?: boolean;
  isDraggable?: boolean;
  isResizable?: boolean;
};

type ResponsiveProps = {
  className?: string;
  layouts?: Record<string, RGLLayout[]>;
  breakpoints?: Record<string, number>;
  cols?: Record<string, number>;
  rowHeight?: number;
  compactType?: "vertical" | "horizontal" | null;
  isDraggable?: boolean;
  isResizable?: boolean;
  onLayoutChange?: (currentLayout: RGLLayout[], allLayouts: Record<string, RGLLayout[]>) => void;
  draggableHandle?: string;
  // v2 requires explicit container width; no auto WidthProvider.
  width: number;
  margin?: readonly [number, number];
  containerPadding?: readonly [number, number];
  children?: React.ReactNode;
};

// ─── 公共类型 ────────────────────────────────────────────────────

export type LayoutItem = {
  id: number;
  layout_x: number;
  layout_y: number;
  layout_w: number;
  layout_h: number;
};

type PanelGridProps = {
  panels: Panel[];
  editMode: boolean;
  onLayoutChange: (items: LayoutItem[]) => void;
  children: (panel: Panel) => React.ReactNode;
};

function panelToLayout(panel: Panel): RGLLayout {
  return {
    i: String(panel.id),
    x: panel.layout_x,
    y: panel.layout_y,
    w: panel.layout_w,
    h: panel.layout_h,
    minW: 2,
    minH: 2,
  };
}

export function PanelGrid({
  panels,
  editMode,
  onLayoutChange,
  children,
}: PanelGridProps) {
  const { width, containerRef } = useContainerWidth();
  // Cast to RefObject<HTMLDivElement> — useContainerWidth returns a nullable
  // ref, but React 18's JSX ref prop accepts that shape at runtime.
  const divRef = containerRef as unknown as RefObject<HTMLDivElement>;

  const layouts: Record<string, RGLLayout[]> = {
    lg: panels.map(panelToLayout),
  };

  function handleLayoutChange(currentLayout: RGLLayout[]) {
    const items: LayoutItem[] = currentLayout.map((l) => ({
      id: Number(l.i),
      layout_x: l.x,
      layout_y: l.y,
      layout_w: l.w,
      layout_h: l.h,
    }));
    onLayoutChange(items);
  }

  return (
    <div ref={divRef} className="w-full">
      {width > 0 ? (
        <ResponsiveGridLayout
          className="layout"
          width={width}
          layouts={layouts}
          breakpoints={{ lg: 1200, md: 996, sm: 768, xs: 480, xxs: 0 }}
          cols={{ lg: 12, md: 10, sm: 6, xs: 4, xxs: 2 }}
          rowHeight={60}
          margin={[12, 12]}
          containerPadding={[0, 0]}
          compactType="vertical"
          isDraggable={editMode}
          isResizable={editMode}
          onLayoutChange={handleLayoutChange}
          draggableHandle=".drag-handle"
        >
          {panels.map((panel) => (
            <div key={String(panel.id)}>{children(panel)}</div>
          ))}
        </ResponsiveGridLayout>
      ) : (
        // Fallback when container width is unknown (e.g. jsdom test environment
        // or pre-measure render): stack panels vertically so children still
        // mount — useContainerWidth will re-render with a real width shortly.
        <div className="flex flex-col gap-3">
          {panels.map((panel) => (
            <div key={String(panel.id)} className="min-h-[240px]">
              {children(panel)}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
