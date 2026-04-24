import type { FC } from "react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

// Terminal color palette is intentionally decoupled from the Xirang site
// theme. Two reasons:
//   1. The 16 ANSI colors (red/green/yellow/blue/…) are a protocol — remote
//      scripts emit `\e[31m` expecting "red", and the terminal must render
//      them consistently regardless of OS light/dark preference. Changing
//      these across themes would break muscle memory and script output.
//   2. Every popular terminal app (VS Code integrated terminal, iTerm,
//      GitHub Codespaces web IDE, Termius) keeps the terminal pane dark by
//      default even under a light OS chrome, matching operator expectation.
//
// If a future release wants a user-selectable "light terminal" option, add
// it as an explicit preference, not by routing through the site theme.
const TERMINAL_PALETTE = {
  background: "#0d1117",
  foreground: "#c9d1d9",
  cursor: "#c9d1d9",
  black: "#0d1117",
  red: "#ff7b72",
  green: "#3fb950",
  yellow: "#d29922",
  blue: "#58a6ff",
  magenta: "#bc8cff",
  cyan: "#39c5cf",
  white: "#b1bac4",
  brightBlack: "#6e7681",
  brightRed: "#ffa198",
  brightGreen: "#56d364",
  brightYellow: "#e3b341",
  brightBlue: "#79c0ff",
  brightMagenta: "#d2a8ff",
  brightCyan: "#56d4dd",
  brightWhite: "#f0f6fc",
} as const;

type WebTerminalProps = {
  nodeId: number;
  token: string;
  onDisconnect?: () => void;
};

const WebTerminal: FC<WebTerminalProps> = ({ nodeId, token, onDisconnect }) => {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!containerRef.current) {
      return;
    }

    let active = true;
    let terminal: Terminal | null = null;
    let fitAddon: FitAddon | null = null;
    let ws: WebSocket | null = null;
    let resizeObserver: ResizeObserver | null = null;
    let sendResize: (() => void) | null = null;

    // 将所有初始化延迟到下一个事件循环，跳过 React StrictMode 的首次 mount→cleanup 循环。
    // StrictMode 的 cleanup 会同步执行并 clearTimeout，因此首次 mount 不会创建任何资源。
    // 这避免了 terminal.open() 抢占焦点→StrictMode dispose→焦点逃逸→Radix Dialog 关闭的问题。
    const timerId = setTimeout(() => {
      if (!active || !containerRef.current) return;

      terminal = new Terminal({
        cursorBlink: true,
        fontFamily: 'Menlo, Monaco, "Courier New", monospace',
        fontSize: 14,
        theme: TERMINAL_PALETTE,
      });

      fitAddon = new FitAddon();
      terminal.loadAddon(fitAddon);
      terminal.open(containerRef.current!);
      fitAddon.fit();

      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const wsURL = `${protocol}//${window.location.host}/api/v1/ws/terminal?node_id=${nodeId}`;
      ws = new WebSocket(wsURL);
      ws.binaryType = "arraybuffer";

      ws.onopen = () => {
        ws!.send(JSON.stringify({ type: "auth", token }));
      };

      ws.onmessage = (event) => {
        if (event.data instanceof ArrayBuffer) {
          terminal!.write(new Uint8Array(event.data));
        } else if (typeof event.data === "string") {
          terminal!.write(event.data);
        }
      };

      ws.onclose = (event) => {
        const detail = event.reason ? ` (${event.code}: ${event.reason})` : ` (code: ${event.code})`;
        terminal?.write(`\r\n\x1b[31m${t("terminal.disconnected")}${detail}\x1b[0m\r\n`);
        // 正常关闭(1000)或服务端主动关闭(1001)时自动关闭弹窗（如用户输入 exit）
        // 异常关闭保留弹窗以便用户查看错误信息
        if (active && (event.code === 1000 || event.code === 1001)) {
          onDisconnect?.();
        }
      };

      ws.onerror = () => {
        terminal?.write(`\r\n\x1b[31m${t("terminal.wsError")}\x1b[0m\r\n`);
      };

      // 键盘输入 → WebSocket
      terminal.onData((data) => {
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(data);
        }
      });

      // 窗口大小变化 → 通知后端
      sendResize = () => {
        fitAddon!.fit();
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(
            JSON.stringify({
              type: "resize",
              cols: terminal!.cols,
              rows: terminal!.rows,
            })
          );
        }
      };

      resizeObserver = new ResizeObserver(() => {
        sendResize!();
      });

      if (containerRef.current) {
        resizeObserver.observe(containerRef.current);
      }

      window.addEventListener("resize", sendResize);
    }, 0);

    return () => {
      active = false;
      clearTimeout(timerId);
      if (sendResize) {
        window.removeEventListener("resize", sendResize);
      }
      resizeObserver?.disconnect();
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
        ws.close();
      }
      terminal?.dispose();
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodeId, token]);

  return (
    <div
      ref={containerRef}
      className="h-full w-full overflow-hidden rounded-md"
      style={{ minHeight: "400px", backgroundColor: TERMINAL_PALETTE.background }}
    />
  );
};

export default WebTerminal;
