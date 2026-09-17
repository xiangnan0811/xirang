import { appendFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

// Only a complete CI push run in this repository's main branch is release evidence.
// A newer failed/pending attempt must never be hidden by an older successful run.
export function selectReleaseCI(runs, repository, sha) {
  const matching = runs.filter(run => run.head_sha === sha && run.event === 'push'
    && run.head_branch === 'main' && run.head_repository?.full_name === repository
    && run.path === '.github/workflows/ci.yml');
  matching.sort((a, b) => b.id - a.id || b.run_attempt - a.run_attempt);
  const latest = matching[0];
  if (!latest || latest.status !== 'completed') return null;
  if (latest.conclusion !== 'success') {
    throw new Error(`CI run ${latest.id} for ${sha} concluded ${latest.conclusion}; release denied`);
  }
  if (!Number.isSafeInteger(latest.id) || latest.id <= 0) throw new Error('Invalid CI run identity');
  return latest;
}

export async function verifyReleaseCI({ repository, sha, request, waitSeconds = 0, now = Date.now, sleep = delay }) {
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository) || !/^[a-f0-9]{40}$/.test(sha)) {
    throw new Error('A repository and immutable 40-hex source SHA are required');
  }
  if (!Number.isFinite(waitSeconds) || waitSeconds < 0 || waitSeconds > 1800) throw new Error('Invalid CI wait bound');
  const deadline = now() + waitSeconds * 1000;
  while (true) {
    const result = await request(`/repos/${repository}/actions/workflows/ci.yml/runs?head_sha=${sha}&event=push&branch=main&per_page=100`);
    if (!Array.isArray(result.workflow_runs)) throw new Error('Invalid CI response; release denied');
    const run = selectReleaseCI(result.workflow_runs, repository, sha);
    if (run) return run;
    if (now() >= deadline) throw new Error(`Complete successful CI for ${sha} is missing or pending; release denied`);
    await sleep(Math.min(15000, deadline - now()));
  }
}

async function main() {
  const repository = process.env.GITHUB_REPOSITORY;
  const token = process.env.GH_TOKEN;
  if (!token || !repository || !/^[\w.-]+\/[\w.-]+$/.test(repository)) throw new Error('GitHub repository/token unavailable');
  const request = async path => {
    const response = await fetch(`https://api.github.com${path}`, {
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' },
      signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) throw new Error(`GitHub verification failed (${response.status}); release denied`);
    return response.json();
  };
  let sha = process.argv[2];
  if (sha === '--ref') {
    const ref = process.argv[3];
    if (!ref || ref.startsWith('-')) throw new Error('Source ref is required');
    // Resolve once, before any matrix job. No worker is allowed to resolve this ref again.
    const commit = await request(`/repos/${repository}/commits/${encodeURIComponent(ref)}`);
    sha = commit.sha;
  }
  const run = await verifyReleaseCI({ repository, sha, request, waitSeconds: Number(process.env.CI_WAIT_SECONDS ?? 1500) });
  const evidence = `source_sha=${sha}\nci_run_id=${run.id}\nci_run_attempt=${run.run_attempt}\n`;
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, evidence);
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY,
    `### Verified release source\n- SHA: \`${sha}\`\n- CI: https://github.com/${repository}/actions/runs/${run.id} (attempt ${run.run_attempt})\n`);
  console.log(evidence.trim());
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
