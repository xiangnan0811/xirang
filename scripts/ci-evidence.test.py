#!/usr/bin/env python3
"""Behavioral regressions for durable CI evidence and its real profile verifier."""
import argparse
import base64
import collections
import hashlib
import fcntl
import http.server
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import urllib.parse
import unittest
import zipfile

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'scripts'))
from ci_evidence import capture, cli, context, snapshot, source, store

VERIFIER = None
SCRATCH = ROOT / '.tmp/agent'


def zip_bytes(files):
    out = io.BytesIO()
    with zipfile.ZipFile(out, 'w') as archive:
        for name, data in files.items():
            archive.writestr(name, data)
    return out.getvalue()


def signed_profile(directory, temp_root):
    env = dict(os.environ, TMPDIR=str(temp_root), CI_PROFILE_FIXTURE_OUTPUT=str(directory))
    env.pop('GOFLAGS', None)
    subprocess.run(['go', 'test', '-count=1', '-run', '^TestVerifyCIProfileEvidence$', './cmd/verify-ci-profile'],
                   cwd=ROOT / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                   check=True, close_fds=True)


def prepare_checkout(root):
    env = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
               GIT_AUTHOR_NAME='Fixture', GIT_AUTHOR_EMAIL='fixture@example.invalid',
               GIT_COMMITTER_NAME='Fixture', GIT_COMMITTER_EMAIL='fixture@example.invalid',
               GIT_AUTHOR_DATE='2026-01-01T00:00:00Z', GIT_COMMITTER_DATE='2026-01-01T00:00:00Z')
    subprocess.run(['git', 'init', '--quiet', str(root)], check=True, env=env,
                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
    (root / 'scripts').mkdir()
    shutil.copytree(ROOT / 'scripts/ci_evidence', root / 'scripts/ci_evidence',
                    ignore=shutil.ignore_patterns('__pycache__'))
    names = ['scripts/ci-evidence.py', 'scripts/classify-ci-event.py',
             'scripts/verify-release-ci.mjs', 'scripts/required-postgres-tests.json',
             '.github/workflows/ci.yml', '.github/workflows/pr-metadata.yml']
    build_files, _ = context.verifier_build_inputs(ROOT)
    names.extend(path.relative_to(ROOT).as_posix() for path in build_files)
    for name in names:
        destination = root / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(ROOT / name, destination)
    subprocess.run(['git', 'add', '--', 'scripts', 'backend', '.github'], cwd=root, env=env, check=True,
                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
    subprocess.run(['git', 'commit', '--quiet', '-m', 'fixture baseline'], cwd=root, env=env, check=True,
                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)


def source_fixture(profile):
    head, tree, repo, repo_id = 'a' * 40, 'b' * 40, 'fixture/example', 1234
    workflow = b'fixture workflow\n'
    workflow_blob = hashlib.sha1(b'blob ' + str(len(workflow)).encode() + b'\0' + workflow).hexdigest()
    run = {'id': 123, 'run_attempt': 1, 'event': 'push', 'path': '.github/workflows/ci.yml',
           'head_sha': head, 'head_branch': 'main', 'status': 'completed', 'conclusion': 'success',
           'check_suite_id': 3000, 'repository': {'id': repo_id, 'full_name': repo},
           'head_repository': {'id': repo_id, 'full_name': repo}}
    steps = {name: {'Checkout': 'success'} for name in source.JOB_NAMES}
    for arch in ('amd64', 'arm64'):
        steps[f'Docker Build ({arch})'].update({name: 'success' for name in (
            'Run asset-content Nginx live gateway probe', 'Build Docker image', 'Inspect local Core image',
            'Scan local Core image', 'Run core-only Compose smoke')})
        steps[f'Asset Worker Runtime Closure ({arch})'].update({name: 'success' for name in (
            'Build native Worker closure image', 'Extract and validate native runtime closure', 'Upload native runtime closure')})
        worker = steps[f'Asset Worker Build & Scan ({arch})']
        worker.update({name: 'success' for name in ('Download amd64 runtime closure', 'Download arm64 runtime closure',
                      'Run malicious fixture and sandbox unit tests', 'Build local Worker image',
                      'Inspect and smoke native Worker image', 'Scan local Worker image')})
        for name in ('Build local Core image for Worker profile smoke', 'Run complete Worker Compose profile smoke'):
            worker[name] = 'success' if arch == 'amd64' else 'skipped'
    for job, step in source.UPLOAD_STEPS.items():
        steps[job][step] = 'success'
    steps['PostgreSQL Migration Parity']['Upload required PostgreSQL evidence'] = 'success'
    steps['Asset Worker Build & Scan (amd64)']['Upload Worker profile signing evidence'] = 'success'
    jobs = []
    logs = {}
    for index, name in enumerate(sorted(source.JOB_NAMES), 1):
        jobs.append({'id': 1000 + index, 'name': name, 'run_id': 123, 'run_attempt': 1, 'head_sha': head,
                     'status': 'completed', 'conclusion': 'success', 'started_at': '2026-01-01T00:00:00Z',
                     'completed_at': '2026-01-01T00:10:00Z',
                     'check_run_url': f'https://api.github.com/repos/{repo}/check-runs/{4000 + index}',
                     'steps': [{'name': name, 'status': 'completed', 'conclusion': conclusion,
                                'completed_at': '2026-01-01T00:01:00Z'} for name, conclusion in steps[name].items()]})
        logs[f'{index}_{name}.txt'] = ('[command]git log -1 --format=%H\n2026-01-01 ' + head + '\n').encode()
    record = {'schema_version': 1, 'repository': repo, 'repository_id': repo_id,
              'workflow_path': '.github/workflows/ci.yml', 'workflow_sha': head, 'run_id': 123,
              'run_attempt': 1, 'event_name': 'push', 'action': None, 'event_ref': 'refs/heads/main',
              'event_sha': head, 'checkout_sha': head, 'checkout_tree': tree, 'change_keys': None,
              'pull_request': None, 'mode': 'source', 'reason': 'source-event', 'source_run': None}

    groups, pg_files, root_index = [], {}, 0
    for index in range(1, 23):
        package = f'./fixture/g{index:02d}'
        tests, events = [], []
        for local in range(24 if index < 22 else 23):
            name = f'TestFixture{local:02d}'
            paths = [name + '/one'] + ([name + '/two'] if root_index < 368 else [])
            root_index += 1
            tests.append({'name': name, 'required_paths': paths})
            pkg = 'xirang/backend/' + package[2:]
            events.append({'Package': pkg, 'Test': name, 'Action': 'run'})
            for path in paths:
                events.extend(({'Package': pkg, 'Test': path, 'Action': 'run'},
                               {'Package': pkg, 'Test': path, 'Action': 'pass'}))
            events.append({'Package': pkg, 'Test': name, 'Action': 'pass'})
        events.append({'Package': 'xirang/backend/' + package[2:], 'Action': 'pass'})
        group = {'package': package, 'tests': tests}
        raw = b''.join(context.canonical(event) for event in events)
        summary = dict(source.replay(raw, group), exit=0)
        groups.append(group)
        pg_files[f'{index:03d}.jsonl'] = raw
        pg_files[f'{index:03d}.stderr'] = b''
        pg_files[f'{index:03d}.summary.json'] = context.canonical(summary)
    inventory = context.canonical({'groups': groups})

    def browser(config, count, nested):
        outcome = {'passed': count, 'failed': 0, 'timedOut': 0, 'skipped': 0, 'interrupted': 0, 'missing': 0}
        evidence = {'config': config, 'collected': {'total': count}, 'selected': {'total': count},
                    'notSelected': {'total': 0, 'tests': []},
                    'run': {'endStatus': 'passed', 'runnerErrors': 0, 'attemptsBegun': count, 'attemptsEnded': count},
                    'first': outcome, 'final': outcome, 'retries': {'retriedTests': 0, 'retryAttempts': 0, 'maxRetry': 0},
                    'outcomes': {'expected': count, 'flaky': 0, 'unexpected': 0, 'skipped': 0, 'missing': 0},
                    'missing': [], 'nonContiguous': [], 'firstFailures': [], 'finalFailures': [], 'issues': []}
        return context.canonical({'config': config, 'head': head, 'exitCode': 0, 'invalidCandidate': False,
                                  'evidence': evidence} if nested else evidence)

    payloads = {'ci-event-123-1': zip_bytes({'record.json': context.canonical(record)}),
                'required-postgres-evidence': zip_bytes(pg_files)}
    for name, (_, members) in source.ARTIFACTS.items():
        if name.startswith('asset-worker-runtime-closure-'):
            arch = name.rsplit('-', 1)[1]
            payloads[name] = zip_bytes({'runtime-closure.v1.json': (profile / f'native-{arch}.json').read_bytes()})
        else:
            payloads[name] = zip_bytes({member: browser(config, count, nested)
                                        for member, (config, count, nested) in members.items()})
    payloads['asset-worker-profile-signing-evidence-amd64'] = zip_bytes(
        {path.name: path.read_bytes() for path in (profile / 'profile').iterdir()})
    artifacts, downloads = [], {}
    for index, (name, raw) in enumerate(payloads.items(), 1):
        artifact_id = 2000 + index
        downloads[f'/repos/{repo}/actions/artifacts/{artifact_id}/zip'] = raw
        artifacts.append({'id': artifact_id, 'name': name, 'expired': False, 'digest': 'sha256:' + hashlib.sha256(raw).hexdigest(),
                          'created_at': '2026-01-01T00:00:30Z',
                          'workflow_run': {'id': 123, 'repository_id': repo_id, 'head_repository_id': repo_id,
                                           'head_sha': head, 'head_branch': 'main'}})
    prefix = f'/repos/{repo}/'
    def api_content(data):
        return context.canonical({'encoding': 'base64', 'content': base64.b64encode(data).decode(),
                                  'sha': hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()})
    routes = {prefix + 'actions/runs/123': context.canonical(run),
              prefix + 'actions/runs/123/attempts/1/jobs': context.canonical({'total_count': len(jobs), 'jobs': jobs}),
              prefix + 'actions/runs/123/artifacts': context.canonical({'total_count': len(artifacts), 'artifacts': artifacts}),
              prefix + 'actions/runs/123/attempts/1/logs': zip_bytes(logs),
              prefix + 'git/commits/' + head: context.canonical({'sha': head, 'tree': {'sha': tree}}),
              prefix + 'contents/.github/workflows/ci.yml': api_content(workflow),
              prefix + 'contents/scripts/required-postgres-tests.json': api_content(inventory), **downloads}
    routes[prefix + 'branches/main'] = context.canonical({'commit': {'sha': head}})
    routes[prefix + 'branches/main/protection'] = context.canonical({
        'required_status_checks': {'strict': True, 'checks': [{'context': name, 'app_id': 15368}
                                                             for name in (*snapshot.NAMES, 'PR Title')]},
        'enforce_admins': {'enabled': True}})
    for job in jobs:
        check_id = int(job['check_run_url'].rsplit('/', 1)[1])
        routes[prefix + 'check-runs/' + str(check_id)] = context.canonical({
            'id': check_id, 'name': job['name'], 'head_sha': head, 'status': job['status'],
            'conclusion': job['conclusion'], 'app': {'id': 15368}, 'check_suite': {'id': 3000}})
    checks = [json.loads(routes[prefix + 'check-runs/' + job['check_run_url'].rsplit('/', 1)[1]])
              for job in jobs if job['name'] in snapshot.NAMES]
    routes[prefix + 'commits/' + head + '/check-runs'] = context.canonical(
        {'total_count': len(checks), 'check_runs': checks})
    request = {'schema_version': 1, 'kind': 'source', 'task_id': 'fixture', 'repository': repo,
               'repository_id': repo_id, 'workflow_head_sha': head, 'workflow_blob': workflow_blob,
               'source': {'run_id': 123, 'attempt': 1, 'event': 'push', 'head_sha': head,
                          'branch': 'main', 'checkout_sha': head, 'checkout_tree': tree},
               'pr': None, 'metadata': None, 'title': None, 'expected': 'success', 'predecessor': None}
    return routes, request, next(path for path in downloads if path.endswith('/2002/zip'))


class EvidenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        SCRATCH.mkdir(mode=0o700, parents=True, exist_ok=True)
        cls.root = Path(tempfile.mkdtemp(prefix='regression-', dir=SCRATCH))
        cls.previous_tmpdir = os.environ.get('TMPDIR')
        os.environ['TMPDIR'] = str(cls.root)

    @classmethod
    def tearDownClass(cls):
        if cls.previous_tmpdir is None:
            os.environ.pop('TMPDIR', None)
        else:
            os.environ['TMPDIR'] = cls.previous_tmpdir
        shutil.rmtree(cls.root)

    def directory(self):
        return Path(tempfile.mkdtemp(prefix='case-', dir=self.root))

    def ignored_nested_checkout(self, root):
        env = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}
        env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)
        nested = root / 'fixture-ignored/nested'
        nested.mkdir(parents=True)
        with (root / '.git/info/exclude').open('a') as exclude:
            exclude.write('\n/fixture-ignored/nested/\n')
        subprocess.run(['git', 'init', '--quiet', str(nested)], env=env, check=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
        (nested / 'content').write_text('unrelated checkout\n')
        ignored = subprocess.run(['git', 'check-ignore', '-q', '--', 'fixture-ignored/nested/'],
                                 cwd=root, env=env, close_fds=True)
        self.assertEqual(ignored.returncode, 0)
        self.assertTrue((nested / '.git').is_dir())
        _, specs = context.verifier_build_inputs(root)
        reported = subprocess.run(['git', 'ls-files', '--others', '--ignored', '--exclude-standard', '-z',
                                   '--', *specs], cwd=root, env=env, check=True, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, close_fds=True).stdout.split(b'\0')
        self.assertFalse(any(path.startswith(b'fixture-ignored/') for path in reported))
        return env

    def test_source_cli_download_kill_resume_and_offline_verify(self):
        profile = self.directory()
        signed_profile(profile, self.root)
        routes, request, blocked_path = source_fixture(profile)
        entered, release = threading.Event(), threading.Event()
        helper_ready, helper_release = threading.Event(), threading.Event()
        counts = collections.Counter()

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                path = urllib.parse.urlsplit(self.path).path
                counts[path] += 1
                if path == '/helper-hold':
                    helper_ready.set()
                    helper_release.wait(30)
                    self.send_response(200)
                    self.send_header('Content-Length', '0')
                    self.end_headers()
                    return
                data = routes.get(path)
                if data is None:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                if path == blocked_path and counts[path] == 1:
                    self.wfile.write(data[:4096])
                    self.wfile.flush()
                    entered.set()
                    release.wait(30)
                    try:
                        self.wfile.write(data[4096:])
                    except (BrokenPipeError, ConnectionResetError):
                        pass
                else:
                    self.wfile.write(data)

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        root = self.directory()
        try:
            prepare_checkout(root)
            agent = root / '.tmp/agent'
            (agent / 'cases').mkdir(parents=True)
            (agent / 'bin').mkdir()
            verifier = agent / 'bin/verify-ci-profile'
            shutil.copy2(VERIFIER, verifier)
            verifier.chmod(0o700)
            gh = agent / 'bin/gh'
            gh.write_text('#!/bin/sh\nprintf "fixture-token\\n"\n')
            gh.chmod(0o700)
            request_path = agent / 'request.json'
            request_path.write_bytes(context.canonical(request))
            case = agent / 'cases/source'
            wrapper = '''import http.client, os, signal, sys, urllib.parse, urllib.request
port = int(sys.argv[1])
real_opener = urllib.request.build_opener
class LocalConnection(http.client.HTTPConnection):
    def __init__(self, host, timeout=None, **kwargs):
        super().__init__("127.0.0.1", port, timeout=timeout)
http.client.HTTPSConnection = LocalConnection
class Opener:
    def open(self, request, timeout=None):
        url = request.full_url if hasattr(request, "full_url") else request
        parts = urllib.parse.urlsplit(url)
        target = "http://127.0.0.1:" + str(port) + parts.path + (("?" + parts.query) if parts.query else "")
        return real_opener().open(target, timeout=timeout)
urllib.request.build_opener = lambda *handlers: Opener()
from ci_evidence import capture, cli, store
if os.environ.get("EVIDENCE_TEST_POLICY") == "changed":
    cli.POLICIES["verify"] = dict(cli.POLICIES["verify"], fixture_revision=2)
mode = os.environ.get("EVIDENCE_TEST_KILL", "")
def kill():
    os.kill(os.getpid(), signal.SIGKILL)
if mode == "published-zip":
    original = capture.publish_bytes
    def capture_hook(path, data, **kwargs):
        if str(path).endswith("/artifacts/2002.json"):
            kill()
        return original(path, data, **kwargs)
    capture.publish_bytes = capture_hook
if mode == "verified":
    original_verify = cli.verify_observation
    def verify_hook(*args, **kwargs):
        value = original_verify(*args, **kwargs)
        kill()
        return value
    cli.verify_observation = verify_hook
if mode == "final-get":
    original_final = cli.final_authority
    def final_hook(*args, **kwargs):
        value = original_final(*args, **kwargs)
        kill()
        return value
    cli.final_authority = final_hook
if mode in ("current-before", "current-after"):
    original_publish = store.publish_bytes
    def pointer_hook(path, data, **kwargs):
        if str(path).endswith("/current-evidence.json"):
            if mode == "current-before":
                kill()
            value = original_publish(path, data, **kwargs)
            kill()
            return value
        return original_publish(path, data, **kwargs)
    store.publish_bytes = pointer_hook
if os.environ.get("EVIDENCE_TEST_PUBLICATION_DRIFT"):
    original_publish = store.publish_bytes
    def drift_hook(path, data, **kwargs):
        result = original_publish(path, data, **kwargs)
        if str(path).endswith("/verification.json"):
            if os.environ["EVIDENCE_TEST_PUBLICATION_DRIFT"] == "binary":
                binary = sys.argv[sys.argv.index("--profile-verifier") + 1]
                replacement = binary + ".replacement"
                with open(binary, "rb") as original, open(replacement, "wb") as changed:
                    changed.write(original.read() + b"\\0")
                os.chmod(replacement, 0o700)
                os.replace(replacement, binary)
            else:
                cli.POLICIES["verify"] = dict(cli.POLICIES["verify"], publication_drift=2)
        return result
    store.publish_bytes = drift_hook
raise SystemExit(cli.main(sys.argv[2:]))
'''
            env = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}
            env.update(PYTHONPATH=str(root / 'scripts'), PATH=str(agent / 'bin') + os.pathsep + os.environ['PATH'],
                       GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)
            env.pop('GOFLAGS', None)
            env.pop('EVIDENCE_TEST_KILL', None)
            env.pop('EVIDENCE_TEST_POLICY', None)
            env.pop('EVIDENCE_TEST_PUBLICATION_DRIFT', None)
            common = [sys.executable, '-B', '-c', wrapper, str(server.server_port)]
            command = ['source', '--context', str(request_path), '--case', str(case), '--profile-verifier', str(verifier)]
            process = subprocess.Popen(common + command + ['--make-current'], cwd=root, env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                if not entered.wait(30):
                    out, err = process.communicate(timeout=10)
                    self.fail('download boundary not reached: ' + out.decode() + err.decode())
                process.kill()
                process.communicate(timeout=10)
            finally:
                release.set()
                if process.poll() is None:
                    process.kill()
                    process.communicate(timeout=10)
            invocations = list((case / 'invocations').glob('*/command.json'))
            self.assertEqual(len(invocations), 1)
            self.assertIsNone(json.loads(invocations[0].read_bytes())['exit'])
            original_command = invocations[0].read_bytes()
            completed = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
            self.assertEqual(completed.returncode, 0, completed.stderr.decode(errors='replace'))
            self.assertEqual(json.loads(completed.stdout)['state'], 'complete')
            self.assertEqual(invocations[0].read_bytes(), original_command)
            sidecars = list(invocations[0].parent.glob('interruption-*.json'))
            self.assertEqual(len(sidecars), 1)
            interruption = json.loads(sidecars[0].read_bytes())
            self.assertEqual(interruption['invocation'], invocations[0].parent.name)
            self.assertEqual(interruption['command_sha256'], hashlib.sha256(original_command).hexdigest())
            self.assertIsNone(interruption['actual_exit'])
            self.assertIsNone(interruption['ended_at'])
            resumed_command = case / 'invocations' / interruption['recovery_invocation'] / 'command.json'
            self.assertEqual(json.loads(resumed_command.read_bytes())['exit'], 0)
            self.assertEqual(counts[blocked_path], 2)
            self.assertEqual(counts['/repos/fixture/example/actions/artifacts/2001/zip'], 1)
            before = dict(counts)
            files_before = {path.relative_to(case).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
                            for path in case.rglob('*') if path.is_file()}
            expired = {key: routes.pop(key) for key in list(routes) if '/actions/artifacts/' in key}
            try:
                offline = subprocess.run(common + ['verify', '--case', str(case), '--profile-verifier', str(verifier)],
                                         cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                         timeout=60, close_fds=True)
            finally:
                routes.update(expired)
            self.assertEqual(offline.returncode, 0, offline.stderr.decode(errors='replace'))
            self.assertEqual(json.loads(offline.stdout)['verification'], 'passed')
            self.assertEqual(dict(counts), before)
            self.assertEqual({path.relative_to(case).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
                              for path in case.rglob('*') if path.is_file()}, files_before)
            module = root / 'scripts/ci_evidence/source.py'
            original_module = module.read_bytes()
            original_summary = (case / 'summary.json').read_bytes()
            module.write_bytes(original_module + b'\n# fixture dirty verifier\n')
            try:
                readonly = subprocess.run(common + ['verify', '--case', str(case), '--profile-verifier', str(verifier)],
                                          cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                          timeout=60, close_fds=True)
                self.assertEqual(readonly.returncode, 6)
                self.assertEqual(json.loads(readonly.stdout)['verification'], 'passed')
                self.assertEqual((case / 'summary.json').read_bytes(), original_summary)
                self.assertEqual(dict(counts), before)
            finally:
                module.write_bytes(original_module)
            # A committed comment leaves the AST projection unchanged but changes adoption identity.
            module.write_bytes(original_module + b'\n# committed fixture drift\n')
            git_env = dict(env, GIT_AUTHOR_NAME='Fixture', GIT_AUTHOR_EMAIL='fixture@example.invalid',
                           GIT_COMMITTER_NAME='Fixture', GIT_COMMITTER_EMAIL='fixture@example.invalid')
            def commit_module(message):
                subprocess.run(['git', 'add', '--', 'scripts/ci_evidence/source.py'], cwd=root, env=git_env,
                               check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
                subprocess.run(['git', 'commit', '--quiet', '-m', message], cwd=root, env=git_env,
                               check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
            commit_module('fixture tool drift')
            try:
                stale_committed = subprocess.run(common + ['status', '--case', str(case)], cwd=root, env=env,
                                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
                self.assertEqual(stale_committed.returncode, 0)
                self.assertEqual(json.loads(stale_committed.stdout)['state'], 'stale')
            finally:
                module.write_bytes(original_module)
                commit_module('restore fixture tool')
            protected = '/repos/fixture/example/branches/main/protection'
            branch = '/repos/fixture/example/branches/main'
            self.ignored_nested_checkout(root)
            index_before = (root / '.git/index').read_bytes()
            files_before_refresh = {path.relative_to(case).as_posix(): path.read_bytes()
                                    for path in case.rglob('*') if path.is_file()}
            live = subprocess.run(common + ['status', '--case', str(case), '--refresh'], cwd=root, env=env,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(live.returncode, 0)
            self.assertEqual((json.loads(live.stdout)['state'], json.loads(live.stdout)['verification'],
                              json.loads(live.stdout)['live_eligibility']),
                             ('complete', 'passed', 'eligible_at_observation'))
            self.assertEqual((root / '.git/index').read_bytes(), index_before)
            self.assertEqual({path.relative_to(case).as_posix(): path.read_bytes()
                              for path in case.rglob('*') if path.is_file()}, files_before_refresh)
            self.assertEqual(json.loads(completed.stdout)['live_eligibility'], 'eligible_at_observation')
            original_protection = routes[protected]
            altered = json.loads(original_protection)
            altered['required_status_checks']['checks'][0]['app_id'] = 1
            routes[protected] = context.canonical(altered)
            try:
                blocked = subprocess.run(common + ['status', '--case', str(case), '--refresh'], cwd=root, env=env,
                                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(json.loads(blocked.stdout)['live_eligibility'], 'blocked')
                collected_blocked = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
                self.assertEqual(collected_blocked.returncode, 0, collected_blocked.stderr.decode(errors='replace'))
                self.assertEqual((json.loads(collected_blocked.stdout)['state'], json.loads(collected_blocked.stdout)['live_eligibility']),
                                 ('complete', 'blocked'))
            finally:
                routes[protected] = original_protection
            del routes[protected]
            try:
                collected_unknown = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
                self.assertEqual(collected_unknown.returncode, 0, collected_unknown.stderr.decode(errors='replace'))
                self.assertEqual((json.loads(collected_unknown.stdout)['state'], json.loads(collected_unknown.stdout)['live_eligibility']),
                                 ('complete', 'unknown'))
            finally:
                routes[protected] = original_protection
            run_route = '/repos/fixture/example/actions/runs/123'
            original_run = routes[run_route]
            failed_run = json.loads(original_run)
            failed_run['conclusion'] = 'failure'
            routes[run_route] = context.canonical(failed_run)
            try:
                protection_reads = counts[protected]
                blocked = subprocess.run(common + ['status', '--case', str(case), '--refresh'], cwd=root, env=env,
                                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(json.loads(blocked.stdout)['live_eligibility'], 'blocked')
                self.assertGreater(counts[protected], protection_reads)
            finally:
                routes[run_route] = original_run
            original_branch = routes[branch]
            routes[branch] = context.canonical({'commit': {'sha': 'c' * 40}})
            try:
                drift = subprocess.run(common + ['status', '--case', str(case), '--refresh'], cwd=root, env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(json.loads(drift.stdout)['state'], 'stale')
            finally:
                routes[branch] = original_branch
            summary_before = json.loads((case / 'summary.json').read_bytes())
            old_receipts = set((case / 'receipts').iterdir())
            with verifier.open('ab') as binary:
                binary.write(b'\0')
            stale = subprocess.run(common + ['status', '--case', str(case)], cwd=root, env=env,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
            self.assertEqual(stale.returncode, 0, stale.stderr.decode(errors='replace'))
            self.assertEqual(json.loads(stale.stdout)['state'], 'stale')
            artifact_counts = {path: count for path, count in counts.items() if '/actions/artifacts/' in path}
            reverified = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
            self.assertEqual(reverified.returncode, 0, reverified.stderr.decode(errors='replace'))
            self.assertEqual({path: count for path, count in counts.items() if '/actions/artifacts/' in path}, artifact_counts)
            summary_after = json.loads((case / 'summary.json').read_bytes())
            self.assertEqual(summary_before['receipts']['capture'], summary_after['receipts']['capture'])
            self.assertEqual(summary_before['receipts']['authenticate'], summary_after['receipts']['authenticate'])
            self.assertNotEqual(summary_before['receipts']['verify'], summary_after['receipts']['verify'])
            self.assertTrue(old_receipts.issubset(set((case / 'receipts').iterdir())))
            baseline = json.loads((case / 'summary.json').read_bytes())
            old_receipts = set((case / 'receipts').iterdir())
            policy_env = dict(env, EVIDENCE_TEST_POLICY='changed')
            artifact_counts = {path: count for path, count in counts.items() if '/actions/artifacts/' in path}
            policy_resume = subprocess.run(common + command + ['--resume'], cwd=root, env=policy_env,
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
            self.assertEqual(policy_resume.returncode, 0, policy_resume.stderr.decode(errors='replace'))
            self.assertEqual({path: count for path, count in counts.items() if '/actions/artifacts/' in path}, artifact_counts)
            updated = json.loads((case / 'summary.json').read_bytes())
            self.assertEqual(baseline['receipts']['capture'], updated['receipts']['capture'])
            self.assertEqual(baseline['receipts']['authenticate'], updated['receipts']['authenticate'])
            self.assertNotEqual(baseline['receipts']['verify'], updated['receipts']['verify'])
            self.assertTrue(old_receipts.issubset(set((case / 'receipts').iterdir())))
            reset_policy = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
            self.assertEqual(reset_policy.returncode, 0, reset_policy.stderr.decode(errors='replace'))
            # Mutate actual copied inputs and require cached acquisition receipts to survive.
            for relative, old, new, changed_stage in (
                ('scripts/ci_evidence/source.py', '    """Reauthenticate saved original inputs and run the actual Go verifier offline."""',
                 '    """Reauthenticate saved original inputs and run the actual Go verifier offline."""\n    if False: raise RuntimeError("fixture")', 'verify'),
                ('scripts/ci_evidence/cli.py', "'name': 'complete-source-or-explicit-snapshot'",
                 "'name': 'complete-source-or-explicit-snapshot-v2'", 'verify'),
                ('scripts/ci_evidence/cli.py', "'name': 'artifact-origin-producer-window-digest'",
                 "'name': 'artifact-origin-producer-window-digest-v2'", 'authenticate'),
                ('backend/cmd/verify-ci-profile/main.go', 'package main', 'package main\n// changed build input', 'verify'),
            ):
                with self.subTest(actual_input=relative, stage=changed_stage):
                    path = root / relative
                    original = path.read_text()
                    self.assertIn(old, original)
                    original_summary = json.loads((case / 'summary.json').read_bytes())
                    original_refs = dict(original_summary['receipts'])
                    original_capture = (case / 'receipts' / original_refs['capture']['path']).read_bytes()
                    original_auth = (case / 'receipts' / original_refs['authenticate']['path']).read_bytes()
                    acquisition = {key: value for key, value in counts.items() if '/actions/artifacts/' in key or key.endswith('/logs')}
                    path.write_text(original.replace(old, new, 1))
                    try:
                        changed = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
                        self.assertEqual(changed.returncode, 0, changed.stderr.decode(errors='replace'))
                        updated = json.loads((case / 'summary.json').read_bytes())
                        self.assertEqual(updated['receipts']['capture'], original_refs['capture'])
                        self.assertEqual((case / 'receipts' / original_refs['capture']['path']).read_bytes(), original_capture)
                        if changed_stage == 'verify':
                            self.assertEqual(updated['receipts']['authenticate'], original_refs['authenticate'])
                            self.assertEqual((case / 'receipts' / original_refs['authenticate']['path']).read_bytes(), original_auth)
                        else:
                            self.assertNotEqual(updated['receipts']['authenticate'], original_refs['authenticate'])
                        self.assertNotEqual(updated['receipts'][changed_stage], original_refs[changed_stage])
                        self.assertEqual({key: value for key, value in counts.items() if '/actions/artifacts/' in key or key.endswith('/logs')}, acquisition)
                    finally:
                        path.write_text(original)
                    restored = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
                    self.assertEqual(restored.returncode, 0, restored.stderr.decode(errors='replace'))
                    self.assertEqual(json.loads((case / 'summary.json').read_bytes())['receipts']['verify'], original_refs['verify'])
            module = root / 'scripts/ci_evidence/source.py'
            original = module.read_text()
            prior = json.loads((case / 'summary.json').read_bytes())
            logs_path = '/repos/fixture/example/actions/runs/123/attempts/1/logs'
            previous_logs = counts[logs_path]
            module.write_text(original.replace('def _time(value):', 'def _time(value):\n    if False: pass', 1))
            try:
                changed = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
                self.assertEqual(changed.returncode, 0, changed.stderr.decode(errors='replace'))
                after = json.loads((case / 'summary.json').read_bytes())
                self.assertNotEqual(after['receipts']['capture'], prior['receipts']['capture'])
                self.assertEqual(counts[logs_path], previous_logs + 1)
            finally:
                module.write_text(original)
            restored = subprocess.run(common + command + ['--resume'], cwd=root, env=env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
            self.assertEqual(restored.returncode, 0, restored.stderr.decode(errors='replace'))
            for mutation in ('binary', 'policy'):
                with self.subTest(publication_drift=mutation):
                    target = agent / 'cases' / ('publication-drift-' + mutation)
                    race_command = ['source', '--context', str(request_path), '--case', str(target),
                                    '--profile-verifier', str(verifier)]
                    original_binary = verifier.read_bytes()
                    race_env = dict(env, EVIDENCE_TEST_PUBLICATION_DRIFT=mutation)
                    try:
                        raced = subprocess.run(common + race_command, cwd=root, env=race_env,
                                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120, close_fds=True)
                        self.assertEqual(raced.returncode, 6, raced.stderr.decode(errors='replace'))
                        self.assertEqual(json.loads(raced.stdout)['state'], 'stale')
                        self.assertTrue(list((target / 'raw').glob('*/verification.json')))
                        self.assertFalse(list((target / 'receipts').glob('verify-*.json')))
                        self.assertFalse(list((target / 'receipts').glob('finalize-*.json')))
                        summary = json.loads((target / 'summary.json').read_bytes())
                        self.assertNotEqual(summary['state'], 'complete')
                        self.assertNotEqual(summary['live_eligibility'], 'eligible_at_observation')
                        if mutation == 'binary':
                            self.assertNotEqual(verifier.read_bytes(), original_binary)
                    finally:
                        verifier.write_bytes(original_binary)
                        verifier.chmod(0o700)
            helper = agent / 'bin/holding-helper'
            helper.write_text('#!' + sys.executable + '\nimport urllib.request\nurllib.request.urlopen("http://127.0.0.1:' +
                              str(server.server_port) + '/helper-hold", timeout=30).read()\n')
            helper.chmod(0o700)
            held_case = agent / 'cases/helper-lock'
            held = subprocess.Popen(common + ['source', '--context', str(request_path), '--case', str(held_case),
                                              '--profile-verifier', str(helper)], cwd=root, env=env,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                if not helper_ready.wait(30):
                    out, err = held.communicate(timeout=10)
                    self.fail('helper boundary not reached: ' + out.decode() + err.decode())
                current_before = (root / '.agent-state/current-evidence.json').read_bytes()
                blocked_case = '.tmp/agent/cases/blocked-writer'
                blocked = subprocess.run(common + ['source', '--context', str(request_path), '--case', blocked_case,
                                                   '--profile-verifier', str(verifier)], cwd=root, env=env,
                                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
                self.assertEqual(blocked.returncode, 7)
                self.assertFalse((root / blocked_case).exists())
                self.assertEqual((root / '.agent-state/current-evidence.json').read_bytes(), current_before)
                held.kill()
                held.communicate(timeout=10)
                with (root / '.agent-state/evidence.lock').open('rb') as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    fcntl.flock(lock, fcntl.LOCK_UN)
            finally:
                helper_release.set()
                if held.poll() is None:
                    held.kill()
                    held.communicate(timeout=10)
            for mode in ('published-zip', 'verified', 'final-get', 'current-before', 'current-after'):
                with self.subTest(crash=mode):
                    target = agent / 'cases' / mode
                    cmd = ['source', '--context', str(request_path), '--case', str(target),
                           '--profile-verifier', str(verifier)]
                    pointer = root / '.agent-state/current-evidence.json'
                    old_pointer = pointer.read_bytes()
                    pointer_mode = mode.startswith('current-')
                    fault_env = dict(env, EVIDENCE_TEST_KILL=mode)
                    interrupted = subprocess.run(common + cmd + (['--make-current'] if pointer_mode else []),
                                                 cwd=root, env=fault_env, stdout=subprocess.PIPE,
                                                 stderr=subprocess.PIPE, timeout=120, close_fds=True)
                    self.assertEqual(interrupted.returncode, -9, interrupted.stderr.decode(errors='replace'))
                    if mode == 'current-before':
                        self.assertEqual(pointer.read_bytes(), old_pointer)
                    if mode == 'current-after':
                        self.assertEqual(json.loads(pointer.read_bytes())['case'], target.relative_to(root).as_posix())
                    original_commands = list((target / 'invocations').glob('*/command.json'))
                    self.assertEqual(len(original_commands), 1)
                    original_bytes = original_commands[0].read_bytes()
                    self.assertIsNone(json.loads(original_bytes)['exit'])
                    downloaded = counts[blocked_path]
                    resumed = subprocess.run(common + cmd + ['--resume'] + (['--make-current'] if pointer_mode else []),
                                             cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                             timeout=120, close_fds=True)
                    self.assertEqual(resumed.returncode, 0, resumed.stderr.decode(errors='replace'))
                    self.assertEqual(json.loads(resumed.stdout)['state'], 'complete')
                    self.assertEqual(original_commands[0].read_bytes(), original_bytes)
                    sidecars = list(original_commands[0].parent.glob('interruption-*.json'))
                    self.assertEqual(len(sidecars), 1)
                    interruption = json.loads(sidecars[0].read_bytes())
                    self.assertEqual(interruption['command_sha256'], hashlib.sha256(original_bytes).hexdigest())
                    self.assertIsNone(interruption['actual_exit'])
                    self.assertIsNone(interruption['ended_at'])
                    self.assertEqual(json.loads((target / 'invocations' / interruption['recovery_invocation'] /
                                                 'command.json').read_bytes())['exit'], 0)
                    if mode in ('published-zip', 'verified', 'final-get'):
                        self.assertEqual(counts[blocked_path], downloaded)
            prefix = '/repos/fixture/example/'
            artifacts_route = prefix + 'actions/runs/123/artifacts'
            jobs_route = prefix + 'actions/runs/123/attempts/1/jobs'
            run_route = prefix + 'actions/runs/123'
            workflow_route = prefix + 'contents/.github/workflows/ci.yml'
            event_route = prefix + 'actions/artifacts/2001/zip'
            target = agent / 'cases/remote-failure'
            cmd = ['source', '--context', str(request_path), '--case', str(target), '--profile-verifier', str(verifier)]
            original_run = routes[run_route]
            failed_run = json.loads(original_run)
            failed_run['conclusion'] = 'failure'
            routes[run_route] = context.canonical(failed_run)
            try:
                failed = subprocess.run(common + cmd, cwd=root, env=env, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(failed.returncode, 9)
            finally:
                routes[run_route] = original_run
            first = next((target / 'invocations').glob('*/command.json'))
            failed_bytes = first.read_bytes()
            self.assertEqual(json.loads(failed_bytes)['exit'], 9)
            recovered = subprocess.run(common + cmd + ['--resume'], cwd=root, env=env, stdout=subprocess.PIPE,
                                       stderr=subprocess.PIPE, timeout=120, close_fds=True)
            self.assertEqual(recovered.returncode, 0, recovered.stderr.decode(errors='replace'))
            self.assertEqual(first.read_bytes(), failed_bytes)
            self.assertFalse(list(first.parent.glob('interruption-*.json')))
            self.assertEqual(sorted(json.loads(path.read_bytes())['exit'] for path in
                                    (target / 'invocations').glob('*/command.json')), [0, 9])
            cases = ('origin', 'producer', 'window', 'attempt', 'bad-zip', 'record', 'workflow', 'pagination')
            for mutation in cases:
                with self.subTest(invalid=mutation):
                    saved = dict(routes)
                    try:
                        listing = json.loads(routes[artifacts_route])
                        if mutation == 'origin':
                            listing['artifacts'][0]['workflow_run']['repository_id'] = 99
                        elif mutation == 'window':
                            listing['artifacts'][0]['created_at'] = '2025-01-01T00:00:00Z'
                        elif mutation == 'pagination':
                            listing['total_count'] += 1
                        elif mutation == 'bad-zip':
                            routes[blocked_path] = b'not a zip'
                            listing['artifacts'][1]['digest'] = 'sha256:' + hashlib.sha256(routes[blocked_path]).hexdigest()
                        elif mutation == 'record':
                            with zipfile.ZipFile(io.BytesIO(routes[event_route])) as archive:
                                record = json.loads(archive.read('record.json'))
                            record['repository_id'] = 99
                            routes[event_route] = zip_bytes({'record.json': context.canonical(record)})
                            listing['artifacts'][0]['digest'] = 'sha256:' + hashlib.sha256(routes[event_route]).hexdigest()
                        routes[artifacts_route] = context.canonical(listing)
                        if mutation == 'producer':
                            jobs = json.loads(routes[jobs_route])
                            producer = next(job for job in jobs['jobs'] if job['name'] == 'CI Event Classification')
                            next(step for step in producer['steps'] if step['name'] == 'Upload CI event record')['conclusion'] = 'failure'
                            routes[jobs_route] = context.canonical(jobs)
                        if mutation == 'attempt':
                            run = json.loads(routes[run_route])
                            run['run_attempt'] = 2
                            routes[run_route] = context.canonical(run)
                        if mutation == 'workflow':
                            data = b'changed workflow\n'
                            routes[workflow_route] = context.canonical({'encoding': 'base64', 'content': base64.b64encode(data).decode(),
                                'sha': hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()})
                        target = agent / 'cases' / ('invalid-' + mutation)
                        cmd = ['source', '--context', str(request_path), '--case', str(target), '--profile-verifier', str(verifier)]
                        rejected = subprocess.run(common + cmd, cwd=root, env=env, stdout=subprocess.PIPE,
                                                  stderr=subprocess.PIPE, timeout=90, close_fds=True)
                        self.assertNotEqual(rejected.returncode, 0)
                        self.assertNotEqual(json.loads(rejected.stdout)['command_exit'], 0)
                    finally:
                        routes.clear()
                        routes.update(saved)
        finally:
            release.set()
            helper_release.set()
            server.shutdown()
            server.server_close()
            thread.join(timeout=10)

    def test_snapshot_pending_terminal_and_integrity_boundaries(self):
        fixture = ROOT / 'scripts/testdata/ci-evidence/snapshots.zip'
        root = self.directory()
        observations = {}
        contexts = {}
        with zipfile.ZipFile(fixture) as archive:
            for label in ('pending', 'terminal'):
                case = root / label
                observation = case / 'raw' / 'observation'
                observation.mkdir(parents=True)
                (case / 'artifacts').mkdir()
                for info in archive.infolist():
                    if not info.filename.startswith(label + '/'):
                        continue
                    name = info.filename.split('/', 1)[1]
                    if name.startswith('candidate-'):
                        artifact_id = name.rsplit('-', 1)[1].removesuffix('.zip')
                        (case / 'artifacts' / (artifact_id + '.zip')).write_bytes(archive.read(info))
                    else:
                        (observation / name).write_bytes(archive.read(info))
                records = [json.loads(path.read_bytes()) for path in observation.glob('record-*.json')]
                source_record = next(item for item in records if item['mode'] == 'source')
                metadata = next(item for item in records if item['mode'] == 'metadata')
                runs = [json.loads(path.read_bytes()) for path in observation.glob('run-*.json')
                        if not path.name.startswith('run-final-')]
                title = next(item for item in runs if item['path'] == '.github/workflows/pr-metadata.yml')
                pr = source_record['pull_request']
                workflow = (observation / 'workflow.yml').read_bytes()
                blob = hashlib.sha1(b'blob ' + str(len(workflow)).encode() + b'\0' + workflow).hexdigest()
                contents = {'encoding': 'base64', 'content': base64.b64encode(workflow).decode(), 'sha': blob}
                refs = [(pr['head_sha'], 'workflow.yml')]
                refs += [(sha, 'workflow-record-' + sha + '.yml') for sha in sorted({record['workflow_sha'] for record in records})]
                request_entries = []
                for index, (ref, name) in enumerate(refs):
                    (observation / name).write_bytes(workflow)
                    (observation / (name + '.api.json')).write_bytes(context.canonical(contents))
                    response = 'response-' + format(index, '032x') + '.json'
                    (observation / response).write_bytes(context.canonical(contents))
                    request_entries.append({'method': 'GET', 'path': '/repos/' + source_record['repository'] +
                                            '/contents/.github/workflows/ci.yml?ref=' + ref, 'raw': response, 'link': None})
                (observation / 'requests.json').write_bytes(context.canonical(request_entries))
                contexts[label] = {'repository': source_record['repository'], 'repository_id': source_record['repository_id'],
                    'workflow_head_sha': pr['head_sha'], 'workflow_blob': blob, 'expected': 'pending' if label == 'pending' else 'success',
                    'source': {'run_id': source_record['run_id'], 'attempt': source_record['run_attempt'],
                               'event': 'pull_request', 'head_sha': pr['head_sha'], 'branch': pr['head_ref'],
                               'checkout_sha': source_record['checkout_sha'], 'checkout_tree': source_record['checkout_tree']},
                    'pr': {'number': pr['number'], 'head_ref': pr['head_ref'], 'head_sha': pr['head_sha'],
                           'base_ref': pr['base_ref'], 'base_sha': pr['base_sha'], 'merge_sha': pr['merge_sha']},
                    'metadata': {'run_id': metadata['run_id'], 'attempt': metadata['run_attempt']},
                    'title': {'run_id': title['id'], 'attempt': title['run_attempt']}}
                observations[label] = observation
        classifier = cli.classifier()
        pending = snapshot.verify_snapshot(contexts['pending'], observations['pending'], classifier)
        self.assertEqual(pending['effective_state'], 'BLOCKED')
        self.assertTrue(pending['waiting_on_whole_run'])
        terminal = snapshot.verify_snapshot(contexts['terminal'], observations['terminal'], classifier, predecessor=pending)
        self.assertEqual((terminal['effective_state'], terminal['skipped']), ('CLEAN', 37))
        failure_case = root / 'failure'
        shutil.copytree(root / 'terminal', failure_case)
        failed = failure_case / 'raw/observation'
        failure_context = dict(contexts['terminal'], expected='failure')
        failing_ids = {terminal['groups'][0]['source_check_id']}
        failing_ids.update(group['metadata_check_id'] for group in terminal['groups'])
        for spec in (failure_context['source'], failure_context['metadata']):
            rid, attempt = spec['run_id'], spec['attempt']
            for name in (f'run-{rid}.json', f'run-final-{rid}.json'):
                path = failed / name
                run = json.loads(path.read_bytes())
                run['status'], run['conclusion'] = 'completed', 'failure'
                path.write_bytes(context.canonical(run))
            path = failed / f'jobs-{rid}-{attempt}.json'
            pages = json.loads(path.read_bytes())
            for page in pages:
                for job in page['jobs']:
                    check_id = int(job['check_run_url'].rsplit('/', 1)[1])
                    if check_id in failing_ids:
                        job['status'], job['conclusion'] = 'completed', 'failure'
                        for step in job['steps']:
                            if step['name'] == 'Verify source check':
                                step['status'], step['conclusion'] = 'completed', 'failure'
            path.write_bytes(context.canonical(pages))
        for path in list(failed.glob('checks-*.json')) + list(failed.glob('suite-*.json')):
            pages = json.loads(path.read_bytes())
            for page in pages:
                for check in page['check_runs']:
                    if check['id'] in failing_ids:
                        check['status'], check['conclusion'] = 'completed', 'failure'
            path.write_bytes(context.canonical(pages))
        graph_path = failed / 'graphql.json'
        graph = json.loads(graph_path.read_bytes())
        def mark_failure(value):
            if isinstance(value, dict):
                if value.get('__typename') == 'CheckRun' and value.get('databaseId') in failing_ids:
                    value['status'], value['conclusion'] = 'COMPLETED', 'FAILURE'
                for child in value.values():
                    mark_failure(child)
            elif isinstance(value, list):
                for child in value:
                    mark_failure(child)
        for page in graph:
            node = page['response']['data']['repository']['pullRequest']
            node['mergeStateStatus'] = 'BLOCKED'
            mark_failure(node)
        graph_path.write_bytes(context.canonical(graph))
        failure = snapshot.verify_snapshot(failure_context, failed, classifier, predecessor=pending)
        self.assertEqual((failure['mode'], failure['effective_state'], failure['skipped']), ('failure', 'BLOCKED', 37))
        bad_predecessor = json.loads(json.dumps(pending))
        bad_predecessor['groups'][0]['metadata_check_id'] += 1
        with self.assertRaises(ValueError):
            snapshot.verify_snapshot(contexts['terminal'], observations['terminal'], classifier, predecessor=bad_predecessor)

        observation = observations['terminal']
        for filename, mutate in (
            ('requests.json', lambda value: [dict(item, path=item['path'].replace('ref=', 'ref=bad')) for item in value]),
            ('workflow.yml.api.json', lambda value: dict(value, content='%%%')),
        ):
            path = observation / filename
            original = path.read_bytes()
            path.write_bytes(context.canonical(mutate(json.loads(original))))
            try:
                with self.assertRaises(store.IntegrityError):
                    snapshot.verify_snapshot(contexts['terminal'], observation, classifier, predecessor=pending)
            finally:
                path.write_bytes(original)
        record_sha = contexts['terminal']['source']['checkout_sha']
        requests_file = observation / 'requests.json'
        original = requests_file.read_bytes()
        entries = json.loads(original)
        for entry in entries:
            if entry['path'].endswith('ref=' + record_sha):
                entry['path'] += '0'
        requests_file.write_bytes(context.canonical(entries))
        try:
            with self.assertRaises(store.IntegrityError):
                snapshot.verify_snapshot(contexts['terminal'], observation, classifier, predecessor=pending)
        finally:
            requests_file.write_bytes(original)
        final = observation / ('run-final-' + str(contexts['terminal']['source']['run_id']) + '.json')
        original = final.read_bytes()
        final.unlink()
        try:
            with self.assertRaises(ValueError):
                snapshot.verify_snapshot(contexts['terminal'], observation, classifier, predecessor=pending)
        finally:
            final.write_bytes(original)
        graphql_path = observation / 'graphql.json'
        original = graphql_path.read_bytes()
        pages = json.loads(original)
        pages = [page for page in pages if page['surface'] != 'head']
        graphql_path.write_bytes(context.canonical(pages))
        try:
            with self.assertRaises(ValueError):
                snapshot.verify_snapshot(contexts['terminal'], observation, classifier, predecessor=pending)
        finally:
            graphql_path.write_bytes(original)
        source_run = json.loads((observation / ('run-' + str(contexts['terminal']['source']['run_id']) + '.json')).read_bytes())
        suite_path = observation / ('suite-' + str(source_run['check_suite_id']) + '.json')
        original = suite_path.read_bytes()
        pages = json.loads(original)
        target = terminal['groups'][0]['source_check_id']
        next(check for page in pages for check in page['check_runs'] if check['id'] == target)['app']['id'] = 1
        suite_path.write_bytes(context.canonical(pages))
        try:
            with self.assertRaises(ValueError):
                snapshot.verify_snapshot(contexts['terminal'], observation, classifier, predecessor=pending)
        finally:
            suite_path.write_bytes(original)

    def test_snapshot_cli_pending_to_terminal_and_readonly_refresh(self):
        fixture = ROOT / 'scripts/testdata/ci-evidence/snapshots.zip'
        sets = {}
        with zipfile.ZipFile(fixture) as archive:
            for label in ('pending', 'terminal'):
                sets[label] = {info.filename.split('/', 1)[1]: archive.read(info) for info in archive.infolist()
                               if info.filename.startswith(label + '/')}
        phase = {'name': 'pending'}
        counts = collections.Counter()

        def build(label):
            files = sets[label]
            def read(name):
                return json.loads(files[name])
            records = [json.loads(data) for name, data in files.items() if name.startswith('record-')]
            src = next(item for item in records if item['mode'] == 'source')
            meta = next(item for item in records if item['mode'] == 'metadata')
            runs = [json.loads(data) for name, data in files.items()
                    if name.startswith('run-') and not name.startswith('run-final-')]
            title = next(run for run in runs if run['path'] == '.github/workflows/pr-metadata.yml')
            pr = src['pull_request']
            workflow = files['workflow.yml']
            blob = hashlib.sha1(b'blob ' + str(len(workflow)).encode() + b'\0' + workflow).hexdigest()
            request = {'schema_version': 1, 'kind': 'snapshot', 'task_id': 'fixture',
                'repository': src['repository'], 'repository_id': src['repository_id'],
                'workflow_head_sha': pr['head_sha'], 'workflow_blob': blob,
                'source': {'run_id': src['run_id'], 'attempt': src['run_attempt'], 'event': 'pull_request',
                           'head_sha': pr['head_sha'], 'branch': pr['head_ref'], 'checkout_sha': src['checkout_sha'],
                           'checkout_tree': src['checkout_tree']},
                'pr': {'number': pr['number'], 'head_ref': pr['head_ref'], 'head_sha': pr['head_sha'],
                       'base_ref': pr['base_ref'], 'base_sha': pr['base_sha'], 'merge_sha': pr['merge_sha']},
                'metadata': {'run_id': meta['run_id'], 'attempt': meta['run_attempt']},
                'title': {'run_id': title['id'], 'attempt': title['run_attempt']},
                'expected': 'pending' if label == 'pending' else 'success', 'predecessor': None}
            prefix = '/repos/' + src['repository'] + '/'
            routes = {prefix + 'pulls/' + str(pr['number']): files['pr.json'],
                      prefix + 'branches/' + pr['base_ref'] + '/protection': files['protection.json'],
                      prefix + 'actions/workflows/ci.yml/runs': context.canonical(read('ci-runs.json')[0]),
                      prefix + 'actions/workflows/pr-metadata.yml/runs': context.canonical(read('title-runs.json')[0]),
                      prefix + 'contents/.github/workflows/ci.yml': context.canonical({
                          'encoding': 'base64', 'content': base64.b64encode(workflow).decode(), 'sha': blob})}
            routes[prefix + 'actions/runs'] = context.canonical({'total_count': len(runs), 'workflow_runs': runs})
            for run in runs:
                rid, attempt = run['id'], run['run_attempt']
                routes[prefix + 'actions/runs/' + str(rid)] = context.canonical(run)
                routes[prefix + f'actions/runs/{rid}/attempts/{attempt}/jobs'] = context.canonical(read(f'jobs-{rid}-{attempt}.json')[0])
                routes[prefix + f'actions/runs/{rid}/artifacts'] = context.canonical(read(f'artifacts-{rid}.json')[0])
                suite = read(f'suite-{run["check_suite_id"]}.json')[0]
                routes[prefix + f'check-suites/{run["check_suite_id"]}/check-runs'] = context.canonical(suite)
                for check in suite['check_runs']:
                    routes[prefix + 'check-runs/' + str(check['id'])] = context.canonical(check)
            for name, data in files.items():
                if name.startswith('checks-'):
                    routes[prefix + 'commits/' + name[7:-5] + '/check-runs'] = context.canonical(json.loads(data)[0])
                if name.startswith('candidate-'):
                    artifact_id = name.rsplit('-', 1)[1].removesuffix('.zip')
                    routes[prefix + 'actions/artifacts/' + artifact_id + '/zip'] = data
            latest = []
            for run, names in ((next(item for item in runs if item['id'] == src['run_id']), snapshot.NAMES),
                               (next(item for item in runs if item['id'] == meta['run_id']), snapshot.NAMES),
                               (title, ('PR Title',))):
                jobs = read(f"jobs-{run['id']}-{run['run_attempt']}.json")[0]['jobs']
                checks = read(f"suite-{run['check_suite_id']}.json")[0]['check_runs']
                for name in names:
                    job = next(item for item in jobs if item['name'] == name)
                    check_id = int(job['check_run_url'].rsplit('/', 1)[1])
                    latest.append(next(item for item in checks if item['id'] == check_id))
            # An earlier opened-title suite remains visible beside the edited title.
            opened_title = json.loads(json.dumps(latest[-1]))
            opened_title['id'] = max(check['id'] for check in latest) + 1
            opened_title['check_suite']['id'] = max(run['check_suite_id'] for run in runs) + 1
            latest.append(opened_title)
            routes[prefix + 'commits/' + pr['head_sha'] + '/check-runs?filter=latest'] = context.canonical({
                'total_count': len(latest), 'check_runs': latest})
            return request, routes, read('graphql.json')

        fixtures = {label: build(label) for label in sets}
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def send_data(self, data):
                self.send_response(200)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            def do_GET(self):
                parts = urllib.parse.urlsplit(self.path)
                path = parts.path + ('?filter=latest' if 'filter=latest' in parts.query else '')
                counts[path] += 1
                data = fixtures[phase['name']][1].get(path)
                if data is None:
                    self.send_error(404)
                else:
                    self.send_data(data)
            def do_POST(self):
                counts['graphql'] += 1
                query = json.loads(self.rfile.read(int(self.headers['Content-Length'])))['query']
                surface = 'head' if 'commits(last: 1)' in query else 'merge' if 'potentialMergeCommit' in query else 'direct'
                import re
                match = re.search(r'after: ("(?:[^"\\\\]|\\\\.)*")', query)
                cursor = json.loads(match.group(1)) if match else None
                pages = fixtures[phase['name']][2]
                entry = next(item for item in pages if item['surface'] == surface and item['after'] == cursor)
                self.send_data(context.canonical(entry['response']))
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        root = self.directory()
        try:
            prepare_checkout(root)
            agent = root / '.tmp/agent'
            (agent / 'cases').mkdir(parents=True)
            (agent / 'bin').mkdir()
            gh = agent / 'bin/gh'
            gh.write_text('#!/bin/sh\nprintf "fixture-token\\n"\n')
            gh.chmod(0o700)
            wrapper = '''import http.client, sys, urllib.parse, urllib.request
port = int(sys.argv[1])
real_opener = urllib.request.build_opener
class LocalConnection(http.client.HTTPConnection):
    def __init__(self, host, timeout=None, **kwargs):
        super().__init__("127.0.0.1", port, timeout=timeout)
http.client.HTTPSConnection = LocalConnection
class Opener:
    def open(self, request, timeout=None):
        url = request.full_url if hasattr(request, "full_url") else request
        parts = urllib.parse.urlsplit(url)
        return real_opener().open("http://127.0.0.1:" + str(port) + parts.path + (("?" + parts.query) if parts.query else ""), timeout=timeout)
urllib.request.build_opener = lambda *handlers: Opener()
from ci_evidence.cli import main
raise SystemExit(main(sys.argv[2:]))
'''
            env = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}
            env.update(PYTHONPATH=str(root / 'scripts'), PATH=str(agent / 'bin') + os.pathsep + os.environ['PATH'],
                       GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)
            env.pop('GOFLAGS', None)
            common = [sys.executable, '-B', '-c', wrapper, str(server.server_port)]
            empty = subprocess.run(common + ['status'], cwd=root, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, timeout=30, close_fds=True)
            self.assertEqual(empty.returncode, 0)
            self.assertEqual(json.loads(empty.stdout)['state'], 'none')
            self.assertFalse((root / '.agent-state').exists())
            pending_case = agent / 'cases/pending'
            pending_request = agent / 'pending.json'
            pending_request.write_bytes(context.canonical(fixtures['pending'][0]))
            for filename in ('scripts/ci_evidence/snapshot.py', 'backend/cmd/verify-ci-profile/main.go'):
                path = root / filename
                original = path.read_bytes()
                path.write_bytes(original + b'\n// dirty fixture\n' if path.suffix == '.go' else original + b'\n# dirty fixture\n')
                try:
                    rejected = subprocess.run(common + ['snapshot', '--context', str(pending_request), '--case',
                                                       str(pending_case), '--make-current'], cwd=root, env=env,
                                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
                    self.assertEqual(rejected.returncode, 6)
                    self.assertFalse((root / '.agent-state').exists())
                finally:
                    path.write_bytes(original)
            for filename, data in (('scripts/ci_evidence/untracked.py', '# fixture\n'),
                                   ('backend/cmd/verify-ci-profile/untracked.go', 'package main\n')):
                path = root / filename
                path.write_text(data)
                try:
                    rejected = subprocess.run(common + ['snapshot', '--context', str(pending_request), '--case',
                                                       str(pending_case), '--make-current'], cwd=root, env=env,
                                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
                    self.assertEqual(rejected.returncode, 6)
                    self.assertFalse((root / '.agent-state').exists())
                finally:
                    path.unlink()
            unrelated = root / 'unrelated-user-file'
            unrelated.write_text('preserve me\n')
            collected = subprocess.run(common + ['snapshot', '--context', str(pending_request), '--case', str(pending_case), '--make-current'],
                                       cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90, close_fds=True)
            self.assertEqual(collected.returncode, 0, collected.stderr.decode(errors='replace'))
            self.assertEqual(json.loads(collected.stdout)['live_eligibility'], 'blocked')
            self.assertEqual(unrelated.read_text(), 'preserve me\n')
            pending_summary = (pending_case / 'summary.json').read_bytes()
            pointer = (root / '.agent-state/current-evidence.json').read_bytes()
            phase['name'] = 'terminal'
            terminal_request = dict(fixtures['terminal'][0])
            terminal_request['predecessor'] = {'case': pending_case.relative_to(root).as_posix(),
                                               'context_sha256': hashlib.sha256((pending_case / 'context.json').read_bytes()).hexdigest()}
            request_path = agent / 'terminal.json'
            request_path.write_bytes(context.canonical(terminal_request))
            terminal_case = agent / 'cases/terminal'
            terminal = subprocess.run(common + ['snapshot', '--context', str(request_path), '--case', str(terminal_case)],
                                      cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90, close_fds=True)
            self.assertEqual(terminal.returncode, 0, terminal.stderr.decode(errors='replace'))
            self.assertEqual(json.loads(terminal.stdout)['live_eligibility'], 'eligible_at_observation')
            before = dict(counts)
            verified = subprocess.run(common + ['verify', '--case', str(terminal_case)], cwd=root, env=env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(verified.returncode, 0, verified.stderr.decode(errors='replace'))
            self.assertEqual(dict(counts), before)
            module = root / 'scripts/ci_evidence/snapshot.py'
            original = module.read_text()
            marker = '    """Verify one captured observation without network access or mutations."""'
            self.assertIn(marker, original)
            prior = json.loads((terminal_case / 'summary.json').read_bytes())
            capture_bytes = (terminal_case / 'receipts' / prior['receipts']['capture']['path']).read_bytes()
            auth_bytes = (terminal_case / 'receipts' / prior['receipts']['authenticate']['path']).read_bytes()
            acquisitions = {key: value for key, value in counts.items() if '/actions/artifacts/' in key or key.endswith('/logs')}
            module.write_text(original.replace(marker, marker + '\n    if False: raise RuntimeError("fixture")', 1))
            try:
                resumed = subprocess.run(common + ['snapshot', '--context', str(request_path), '--case', str(terminal_case), '--resume'],
                                         cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90, close_fds=True)
                self.assertEqual(resumed.returncode, 0, resumed.stderr.decode(errors='replace'))
                changed = json.loads((terminal_case / 'summary.json').read_bytes())
                self.assertEqual(changed['receipts']['capture'], prior['receipts']['capture'])
                self.assertEqual(changed['receipts']['authenticate'], prior['receipts']['authenticate'])
                self.assertNotEqual(changed['receipts']['verify'], prior['receipts']['verify'])
                self.assertEqual((terminal_case / 'receipts' / prior['receipts']['capture']['path']).read_bytes(), capture_bytes)
                self.assertEqual((terminal_case / 'receipts' / prior['receipts']['authenticate']['path']).read_bytes(), auth_bytes)
                self.assertEqual({key: value for key, value in counts.items() if '/actions/artifacts/' in key or key.endswith('/logs')}, acquisitions)
            finally:
                module.write_text(original)
            restored = subprocess.run(common + ['snapshot', '--context', str(request_path), '--case', str(terminal_case), '--resume'],
                                      cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90, close_fds=True)
            self.assertEqual(restored.returncode, 0, restored.stderr.decode(errors='replace'))
            self.assertEqual(json.loads((terminal_case / 'summary.json').read_bytes())['receipts']['verify'], prior['receipts']['verify'])
            refreshed = subprocess.run(common + ['status', '--case', str(pending_case), '--refresh'], cwd=root, env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(refreshed.returncode, 0, refreshed.stderr.decode(errors='replace'))
            self.assertEqual(json.loads(refreshed.stdout)['state'], 'stale')
            self.assertEqual((pending_case / 'summary.json').read_bytes(), pending_summary)
            self.assertEqual((root / '.agent-state/current-evidence.json').read_bytes(), pointer)
            terminal_summary = (terminal_case / 'summary.json').read_bytes()
            live = subprocess.run(common + ['status', '--case', str(terminal_case), '--refresh'], cwd=root, env=env,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(live.returncode, 0)
            self.assertEqual(json.loads(live.stdout)['live_eligibility'], 'eligible_at_observation')
            self.assertEqual((terminal_case / 'summary.json').read_bytes(), terminal_summary)
            source_view = agent / 'source-view'
            source_observation = source_view / 'raw/observation'
            source_observation.mkdir(parents=True)
            stored = terminal_case / json.loads(terminal_summary)['observation']
            rid = terminal_request['source']['run_id']
            attempt = terminal_request['source']['attempt']
            for old, new in ((f'record-{rid}-{attempt}.json', 'record.json'),
                             (f'jobs-{rid}-{attempt}.json', 'jobs.json'),
                             (f'artifacts-{rid}.json', 'artifacts.json')):
                shutil.copy2(stored / old, source_observation / new)
            source_context = dict(terminal_request, kind='source', metadata=None, title=None,
                                  expected='success', predecessor=None)
            direct_wrapper = wrapper.split('from ci_evidence.cli import main', 1)[0] + '''
import json
from pathlib import Path
from ci_evidence.cli import refresh
print(json.dumps(refresh(json.loads(sys.argv[2]), Path(sys.argv[3]), {"observation": sys.argv[4]})))
'''
            direct = [sys.executable, '-B', '-c', direct_wrapper, str(server.server_port),
                      json.dumps(source_context), str(source_view), 'raw/observation']
            current_routes = fixtures['terminal'][1]
            prefix = '/repos/' + source_context['repository'] + '/'
            latest_path = prefix + 'commits/' + source_context['source']['head_sha'] + '/check-runs?filter=latest'
            multiple_suites = current_routes[latest_path]
            listing = json.loads(multiple_suites)
            source_run = json.loads(current_routes[prefix + 'actions/runs/' + str(rid)])
            meta_run_object = json.loads(current_routes[prefix + 'actions/runs/' + str(terminal_request['metadata']['run_id'])])
            title_run_object = json.loads(current_routes[prefix + 'actions/runs/' + str(terminal_request['title']['run_id'])])
            suite_names = [(source_run['check_suite_id'], snapshot.NAMES[0]),
                           (meta_run_object['check_suite_id'], snapshot.NAMES[0]),
                           (title_run_object['check_suite_id'], 'PR Title')]
            snapshot_view = agent / 'snapshot-view'
            shutil.copytree(stored, snapshot_view / 'raw/observation')
            snapshot_direct = [sys.executable, '-B', '-c', direct_wrapper, str(server.server_port),
                               json.dumps(terminal_request), str(snapshot_view), 'raw/observation']
            accepted = subprocess.run(snapshot_direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                      stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(accepted.returncode, 0)
            self.assertEqual(json.loads(accepted.stdout), ['eligible_at_observation', False])
            ambiguous = subprocess.run(direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                       stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(ambiguous.returncode, 0)
            self.assertEqual(json.loads(ambiguous.stdout), ['blocked', False])
            protection_path = prefix + 'branches/' + terminal_request['pr']['base_ref'] + '/protection'
            original_protection = current_routes[protection_path]
            protection = json.loads(original_protection)
            protection['required_status_checks']['strict'] = False
            current_routes[protection_path] = context.canonical(protection)
            try:
                blocked = subprocess.run(snapshot_direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                         stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(blocked.returncode, 0)
                self.assertEqual(json.loads(blocked.stdout), ['blocked', False])
            finally:
                current_routes[protection_path] = original_protection
            for suite_id, name in suite_names:
                for mutation in ('missing', 'duplicate', 'replacement_id', 'wrong_app'):
                    changed = json.loads(multiple_suites)
                    checks = changed['check_runs']
                    index = next(i for i, item in enumerate(checks)
                                 if item['name'] == name and item['check_suite']['id'] == suite_id)
                    if mutation == 'missing':
                        checks.pop(index)
                    elif mutation == 'duplicate':
                        duplicate = json.loads(json.dumps(checks[index]))
                        duplicate['id'] = max(item['id'] for item in checks) + 1
                        checks.append(duplicate)
                    elif mutation == 'replacement_id':
                        checks[index]['id'] = max(item['id'] for item in checks) + 1
                    else:
                        checks[index]['app']['id'] = 1
                    changed['total_count'] = len(checks)
                    current_routes[latest_path] = context.canonical(changed)
                    try:
                        rejected = subprocess.run(snapshot_direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                                  stderr=subprocess.PIPE, timeout=60, close_fds=True)
                        self.assertEqual(rejected.returncode, 0)
                        self.assertEqual(json.loads(rejected.stdout), ['blocked', False], (suite_id, name, mutation))
                    finally:
                        current_routes[latest_path] = multiple_suites
            for role in ('source', 'metadata', 'title'):
                run_path = prefix + 'actions/runs/' + str(terminal_request[role]['run_id'])
                original_run = current_routes[run_path]
                changed = json.loads(original_run)
                changed['run_attempt'] += 1
                current_routes[run_path] = context.canonical(changed)
                try:
                    rejected = subprocess.run(snapshot_direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                              stderr=subprocess.PIPE, timeout=60, close_fds=True)
                    self.assertEqual(rejected.returncode, 0)
                    self.assertEqual(json.loads(rejected.stdout), ['blocked', True], role)
                finally:
                    current_routes[run_path] = original_run
            # Source contexts have no declared metadata/title identities and remain conservative.
            selected_suites = {meta_run_object['check_suite_id'], title_run_object['check_suite_id']}
            listing['check_runs'] = [item for item in listing['check_runs']
                                     if item['check_suite']['id'] in selected_suites]
            listing['total_count'] = len(listing['check_runs'])
            current_routes[latest_path] = context.canonical(listing)
            source_live = subprocess.run(direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                         stderr=subprocess.PIPE, timeout=60, close_fds=True)
            self.assertEqual(source_live.returncode, 0)
            self.assertEqual(json.loads(source_live.stdout), ['eligible_at_observation', False])
            current_routes = fixtures['terminal'][1]
            prefix = '/repos/' + source_context['repository'] + '/'
            meta_run = terminal_request['metadata']['run_id']
            meta_attempt = terminal_request['metadata']['attempt']
            listing_path = prefix + f'actions/runs/{meta_run}/artifacts'
            original_listing = current_routes[listing_path]
            listing = json.loads(original_listing)
            artifact = next(item for item in listing['artifacts'] if item['name'] == f'ci-event-{meta_run}-{meta_attempt}')
            zip_path = prefix + 'actions/artifacts/' + str(artifact['id']) + '/zip'
            original_zip = current_routes[zip_path]
            with zipfile.ZipFile(io.BytesIO(original_zip)) as archive:
                meta_record = json.loads(archive.read('record.json'))
            meta_record['source_run']['artifact_id'] += 1
            changed_zip = zip_bytes({'record.json': context.canonical(meta_record)})
            artifact['digest'] = 'sha256:' + hashlib.sha256(changed_zip).hexdigest()
            current_routes[listing_path] = context.canonical(listing)
            current_routes[zip_path] = changed_zip
            try:
                pointer_drift = subprocess.run(direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                               stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(pointer_drift.returncode, 0)
                self.assertEqual(json.loads(pointer_drift.stdout), ['blocked', True])
                narrowed_listing = current_routes[latest_path]
                current_routes[latest_path] = multiple_suites
                try:
                    snapshot_drift = subprocess.run(snapshot_direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                                    stderr=subprocess.PIPE, timeout=60, close_fds=True)
                    self.assertEqual(snapshot_drift.returncode, 0)
                    self.assertEqual(json.loads(snapshot_drift.stdout), ['blocked', True])
                finally:
                    current_routes[latest_path] = narrowed_listing
            finally:
                current_routes[listing_path] = original_listing
                current_routes[zip_path] = original_zip
            latest_path = prefix + 'commits/' + source_context['source']['head_sha'] + '/check-runs?filter=latest'
            original_latest = current_routes[latest_path]
            latest = json.loads(original_latest)
            latest['check_runs'][0]['app']['id'] = 1
            current_routes[latest_path] = context.canonical(latest)
            try:
                bad_app = subprocess.run(direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                         stderr=subprocess.PIPE, timeout=60, close_fds=True)
                self.assertEqual(bad_app.returncode, 0)
                self.assertEqual(json.loads(bad_app.stdout), ['blocked', False])
            finally:
                current_routes[latest_path] = original_latest
            for mutation in ('missing', 'duplicate', 'different_attempt_id'):
                latest = json.loads(original_latest)
                if mutation == 'missing':
                    latest['check_runs'].pop(0)
                elif mutation == 'duplicate':
                    duplicate = dict(latest['check_runs'][0])
                    duplicate['id'] += 999999
                    latest['check_runs'].append(duplicate)
                else:
                    latest['check_runs'][0]['id'] += 999999
                latest['total_count'] = len(latest['check_runs'])
                current_routes[latest_path] = context.canonical(latest)
                try:
                    rejected = subprocess.run(direct, cwd=root, env=env, stdout=subprocess.PIPE,
                                              stderr=subprocess.PIPE, timeout=60, close_fds=True)
                    self.assertEqual(rejected.returncode, 0)
                    self.assertNotEqual(json.loads(rejected.stdout)[0], 'eligible_at_observation', mutation)
                finally:
                    current_routes[latest_path] = original_latest
            foreign_root = self.directory()
            prepare_checkout(foreign_root)
            foreign_case = foreign_root / '.tmp/agent/cases/copied'
            foreign_case.parent.mkdir(parents=True)
            shutil.copytree(terminal_case, foreign_case)
            foreign_env = dict(env, PYTHONPATH=str(foreign_root / 'scripts'))
            foreign = subprocess.run(common + ['status', '--case', str(foreign_case)], cwd=foreign_root,
                                     env=foreign_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                     timeout=30, close_fds=True)
            self.assertEqual(foreign.returncode, 4)
            self.assertFalse((foreign_root / '.agent-state').exists())
            gh.write_text('#!/bin/sh\nexit 1\n')
            before = dict(counts)
            unknown = subprocess.run(common + ['status', '--case', str(terminal_case), '--refresh'], cwd=root, env=env,
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
            self.assertEqual(unknown.returncode, 0)
            self.assertEqual(json.loads(unknown.stdout)['live_eligibility'], 'unknown')
            self.assertEqual(dict(counts), before)
            module = root / 'scripts/ci_evidence/snapshot.py'
            original_module = module.read_bytes()
            module.write_bytes(original_module + b'\n# fixture identity change\n')
            try:
                dirty = subprocess.run(common + ['status', '--case', str(terminal_case)], cwd=root, env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, close_fds=True)
                self.assertEqual(dirty.returncode, 0)
                self.assertEqual(json.loads(dirty.stdout)['state'], 'stale')
            finally:
                module.write_bytes(original_module)
            pointer_path = root / '.agent-state/current-evidence.json'
            pointer_path.write_bytes(b'{bad')
            try:
                broken = subprocess.run(common + ['status'], cwd=root, env=env, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, timeout=30, close_fds=True)
                self.assertNotEqual(broken.returncode, 0)
            finally:
                pointer_path.write_bytes(pointer)
            pointer_path.unlink()
            try:
                missing = subprocess.run(common + ['status'], cwd=root, env=env, stdout=subprocess.PIPE,
                                         stderr=subprocess.PIPE, timeout=30, close_fds=True)
                self.assertNotEqual(missing.returncode, 0)
            finally:
                pointer_path.write_bytes(pointer)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=10)

    def test_audited_projection_uses_real_module_structure(self):
        root = self.directory()
        for name, function in (('source.py', 'verify_source'), ('snapshot.py', 'verify_snapshot')):
            path = root / name
            original = (ROOT / 'scripts/ci_evidence' / name).read_text()
            path.write_text(original)
            baseline = capture.projected_module(path, 'capture', name[:-3])
            signature = 'def ' + function + '('
            start = original.index(signature)
            body = original.index('\n', original.index('):', start)) + 1
            path.write_text(original[:body] + '    if False: pass\n' + original[body:])
            self.assertEqual(capture.projected_module(path, 'capture', name[:-3]), baseline)
            self.assertNotEqual(capture.projected_module(path, 'verify', name[:-3]),
                                capture.projected_module(ROOT / 'scripts/ci_evidence' / name, 'verify', name[:-3]))
            path.write_text(original + '\ndef newly_added():\n    return 1\n')
            self.assertNotEqual(capture.projected_module(path, 'capture', name[:-3]), baseline)
            path.write_text(original.replace(signature, 'def ' + function + '(extra, ', 1))
            self.assertNotEqual(capture.projected_module(path, 'capture', name[:-3]), baseline)
            path.write_text(original.replace(signature, '@staticmethod\n' + signature, 1))
            self.assertNotEqual(capture.projected_module(path, 'capture', name[:-3]), baseline)
            path.write_text(original + '\n' + function + ' = None\n')
            self.assertNotEqual(capture.projected_module(path, 'capture', name[:-3]), baseline)
        path = root / 'source.py'
        original = (ROOT / 'scripts/ci_evidence/source.py').read_text()
        path.write_text(original.replace("def _time(value):", "def _time(value):\n    if False: pass", 1))
        self.assertNotEqual(capture.projected_module(path, 'capture', 'source'),
                            capture.projected_module(ROOT / 'scripts/ci_evidence/source.py', 'capture', 'source'))

    def test_missing_initial_command_recovered_after_child_kill(self):
        root = self.directory()
        prepare_checkout(root)
        case = root / '.tmp/agent/cases/interrupted'
        case.parent.mkdir(parents=True)
        repository_key, workspace_key = context.workspace_identity(root)
        request = {'kind': 'source', 'task_id': 'fixture', 'repository_key': repository_key,
                   'workspace_key': workspace_key}
        code = '''import json, os, signal, sys
from pathlib import Path
from ci_evidence import store
root, case = map(Path, sys.argv[1:3])
request = json.loads(sys.argv[3])
original = store.publish_bytes
def interrupt(path, data, **kwargs):
    if Path(path).name == "command.json":
        Path(str(path) + ".part.fixture").write_bytes(b"partial")
        os.kill(os.getpid(), signal.SIGKILL)
    return original(path, data, **kwargs)
store.publish_bytes = interrupt
with store.open_case(root, case, request, resume=False, make_current=False):
    pass
'''
        env = dict(os.environ, PYTHONPATH=str(root / 'scripts'))
        child = subprocess.run([sys.executable, '-B', '-c', code, str(root), str(case), json.dumps(request)],
                               cwd=root, env=env, close_fds=True)
        self.assertEqual(child.returncode, -9)
        invocations = case / 'invocations'
        child_dir = next(invocations.iterdir())
        child_id = child_dir.name
        with store.open_case(root, case, request, resume=True, make_current=False) as opened:
            recovery_id = opened[1]
        sidecar = child_dir / ('interruption-' + recovery_id + '.json')
        saved = sidecar.read_bytes()
        record = json.loads(saved)
        self.assertEqual(record['kind'], 'missing_command')
        self.assertIsNone(record['command_sha256'])
        self.assertIsNone(record['actual_exit'])
        self.assertIsNone(record['ended_at'])
        self.assertEqual((invocations / child_id / 'command.json.part.fixture').read_bytes(), b'partial')
        store.record_interruptions(case, 'c' * 32)
        self.assertEqual(sidecar.read_bytes(), saved)
        self.assertEqual(len(list((invocations / child_id).glob('interruption-*.json'))), 1)

    def test_atomic_faults_preserve_immutable_objects_and_orphans(self):
        from unittest.mock import patch
        root = self.directory()
        case = root / 'case'
        (case / 'receipts').mkdir(parents=True)
        target = case / 'input'
        store.publish_bytes(target, b'original')
        with patch.object(store, 'publish_part_noreplace', side_effect=OSError('rename failed')):
            with self.assertRaises(OSError):
                store.publish_bytes(target, b'changed')
        self.assertEqual(target.read_bytes(), b'original')
        inputs = store.file_hashes(root, ['case/input'])
        policy_sha = context.digest(b'policy')
        real_sync = store._fsync_dir
        with patch.object(store, '_fsync_dir', side_effect=OSError('fsync failed')):
            with self.assertRaises(OSError):
                store.publish_receipt(root, case, 'capture', 'a' * 64, inputs, {}, None, policy_sha, inputs)
        self.assertEqual(len(list((case / 'receipts').glob('capture-*.json'))), 1)
        calls = []
        def sync(path):
            calls.append(Path(path))
            real_sync(path)
        with patch.object(store, '_fsync_dir', side_effect=sync):
            receipt, _ = store.publish_receipt(root, case, 'capture', 'a' * 64, inputs, {}, None, policy_sha, inputs)
        self.assertEqual(receipt['result'], 'passed')
        self.assertIn(case / 'receipts', calls)
        self.assertIn(case, calls)
        failed_case = root / 'generation-failure'
        (failed_case / 'raw').mkdir(parents=True)
        (failed_case / 'receipts').mkdir()
        summary_path = failed_case / 'summary.json'
        initial_summary = context.canonical({'state': 'incomplete', 'receipts': {}})
        summary_path.write_bytes(initial_summary)
        def fail_raw(path):
            self.assertEqual(Path(path), failed_case / 'raw')
            raise OSError('raw directory fsync failed')
        with patch.object(capture, '_fsync_dir', side_effect=fail_raw):
            with self.assertRaises(OSError):
                capture.Client(failed_case, {}, 'a' * 32, token='fixture-token')
        self.assertTrue((failed_case / 'raw' / ('a' * 32)).is_dir())
        self.assertEqual(summary_path.read_bytes(), initial_summary)
        self.assertFalse(list((failed_case / 'receipts').iterdir()))

    def test_clean_adoption_ignored_go_inputs_with_unrelated_checkout(self):
        root = self.directory()
        prepare_checkout(root)
        env = self.ignored_nested_checkout(root)
        package = root / 'backend/cmd/verify-ci-profile'
        test_file = package / 'ignored_test.go'
        production_file = package / 'ignored_production.go'
        with (root / '.git/info/exclude').open('a') as exclude:
            exclude.write('\n/backend/cmd/verify-ci-profile/ignored_test.go\n'
                          '/backend/cmd/verify-ci-profile/ignored_production.go\n')
        test_file.write_text('package main\n')
        production_file.write_text('package main\n')
        for path in (test_file, production_file):
            ignored = subprocess.run(['git', 'check-ignore', '-q', '--', path.relative_to(root).as_posix()],
                                     cwd=root, env=env, close_fds=True)
            self.assertEqual(ignored.returncode, 0)
        _, specs = context.verifier_build_inputs(root)
        reported = subprocess.run(['git', 'ls-files', '--others', '--ignored', '--exclude-standard', '-z',
                                   '--', *specs], cwd=root, env=env, check=True, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, close_fds=True).stdout.split(b'\0')
        self.assertIn(b'backend/cmd/verify-ci-profile/ignored_test.go', reported)
        self.assertIn(b'backend/cmd/verify-ci-profile/ignored_production.go', reported)
        index_before = (root / '.git/index').read_bytes()
        with self.assertRaises(context.StaleError):
            context.require_clean_adoption(root)
        self.assertEqual((root / '.git/index').read_bytes(), index_before)
        production_file.unlink()
        context.require_clean_adoption(root)
        self.assertEqual((root / '.git/index').read_bytes(), index_before)

    def test_clean_adoption_rejects_ignored_embed_directories(self):
        root = self.directory()
        prepare_checkout(root)
        env = self.ignored_nested_checkout(root)
        package = root / 'backend/cmd/verify-ci-profile'
        embedded = package / 'fixture-inputs'
        embedded.mkdir()
        (embedded / 'tracked.txt').write_text('embedded fixture\n')
        source_file = package / 'main.go'
        source_file.write_bytes(source_file.read_bytes() + b'\n//go:embed fixture-inputs\nvar fixtureInputs string\n')
        git_env = dict(env, GIT_AUTHOR_NAME='Fixture', GIT_AUTHOR_EMAIL='fixture@example.invalid',
                       GIT_COMMITTER_NAME='Fixture', GIT_COMMITTER_EMAIL='fixture@example.invalid',
                       GIT_AUTHOR_DATE='2026-01-01T00:00:00Z', GIT_COMMITTER_DATE='2026-01-01T00:00:00Z')
        subprocess.run(['git', 'add', '--', 'backend/cmd/verify-ci-profile'], cwd=root, env=git_env,
                       check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
        subprocess.run(['git', 'commit', '--quiet', '-m', 'fixture embedded directory'], cwd=root, env=git_env,
                       check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
        with (root / '.git/info/exclude').open('a') as exclude:
            exclude.write('\n/backend/cmd/verify-ci-profile/fixture-inputs/opaque/\n'
                          '/backend/cmd/verify-ci-profile/fixture-inputs/opaque_test.go/\n')
        index_before = (root / '.git/index').read_bytes()
        for name in ('opaque', 'opaque_test.go'):
            with self.subTest(name=name):
                directory = embedded / name
                directory.mkdir()
                subprocess.run(['git', 'init', '--quiet', str(directory)], env=env, check=True,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
                (directory / 'content').write_text('ignored embedded content\n')
                relative = directory.relative_to(root).as_posix() + '/'
                ignored = subprocess.run(['git', 'check-ignore', '-q', '--', relative],
                                         cwd=root, env=env, close_fds=True)
                self.assertEqual(ignored.returncode, 0)
                _, specs = context.verifier_build_inputs(root)
                reported = subprocess.run(['git', 'ls-files', '--others', '--ignored', '--exclude-standard', '-z',
                                           '--', *specs], cwd=root, env=env, check=True, stdout=subprocess.PIPE,
                                          stderr=subprocess.PIPE, close_fds=True).stdout.split(b'\0')
                self.assertIn(os.fsencode(relative), reported)
                with self.assertRaises(context.StaleError):
                    context.require_clean_adoption(root)
                self.assertEqual((root / '.git/index').read_bytes(), index_before)
                shutil.rmtree(directory)
        context.require_clean_adoption(root)
        self.assertEqual((root / '.git/index').read_bytes(), index_before)

    def test_make_current_rechecks_dirty_inputs_under_lock(self):
        root = self.directory()
        prepare_checkout(root)
        agent = root / '.tmp/agent'
        (agent / 'cases').mkdir(parents=True)
        repository_key, workspace_key = context.workspace_identity(root)
        request = {'kind': 'source', 'task_id': 'fixture', 'repository_key': repository_key,
                   'workspace_key': workspace_key}
        context.require_clean_adoption(root)
        (root / 'scripts/ci_evidence/source.py').write_text('changed after preflight\n')
        case = agent / 'cases/dirty'
        with self.assertRaises(context.StaleError):
            with store.open_case(root, case, request, resume=False, make_current=True):
                self.fail('dirty adoption accepted')
        self.assertFalse((root / '.agent-state/current-evidence.json').exists())
        record = json.loads(next((case / 'invocations').glob('*/command.json')).read_bytes())
        self.assertEqual((record['exit'], record['stderr']), (6, 'E_STALE'))

    def test_strict_json_rejects_duplicates_and_nonfinite(self):
        for raw in (b'{"a":1,"a":2}', b'{"a":NaN}', b'{"x":{"a":1,"a":2}}'):
            with self.subTest(raw=raw), self.assertRaises(context.InputError):
                context.strict_json(raw)
        self.assertEqual(context.canonical({'z': 1, 'a': 'é'}), b'{"a":"\xc3\xa9","z":1}\n')

    def test_context_identity_and_rejected_requests(self):
        request = {'schema_version': 1, 'kind': 'source', 'task_id': 'fixture',
                   'repository': 'fixture/example', 'repository_id': 1,
                   'workflow_head_sha': 'a' * 40, 'workflow_blob': 'b' * 40,
                   'source': {'run_id': 2, 'attempt': 1, 'event': 'push',
                              'head_sha': 'a' * 40, 'branch': 'main',
                              'checkout_sha': 'a' * 40, 'checkout_tree': 'c' * 40},
                   'pr': None, 'metadata': None, 'title': None,
                   'expected': 'success', 'predecessor': None}
        path = self.directory() / 'request.json'
        path.write_bytes(context.canonical(request))
        parsed = context.parse_context(path, project_root=ROOT, kind='source')
        self.assertEqual(parsed['source'], request['source'])
        self.assertEqual(len(parsed['workspace_key']), 64)
        for change in ('boolean-id', 'unknown-key', 'bad-sha', 'push-mismatch'):
            candidate = json.loads(json.dumps(request))
            if change == 'boolean-id':
                candidate['repository_id'] = True
            elif change == 'unknown-key':
                candidate['extra'] = None
            elif change == 'bad-sha':
                candidate['workflow_blob'] = 'A' * 40
            else:
                candidate['source']['checkout_sha'] = 'd' * 40
            path.write_bytes(context.canonical(candidate))
            with self.subTest(change=change), self.assertRaises(context.InputError):
                context.parse_context(path, project_root=ROOT, kind='source')

    def test_pagination_rejects_cross_repository_and_cycles(self):
        valid = '<https://api.github.com/repositories/7/actions/runs?page=2>; rel="next"'
        self.assertEqual(capture.next_page(valid, 'fixture/example', 7), '/repositories/7/actions/runs?page=2')
        for link in ('<https://api.github.com/repositories/8/actions/runs?page=2>; rel="next"',
                     '<https://other.example/repos/fixture/example/runs?page=2>; rel="next"',
                     '<http://api.github.com/repos/fixture/example/runs?page=2>; rel="next"'):
            with self.subTest(link=link), self.assertRaises(store.IntegrityError):
                capture.next_page(link, 'fixture/example', 7)

    def test_immutable_publication_and_receipt_inputs(self):
        root = self.directory()
        case = root / 'case'
        case.mkdir()
        (case / 'receipts').mkdir()
        file = case / 'input'
        store.publish_bytes(file, b'first')
        store.publish_bytes(file, b'first')
        with self.assertRaises(store.IntegrityError):
            store.publish_bytes(file, b'second')
        inputs = store.file_hashes(root, ['case/input'])
        p = context.digest(b'policy')
        receipt, receipt_hash = store.publish_receipt(root, case, 'capture', 'a' * 64, inputs, {}, None, p, inputs)
        again, again_hash = store.publish_receipt(root, case, 'capture', 'a' * 64, inputs, {}, None, p, inputs)
        self.assertEqual((receipt, receipt_hash), (again, again_hash))
        changed_policy = context.digest(b'changed policy')
        _, alternate_hash = store.publish_receipt(root, case, 'capture', 'a' * 64, inputs, {}, None, changed_policy, inputs)
        self.assertNotEqual(receipt_hash, alternate_hash)
        self.assertEqual(len(list((case / 'receipts').glob('capture-*.json'))), 2)
        other = case / 'other-input'
        store.publish_bytes(other, b'other')
        other_inputs = store.file_hashes(root, ['case/other-input'])
        _, first_verify = store.publish_receipt(root, case, 'verify', 'a' * 64, inputs,
                                                {'capture': receipt_hash}, 'b' * 64, p, {})
        _, changed_input = store.publish_receipt(root, case, 'verify', 'a' * 64, other_inputs,
                                                 {'capture': receipt_hash}, 'b' * 64, p, {})
        _, changed_dependency = store.publish_receipt(root, case, 'verify', 'a' * 64, inputs,
                                                      {'capture': alternate_hash}, 'b' * 64, p, {})
        self.assertEqual(len({first_verify, changed_input, changed_dependency}), 3)
        self.assertEqual(len(list((case / 'receipts').glob('verify-*.json'))), 3)
        file.write_bytes(b'changed')
        with self.assertRaises(store.IntegrityError):
            store.get_receipt(root, case, 'capture', 'a' * 64, inputs, {}, None, p)

    def test_lock_held_by_process_and_released_after_kill(self):
        root = self.directory()
        code = ('import sys; from pathlib import Path; from ci_evidence.store import checkout_lock; '
                'c=checkout_lock(Path(sys.argv[1])); c.__enter__(); print("locked",flush=True); '
                'sys.stdin.read(); c.__exit__(None,None,None)')
        env = dict(os.environ, PYTHONPATH=str(ROOT / 'scripts'))
        process = subprocess.Popen([sys.executable, '-B', '-c', code, str(root)], env=env,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            self.assertEqual(process.stdout.readline(), b'locked\n')
            with self.assertRaises(store.BusyError):
                with store.checkout_lock(root):
                    self.fail('concurrent lock acquired')
        finally:
            process.kill()
            process.communicate(timeout=10)
        with store.checkout_lock(root):
            pass

    def test_artifact_reuse_checks_digest_without_redownload(self):
        import hashlib
        root = self.directory()
        (root / 'artifacts').mkdir()
        archive = zip_bytes({'record.json': b'{}'})
        digest = 'sha256:' + hashlib.sha256(archive).hexdigest()
        artifact = {'id': 7, 'digest': digest}
        provenance = {'selected_id': 7, 'digest': digest, 'producer': {'id': 4}, 'upload_step': {'name': 'upload'}}
        class Client:
            downloads = 0
            def artifact_stream(self, artifact_id):
                self.downloads += 1
                return io.BytesIO(archive)
        client = Client()
        (root / 'artifacts/7.zip.part.abandoned').write_bytes(archive)
        with self.assertRaises(store.IntegrityError):
            capture.get_artifact(root, client, artifact, dict(provenance, selected_id=8))
        self.assertEqual(client.downloads, 0)
        self.assertFalse((root / 'artifacts/7.zip').exists())
        first = capture.get_artifact(root, client, artifact, provenance)
        self.assertEqual(first.read_bytes(), archive)
        self.assertEqual(capture.get_artifact(root, client, artifact, provenance), first)
        self.assertEqual(client.downloads, 1)
        first.write_bytes(b'wrong')
        with self.assertRaises(store.IntegrityError):
            capture.get_artifact(root, client, artifact, provenance)
        self.assertEqual(client.downloads, 1)

    def test_checkout_log_formats_and_ambiguity(self):
        checkout = 'a' * 40
        text = ('[command]git log -1 --format=%H\n2026-01-01 ' + checkout + '\n').encode()
        job = {'id': 1, 'name': 'Build', 'steps': [{'name': 'Checkout', 'status': 'completed', 'conclusion': 'success'}]}
        for member in ('Build/1_Checkout.txt', '1_Build.txt'):
            with self.subTest(member=member):
                result = source.strict_checkout_logs(zip_bytes({member: text}), {'Build': job}, checkout)
                self.assertEqual(result[0]['checkout_sha'], checkout)
        with self.assertRaises(RuntimeError):
            source.strict_checkout_logs(zip_bytes({'Build/1_Checkout.txt': text + text}), {'Build': job}, checkout)

    def test_real_profile_verifier_positive_and_tampering(self):
        fixture = self.directory()
        signed_profile(fixture, self.root)
        args = [str(VERIFIER), str(fixture / 'profile'), str(fixture / 'native-amd64.json'),
                str(fixture / 'native-arm64.json'), 'fixture/example', '123', '1', 'a' * 40]
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors='replace'))
        self.assertTrue(json.loads(result.stdout)['closures_verified'])
        closure = fixture / 'native-arm64.json'
        closure.write_bytes(closure.read_bytes() + b' ')
        self.assertNotEqual(subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                          close_fds=True).returncode, 0)
        closure.write_bytes(closure.read_bytes()[:-1])
        receipt = fixture / 'profile/receipt.json'
        data = json.loads(receipt.read_bytes())
        data['post_restart_pipeline_fingerprint'] = '0' * 64
        receipt.write_text(json.dumps(data))
        self.assertNotEqual(subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                          close_fds=True).returncode, 0)
        data['post_restart_pipeline_fingerprint'] = data['pipeline_fingerprint']
        receipt.write_text(json.dumps(data))
        for name in ('manifest.json', 'bundle.tar', 'attestations.v1.json', 'trust.json'):
            path = fixture / 'profile' / name
            original = path.read_bytes()
            with self.subTest(member=name):
                if name in ('manifest.json', 'trust.json'):
                    value = json.loads(original)
                    if name == 'manifest.json':
                        value['version'] = '1.0.1'
                    else:
                        value['keys'][0]['public_key'] = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
                    path.write_text(json.dumps(value))
                else:
                    path.write_bytes(original + b' ')
                self.assertNotEqual(subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                                  close_fds=True).returncode, 0)
            path.write_bytes(original)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--profile-verifier', required=True)
    args, remaining = parser.parse_known_args()
    VERIFIER = Path(args.profile_verifier).resolve(strict=True)
    unittest.main(argv=[sys.argv[0], *remaining])
