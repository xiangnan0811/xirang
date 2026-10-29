#!/usr/bin/env bash
# Exercise the runner protocol through a fake Go test process; never contact PostgreSQL.
set -euo pipefail
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RUNNER="$ROOT_DIR/scripts/run-required-postgres-tests.sh"
REAL_GO=$(command -v go)
if [[ -n "${TMPDIR:-}" ]]; then
  scratch_root=$TMPDIR
  [[ -d "$scratch_root" ]] || { echo "TMPDIR is not a directory" >&2; exit 2; }
else
  scratch_root="$ROOT_DIR/.tmp/agent/required-postgres-selftest"
  mkdir -p -- "$scratch_root"
fi
WORK=$(mktemp -d "$scratch_root/run.XXXXXXXX")
trap 'rm -rf -- "$WORK"' EXIT
mkdir "$WORK/bin"
cat >"$WORK/exec-cli" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
export PATH="$FAKE_BIN:$PATH"
[[ "$(command -v go)" == "$FAKE_BIN/go" ]] || { echo "fake go isolation failed" >&2; exit 1; }
exec "$@"
SH
chmod +x "$WORK/exec-cli"
cat >"$WORK/bin/go" <<'PY'
#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if args and args[0] == 'run':
    wrapper = os.environ['FAKE_EXEC']
    if "'" not in wrapper:
        wrapper = "'" + wrapper + "'"
    elif '"' not in wrapper:
        wrapper = '"' + wrapper + '"'
    else:
        sys.exit('selftest scratch path cannot contain both quote types')
    os.execv(os.environ['REAL_GO'], [os.environ['REAL_GO'], 'run', '-exec', wrapper] + args[1:])
if len(args) < 2 or args[0] != 'test':
    sys.exit(2)
with open(os.environ['FAKE_INVENTORY'], encoding='utf-8') as f:
    inventory = json.load(f)
group = next((g for g in inventory['groups'] if g['package'] == args[1]), None)
excluded = [e['name'] for e in inventory['exclusions'] if e['package'] == args[1]]
if group is None and not excluded:
    sys.exit(2)
with open(os.environ['FAKE_LOG'], 'a', encoding='utf-8') as f:
    f.write(json.dumps({'args': args, 'goflags': os.environ.get('GOFLAGS'), 'tag': os.environ.get('FAKE_TAG'),
                        'require': {k: v for k, v in os.environ.items() if k.startswith('REQUIRE_POSTGRES_')}}) + '\n')
if '-list' in args:
    for test in (group or {'tests': []})['tests']:
        print(test['name'])
    for name in excluded:
        print(name)
    sys.exit(0)
mode = os.environ.get('FAKE_MODE', '')
package = 'xirang/backend/' + group['package'][2:]
def emit(action, name=''):
    print(json.dumps({'Action': action, 'Package': package, 'Test': name}))
if mode == 'invalid':
    print('not JSON'); sys.exit(0)
if mode == 'exit':
    sys.exit(1)
for test in group['tests']:
    names = {test['name']}
    for path in test['required_paths']:
        parts = path.split('/')
        names.update('/'.join(parts[:i]) for i in range(1, len(parts)+1))
    names = sorted(names, key=lambda p: (p.count('/'), p))
    if mode == 'missing' and len(names) > 1:
        names.pop()
    for name in names:
        emit('run', name)
    for name in reversed(names):
        emit('skip' if mode == 'skip' and name != test['name'] else 'pass', name)
emit('pass')
PY
chmod +x "$WORK/bin/go"
export REAL_GO FAKE_INVENTORY="$ROOT_DIR/scripts/required-postgres-tests.json" FAKE_LOG="$WORK/fake.log"
export FAKE_BIN="$WORK/bin" FAKE_EXEC="$WORK/exec-cli"
export PATH="$WORK/bin:$PATH"
unset TEST_POSTGRES_DSN POSTGRES_TEST_TIMEOUT GOFLAGS FAKE_MODE FAKE_TAG
unset REQUIRE_POSTGRES_MIGRATION_TEST REQUIRE_POSTGRES_RECOVERY_TEST REQUIRE_POSTGRES_CATALOG_TEST REQUIRE_POSTGRES_SEARCH_TEST REQUIRE_POSTGRES_OVERLAY_TEST REQUIRE_POSTGRES_CONTENT_TEST REQUIRE_POSTGRES_PROCESSING_TEST REQUIRE_POSTGRES_EXPORT_TEST REQUIRE_POSTGRES_GENERAL_TEST REQUIRE_POSTGRES_TASK_TERMINAL_TEST REQUIRE_POSTGRES_ESCALATION_PREVIEW_TEST REQUIRE_POSTGRES_LEASE_TEST REQUIRE_POSTGRES_REPOSITORY_TEST REQUIRE_POSTGRES_PRODUCER_RETENTION_TEST

bash "$RUNNER" --list-only >"$WORK/list.out"
if bash "$RUNNER" >"$WORK/missing.out" 2>&1; then echo 'missing DSN accepted' >&2; exit 1; fi
if bash "$RUNNER" label ./internal/database TestSomething >"$WORK/positional.out" 2>&1; then echo 'positional selector accepted' >&2; exit 1; fi
export TEST_POSTGRES_DSN='postgres://contract.invalid/fake'
FAKE_TAG=default GOFLAGS='-run=unwanted' REQUIRE_POSTGRES_LEASE_TEST=0 bash "$RUNNER" --evidence-dir "$WORK/default" >"$WORK/default.out"
FAKE_TAG=override POSTGRES_TEST_TIMEOUT=30m bash "$RUNNER" --evidence-dir "$WORK/override" >"$WORK/override.out"
if POSTGRES_TEST_TIMEOUT=0 bash "$RUNNER" --evidence-dir "$WORK/invalid-timeout" >"$WORK/invalid-timeout.out" 2>&1; then echo 'invalid timeout accepted' >&2; exit 1; fi
for mode in missing skip invalid exit; do
  if FAKE_MODE=$mode bash "$RUNNER" --evidence-dir "$WORK/$mode" >"$WORK/$mode.out" 2>&1; then echo "$mode accepted" >&2; exit 1; fi
done
python3 - "$WORK/fake.log" <<'PY'
import json, sys
rows = [json.loads(line) for line in open(sys.argv[1], encoding='utf-8')]
runs = [r for r in rows if '-run' in r['args']]
assert runs and all(r['goflags'] is None for r in rows)
required = ['MIGRATION', 'RECOVERY', 'CATALOG', 'SEARCH', 'OVERLAY', 'CONTENT',
            'PROCESSING', 'EXPORT', 'GENERAL', 'TASK_TERMINAL', 'ESCALATION_PREVIEW',
            'LEASE', 'REPOSITORY', 'PRODUCER_RETENTION']
assert all(all(r['require'].get('REQUIRE_POSTGRES_' + k + '_TEST') == '1' for k in required) for r in rows)
assert any(r['tag'] == 'default' and '-timeout=20m' in r['args'] for r in runs)
assert any(r['tag'] == 'default' and r['args'][1] == './internal/database' and '-timeout=30m' in r['args'] for r in runs)
assert all('-timeout=30m' in r['args'] for r in runs if r['tag'] == 'override')
PY
printf 'required PostgreSQL test runner self-test: PASS\n'
