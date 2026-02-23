import { Input } from "@/components/ui/input";
import type { NodeRecord } from "@/types/domain";

type MobileNodeSearchDrawerProps = {
  open: boolean;
  keyword: string;
  nodes: NodeRecord[];
  onKeywordChange: (value: string) => void;
  onClose: () => void;
  onPickNode: (name: string) => void;
};

export function MobileNodeSearchDrawer({
  open,
  keyword,
  nodes,
  onKeywordChange,
  onClose,
  onPickNode,
}: MobileNodeSearchDrawerProps) {
  return (
    <div
      className="fixed inset-0 z-50 md:hidden"
      style={{ visibility: open ? "visible" : "hidden" }}
      aria-label="移动端节点搜索侧滑面板"
    >
      <button
        className="absolute inset-0 bg-black/45 transition-opacity"
        style={{ opacity: open ? 1 : 0 }}
        onClick={onClose}
        tabIndex={open ? 0 : -1}
      />
      <section
        className="absolute right-0 top-0 h-full w-[86%] border-l border-border/75 bg-background/95 p-4 shadow-panel thin-scrollbar transition-transform"
        style={{ transform: open ? "translateX(0)" : "translateX(100%)" }}
      >
        <h3 className="text-sm font-semibold">侧滑全局搜索</h3>
        <p className="mt-1 text-xs text-muted-foreground">通过名称或 IP 快速定位任意主机</p>
        <Input
          className="mt-3"
          placeholder="搜索主机"
          value={keyword}
          onChange={(event) => onKeywordChange(event.target.value)}
        />
        <div className="mt-3 space-y-2 overflow-auto">
          {nodes.map((node) => (
            <button
              key={node.id}
              onClick={() => {
                onPickNode(node.name);
              }}
              className="w-full rounded-md border px-3 py-2 text-left"
            >
              <p className="text-sm font-medium">{node.name}</p>
              <p className="text-xs text-muted-foreground">
                {node.host}:{node.port}
              </p>
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}
