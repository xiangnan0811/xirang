import type {
  LoginResponse,
  RecoveryEnrollmentLoginResponse,
  StandardLoginResponse,
} from "@/types/domain";
import i18n from "@/i18n";
import { ApiError, AuthTransitionRejectedError, request } from "./core";
import { finiteNumber } from "./number-utils";

export type CaptchaResponse = {
  /** True when login.captcha_enabled — only then are id/question present. */
  enabled: boolean;
  id?: string;
  question?: string;
  secondRequired?: boolean;
  secondId?: string;
  secondQuestion?: string;
};

type RawCaptchaResponse = {
  enabled?: unknown;
  id?: unknown;
  question?: unknown;
  second_required?: unknown;
  second_id?: unknown;
  second_question?: unknown;
};

type RawLoginResponse = {
  token?: unknown;
  user?: {
    id?: unknown;
    username?: unknown;
    role?: unknown;
    totp_enabled?: unknown;
  };
  requires_2fa?: unknown;
  login_token?: unknown;
};

function mapLoginRole(raw: unknown): "admin" | "operator" | "viewer" {
  return raw === "admin" || raw === "operator" || raw === "viewer" ? raw : "viewer";
}

export function mapLoginResponse(raw: RawLoginResponse): StandardLoginResponse {
  return {
    token: typeof raw.token === "string" ? raw.token : undefined,
    user: raw.user
      ? {
          id: finiteNumber(raw.user.id),
          username: String(raw.user.username ?? ""),
          role: mapLoginRole(raw.user.role),
          totpEnabled: Boolean(raw.user.totp_enabled),
        }
      : undefined,
    requires2FA: Boolean(raw.requires_2fa),
    loginToken: typeof raw.login_token === "string" ? raw.login_token : undefined,
  };
}

// Any of these keys moves a login response onto the strict recovery branch; a response that
// mixes them with ordinary session or pending-2FA fields is rejected instead of guessed at.
const RECOVERY_LOGIN_KEYS = ["requires_reenrollment", "recovery_token", "enrollment"] as const;
const RECOVERY_ENROLLMENT_KEYS = ["secret", "qr_url", "issuer", "enrollment_id", "expires_at"] as const;
const RECOVERY_CODES_KEYS = ["recovery_codes"] as const;
export const RECOVERY_CODE_COUNT = 8;

const JWT_COMPACT_PATTERN = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;
const BASE32_SECRET_PATTERN = /^[A-Z2-7]+=*$/;
const PRINTABLE_TOKEN_PATTERN = /^[\x21-\x7e]+$/;
const RFC3339_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|([+-])(\d{2}):(\d{2}))$/;
const RECOVERY_VERIFY_CODE_PATTERN = /^\d{6}$/;

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasExactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const own = Object.keys(value);
  return own.length === keys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(value, key));
}

/** Strict RFC3339 (Go time.Time JSON) to epoch milliseconds; null for any invalid instant. */
export function parseRFC3339(value: string): number | null {
  const match = RFC3339_PATTERN.exec(value);
  if (!match) return null;
  const [, year, month, day, hour, minute, second, fraction = "", zone, sign, offsetHour, offsetMinute] = match;
  const y = Number(year);
  const mo = Number(month);
  const d = Number(day);
  const h = Number(hour);
  const mi = Number(minute);
  const s = Number(second);
  if (y < 1970 || mo < 1 || mo > 12 || d < 1 || h > 23 || mi > 59 || s > 59) return null;
  const daysInMonth = new Date(Date.UTC(y, mo, 0)).getUTCDate();
  if (d > daysInMonth) return null;
  let offsetMs = 0;
  if (zone !== "Z") {
    const oh = Number(offsetHour);
    const om = Number(offsetMinute);
    if (oh > 23 || om > 59) return null;
    offsetMs = (sign === "-" ? -1 : 1) * (oh * 60 + om) * 60_000;
  }
  const ms = Date.UTC(y, mo - 1, d, h, mi, s, Number(`${fraction}000`.slice(0, 3))) - offsetMs;
  return Number.isFinite(ms) ? ms : null;
}

function isTOTPProvisioningUrl(qrUrl: string, secret: string): boolean {
  let url: URL;
  try {
    url = new URL(qrUrl);
  } catch {
    return false;
  }
  // The QR must provision the same secret that is shown for manual entry.
  return url.protocol === "otpauth:" && url.host === "totp" && url.searchParams.get("secret") === secret;
}

