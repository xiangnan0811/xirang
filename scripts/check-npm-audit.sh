#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ALLOWLIST="$ROOT_DIR/web/npm-audit-allowlist.json"
cd "$ROOT_DIR/web"
env -u NODE_ENV npm audit --audit-level=high --json > /tmp/xirang-npm-audit.json || true
python3 - "$ALLOWLIST" /tmp/xirang-npm-audit.json <<'PY'
import json, sys
allow = set(json.load(open(sys.argv[1])).get("allowedGHSA", []))
report = json.load(open(sys.argv[2]))
bad = []
for name, vuln in report.get("vulnerabilities", {}).items():
    if vuln.get("severity") not in ("high", "critical"):
        continue
    ghsas = []
    for item in vuln.get("via") or []:
        if isinstance(item, dict):
            url = item.get("url") or ""
            if "GHSA-" in url:
                ghsas.append(url.rsplit("/", 1)[-1])
    if ghsas and set(ghsas) <= allow:
        continue
    bad.append(f"{name}: {ghsas or vuln.get('via')}")
if bad:
    print("npm high+ vulnerabilities not allowlisted:")
    print("\n".join(bad))
    raise SystemExit(1)
print("npm audit high+: clean or allowlisted")
PY
