import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, copyFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { DEFAULT_CI_WAIT_SECONDS, selectReleaseCI, verifyReleaseCI } from './verify-release-ci.mjs';

const repository = 'owner/project';
const sha = 'a'.repeat(40);
const success = { id: 10, run_attempt: 1, head_sha: sha, event: 'push', head_branch: 'main',
  head_repository: { full_name: repository }, path: '.github/workflows/ci.yml', status: 'completed', conclusion: 'success' };

test('only exact repository main push CI is release evidence', () => {
  const ineligible = [
    { ...success, head_sha: 'b'.repeat(40) },
    { ...success, head_repository: { full_name: 'fork/project' } },
    { ...success, event: 'pull_request' },
    { ...success, path: '.github/workflows/publish-images.yml' },
    { ...success, head_branch: 'topic' },
  ];
  assert.equal(selectReleaseCI(ineligible, repository, sha), null);
  assert.equal(selectReleaseCI([...ineligible, success], repository, sha).id, 10);
});

test('newer failed or unfinished CI cannot be hidden by earlier success', () => {
  assert.throws(() => selectReleaseCI([success, { ...success, id: 11, conclusion: 'failure' }], repository, sha), /release denied/);
  assert.equal(selectReleaseCI([success, { ...success, id: 11, status: 'in_progress', conclusion: null }], repository, sha), null);
  assert.throws(() => selectReleaseCI([{ ...success, conclusion: 'cancelled' }], repository, sha), /release denied/);
});

test('pending evidence remains denied until complete and only within the wait bound', async () => {
  let clock = 0;
  let checks = 0;
  const request = async () => ({ workflow_runs: [{ ...success, status: ++checks < 2 ? 'in_progress' : 'completed' }] });
  const run = await verifyReleaseCI({ repository, sha, request, waitSeconds: 30, now: () => clock, sleep: async ms => { clock += ms; } });
  assert.equal(run.id, 10);
  await assert.rejects(verifyReleaseCI({ repository, sha, request: async () => ({ workflow_runs: [] }),
    waitSeconds: 1, now: () => clock, sleep: async ms => { clock += ms; } }), /missing or pending/);
});

test('a movable ref or malformed verification response never authorizes promotion', async () => {
  await assert.rejects(verifyReleaseCI({ repository, sha: 'main', request: async () => ({ workflow_runs: [success] }) }), /immutable/);
  await assert.rejects(verifyReleaseCI({ repository, sha, request: async () => ({}) }), /Invalid CI response/);
});

test('slow complete CI can succeed after the former 25-minute deadline', async () => {
  let clock = 0;
  const run = await verifyReleaseCI({ repository, sha, waitSeconds: DEFAULT_CI_WAIT_SECONDS,
    request: async () => ({ workflow_runs: [{ ...success, status: clock < 35 * 60_000 ? 'in_progress' : 'completed' }] }),
    now: () => clock, sleep: async ms => { clock += ms; } });
  assert.equal(run.id, success.id);
  assert.equal(clock, 35 * 60_000);
});

test('extended wait still denies missing CI at its deadline', async () => {
  let clock = 0;
  await assert.rejects(verifyReleaseCI({ repository, sha, waitSeconds: DEFAULT_CI_WAIT_SECONDS,
    request: async () => ({ workflow_runs: [] }), now: () => clock,
    sleep: async ms => { clock += ms; } }), /missing or pending/);
  assert.equal(clock, DEFAULT_CI_WAIT_SECONDS * 1000);
});

test('failed CI aborts the extended wait immediately', async () => {
  let clock = 0;
  await assert.rejects(verifyReleaseCI({ repository, sha, waitSeconds: DEFAULT_CI_WAIT_SECONDS,
    request: async () => ({ workflow_runs: [{ ...success, status: clock === 0 ? 'in_progress' : 'completed', conclusion: 'failure' }] }),
    now: () => clock, sleep: async ms => { clock += ms; } }), /concluded failure/);
  assert.equal(clock, 15_000);
});

test('promotion with zero wait still refuses pending CI without sleeping', async () => {
  await assert.rejects(verifyReleaseCI({ repository, sha, waitSeconds: 0,
    request: async () => ({ workflow_runs: [{ ...success, status: 'in_progress' }] }),
    sleep: async () => { assert.fail('promotion must not wait'); } }), /missing or pending/);
});

test('invalid wait bounds are rejected before querying CI', async () => {
  for (const waitSeconds of [-1, NaN, Infinity, DEFAULT_CI_WAIT_SECONDS + 1]) {
    await assert.rejects(verifyReleaseCI({ repository, sha, waitSeconds,
      request: async () => { assert.fail('invalid wait must not query CI'); } }), /Invalid CI wait bound/);
  }
});

const sourceRoot = fileURLToPath(new URL('../', import.meta.url));
const workerProjection = 'backend/internal/backupasset/processing/capabilities/production_apk_inputs.json';

function publishInputGate() {
  const workflow = readFileSync(join(sourceRoot, '.github/workflows/publish-images.yml'), 'utf8');
  const marker = '      - name: Verify preserved build input contract\n';
  assert.equal(workflow.split(marker).length, 2, 'publish workflow must have one preserved-input gate');
  const lines = workflow.split(marker)[1].split('\n      - name:')[0].split('\n');
  const start = lines.indexOf('        run: |');
  assert.notEqual(start, -1, 'input adoption policy must live in the current workflow');
  return lines.slice(start + 1).map(line => {
    assert.ok(line === '' || line.startsWith('          '), 'unexpected workflow script indentation');
    return line.slice(10);
  }).join('\n');
}