function mapRecoveryEnrollmentLogin(raw: Record<string, unknown>): RecoveryEnrollmentLoginResponse | null {
  if (!hasExactKeys(raw, RECOVERY_LOGIN_KEYS) || raw.requires_reenrollment !== true) return null;
  const recoveryToken = raw.recovery_token;
  const enrollment = raw.enrollment;
  if (typeof recoveryToken !== "string" || !JWT_COMPACT_PATTERN.test(recoveryToken)) return null;
  if (!isPlainObject(enrollment) || !hasExactKeys(enrollment, RECOVERY_ENROLLMENT_KEYS)) return null;
  const { secret, qr_url: qrUrl, issuer, enrollment_id: enrollmentId, expires_at: expiresAt } = enrollment;
  if (typeof secret !== "string" || !BASE32_SECRET_PATTERN.test(secret)) return null;
  if (typeof qrUrl !== "string" || !isTOTPProvisioningUrl(qrUrl, secret)) return null;
  if (typeof issuer !== "string" || issuer.trim() === "") return null;
  if (typeof enrollmentId !== "string" || !PRINTABLE_TOKEN_PATTERN.test(enrollmentId)) return null;
  if (typeof expiresAt !== "string") return null;
  const expiresAtMs = parseRFC3339(expiresAt);
  if (expiresAtMs === null) return null;
  return {
    recoveryEnrollment: { recoveryToken, secret, qrUrl, issuer, enrollmentId, expiresAt, expiresAtMs },
  };
}

export type RecoveryEnrollmentFailureKind =
  /** 401 RECOVERY_ENROLLMENT_INVALID: expired, replaced, consumed or not a recovery credential. */
  | "enrollment-invalid"
  /** 403 TOTP_CODE_INVALID: the enrollment is still pending; another code may be tried. */
  | "code-invalid"
  /** 400 or client-side refusal: the request body was rejected before any commit. */
  | "invalid-request"
  /** 429 from the login rate limiter: rejected before the handler ran. */
  | "rate-limited"
  /** Request was refused locally before it was sent. */
  | "not-sent"
  /** Network failure, 5xx, unexpected status or malformed success: the commit may have happened. */
  | "unknown";

export class RecoveryEnrollmentError extends Error {
  readonly kind: RecoveryEnrollmentFailureKind;
  readonly retryAfter?: number;

  constructor(kind: RecoveryEnrollmentFailureKind, retryAfter?: number) {
    super(`recovery enrollment verification failed: ${kind}`);
    this.name = "RecoveryEnrollmentError";
    this.kind = kind;
    this.retryAfter = retryAfter;
  }
}

/** Classifies by the real HTTP status recorded by core.request, never the envelope code alone. */
export function classifyRecoveryEnrollmentFailure(error: unknown): RecoveryEnrollmentError {
  if (error instanceof RecoveryEnrollmentError) return error;
  if (error instanceof AuthTransitionRejectedError) return new RecoveryEnrollmentError("not-sent");
  if (!(error instanceof ApiError) || typeof error.httpStatus !== "number") {
    return new RecoveryEnrollmentError("unknown");
  }
  const detail = error.detail;
  const errorCode = isPlainObject(detail) && isPlainObject(detail.data) ? detail.data.error_code : undefined;
  switch (error.httpStatus) {
    case 401:
      return new RecoveryEnrollmentError(errorCode === "RECOVERY_ENROLLMENT_INVALID" ? "enrollment-invalid" : "unknown");
    case 403:
      return new RecoveryEnrollmentError(errorCode === "TOTP_CODE_INVALID" ? "code-invalid" : "unknown");
    case 400:
      return new RecoveryEnrollmentError("invalid-request");
    case 429:
      return new RecoveryEnrollmentError("rate-limited", error.retryAfter);
    default:
      return new RecoveryEnrollmentError("unknown");
  }
}

function mapRecoveryCodes(raw: unknown): string[] | null {
  if (!isPlainObject(raw) || !hasExactKeys(raw, RECOVERY_CODES_KEYS)) return null;
  const codes = raw.recovery_codes;
  if (!Array.isArray(codes) || codes.length !== RECOVERY_CODE_COUNT) return null;
  const result: string[] = [];
  for (const code of codes) {
    if (typeof code !== "string" || !PRINTABLE_TOKEN_PATTERN.test(code)) return null;
    result.push(code);
  }
  return new Set(result).size === RECOVERY_CODE_COUNT ? result : null;
}

