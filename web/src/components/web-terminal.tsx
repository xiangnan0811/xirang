import type { FC, FormEvent } from "react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { ApiError, getAuthSessionGeneration, isStepUpRequiredError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { assertStepUpPrerequisite, StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import { cn } from "@/lib/utils";
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

type ConnectionStatus = "idle" | "connecting" | "connected" | "ended" | "failed";
type SummaryTone = "info" | "critical";
type AttemptMode = "initial" | "manual" | "handoff";
type AdmissionKind = "cancel" | "prerequisite" | "failed";

type TerminalSummary = {
  copyText: string;
  tone: SummaryTone;
};

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
};

type AttemptBinding = {
  id: number;
  nodeId: number;
  token: string;
  authGeneration: number;
};

type WebTerminalProps = {
  nodeId: number;
  token: string;
};

type ClosePresentation = {
  status: "ended" | "failed";
  tone: SummaryTone;
  key:
    | "terminal.connectionEnded"
    | "terminal.networkInterrupted"
    | "terminal.nodeUnavailable"
    | "terminal.connectionFailed"
    | "terminal.reverificationRequired"
    | "terminal.connectionEndedGeneric";
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

function closePresentation(code: number): ClosePresentation {
  switch (code) {
    case 1000:
    case 1001:
      return { status: "ended", tone: "info", key: "terminal.connectionEnded" };
    case 1006:
      return { status: "failed", tone: "critical", key: "terminal.networkInterrupted" };
    case 1007:
      return { status: "failed", tone: "critical", key: "terminal.nodeUnavailable" };
    case 1011:
      return { status: "failed", tone: "critical", key: "terminal.connectionFailed" };
    case 1008:
      return { status: "failed", tone: "critical", key: "terminal.reverificationRequired" };
    default:
      return { status: "ended", tone: "info", key: "terminal.connectionEndedGeneric" };
  }
}

const WebTerminal: FC<WebTerminalProps> = ({ nodeId, token }) => {
  const { t } = useTranslation();
  const { ensureStepUpProof, clearStepUpProof, totpEnabled, authTransitioning } = useAuth();
  const authGeneration = getAuthSessionGeneration();
  const containerRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const handoffRef = useRef<TerminalProofHandoff | null>(null);
  const operationRef = useRef<TerminalGrantOperation | null>(null);
  const operationSeqRef = useRef(0);
  const attemptSeqRef = useRef(0);
  const attemptRef = useRef<AttemptBinding | null>(null);
  const attemptLockRef = useRef(false);
  const socketRef = useRef<ReconnectingSocket | null>(null);
  const authSentRef = useRef(false);
  const connectionProofRef = useRef("");
  const mountedRef = useRef(true);
  const sessionAliveRef = useRef(false);
  const terminalReadyRef = useRef(false);
  const initialStartedRef = useRef(false);
  const sessionGenerationRef = useRef(authGeneration);
  const previousIdentityRef = useRef({ nodeId, token });
  const canSendFramesRef = useRef<() => boolean>(() => false);
  const beginAttemptRef = useRef<(mode: AttemptMode) => void>(() => {});
  const suppressGrantDismissRef = useRef(false);
  const resizeRef = useRef<() => void>(() => {});
  const ensureStepUpProofRef = useRef(ensureStepUpProof);
  const clearStepUpProofRef = useRef(clearStepUpProof);
  const totpEnabledRef = useRef(totpEnabled);
  const authTransitioningRef = useRef(authTransitioning);
  const tRef = useRef(t);
  const openGrantDialogRef = useRef<(message: string) => void>(() => {});
  const showSummaryRef = useRef<(tone: SummaryTone, message: string, code?: number) => void>(() => {});
  const statusRef = useRef<ConnectionStatus>("idle");

  const [connectionStatus, setConnectionStatus] = useState<ConnectionStatus>("idle");
  const [summary, setSummary] = useState<TerminalSummary | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);
  const [pendingGrant, setPendingGrant] = useState<PendingGrantRequest | null>(null);
  const [grantReason, setGrantReason] = useState("");
  const [grantError, setGrantError] = useState<string | null>(null);
  const [grantSubmitting, setGrantSubmitting] = useState(false);
  const grantOpenRef = useRef(false);

  const setStatus = (next: ConnectionStatus) => {
    statusRef.current = next;
    setConnectionStatus(next);
  };

  const releaseSockets = () => {
    attemptSeqRef.current += 1;
    attemptRef.current = null;
    attemptLockRef.current = false;
    authSentRef.current = false;
    connectionProofRef.current = "";
    handoffRef.current = null;
    operationRef.current = null;
    const current = socketRef.current;
    socketRef.current = null;
    current?.close();
  };

  const canSendFrames = useCallback(() => {
    const attempt = attemptRef.current;
    return mountedRef.current
      && sessionAliveRef.current
      && statusRef.current === "connected"
      && authSentRef.current
      && totpEnabledRef.current
      && !authTransitioningRef.current
      && attempt !== null
      && attempt.nodeId === nodeId
      && attempt.token === token
      && getAuthSessionGeneration() === attempt.authGeneration;
  }, [nodeId, token]);

  const openGrantDialog = useCallback((message: string) => {
    suppressGrantDismissRef.current = false;
    setPendingGrant({ message });
    setGrantReason("");
    setGrantError(null);
  }, []);

  useLayoutEffect(() => {
    grantOpenRef.current = pendingGrant !== null;
    ensureStepUpProofRef.current = ensureStepUpProof;
    clearStepUpProofRef.current = clearStepUpProof;
    totpEnabledRef.current = totpEnabled;
    authTransitioningRef.current = authTransitioning;
    tRef.current = t;
    openGrantDialogRef.current = openGrantDialog;
    canSendFramesRef.current = canSendFrames;
    showSummaryRef.current = (tone, message, code) => {
      const copyText = typeof code === "number" ? `${message} (${code})` : message;
      const next = tone === "critical" ? "failed" : "ended";
      statusRef.current = next;
      setConnectionStatus(next);
      setSummary({ copyText, tone });
      setCopyFailed(false);
      const color = tone === "critical" ? "31" : "33";
      terminalRef.current?.write(`\r\n\x1b[${color}m${copyText}\x1b[0m\r\n`);
    };
  }, [
    authTransitioning,
    canSendFrames,
    clearStepUpProof,
    ensureStepUpProof,
    openGrantDialog,
    pendingGrant,
    t,
    totpEnabled,
  ]);

  const closeGrantDialog = useCallback(() => {
    if (suppressGrantDismissRef.current) {
      suppressGrantDismissRef.current = false;
      return;
    }
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
    connectionProofRef.current = "";
    const message = tRef.current("terminal.grantCancelled");
    showSummaryRef.current("info", message);
  }, [grantSubmitting]);

  const copySummary = async () => {
    if (!summary) {
      return;
    }
    try {
      await navigator.clipboard.writeText(summary.copyText);
      setCopyFailed(false);
    } catch {
      setCopyFailed(true);
    }
  };

  const handleGrantSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const op = operationRef.current;
    if (!op || !pendingGrant || authTransitioningRef.current || !totpEnabledRef.current) {
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
      && sessionAliveRef.current
      && !authTransitioningRef.current
      && totpEnabledRef.current
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
      assertStepUpPrerequisite(submitToken, totpEnabledRef.current);
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
        assertStepUpPrerequisite(submitToken, totpEnabledRef.current);
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
      suppressGrantDismissRef.current = true;
      setPendingGrant(null);
      setGrantReason("");
      setGrantError(null);
      beginAttemptRef.current("handoff");
    } catch (error) {
      if (error instanceof StepUpPrerequisiteError || !stillCurrent()) {
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
    const nodeIdAtStart = nodeId;
    const tokenAtStart = token;
    let active = true;
    const previousIdentity = previousIdentityRef.current;
    const identityChanged = previousIdentity.nodeId !== nodeId || previousIdentity.token !== token;
    const sessionNotStarted = !initialStartedRef.current;
    previousIdentityRef.current = { nodeId, token };
    sessionAliveRef.current = true;
    suppressGrantDismissRef.current = grantOpenRef.current;
    initialStartedRef.current = false;
    terminalReadyRef.current = false;
    if (identityChanged || sessionNotStarted) {
      setStatus("idle");
      setSummary(null);
      setCopyFailed(false);
      setPendingGrant(null);
      setGrantReason("");
      setGrantError(null);
      setGrantSubmitting(false);
    }

    const isAttemptCurrent = (attemptId: number) => {
      const attempt = attemptRef.current;
      return active
        && sessionAliveRef.current
        && mountedRef.current
        && attempt?.id === attemptId
        && attempt.nodeId === nodeIdAtStart
        && attempt.token === tokenAtStart
        && getAuthSessionGeneration() === attempt.authGeneration
        && !authTransitioningRef.current;
    };

    const beginAttempt = (mode: AttemptMode) => {
      if (!active || !sessionAliveRef.current || !mountedRef.current) {
        return;
      }
      if (attemptLockRef.current) {
        return;
      }
      const generationNow = getAuthSessionGeneration();
      if (mode === "initial" && generationNow !== sessionGenerationRef.current) {
        return;
      }
      if (!totpEnabledRef.current || authTransitioningRef.current) {
        if (mode === "handoff") {
          handoffRef.current = null;
        }
        return;
      }
      try {
        assertStepUpPrerequisite(tokenAtStart, totpEnabledRef.current);
      } catch (error) {
        if (mode === "handoff") {
          handoffRef.current = null;
        }
        if (error instanceof StepUpPrerequisiteError) {
          return;
        }
        showSummaryRef.current("critical", tRef.current("terminal.stepUpFailed"));
        return;
      }

      let consumedProof = "";
      let operationId = 0;
      let grantContinuation = false;
      if (mode === "handoff") {
        const handoff = handoffRef.current;
        const matches = Boolean(
          handoff
          && handoff.nodeId === nodeIdAtStart
          && handoff.token === tokenAtStart
          && handoff.authGeneration === generationNow
          && handoff.proof,
        );
        handoffRef.current = null;
        if (!matches || !handoff) {
          return;
        }
        consumedProof = handoff.proof;
        operationId = handoff.operationId;
        grantContinuation = true;
        operationRef.current = {
          id: operationId,
          nodeId: nodeIdAtStart,
          token: tokenAtStart,
          authGeneration: generationNow,
          proof: consumedProof,
          grantContinuation: true,
        };
      } else {
        handoffRef.current = null;
        operationId = operationSeqRef.current + 1;
        operationSeqRef.current = operationId;
        operationRef.current = {
          id: operationId,
          nodeId: nodeIdAtStart,
          token: tokenAtStart,
          authGeneration: generationNow,
          proof: "",
          grantContinuation: false,
        };
      }

      attemptLockRef.current = true;
      const attemptId = attemptSeqRef.current + 1;
      attemptSeqRef.current = attemptId;
      attemptRef.current = {
        id: attemptId,
        nodeId: nodeIdAtStart,
        token: tokenAtStart,
        authGeneration: generationNow,
      };
      sessionGenerationRef.current = generationNow;
      authSentRef.current = false;
      connectionProofRef.current = "";
      const previous = socketRef.current;
      socketRef.current = null;
      previous?.close();

      if (mode === "manual") {
        terminalRef.current?.write(`\r\n\x1b[33m${tRef.current("terminal.sessionSeparator")}\x1b[0m\r\n`);
      }
      setSummary(null);
      setCopyFailed(false);
      setStatus("connecting");

      let admissionKind: AdmissionKind | null = null;
      let ignoreClose = false;
      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const wsURL = `${protocol}//${window.location.host}/api/v1/ws/terminal?node_id=${nodeIdAtStart}`;
      const socket = new ReconnectingSocket({
        url: wsURL,
        binaryType: "arraybuffer",
        autoReconnect: false,
        heartbeatIntervalMs: 0,
        beforeConnect: async () => {
          if (!isAttemptCurrent(attemptId)) {
            throw new Error("stale-terminal-admission");
          }
          try {
            assertStepUpPrerequisite(tokenAtStart, totpEnabledRef.current);
          } catch (error) {
            if (error instanceof StepUpPrerequisiteError) {
              admissionKind = "prerequisite";
            }
            throw error;
          }
          if (grantContinuation) {
            if (!consumedProof || !isAttemptCurrent(attemptId)) {
              throw new Error("stale-terminal-admission");
            }
            connectionProofRef.current = consumedProof;
            operationRef.current = {
              id: operationId,
              nodeId: nodeIdAtStart,
              token: tokenAtStart,
              authGeneration: generationNow,
              proof: consumedProof,
              grantContinuation: true,
            };
            return;
          }
          clearStepUpProofRef.current(STEP_UP_ACTIONS.terminalOpen);
          try {
            const proof = await ensureStepUpProofRef.current(STEP_UP_ACTIONS.terminalOpen, FRESH_TERMINAL_PROOF);
            if (!isAttemptCurrent(attemptId)) {
              throw new Error("stale-terminal-admission");
            }
            connectionProofRef.current = proof;
            const currentOperation = operationRef.current;
            if (currentOperation?.id === operationId) {
              operationRef.current = { ...currentOperation, proof, grantContinuation: false };
            }
          } catch (error) {
            if (isAttemptCurrent(attemptId)) {
              const message = error instanceof Error ? error.message : "";
              admissionKind = message === tRef.current("stepUp.cancelled") ? "cancel" : "failed";
            }
            throw error instanceof Error ? error : new Error(tRef.current("terminal.stepUpFailed"));
          }
        },
        onOpen: (ws) => {
          if (attemptRef.current?.id !== attemptId) {
            return;
          }
          const proof = connectionProofRef.current;
          if (!isAttemptCurrent(attemptId) || !proof) {
            attemptLockRef.current = false;
            authSentRef.current = false;
            return;
          }
          ws.send(JSON.stringify({ type: "auth", token: tokenAtStart, step_up_proof: proof }));
          authSentRef.current = true;
          attemptLockRef.current = false;
          setSummary(null);
          setStatus("connected");
        },
        onMessage: (messageEvent) => {
          if (!isAttemptCurrent(attemptId)) {
            return;
          }
          if (messageEvent.data instanceof ArrayBuffer) {
            terminalRef.current?.write(new Uint8Array(messageEvent.data));
          } else if (typeof messageEvent.data === "string") {
            terminalRef.current?.write(messageEvent.data);
          }
        },
        onClose: (closeEvent) => {
          if (attemptRef.current?.id !== attemptId || ignoreClose) {
            return;
          }
          authSentRef.current = false;
          attemptLockRef.current = false;
          if (!mountedRef.current || !sessionAliveRef.current) {
            return;
          }
          if (getAuthSessionGeneration() !== attemptRef.current?.authGeneration || authTransitioningRef.current) {
            setStatus("ended");
            return;
          }
          if (isTerminalGrantClose(closeEvent)) {
            ignoreClose = true;
            socket.close(1008, "grant-required");
            const message = terminalGrantMessage(closeEvent.reason, tRef.current("terminal.grantRequired"));
            terminalRef.current?.write(`\r\n\x1b[33m${message}\x1b[0m\r\n`);
            const continued = operationRef.current;
            if (continued?.grantContinuation && continued.id === operationId) {
              connectionProofRef.current = "";
              operationRef.current = { ...continued, proof: "", grantContinuation: false };
              handoffRef.current = null;
              suppressGrantDismissRef.current = false;
              setSummary(null);
              setStatus("ended");
              setPendingGrant({ message });
              setGrantError(message);
              return;
            }
            setSummary(null);
            setStatus("ended");
            openGrantDialogRef.current(message);
            return;
          }
          if (closeEvent.code === 1008) {
            clearStepUpProofRef.current(STEP_UP_ACTIONS.terminalOpen);
            connectionProofRef.current = "";
            const denied = operationRef.current;
            if (denied?.id === operationId) {
              operationRef.current = { ...denied, proof: "" };
            }
            handoffRef.current = null;
            ignoreClose = true;
            socket.close(1008, "step-up-required");
          } else {
            handoffRef.current = null;
            const currentOperation = operationRef.current;
            if (currentOperation?.id === operationId) {
              operationRef.current = { ...currentOperation, proof: "" };
            }
          }
          const presentation = closePresentation(closeEvent.code);
          showSummaryRef.current(presentation.tone, tRef.current(presentation.key), closeEvent.code);
        },
        onGiveUp: () => {
          if (attemptRef.current?.id !== attemptId) {
            return;
          }
          attemptLockRef.current = false;
          authSentRef.current = false;
          connectionProofRef.current = "";
          if (!mountedRef.current || !sessionAliveRef.current) {
            return;
          }
          if (getAuthSessionGeneration() !== attemptRef.current?.authGeneration || authTransitioningRef.current) {
            setStatus("ended");
            return;
          }
          if (admissionKind === "prerequisite") {
            setSummary(null);
            setStatus("idle");
            return;
          }
          if (admissionKind === "cancel") {
            showSummaryRef.current("info", tRef.current("stepUp.cancelled"));
            return;
          }
          showSummaryRef.current("critical", tRef.current("terminal.stepUpFailed"));
        },
      });
      socketRef.current = socket;
      socket.connect();
    };
    beginAttemptRef.current = beginAttempt;

    // 将展示初始化延迟到下一个事件循环，跳过 React StrictMode 的首次 mount→cleanup。
    // StrictMode 的 cleanup 会同步 clearTimeout，因此首次 mount 不会创建终端或 socket。
    const timerId = setTimeout(() => {
      if (!active || !sessionAliveRef.current || !containerRef.current) {
        return;
      }
      const terminal = new Terminal({
        cursorBlink: true,
        fontFamily: 'Menlo, Monaco, "Courier New", monospace',
        fontSize: 14,
        theme: TERMINAL_PALETTE,
      });
      const fitAddon = new FitAddon();
      terminal.loadAddon(fitAddon);
      terminal.open(containerRef.current);
      fitAddon.fit();
      terminalRef.current = terminal;
      fitRef.current = fitAddon;
      terminal.onData((data) => {
        if (!canSendFramesRef.current()) {
          return;
        }
        socketRef.current?.send(data);
      });
      resizeRef.current = () => {
        fitAddon.fit();
        if (!canSendFramesRef.current()) {
          return;
        }
        socketRef.current?.send(JSON.stringify({
          type: "resize",
          cols: terminal.cols ?? 80,
          rows: terminal.rows ?? 24,
        }));
      };
      const resizeObserver = new ResizeObserver(() => {
        resizeRef.current();
      });
      resizeObserver.observe(containerRef.current);
      window.addEventListener("resize", resizeRef.current);
      terminalReadyRef.current = true;
      const observer = resizeObserver;
      const onResize = resizeRef.current;
      sessionCleanup.observer = observer;
      sessionCleanup.onResize = onResize;
      if (!totpEnabledRef.current || authTransitioningRef.current || initialStartedRef.current) {
        return;
      }
      initialStartedRef.current = true;
      beginAttempt("initial");
    }, 0);

    const sessionCleanup: { observer: ResizeObserver | null; onResize: (() => void) | null } = {
      observer: null,
      onResize: null,
    };

    return () => {
      active = false;
      sessionAliveRef.current = false;
      clearTimeout(timerId);
      if (sessionCleanup.onResize) {
        window.removeEventListener("resize", sessionCleanup.onResize);
      }
      sessionCleanup.observer?.disconnect();
      resizeRef.current = () => {};
      beginAttemptRef.current = () => {};
      releaseSockets();
      terminalRef.current?.dispose();
      terminalRef.current = null;
      fitRef.current = null;
      terminalReadyRef.current = false;
    };
  }, [nodeId, token]);

  useEffect(() => {
    if (sessionGenerationRef.current === authGeneration || !sessionAliveRef.current) {
      sessionGenerationRef.current = authGeneration;
      return;
    }
    sessionGenerationRef.current = authGeneration;
    initialStartedRef.current = true;
    releaseSockets();
    if (grantOpenRef.current) {
      suppressGrantDismissRef.current = true;
    }
    setPendingGrant(null);
    setGrantReason("");
    setGrantError(null);
    setGrantSubmitting(false);
    setSummary(null);
    setCopyFailed(false);
    setStatus("ended");
  }, [authGeneration]);

  useEffect(() => {
    if (authTransitioning || !totpEnabled) {
      const activeAttempt = statusRef.current === "connecting" || statusRef.current === "connected" || socketRef.current !== null;
      const shouldAnnounce = authTransitioning && totpEnabled && activeAttempt;
      const shouldEnd = !totpEnabled && (statusRef.current !== "idle" || socketRef.current !== null);
      const grantOpen = grantOpenRef.current;
      releaseSockets();
      if (grantOpen) {
        suppressGrantDismissRef.current = true;
        setPendingGrant(null);
        setGrantReason("");
        setGrantError(null);
        setGrantSubmitting(false);
      }
      if (shouldAnnounce) {
        showSummaryRef.current("info", tRef.current("stepUp.authTransitioning"));
        return;
      }
      if (shouldEnd) {
        setSummary(null);
        setStatus("ended");
      }
      return;
    }
    if (!initialStartedRef.current && terminalReadyRef.current && sessionAliveRef.current && mountedRef.current) {
      initialStartedRef.current = true;
      beginAttemptRef.current("initial");
    }
  }, [authTransitioning, totpEnabled]);

  const showReconnect = totpEnabled
    && !authTransitioning
    && pendingGrant === null
    && (connectionStatus === "connected" || connectionStatus === "ended" || connectionStatus === "failed");
  const showBanner = pendingGrant === null && connectionStatus !== "idle";
  const failed = connectionStatus === "failed" && summary?.tone === "critical";

  return (
    <>
      <div data-connection-status={connectionStatus} className="flex h-full min-h-[400px] flex-col gap-2">
        <StepUpPrerequisiteNotice className="shrink-0" />
        {showBanner && connectionStatus === "connecting" ? (
          <p role="status" className="shrink-0 text-sm text-muted-foreground">{t("terminal.connecting")}</p>
        ) : null}
        {showBanner && connectionStatus !== "connecting" && (summary || showReconnect || connectionStatus === "connected") ? (
          <div
            role={failed ? "alert" : "status"}
            className={cn(
              "shrink-0",
              summary
                ? cn(
                  "rounded-md border px-3 py-2",
                  failed ? "border-destructive/40 bg-destructive/10" : "border-border bg-muted/40",
                )
                : "flex flex-wrap items-center justify-between gap-2",
            )}
          >
            {summary ? (
              <p id="terminal-connection-summary" className={cn("select-text text-sm font-medium", failed ? "text-destructive" : "text-foreground")}>
                {summary.copyText}
              </p>
            ) : connectionStatus === "connected" ? (
              <p className="sr-only">{t("terminal.connected")}</p>
            ) : null}
            {showReconnect ? (
              <p id="terminal-reconnect-hint" className={cn("text-xs text-muted-foreground", summary ? "mt-1" : "")}>
                {t("terminal.reconnectHint")}
              </p>
            ) : null}
            {showReconnect || summary ? (
              <div className={cn("flex flex-wrap gap-2", summary ? "mt-2" : "")}>
                {summary ? (
                  <Button type="button" variant="outline" size="sm" onClick={() => void copySummary()}>
                    {t("terminal.copySummary")}
                  </Button>
                ) : null}
                {showReconnect ? (
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    aria-describedby="terminal-reconnect-hint"
                    onClick={() => beginAttemptRef.current("manual")}
                  >
                    {t("terminal.reconnect")}
                  </Button>
                ) : null}
              </div>
            ) : null}
          </div>
        ) : null}
        {copyFailed ? (
          <p role="alert" className="shrink-0 text-xs text-destructive">{t("terminal.copyFailed")}</p>
        ) : null}
        <div
          ref={containerRef}
          className="min-h-0 w-full flex-1 overflow-hidden rounded-md"
          role="region"
          aria-label={t("terminal.ariaLabel")}
          style={{ backgroundColor: TERMINAL_PALETTE.background }}
        />
      </div>
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
