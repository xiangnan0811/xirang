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
import { ApiError, getAuthSessionGeneration, isStepUpRequiredError } from "@/lib/api/core";
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
const FRESH_TERMINAL_PROOF = { persist: false, reuseCached: false } as const;

type PendingGrantRequest = {
  message: string;
};

type TerminalGrantOperation = {
  id: number;
  nodeId: number;
  token: string;
  authGeneration: number;
  proof: string;
  grantContinuation: boolean;
};

type TerminalProofHandoff = {
  nodeId: number;
  token: string;
  authGeneration: number;
  operationId: number;
  proof: string;
  preserveAcrossRetry: boolean;
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
  const authGeneration = getAuthSessionGeneration();
  const containerRef = useRef<HTMLDivElement>(null);
  const handoffRef = useRef<TerminalProofHandoff | null>(null);
  const operationRef = useRef<TerminalGrantOperation | null>(null);
  const operationSeqRef = useRef(0);
  const mountedRef = useRef(true);
  const grantBindingRef = useRef({ nodeId, token, authGeneration });
  const [retryNonce, setRetryNonce] = useState(0);
  const [pendingGrant, setPendingGrant] = useState<PendingGrantRequest | null>(null);
  const [grantReason, setGrantReason] = useState("");
  const [grantError, setGrantError] = useState<string | null>(null);
  const [grantSubmitting, setGrantSubmitting] = useState(false);

  const openGrantDialog = useCallback((message: string) => {
    setPendingGrant({ message });
    setGrantReason("");
    setGrantError(null);
  }, []);

  // 父页面轮询会重渲染，nodes-page 的 onDisconnect 是内联函数；鉴权 helper 与 t 的身份也可能变。
  // 这些只在连接事件里读取。放进连接 effect 依赖会在授权弹窗仍打开时拆掉 socket 并重新请求 OTP。
  const onDisconnectRef = useRef(onDisconnect);
  const ensureStepUpProofRef = useRef(ensureStepUpProof);
  const clearStepUpProofRef = useRef(clearStepUpProof);
  const openGrantDialogRef = useRef(openGrantDialog);
  const tRef = useRef(t);

  useEffect(() => {
    onDisconnectRef.current = onDisconnect;
    ensureStepUpProofRef.current = ensureStepUpProof;
    clearStepUpProofRef.current = clearStepUpProof;
    openGrantDialogRef.current = openGrantDialog;
    tRef.current = t;
  }, [clearStepUpProof, ensureStepUpProof, onDisconnect, openGrantDialog, t]);

  const closeGrantDialog = useCallback(() => {
    if (grantSubmitting) {
      return;
    }
    setPendingGrant(null);
    setGrantReason("");
    setGrantError(null);
    handoffRef.current = null;
    const currentOperation = operationRef.current;
    if (currentOperation) {
      operationRef.current = { ...currentOperation, proof: "" };
    }
  }, [grantSubmitting]);

  const handleGrantSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const op = operationRef.current;
    if (!op || !pendingGrant) {
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

    let activeOpId = op.id;
    let activeGeneration = op.authGeneration;
    const submitNodeId = op.nodeId;
    const submitToken = op.token;
    const stillCurrent = () =>
      mountedRef.current
      && operationRef.current?.id === activeOpId
      && operationRef.current.nodeId === submitNodeId
      && operationRef.current.token === submitToken
      && operationRef.current.authGeneration === activeGeneration
      && getAuthSessionGeneration() === activeGeneration
      && nodeId === submitNodeId
      && token === submitToken;

    setGrantSubmitting(true);
    setGrantError(null);
    try {
      let proof = op.proof;
      if (!proof) {
        const generationNow = getAuthSessionGeneration();
        if (!mountedRef.current || nodeId !== submitNodeId || token !== submitToken || generationNow !== activeGeneration) {
          return;
        }
        activeOpId = operationSeqRef.current + 1;
        operationSeqRef.current = activeOpId;
        activeGeneration = generationNow;
        operationRef.current = {
          id: activeOpId,
          nodeId: submitNodeId,
          token: submitToken,
          authGeneration: activeGeneration,
          proof: "",
          grantContinuation: false,
        };
        proof = await ensureStepUpProof(STEP_UP_ACTIONS.terminalOpen, FRESH_TERMINAL_PROOF);
        if (!stillCurrent()) {
          return;
        }
        const admitted = operationRef.current;
        if (!admitted) {
          return;
        }
        operationRef.current = { ...admitted, proof };
      }

      const requestGrant = (grantProof: string) => apiClient.requestTerminalCredentialGrant(
        submitToken,
        { nodeId: submitNodeId, reason, requestedTtlSeconds: TERMINAL_GRANT_TTL_SECONDS },
        grantProof,
      );

      try {
        await requestGrant(proof);
      } catch (error) {
        if (!stillCurrent()) {
          return;
        }
        if (!isStepUpRequiredError(error)) {
          throw error;
        }
        const freshProof = await ensureStepUpProof(STEP_UP_ACTIONS.terminalOpen, FRESH_TERMINAL_PROOF);
        if (!stillCurrent()) {
          return;
        }
        proof = freshProof;
        const refreshed = operationRef.current;
        if (!refreshed) {
          return;
        }
        operationRef.current = {
          ...refreshed,
          proof: freshProof,
          grantContinuation: false,
        };
        await requestGrant(freshProof);
      }

      if (!stillCurrent()) {
        return;
      }
      handoffRef.current = {
        nodeId: submitNodeId,
        token: submitToken,
        authGeneration: activeGeneration,
        operationId: activeOpId,
        proof,
        preserveAcrossRetry: true,
      };
      const succeeded = operationRef.current;
      if (!succeeded) {
        return;
      }
      operationRef.current = {
        ...succeeded,
        proof,
        grantContinuation: false,
      };
      setPendingGrant(null);
      setGrantReason("");
      setGrantError(null);
      setRetryNonce((value) => value + 1);
    } catch (error) {
      if (!stillCurrent()) {
        return;
      }
      const failedOperation = operationRef.current;
      if (failedOperation?.id === activeOpId) {
        operationRef.current = { ...failedOperation, proof: "" };
      }
      const message = error instanceof ApiError || error instanceof Error
        ? error.message
        : t("terminal.grantRequestFailed");
      setGrantError(message || t("terminal.grantRequestFailed"));
    } finally {
      if (stillCurrent()) {
        setGrantSubmitting(false);
      }
    }
  }, [ensureStepUpProof, grantReason, nodeId, pendingGrant, t, token]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    const previous = grantBindingRef.current;
    grantBindingRef.current = { nodeId, token, authGeneration };
    if (previous.nodeId === nodeId && previous.token === token && previous.authGeneration === authGeneration) {
      return;
    }
    const generation = getAuthSessionGeneration();
    const handoff = handoffRef.current;
    if (handoff && (handoff.nodeId !== nodeId || handoff.token !== token || handoff.authGeneration !== generation)) {
      handoffRef.current = null;
    }
    const op = operationRef.current;
    if (op && (op.nodeId !== nodeId || op.token !== token || op.authGeneration !== generation)) {
      operationRef.current = null;
    }
    setPendingGrant(null);
    setGrantReason("");
    setGrantError(null);
    setGrantSubmitting(false);
  }, [authGeneration, nodeId, token]);

  useEffect(() => {
    if (!containerRef.current) {
      return;
    }

    const nodeIdAtStart = nodeId;
    const tokenAtStart = token;
    const generationAtStart = authGeneration;
    let active = true;
    let terminal: Terminal | null = null;
    let fitAddon: FitAddon | null = null;
    let socket: ReconnectingSocket | null = null;
    let resizeObserver: ResizeObserver | null = null;
    let sendResize: (() => void) | null = null;
    let connectionProof = "";
    let authSent = false;
    let admissionError: string | null = null;
    let suppressSocketClose = false;

    const existingHandoff = handoffRef.current;
    const handoffMatches = Boolean(
      existingHandoff
      && existingHandoff.nodeId === nodeIdAtStart
      && existingHandoff.token === tokenAtStart
      && existingHandoff.authGeneration === generationAtStart
      && existingHandoff.proof,
    );
    let operationId: number;
    if (handoffMatches && existingHandoff) {
      operationId = existingHandoff.operationId;
      operationRef.current = {
        id: operationId,
        nodeId: nodeIdAtStart,
        token: tokenAtStart,
        authGeneration: generationAtStart,
        proof: existingHandoff.proof,
        grantContinuation: false,
      };
    } else {
      if (handoffRef.current?.nodeId === nodeIdAtStart && handoffRef.current.token === tokenAtStart) {
        handoffRef.current = null;
      }
      operationId = operationSeqRef.current + 1;
      operationSeqRef.current = operationId;
      operationRef.current = {
        id: operationId,
        nodeId: nodeIdAtStart,
        token: tokenAtStart,
        authGeneration: generationAtStart,
        proof: "",
        grantContinuation: false,
      };
    }

    const isCurrent = () =>
      active
      && mountedRef.current
      && operationRef.current?.id === operationId
      && operationRef.current.nodeId === nodeIdAtStart
      && operationRef.current.token === tokenAtStart
      && getAuthSessionGeneration() === generationAtStart;

    // 将所有初始化延迟到下一个事件循环，跳过 React StrictMode 的首次 mount→cleanup 循环。
    // StrictMode 的 cleanup 会同步执行并 clearTimeout，因此首次 mount 不会创建任何资源。
    // 这避免了 terminal.open() 抢占焦点→StrictMode dispose→焦点逃逸→Radix Dialog 关闭的问题。
    const timerId = setTimeout(() => {
      if (!active || !containerRef.current) {
        return;
      }

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
      const wsURL = `${protocol}//${window.location.host}/api/v1/ws/terminal?node_id=${nodeIdAtStart}`;

      const beforeConnect = async () => {
        connectionProof = "";
        authSent = false;
        admissionError = null;
        if (!isCurrent()) {
          throw new Error("stale-terminal-admission");
        }

        const pendingHandoff = handoffRef.current;
        const canConsume = Boolean(
          pendingHandoff
          && pendingHandoff.nodeId === nodeIdAtStart
          && pendingHandoff.token === tokenAtStart
          && pendingHandoff.authGeneration === generationAtStart
          && pendingHandoff.operationId === operationId
          && pendingHandoff.proof
          && !pendingHandoff.preserveAcrossRetry,
        );
        if (canConsume && pendingHandoff) {
          const proof = pendingHandoff.proof;
          handoffRef.current = null;
          if (!isCurrent()) {
            throw new Error("stale-terminal-admission");
          }
          connectionProof = proof;
          operationRef.current = {
            id: operationId,
            nodeId: nodeIdAtStart,
            token: tokenAtStart,
            authGeneration: generationAtStart,
            proof,
            grantContinuation: true,
          };
          return;
        }

        clearStepUpProofRef.current(STEP_UP_ACTIONS.terminalOpen);
        let proof = "";
        try {
          proof = await ensureStepUpProofRef.current(STEP_UP_ACTIONS.terminalOpen, FRESH_TERMINAL_PROOF);
        } catch (error) {
          if (isCurrent()) {
            admissionError = error instanceof Error ? error.message : tRef.current("terminal.stepUpFailed");
          }
          throw error instanceof Error ? error : new Error(tRef.current("terminal.stepUpFailed"));
        }
        if (!isCurrent()) {
          throw new Error("stale-terminal-admission");
        }
        connectionProof = proof;
        operationRef.current = {
          id: operationId,
          nodeId: nodeIdAtStart,
          token: tokenAtStart,
          authGeneration: generationAtStart,
          proof,
          grantContinuation: false,
        };
      };

      socket = new ReconnectingSocket({
        url: wsURL,
        binaryType: "arraybuffer",
        // SSH PTY 是状态化连接，重连后旧 session 已失效；这里不发心跳避免被旧 session 误识别
        heartbeatIntervalMs: 0,
        beforeConnect,
        onOpen: (ws) => {
          if (!isCurrent() || !connectionProof) {
            return;
          }
          ws.send(JSON.stringify({ type: "auth", token: tokenAtStart, step_up_proof: connectionProof }));
          authSent = true;
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
          terminal?.write(`\r\n\x1b[33m${tRef.current("terminal.reconnected")}\x1b[0m\r\n`);
        },
        onClose: (event) => {
          authSent = false;
          if (suppressSocketClose) {
            return;
          }
          if (isTerminalGrantClose(event)) {
            suppressSocketClose = true;
            socket?.close(1008, "grant-required");
            const message = terminalGrantMessage(event.reason, tRef.current("terminal.grantRequired"));
            terminal?.write(`\r\n\x1b[33m${message}\x1b[0m\r\n`);
            if (!isCurrent()) {
              return;
            }
            const continued = operationRef.current;
            if (continued?.grantContinuation) {
              connectionProof = "";
              operationRef.current = { ...continued, proof: "" };
              handoffRef.current = null;
              setPendingGrant({ message });
              setGrantError(message);
              return;
            }
            openGrantDialogRef.current(message);
            return;
          }
          if (event.code === 1008) {
            clearStepUpProofRef.current(STEP_UP_ACTIONS.terminalOpen);
            connectionProof = "";
            const denied = operationRef.current;
            if (denied?.id === operationId) {
              operationRef.current = { ...denied, proof: "" };
            }
            if (handoffRef.current?.operationId === operationId) {
              handoffRef.current = null;
            }
            suppressSocketClose = true;
            socket?.close(1008, "step-up-required");
          }
          const detail = event.reason
            ? ` (${event.code}: ${event.reason})`
            : ` (code: ${event.code})`;
          terminal?.write(`\r\n\x1b[31m${tRef.current("terminal.disconnected")}${detail}\x1b[0m\r\n`);
          // 正常关闭(1000)或服务端主动关闭(1001)时自动关闭弹窗（如用户输入 exit）
          // 异常关闭保留弹窗以便用户查看错误信息（重连流程会接管）
          if (isCurrent() && (event.code === 1000 || event.code === 1001)) {
            onDisconnectRef.current?.();
          }
        },
        onError: () => {
          terminal?.write(`\r\n\x1b[31m${tRef.current("terminal.wsError")}\x1b[0m\r\n`);
        },
        onGiveUp: () => {
          if (!isCurrent()) {
            return;
          }
          const message = admissionError ?? tRef.current("terminal.giveUp");
          admissionError = null;
          terminal?.write(`\r\n\x1b[31m${message}\x1b[0m\r\n`);
          onDisconnectRef.current?.();
        },
      });

      terminal.onData((data) => {
        if (!authSent || !isCurrent()) {
          return;
        }
        socket?.send(data);
      });

      sendResize = () => {
        fitAddon?.fit();
        if (!authSent || !isCurrent()) {
          return;
        }
        socket?.send(JSON.stringify({
          type: "resize",
          cols: terminal?.cols ?? 80,
          rows: terminal?.rows ?? 24,
        }));
      };

      resizeObserver = new ResizeObserver(() => {
        sendResize?.();
      });
      if (containerRef.current) {
        resizeObserver.observe(containerRef.current);
      }
      window.addEventListener("resize", sendResize);
      socket.connect();
    }, 0);

    return () => {
      active = false;
      clearTimeout(timerId);
      if (sendResize) {
        window.removeEventListener("resize", sendResize);
      }
      resizeObserver?.disconnect();
      suppressSocketClose = true;
      socket?.close();
      terminal?.dispose();

      const pendingHandoff = handoffRef.current;
      const preserve = Boolean(
        pendingHandoff
        && pendingHandoff.preserveAcrossRetry
        && pendingHandoff.nodeId === nodeIdAtStart
        && pendingHandoff.token === tokenAtStart
        && pendingHandoff.authGeneration === getAuthSessionGeneration(),
      );
      if (preserve && pendingHandoff) {
        pendingHandoff.preserveAcrossRetry = false;
      } else {
        if (
          handoffRef.current
          && handoffRef.current.nodeId === nodeIdAtStart
          && handoffRef.current.token === tokenAtStart
        ) {
          handoffRef.current = null;
        }
        if (operationRef.current?.id === operationId) {
          operationRef.current = null;
        }
      }
    };
  }, [authGeneration, nodeId, retryNonce, token]);

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
