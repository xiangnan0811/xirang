import { TerminalSquare } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import type { NodeRecord } from "@/types/domain";

type NodeTerminalCardProps = {
  node: NodeRecord;
  command: string;
  timeoutSeconds: number;
  output: string;
  running: boolean;
  onCommandChange: (value: string) => void;
  onTimeoutChange: (value: number) => void;
  onRun: () => void;
  onClose: () => void;
};

export function NodeTerminalCard({
  node,
  command,
  timeoutSeconds,
  output,
  running,
  onCommandChange,
  onTimeoutChange,
  onRun,
  onClose,
}: NodeTerminalCardProps) {
  return (
    <Card className="border-cyan-500/30 bg-gradient-to-br from-cyan-500/10 via-transparent to-transparent">
      <CardHeader>
        <div className="flex items-center justify-between gap-2">
          <CardTitle className="text-base">节点终端（真实 SSH 命令执行）</CardTitle>
          <Button variant="outline" size="sm" onClick={onClose}>
            关闭
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          当前节点：{node.name} · {node.host}:{node.port}
        </p>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="grid gap-2 md:grid-cols-[1fr_auto_auto]">
          <Input
            value={command}
            onChange={(event) => onCommandChange(event.target.value)}
            placeholder="输入命令，例如 df -h /"
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                onRun();
              }
            }}
          />
          <select
            className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
            value={timeoutSeconds}
            onChange={(event) => onTimeoutChange(Number(event.target.value || 20))}
          >
            <option value={10}>超时 10s</option>
            <option value={20}>超时 20s</option>
            <option value={30}>超时 30s</option>
            <option value={60}>超时 60s</option>
          </select>
          <Button onClick={onRun} disabled={running}>
            <TerminalSquare className="mr-1 size-4" />
            {running ? "执行中..." : "执行命令"}
          </Button>
        </div>

        <div className="terminal-surface min-h-52 overflow-auto rounded-lg p-3 font-mono text-xs text-slate-100 thin-scrollbar">
          <pre className="whitespace-pre-wrap break-all">{output || "等待命令执行输出..."}</pre>
        </div>
      </CardContent>
    </Card>
  );
}

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

