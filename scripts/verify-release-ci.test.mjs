import assert from 'node:assert/strict';
import { test } from 'node:test';
import { selectReleaseCI, verifyReleaseCI } from './verify-release-ci.mjs';

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
