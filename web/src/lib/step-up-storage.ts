// Keep the action registry in this dependency leaf: API core clears proofs on
// 401, while the TOTP client re-exports these values for its public API.
// Importing the TOTP client here would create core -> storage -> TOTP -> core.
export const STEP_UP_ACTIONS = {
  sshKeyExport: "ssh_key.export",
  terminalOpen: "terminal.open",
  configImport: "config.import",
  configExport: "config.export",
  snapshotRestore: "snapshot.restore",
  taskRestoreTrigger: "task.restore_trigger",
  taskManualTrigger: "task.manual_trigger",
  taskBatchTrigger: "task.batch_trigger",
  batchCommandCreate: "batch_command.create",
  assetSecretReveal: "asset.secret_reveal",
  assetDownload: "asset.download",
  assetExportCreate: "asset.export_create",
  assetExportDownload: "asset.export_download",
  assetRecover: "asset.recover",
  recoveryResultDownload: "recovery.result_download",
  recoveryResultRetain: "recovery.result_retain",
  repositoryPurge: "repository.purge",
  retentionHoldRelease: "retention.hold_release",
} as const;

export type StepUpAction = (typeof STEP_UP_ACTIONS)[keyof typeof STEP_UP_ACTIONS];

export const ALL_STEP_UP_ACTIONS: readonly StepUpAction[] = Object.values(STEP_UP_ACTIONS);

const STEP_UP_PROOFS_V2_KEY = "xirang-step-up-proofs-v2";
const LEGACY_STEP_UP_PROOF_KEY = "xirang-step-up-proof";
const LEGACY_STEP_UP_EXPIRES_AT_KEY = "xirang-step-up-expires-at";

export type StepUpProofState = {
  proof: string;
  expiresAt: number;
};

type StepUpProofMap = Partial<Record<StepUpAction, StepUpProofState>>;

function isKnownStepUpAction(action: string): action is StepUpAction {
  return (ALL_STEP_UP_ACTIONS as readonly string[]).includes(action);
}

function getSessionStorage(): Storage | null {
  if (typeof window === "undefined") {
    return null;
  }
  return window.sessionStorage;
}

function safeGetItem(storage: Storage | null, key: string): string | null {
  try {
    return storage?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

function safeSetItem(storage: Storage | null, key: string, value: string): void {
  try {
    storage?.setItem(key, value);
  } catch {
    // ignore storage failures
  }
}

function safeRemoveItem(storage: Storage | null, key: string): void {
  try {
    storage?.removeItem(key);
  } catch {
    // ignore storage failures
  }
}

function clearLegacyProofs(storage: Storage | null): void {
  safeRemoveItem(storage, LEGACY_STEP_UP_PROOF_KEY);
  safeRemoveItem(storage, LEGACY_STEP_UP_EXPIRES_AT_KEY);
}

function readProofMap(storage: Storage | null): StepUpProofMap {
  clearLegacyProofs(storage);
  const serialized = safeGetItem(storage, STEP_UP_PROOFS_V2_KEY);
  if (!serialized) {
    return {};
  }
  try {
    const raw: unknown = JSON.parse(serialized);
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
      safeRemoveItem(storage, STEP_UP_PROOFS_V2_KEY);
      return {};
    }
    const result: StepUpProofMap = {};
    for (const [action, state] of Object.entries(raw)) {
      if (!isKnownStepUpAction(action) || !state || typeof state !== "object" || Array.isArray(state)) {
        continue;
      }
      const proof = "proof" in state && typeof state.proof === "string" ? state.proof.trim() : "";
      const expiresAt = "expiresAt" in state && typeof state.expiresAt === "number" ? state.expiresAt : Number.NaN;
      if (proof && Number.isFinite(expiresAt)) {
        result[action] = { proof, expiresAt };
      }
    }
    return result;
  } catch {
    safeRemoveItem(storage, STEP_UP_PROOFS_V2_KEY);
    return {};
  }
}

function writeProofMap(storage: Storage | null, proofs: StepUpProofMap): void {
  if (Object.keys(proofs).length === 0) {
    safeRemoveItem(storage, STEP_UP_PROOFS_V2_KEY);
    return;
  }
  safeSetItem(storage, STEP_UP_PROOFS_V2_KEY, JSON.stringify(proofs));
}

export function readStepUpProof(action: StepUpAction, now = Date.now()): StepUpProofState | null {
  const storage = getSessionStorage();
  const proofs = readProofMap(storage);
  const state = proofs[action];
  if (!state || state.expiresAt <= now) {
    if (state) {
      delete proofs[action];
      writeProofMap(storage, proofs);
    }
    return null;
  }
  return state;
}

export function saveStepUpProof(action: StepUpAction, proof: string, expiresAt: number): void {
  const storage = getSessionStorage();
  const proofs = readProofMap(storage);
  const cleanProof = proof.trim();
  if (!cleanProof || !Number.isFinite(expiresAt) || expiresAt <= Date.now()) {
    delete proofs[action];
    writeProofMap(storage, proofs);
    return;
  }
  proofs[action] = { proof: cleanProof, expiresAt: Math.floor(expiresAt) };
  writeProofMap(storage, proofs);
}

export function clearStepUpProof(action?: StepUpAction): void {
  const storage = getSessionStorage();
  clearLegacyProofs(storage);
  if (!action) {
    safeRemoveItem(storage, STEP_UP_PROOFS_V2_KEY);
    return;
  }
  const proofs = readProofMap(storage);
  delete proofs[action];
  writeProofMap(storage, proofs);
}
