import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";
import type { ComponentType } from "react";
// react-grid-layout v2 exports ResponsiveGridLayout as a named ESM export.
// @types/react-grid-layout is for v1 and has a conflicting namespace; we
// import as unknown and cast to our local ResponsiveProps type below.
import { ResponsiveGridLayout as RawResponsiveGridLayout } from "react-grid-layout";
import type { Panel } from "@/types/domain";

const ResponsiveGridLayout =
  RawResponsiveGridLayout as unknown as ComponentType<ResponsiveProps>;

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
    <ResponsiveGridLayout
      className="layout"
      layouts={layouts}
      breakpoints={{ lg: 1200, md: 996, sm: 768, xs: 480, xxs: 0 }}
      cols={{ lg: 12, md: 10, sm: 6, xs: 4, xxs: 2 }}
      rowHeight={60}
      compactType="vertical"
      isDraggable={editMode}
      isResizable={editMode}
      onLayoutChange={handleLayoutChange}
      draggableHandle=".drag-handle"
    >
      {panels.map((panel) => (
        <div key={String(panel.id)}>
          {children(panel)}
        </div>
      ))}
    </ResponsiveGridLayout>
  );
}