export type LoginCaptchaPayload = {
  captchaId?: string;
  captchaAnswer?: string;
  secondCaptchaId?: string;
  secondCaptchaAnswer?: string;
};

export function createAuthApi() {
  return {
    async getCaptcha(): Promise<CaptchaResponse> {
      const raw = await request<RawCaptchaResponse>("/auth/captcha", { method: "GET" });
      const enabled = Boolean(raw?.enabled);
      const secondRequired = Boolean(raw?.second_required);
      return {
        enabled,
        id: enabled && typeof raw?.id === "string" ? raw.id : undefined,
        question: enabled && typeof raw?.question === "string" ? raw.question : undefined,
        secondRequired,
        secondId:
          secondRequired && typeof raw?.second_id === "string" ? raw.second_id : undefined,
        secondQuestion:
          secondRequired && typeof raw?.second_question === "string"
            ? raw.second_question
            : undefined,
      };
    },

    async login(
      username: string,
      password: string,
      captcha?: LoginCaptchaPayload | string,
      captchaAnswer?: string
    ): Promise<LoginResponse> {
      // Backward-compatible overload: login(user, pass, captchaId?, captchaAnswer?)
      // or login(user, pass, { captchaId, captchaAnswer, secondCaptchaId, secondCaptchaAnswer })
      const body: Record<string, string> = { username, password };
      if (typeof captcha === "object" && captcha !== null) {
        if (captcha.captchaId) body.captcha_id = captcha.captchaId;
        if (captcha.captchaAnswer) body.captcha_answer = captcha.captchaAnswer;
        if (captcha.secondCaptchaId) body.second_captcha_id = captcha.secondCaptchaId;
        if (captcha.secondCaptchaAnswer) body.second_captcha_answer = captcha.secondCaptchaAnswer;
      } else {
        if (captcha) body.captcha_id = captcha;
        if (captchaAnswer) body.captcha_answer = captchaAnswer;
      }
      const result = await request<unknown>("/auth/login", {
        method: "POST",
        body
      });
      if (!isPlainObject(result)) {
        throw new ApiError(500, i18n.t("login.errorLoginFormat"), result);
      }
      if (RECOVERY_LOGIN_KEYS.some((key) => Object.prototype.hasOwnProperty.call(result, key))) {
        const recovery = mapRecoveryEnrollmentLogin(result);
        // Never echo a recovery payload into error detail: it carries a secret and a token.
        if (!recovery) throw new ApiError(500, i18n.t("login.errorLoginFormat"));
        return recovery;
      }
      if (!("token" in result) && !("requires_2fa" in result)) {
        throw new ApiError(500, i18n.t("login.errorLoginFormat"), result);
      }
      return mapLoginResponse(result as RawLoginResponse);
    },

    /**
     * Completes an offline-recovery re-enrollment with an explicit recovery Bearer. It never
     * installs a session or triggers step-up; success returns only this response's codes.
     * Failures reject with RecoveryEnrollmentError; callers MUST NOT replay "unknown".
     */
    async completeRecoveryEnrollment(token: string, code: string): Promise<{ recoveryCodes: string[] }> {
      if (!JWT_COMPACT_PATTERN.test(token)) throw new RecoveryEnrollmentError("enrollment-invalid");
      if (!RECOVERY_VERIFY_CODE_PATTERN.test(code)) throw new RecoveryEnrollmentError("invalid-request");
      let raw: unknown;
      try {
        raw = await request<unknown>("/auth/recovery/enrollment/verify", {
          method: "POST",
          token,
          body: { code },
        });
      } catch (error) {
        throw classifyRecoveryEnrollmentFailure(error);
      }
      const recoveryCodes = mapRecoveryCodes(raw);
      if (!recoveryCodes) throw new RecoveryEnrollmentError("unknown");
      return { recoveryCodes };
    },

    async logout(token: string): Promise<void> {
      await request("/auth/logout", {
        method: "POST",
        token
      });
    },

    async changePassword(token: string, currentPassword: string, newPassword: string): Promise<void> {
      await request("/auth/change-password", {
        method: "POST",
        token,
        body: {
          current_password: currentPassword,
          new_password: newPassword
        }
      });
    }
  };
}