function inputSourceFixture(t, adopted = false) {
  const root = mkdtempSync(join(tmpdir(), 'xirang-publish-inputs-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  if (adopted) {
    // Exercise the real verifier and committed lock, without registry/network access.
    for (const relative of ['scripts/build-inputs.py', 'scripts/capture-apk-inputs.sh',
      'scripts/install-apk-inputs.sh', 'scripts/test-build-inputs.py', 'deploy/build-inputs',
      workerProjection, 'deploy/allinone/Dockerfile', 'deploy/worker/Dockerfile']) {
      const target = join(root, relative);
      mkdirSync(dirname(target), { recursive: true });
      cpSync(join(sourceRoot, relative), target, { recursive: true });
    }
  } else {
    // Exact v0.73.4 Dockerfiles from 6f3de179da6b6aae4671ef2ef4830e7c3b27b526.
    const originalDigests = {
      allinone: 'a2642bbb8600c1c170403ef89c899961ee0347c1463171ec5fd2d403be917bd1',
      worker: 'b7e510dd9019494ad5fda48f8ea1080964119b73148af52ba10964e2debe19f3',
    };
    for (const [role, digest] of Object.entries(originalDigests)) {
      const original = join(sourceRoot, `scripts/testdata/publish-legacy-0.73.4/${role}.Dockerfile`);
      assert.equal(createHash('sha256').update(readFileSync(original)).digest('hex'), digest);
      const target = join(root, `deploy/${role}/Dockerfile`);
      mkdirSync(dirname(target), { recursive: true });
      copyFileSync(original, target);
    }
  }
  return root;
}

function runPublishInputGate(root) {
  const result = spawnSync('bash', ['-c', publishInputGate()], { cwd: root, encoding: 'utf8', timeout: 30_000 });
  assert.ifError(result.error);
  return result;
}

test('real v0.73.4 source may reach the original build path without an input contract', t => {
  const result = runPublishInputGate(inputSourceFixture(t));
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Legacy source predates the preserved build-input contract/);
});

test('legacy source does not need the later Worker Dockerfile', t => {
  const root = inputSourceFixture(t);
  rmSync(join(root, 'deploy/worker/Dockerfile'));
  const result = runPublishInputGate(root);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Legacy source predates/);
});

test('complete adopted source must pass the real input verifier', t => {
  const result = runPublishInputGate(inputSourceFixture(t, true));
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Build input lock, full transaction manifests and consumer identities verified/);
  assert.doesNotMatch(result.stdout, /Legacy source predates/);
});

for (const [name, relative] of [
  ['verifier', 'scripts/build-inputs.py'],
  ['lock', 'deploy/build-inputs/lock.json'],
  ['installer', 'scripts/install-apk-inputs.sh'],
  ['platform manifests', 'deploy/build-inputs/manifests'],
  ['Worker projection', workerProjection],
]) {
  test(`adopted source with missing ${name} fails closed`, t => {
    const root = inputSourceFixture(t, true);
    rmSync(join(root, relative), { recursive: true, force: true });
    const result = runPublishInputGate(root);
    assert.notEqual(result.status, 0, 'partial adoption was accepted');
    assert.doesNotMatch(result.stdout, /Legacy source predates/);
  });
}

for (const relative of ['scripts/build-inputs.py', 'scripts/capture-apk-inputs.sh',
  'scripts/install-apk-inputs.sh', 'scripts/test-build-inputs.py', workerProjection]) {
  test(`an isolated ${relative} is not classified as legacy`, t => {
    const root = inputSourceFixture(t);
    const target = join(root, relative);
    mkdirSync(dirname(target), { recursive: true });
    writeFileSync(target, 'INVALID_PARTIAL_INPUT_CONTRACT_FOR_TEST_ONLY\n');
    const result = runPublishInputGate(root);
    assert.notEqual(result.status, 0, 'isolated contract file was accepted');
    assert.doesNotMatch(result.stdout, /Legacy source predates/);
  });
}

for (const [name, instruction] of [
  ['input stage', 'FROM scratch AS worker-runtime-inputs'],
  ['input mount', 'RUN --mount=type=bind,source=/inputs,target=/retained true'],
  ['installer call', 'RUN sh /build-inputs/install.sh worker-runtime "$TARGETARCH" --'],
]) {
  test(`a residual Dockerfile ${name} requires the full input contract`, t => {
    const root = inputSourceFixture(t);
    appendFileSync(join(root, 'deploy/worker/Dockerfile'), `\n${instruction}\n`);
    const result = runPublishInputGate(root);
    assert.notEqual(result.status, 0, 'residual Dockerfile input contract was accepted');
    assert.doesNotMatch(result.stdout, /Legacy source predates/);
  });
}

for (const dangling of [false, true]) {
  test(`${dangling ? 'dangling' : 'empty'} input directory cannot bypass input verification`, t => {
    const root = inputSourceFixture(t);
    const target = join(root, 'deploy/build-inputs');
    if (dangling) symlinkSync('missing-input-directory', target);
    else mkdirSync(target);
    const result = runPublishInputGate(root);
    assert.notEqual(result.status, 0, 'partial input directory was classified as legacy');
    assert.doesNotMatch(result.stdout, /Legacy source predates/);
  });
}
