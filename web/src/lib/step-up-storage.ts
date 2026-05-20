const STEP_UP_PROOF_KEY = "xirang-step-up-proof";
const STEP_UP_EXPIRES_AT_KEY = "xirang-step-up-expires-at";

export type StepUpProofState = {
  proof: string;
  expiresAt: number;
};

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

export function readStepUpProof(now = Date.now()): StepUpProofState | null {
  const storage = getSessionStorage();
  const proof = safeGetItem(storage, STEP_UP_PROOF_KEY)?.trim() ?? "";
  const rawExpiresAt = safeGetItem(storage, STEP_UP_EXPIRES_AT_KEY);
  const expiresAt = rawExpiresAt ? Number.parseInt(rawExpiresAt, 10) : Number.NaN;
  if (!proof || !Number.isFinite(expiresAt) || expiresAt <= now) {
    clearStepUpProof();
    return null;
  }
  return { proof, expiresAt };
}

export function saveStepUpProof(proof: string, expiresAt: number): void {
  const cleanProof = proof.trim();
  if (!cleanProof || !Number.isFinite(expiresAt) || expiresAt <= Date.now()) {
    clearStepUpProof();
    return;
  }
  const storage = getSessionStorage();
  safeSetItem(storage, STEP_UP_PROOF_KEY, cleanProof);
  safeSetItem(storage, STEP_UP_EXPIRES_AT_KEY, String(Math.floor(expiresAt)));
}

export function clearStepUpProof(): void {
  const storage = getSessionStorage();
  safeRemoveItem(storage, STEP_UP_PROOF_KEY);
  safeRemoveItem(storage, STEP_UP_EXPIRES_AT_KEY);
}
