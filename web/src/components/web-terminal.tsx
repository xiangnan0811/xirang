import type { FC, FormEvent } from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { ApiError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { ReconnectingSocket } from "@/lib/ws/reconnecting-socket";

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

const TERMINAL_GRANT_REQUIRED_CODE = "CREDENTIAL_GRANT_REQUIRED";
const TERMINAL_GRANT_MAX_REASON_LENGTH = 240;
const TERMINAL_GRANT_TTL_SECONDS = 600;

type PendingGrantRequest = {
  retry: () => void;
  message: string;
};

type WebTerminalProps = {
  nodeId: number;
  token: string;
  onDisconnect?: () => void;
};

function isTerminalGrantClose(event: Pick<CloseEvent, "code" | "reason">): boolean {
  return event.code === 1008 && typeof event.reason === "string" && event.reason.startsWith(`${TERMINAL_GRANT_REQUIRED_CODE}:`);
}

function terminalGrantMessage(reason: string, fallback: string): string {
  const [, detail] = reason.split(":", 2);
  const safeDetail = (detail || "")
    .replace(/[^\p{L}\p{N}_. -]/gu, "")
    .trim()
    .slice(0, 32);
  return safeDetail ? fallback + ` (${safeDetail})` : fallback;
}

const WebTerminal: FC<WebTerminalProps> = ({ nodeId, token, onDisconnect }) => {
  const { t } = useTranslation();
  const { ensureStepUpProof, clearStepUpProof } = useAuth();
  const containerRef = useRef<HTMLDivElement>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const [pendingGrant, setPendingGrant] = useState<PendingGrantRequest | null>(null);
  const [grantReason, setGrantReason] = useState("");
  const [grantError, setGrantError] = useState<string | null>(null);
  const [grantSubmitting, setGrantSubmitting] = useState(false);

  const openGrantDialog = useCallback((message: string) => {
    setPendingGrant({
      message,
      retry: () => setRetryNonce((value) => value + 1),
    });
    setGrantReason("");
    setGrantError(null);
  }, []);

  const closeGrantDialog = useCallback(() => {
    if (grantSubmitting) {
      return;
    }
    setPendingGrant(null);
    setGrantReason("");
    setGrantError(null);
  }, [grantSubmitting]);

  const handleGrantSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!pendingGrant) {
      return;
    }
    const reason = grantReason.trim();
    if (!reason) {
      setGrantError(t("terminal.grantReasonRequired"));
      return;
    }
    if (Array.from(reason).length > TERMINAL_GRANT_MAX_REASON_LENGTH) {
      setGrantError(t("terminal.grantReasonTooLong", { max: TERMINAL_GRANT_MAX_REASON_LENGTH }));
      return;
    }
    setGrantSubmitting(true);
    setGrantError(null);
    try {
      const proof = await ensureStepUpProof(STEP_UP_ACTIONS.terminalOpen);
      await apiClient.requestTerminalCredentialGrant(
        token,
        { nodeId, reason, requestedTtlSeconds: TERMINAL_GRANT_TTL_SECONDS },
        proof,
      );
      const retry = pendingGrant.retry;
      setPendingGrant(null);
      setGrantReason("");
      retry();
    } catch (error) {
      const message = error instanceof ApiError || error instanceof Error
        ? error.message
        : t("terminal.grantRequestFailed");
      setGrantError(message || t("terminal.grantRequestFailed"));
    } finally {
      setGrantSubmitting(false);
    }
  }, [ensureStepUpProof, grantReason, nodeId, pendingGrant, t, token]);

  useEffect(() => {
    if (!containerRef.current) {
      return;
    }

    let active = true;
    let terminal: Terminal | null = null;
    let fitAddon: FitAddon | null = null;
    let socket: ReconnectingSocket | null = null;
    let resizeObserver: ResizeObserver | null = null;
    let sendResize: (() => void) | null = null;
    let stepUpProof = "";

    // 将所有初始化延迟到下一个事件循环，跳过 React StrictMode 的首次 mount→cleanup 循环。
    // StrictMode 的 cleanup 会同步执行并 clearTimeout，因此首次 mount 不会创建任何资源。
    // 这避免了 terminal.open() 抢占焦点→StrictMode dispose→焦点逃逸→Radix Dialog 关闭的问题。
    const timerId = setTimeout(() => {
      void (async () => {
        try {
          stepUpProof = await ensureStepUpProof(STEP_UP_ACTIONS.terminalOpen);
        } catch (error) {
          if (active) {
            const message = error instanceof Error ? error.message : t("terminal.stepUpFailed");
            terminal?.write(`\r\n\x1b[31m${message}\x1b[0m\r\n`);
            onDisconnect?.();
          }
          return;
        }

        if (!active || !containerRef.current) return;

        terminal = new Terminal({
          cursorBlink: true,
          fontFamily: 'Menlo, Monaco, "Courier New", monospace',
          fontSize: 14,
          theme: TERMINAL_PALETTE,
        });

        fitAddon = new FitAddon();
        terminal.loadAddon(fitAddon);
        terminal.open(containerRef.current);
        fitAddon.fit();

        const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
        const wsURL = `${protocol}//${window.location.host}/api/v1/ws/terminal?node_id=${nodeId}`;

        socket = new ReconnectingSocket({
          url: wsURL,
          binaryType: "arraybuffer",
          // SSH PTY 是状态化连接，重连后旧 session 已失效；这里不发心跳避免被旧 session 误识别
          heartbeatIntervalMs: 0,
          onOpen: (ws) => {
            ws.send(JSON.stringify({ type: "auth", token, step_up_proof: stepUpProof }));
          },
          onMessage: (event) => {
            if (event.data instanceof ArrayBuffer) {
              terminal?.write(new Uint8Array(event.data));
            } else if (typeof event.data === "string") {
              terminal?.write(event.data);
            }
          },
          onReconnect: () => {
            // SSH PTY 是状态化连接，重连后旧 session 已失效，必须提示用户重新登录
            terminal?.clear();
            terminal?.write(`\r\n\x1b[33m${t("terminal.reconnected")}\x1b[0m\r\n`);
          },
          onClose: (event) => {
            if (isTerminalGrantClose(event)) {
              socket?.close(1008, "grant-required");
              const message = terminalGrantMessage(event.reason, t("terminal.grantRequired"));
              terminal?.write(`\r\n\x1b[33m${message}\x1b[0m\r\n`);
              if (active) {
                openGrantDialog(message);
              }
              return;
            }
            if (event.code === 1008) {
              clearStepUpProof(STEP_UP_ACTIONS.terminalOpen);
              socket?.close(1008, "step-up-required");
            }
            const detail = event.reason
              ? ` (${event.code}: ${event.reason})`
              : ` (code: ${event.code})`;
            terminal?.write(`\r\n\x1b[31m${t("terminal.disconnected")}${detail}\x1b[0m\r\n`);
            // 正常关闭(1000)或服务端主动关闭(1001)时自动关闭弹窗（如用户输入 exit）
            // 异常关闭保留弹窗以便用户查看错误信息（重连流程会接管）
            if (active && (event.code === 1000 || event.code === 1001)) {
              onDisconnect?.();
            }
          },
          onError: () => {
            terminal?.write(`\r\n\x1b[31m${t("terminal.wsError")}\x1b[0m\r\n`);
          },
          onGiveUp: () => {
            terminal?.write(`\r\n\x1b[31m${t("terminal.giveUp")}\x1b[0m\r\n`);
            if (active) {
              onDisconnect?.();
            }
          },
        });

        socket.connect();

        // 键盘输入 → WebSocket
        terminal.onData((data) => {
          socket?.send(data);
        });

        // 窗口大小变化 → 通知后端
        sendResize = () => {
          fitAddon?.fit();
          socket?.send(
            JSON.stringify({
              type: "resize",
              cols: terminal?.cols ?? 80,
              rows: terminal?.rows ?? 24,
            })
          );
        };

        resizeObserver = new ResizeObserver(() => {
          sendResize?.();
        });

        if (containerRef.current) {
          resizeObserver.observe(containerRef.current);
        }

        window.addEventListener("resize", sendResize);
      })();
    }, 0);

    return () => {
      active = false;
      clearTimeout(timerId);
      if (sendResize) {
        window.removeEventListener("resize", sendResize);
      }
      resizeObserver?.disconnect();
      socket?.close();
      terminal?.dispose();
    };
  }, [clearStepUpProof, ensureStepUpProof, nodeId, onDisconnect, openGrantDialog, retryNonce, t, token]);

  return (
    <>
      <div
        ref={containerRef}
        className="h-full w-full overflow-hidden rounded-md"
        role="region"
        aria-label={t("terminal.ariaLabel")}
        style={{ minHeight: "400px", backgroundColor: TERMINAL_PALETTE.background }}
      />
      <Dialog open={pendingGrant !== null} onOpenChange={(open) => { if (!open) closeGrantDialog(); }}>
        <DialogContent size="sm">
          <form onSubmit={handleGrantSubmit}>
            <DialogHeader>
              <DialogTitle>{t("terminal.grantTitle")}</DialogTitle>
              <DialogDescription>{t("terminal.grantDescription")}</DialogDescription>
            </DialogHeader>
            <DialogBody className="space-y-3">
              {pendingGrant?.message ? (
                <p className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning" role="status">
                  {pendingGrant.message}
                </p>
              ) : null}
              <div className="space-y-1.5">
                <label className="text-sm font-medium" htmlFor="terminal-grant-reason">
                  {t("terminal.grantReasonLabel")}
                </label>
                <Textarea
                  id="terminal-grant-reason"
                  value={grantReason}
                  onChange={(event) => setGrantReason(event.target.value)}
                  maxLength={TERMINAL_GRANT_MAX_REASON_LENGTH}
                  placeholder={t("terminal.grantReasonPlaceholder")}
                  disabled={grantSubmitting}
                  aria-describedby="terminal-grant-reason-hint"
                  aria-invalid={grantError ? true : undefined}
                />
                <p id="terminal-grant-reason-hint" className="text-xs text-muted-foreground">
                  {t("terminal.grantReasonHint", { max: TERMINAL_GRANT_MAX_REASON_LENGTH })}
                </p>
              </div>
              {grantError ? (
                <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive" role="alert">
                  {grantError}
                </p>
              ) : null}
            </DialogBody>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={closeGrantDialog} disabled={grantSubmitting}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" loading={grantSubmitting}>
                {t("terminal.grantSubmit")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
};

export default WebTerminal;
