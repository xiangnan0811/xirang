# P1d Step-up Authentication for High-risk Operations

## Goal

Require a recent stronger authentication check before allowing selected high-risk credential operations, using the P1 credential-use audit model as evidence and feedback.

## Requirements

- Define the initial high-risk operation set, such as SSH key export, config export with secrets, destructive restore, terminal open, batch command creation, and other credential-sensitive actions.
- Add a step-up challenge flow compatible with existing authentication and 2FA capabilities.
- Enforce step-up at backend mutation/export/session boundaries, fail closed, and return sanitized errors.
- Record step-up success/failure/required outcomes in audit evidence without storing secrets, OTP values, recovery codes, or challenge tokens.
- Add frontend prompts and state handling for operations that require step-up.

## Acceptance Criteria

- High-risk operations require a recent valid step-up session or challenge result.
- Step-up bypass is not possible by calling backend endpoints directly.
- Credential-use audit records can show when step-up was required/satisfied/failed without exposing challenge secrets.
- Backend and frontend tests cover success, expired/missing step-up, invalid challenge, RBAC interactions, and UI prompts.
