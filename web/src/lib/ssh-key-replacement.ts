import { isStepUpRequiredError } from "@/lib/api/core";

/**
 * Ownership for one explicit SSH key replacement submission. The caller owns the
 * signal and currentness; `requestProof` must obtain a fresh, uncached
 * `ssh_key.replace` proof bound to that same signal.
 */
export type SSHKeyReplacementOptions = {
  signal: AbortSignal;
  isCurrent: () => boolean;
  requestProof: () => Promise<string>;
};

/**
 * Proof acquisition failed or was cancelled after a real challenge. No second
 * submission was sent, so this is never an unknown write outcome. `cause` holds
 * the acquisition error; the message never carries the candidate or a proof.
 */
export class SSHKeyReplacementProofError extends Error {
  constructor(cause: unknown) {
    super("SSH key replacement step-up proof was not obtained", { cause });
    this.name = "SSHKeyReplacementProofError";
  }
}

function assertCurrent(options: SSHKeyReplacementOptions): void {
  if (options.signal.aborted || !options.isCurrent()) {
    throw new DOMException("SSH key replacement no longer belongs to its owner", "AbortError");
  }
}

/**
 * Submits once without a proof. Only a real HTTP 403 `STEP_UP_REQUIRED` leads to
 * one proof request and exactly one resubmission; a second challenge, an HTTP 2xx
 * failure envelope, and every other failure return to the caller unchanged.
 * Losing ownership at any await throws `AbortError`.
 */
export async function runSSHKeyReplacement<T>(
  operation: (proof?: string) => Promise<T>,
  options: SSHKeyReplacementOptions,
): Promise<T> {
  assertCurrent(options);
  try {
    const result = await operation();
    assertCurrent(options);
    return result;
  } catch (error) {
    assertCurrent(options);
    if (!isStepUpRequiredError(error) || error.httpStatus !== 403) {
      throw error;
    }
  }
  let proof: string;
  try {
    proof = await options.requestProof();
  } catch (error) {
    assertCurrent(options);
    throw new SSHKeyReplacementProofError(error);
  }
  assertCurrent(options);

  try {
    const result = await operation(proof);
    assertCurrent(options);
    return result;
  } catch (error) {
    assertCurrent(options);
    throw error;
  }
}
