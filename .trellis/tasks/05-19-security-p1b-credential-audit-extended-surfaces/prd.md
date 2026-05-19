# P1b Extend Credential Audit Coverage

## Goal

Extend the P1 credential-use audit model beyond the core Approach B surfaces to the remaining high-risk SSH and secret-bearing paths, while preserving secret-safe bounded event metadata.

## Requirements

- Add credential-use audit events for SFTP/file list and file read paths.
- Add credential-use audit events for Docker volume discovery over SSH.
- Add credential-use audit events for config export, especially `include_secrets=true`, without storing exported payloads or secret values.
- Add credential-use audit events for node doctor and migration preflight SSH diagnostics.
- Review probes/background workers and add scoped system-actor events where useful and not excessively noisy.
- Keep all event metadata bounded and sanitized; never store private keys, passwords, tokens, command output, terminal/file streams, executor config, or exported secret material.

## Acceptance Criteria

- New events include actor/system context, node/key/task identifiers where available, purpose/action, credential source/key ID where resolved, and outcome.
- Sensitive GET-style operations that are skipped by the generic HTTP audit middleware have explicit credential-use events.
- Tests cover representative success/failure/blocked paths and assert no raw secret or content payload is persisted.
- Settings/security-health signals continue to aggregate high-risk credential operations without exposing raw sensitive data.
