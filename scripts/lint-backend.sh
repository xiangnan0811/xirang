#!/usr/bin/env bash
# Build the pinned linter with the same active Go toolchain as the project.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR/backend"

# Keep this release aligned with .github/workflows/ci.yml. A version-qualified
# go run isolates tool dependencies from backend/go.mod and reuses Go's cache.
go version
exec go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...
