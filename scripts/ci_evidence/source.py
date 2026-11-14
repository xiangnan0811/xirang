"""Authentication of evidence from a specific workflow attempt."""
import datetime
import hashlib
import io
import json
import os
import zipfile
import re
import base64
import stat
import subprocess

from .capture import get_artifact
from .context import canonical
from .store import _fsync_dir, publish_bytes


def _time(value):
    if not isinstance(value, str) or not value.endswith('Z'):
        raise ValueError('missing or invalid UTC timestamp')
    return datetime.datetime.fromisoformat(value[:-1] + '+00:00')


def select_attempt_artifact(artifacts, jobs, run, expected_attempt, name, producer, upload_step, *, repository_id):
    if type(expected_attempt) is not int or expected_attempt <= 0 or run['run_attempt'] != expected_attempt:
        raise ValueError('current attempt mismatch')
    matching_jobs = [job for job in jobs if job['name'] == producer]
    if len(matching_jobs) != 1:
        raise ValueError('producer missing or ambiguous')
    job = matching_jobs[0]
    if (job['run_id'] != run['id'] or job['run_attempt'] != expected_attempt or
            job['head_sha'] != run['head_sha'] or
            not (job['status'] == 'in_progress' or
                 (job['status'] == 'completed' and job['conclusion'] == 'success'))):
        raise ValueError('producer identity or outcome mismatch')
    steps = [step for step in job['steps'] if step['name'] == upload_step]
    if len(steps) != 1 or steps[0]['status'] != 'completed' or steps[0]['conclusion'] != 'success':
        raise ValueError('upload step missing or unsuccessful')
    start = _time(job['started_at'])
    end = _time(job['completed_at'] if job['status'] == 'completed' else steps[0]['completed_at'])
    if end < start:
        raise ValueError('invalid producer window')
    selected, excluded = [], []
    for artifact in artifacts:
        if artifact['name'] != name or artifact['expired'] is not False:
            continue
        origin = artifact['workflow_run']
        if (origin['id'] != run['id'] or origin['repository_id'] != repository_id or
                origin['head_repository_id'] != repository_id or origin['head_sha'] != run['head_sha'] or
                origin['head_branch'] != run['head_branch']):
            raise ValueError('artifact origin mismatch')
        created = _time(artifact['created_at'])
        if start <= created <= end:
            selected.append(artifact)
        else:
            excluded.append(artifact['id'])
    if len(selected) != 1:
        raise ValueError('current attempt artifact missing or ambiguous')
    artifact = selected[0]
    if not isinstance(artifact.get('digest'), str) or not re.fullmatch(r'sha256:[0-9a-f]{64}', artifact['digest']):
        raise ValueError('artifact digest missing')
    return artifact, {'producer': job, 'upload_step': steps[0], 'window_start': start.isoformat(),
                      'window_end': end.isoformat(), 'selected_id': artifact['id'],
                      'excluded_ids': excluded, 'digest': artifact['digest']}


def replay(raw, group):
    fields = ('tests', 'tests_run', 'tests_pass', 'required', 'required_run', 'required_pass', 'required_missing', 'required_skipped', 'required_failed', 'optional_skips')
    result = dict.fromkeys(fields, 0)
    result.update(package=group['package'], package_pass=False, errors=[])
    states = {}
    selected = {test['name'] for test in group['tests']}
    pkg = 'xirang/backend/' + group['package'].removeprefix('./')
    for line in raw.splitlines():
        if len(line) >= 4 * 1024 * 1024:
            result['errors'].append('event line exceeds limit')
            continue
        try:
            event = json.loads(line)
        except (ValueError, UnicodeError):
            result['errors'].append('invalid JSON event')
            continue
        if event.get('Package') != pkg:
            result['errors'].append('unexpected package identity')
            continue
        name, action = event.get('Test', ''), event.get('Action')
        if not name:
            if action == 'pass':
                result['package_pass'] = True
            if action == 'fail':
                result['errors'].append('package failed')
            continue
        if name.split('/', 1)[0] not in selected:
            result['errors'].append('unexpected test identity')
            continue
        state = states.setdefault(name, set())
        if action == 'run':
            if state:
                result['errors'].append('invalid run sequence: ' + name)
            state.add(action)
        elif action in ('pass', 'fail', 'skip'):
            if 'run' not in state or state.intersection(('pass', 'fail', 'skip')):
                result['errors'].append('invalid terminal sequence: ' + name)
            state.add(action)
        elif action not in ('output', 'pause', 'cont'):
            result['errors'].append('invalid test event action')
    if not result['package_pass']:
        result['errors'].append('missing package pass')
    required = []
    required_paths = []
    def passed(name):
        state = states.get(name, set())
        return 'pass' in state and not state.intersection(('fail', 'skip'))
    for test in group['tests']:
        name = test['name']
        result['tests'] += 1
        result['tests_run'] += 'run' in states.get(name, set())
        result['tests_pass'] += passed(name)
        required.append(name)
        for path in test['required_paths']:
            required.append(path)
            required_paths.append(path)
            state = states.get(path, set())
            result['required'] += 1
            result['required_run'] += 'run' in state
            result['required_pass'] += passed(path)
            result['required_missing'] += 'run' not in state or not state.intersection(('pass', 'fail', 'skip'))
    for name in required:
        if 'run' not in states.get(name, set()) or not passed(name):
            result['errors'].append('required test missing or failed: ' + name)
    for name, state in states.items():
        if not state.intersection(('skip', 'fail')):
            continue
        under = any(name == path or name.startswith(path + '/') for path in required_paths)
        if under:
            result['required_skipped'] += 'skip' in state
            result['required_failed'] += 'fail' in state
            result['errors'].append('required descendant skipped or failed: ' + name)
        elif 'skip' in state:
            result['optional_skips'] += 1
        else:
            result['errors'].append('test failed: ' + name)
    result['errors'].sort()
    return result

UPLOAD_STEPS = {
    'CI Event Classification': 'Upload CI event record',
    'Frontend Test & Build': 'Upload browser execution evidence',
    'Account Recovery Browser Acceptance': 'Upload safe account recovery counts',
    'Complete Browser Walkthrough': 'Upload walkthrough and state evidence',
    'Asset Worker Runtime Closure (amd64)': 'Upload native runtime closure',
    'Asset Worker Runtime Closure (arm64)': 'Upload native runtime closure',
}

ARTIFACTS = {
    'browser-execution-evidence': ('Frontend Test & Build', {
        'e2e-default/summary.json': ('playwright.config.ts', 39, True),
        'e2e-real-backend/summary.json': ('playwright.real-backend.config.ts', 31, True),
    }),
    'account-recovery-execution-evidence': ('Account Recovery Browser Acceptance', {
        'summary.json': ('playwright.account-recovery.config.ts', 13, True),
    }),
    'walkthrough-execution-evidence': ('Complete Browser Walkthrough', {
        'walkthrough-ci/execution-summary.json': ('playwright.walkthrough.config.ts', 464, False),
        'e2e-surface-states/execution-summary.json': ('playwright.surface-states.config.ts', 94, False),
        'e2e-surface-states/summary.json': ('playwright.surface-states.config.ts', 94, True),
    }),
    'asset-worker-runtime-closure-amd64': ('Asset Worker Runtime Closure (amd64)', {}),
    'asset-worker-runtime-closure-arm64': ('Asset Worker Runtime Closure (arm64)', {}),
}

JOB_NAMES = {
    'CI Event Classification', 'Frontend Test & Build', 'Backend Test & Build',
    'Doc Freshness Check', 'PostgreSQL Migration Parity', 'Migration UTC Safety',
    'Isolated Cron Backup Roundtrip', 'Account Recovery Browser Acceptance',
    'Complete Browser Walkthrough',
    *(f'Docker Build ({arch})' for arch in ('amd64', 'arm64')),
    *(f'Asset Worker Runtime Closure ({arch})' for arch in ('amd64', 'arm64')),
    *(f'Asset Worker Build & Scan ({arch})' for arch in ('amd64', 'arm64')),
}

def require(condition, message):
    if not condition:
        raise RuntimeError(message)

def digest(raw):
    return hashlib.sha256(raw).hexdigest()

def unique(items, description):
    require(len(items) == 1, f'{description}: expected one, got {len(items)}')
    return items[0]

def archive_members(raw):
    z = zipfile.ZipFile(io.BytesIO(raw))
    names = z.namelist()
    require(len(names) == len(set(names)), 'duplicate archive members')
    return z

def assert_summary(data, config, count, nested, checkout):
    require(data.get('config') == config, f'{config}: wrong config')
    if nested:
        require(data.get('head') == checkout and data.get('exitCode') == 0 and data.get('invalidCandidate') is False,
                f'{config}: runner checkout or outcome mismatch')
        evidence = data['evidence']
    else:
        evidence = data
        # The walkthrough execution summary has no head field. Its producer's
        # independently verified checkout log supplies that identity instead.
        if 'head' in data:
            require(data['head'] == checkout, f'{config}: checkout mismatch')
    require(evidence.get('config') == config, f'{config}: evidence config mismatch')
    require(evidence['collected']['total'] == count and evidence['selected']['total'] == count,
            f'{config}: collected/selected count mismatch')
    require(evidence['notSelected']['total'] == 0 and evidence['notSelected']['tests'] == [],
            f'{config}: tests not selected')
    require(evidence['run'] == {'endStatus': 'passed', 'runnerErrors': 0,
                               'attemptsBegun': count, 'attemptsEnded': count},
            f'{config}: runner state mismatch')
    expected = {'passed': count, 'failed': 0, 'timedOut': 0, 'skipped': 0, 'interrupted': 0, 'missing': 0}
    require(evidence['first'] == expected and evidence['final'] == expected,
            f'{config}: first/final results mismatch')
    require(evidence['retries'] == {'retriedTests': 0, 'retryAttempts': 0, 'maxRetry': 0},
            f'{config}: retries occurred or unavailable')
    require(evidence['outcomes'] == {'expected': count, 'flaky': 0, 'unexpected': 0, 'skipped': 0, 'missing': 0},
            f'{config}: outcomes mismatch')
    for key in ('missing', 'nonContiguous', 'firstFailures', 'finalFailures', 'issues'):
        require(evidence[key] == [], f'{config}: {key} is not empty')
    return {'config': config, 'selected': count, 'first_passed': count,
            'checkout_evidence': 'summary head' if 'head' in data else 'producer checkout log'}

def required_step(job, name, expected='success'):
    step = unique([s for s in job['steps'] if s['name'] == name], f"{job['name']}: step {name}")
    require(step['status'] == 'completed' and step['conclusion'] == expected,
            f"{job['name']}: {name} is not {expected}")

def check_architecture(jobs):
    result = []
    for arch in ('amd64', 'arm64'):
        matrix = {
            f'Docker Build ({arch})': ['Run asset-content Nginx live gateway probe', 'Build Docker image',
                                      'Inspect local Core image', 'Scan local Core image', 'Run core-only Compose smoke'],
            f'Asset Worker Runtime Closure ({arch})': ['Build native Worker closure image',
                'Extract and validate native runtime closure', 'Upload native runtime closure'],
            f'Asset Worker Build & Scan ({arch})': ['Download amd64 runtime closure', 'Download arm64 runtime closure',
                'Run malicious fixture and sandbox unit tests', 'Build local Worker image',
                'Inspect and smoke native Worker image', 'Scan local Worker image'],
        }
        for name, steps in matrix.items():
            for step in steps:
                required_step(jobs[name], step)
            result.append({'job': name, 'id': jobs[name]['id'], 'steps': steps})
        worker = jobs[f'Asset Worker Build & Scan ({arch})']
        for step in ('Build local Core image for Worker profile smoke', 'Run complete Worker Compose profile smoke'):
            required_step(worker, step, 'success' if arch == 'amd64' else 'skipped')
    return result


def strict_checkout_logs(raw, jobs, checkout):
    results = []
    with archive_members(raw) as archive:
        for name, job in jobs.items():
            required_step(job, 'Checkout')
            per_step = re.compile(re.escape(name) + r'/[0-9]+_Checkout\.txt\Z')
            matches = [member for member in archive.namelist() if per_step.fullmatch(member)]
            if not matches:
                combined = re.compile(r'[0-9]+_' + re.escape(name) + r'\.txt\Z')
                matches = [member for member in archive.namelist() if combined.fullmatch(member)]
            member = unique(matches, name + ': exact checkout log')
            text = archive.read(member).decode('utf-8')
            shas = re.findall(r'\[command\][^\r\n]*git log -1 --format=%H\r?\n[^\r\n]*?\s([0-9a-f]{40})\s*(?:\r?\n|\Z)', text)
            require(shas == [checkout], name + ': checkout log mismatch')
            results.append({'job': name, 'id': job['id'], 'member': member, 'checkout_sha': checkout})
    return results


def verify_postgres(inventory, archive_raw):
    groups = inventory['groups']
    require(len(groups) == 22, 'PostgreSQL group inventory mismatch')
    expected = {f'{index:03d}.{suffix}' for index in range(1, len(groups) + 1)
                for suffix in ('jsonl', 'stderr', 'summary.json')}
    results = []
    with archive_members(archive_raw) as archive:
        require(set(archive.namelist()) == expected, 'PostgreSQL archive inventory mismatch')
        for index, group in enumerate(groups, 1):
            result = replay(archive.read(f'{index:03d}.jsonl'), group)
            summary = json.loads(archive.read(f'{index:03d}.summary.json'))
            require(summary.get('exit') == 0 and not summary.get('errors') and not result['errors'],
                    'PostgreSQL group failed')
            require(all(summary.get(key) == value for key, value in result.items()),
                    'PostgreSQL group summary mismatch')
            results.append(result)
    require(sum(result['tests'] for result in results) == 527, 'PostgreSQL root count mismatch')
    require(sum(result['required'] for result in results) == 895, 'PostgreSQL required count mismatch')
    for key in ('required_missing', 'required_skipped', 'required_failed', 'optional_skips'):
        require(sum(result[key] for result in results) == 0, 'PostgreSQL evidence not complete')
    return results


def validate_run_jobs(context, run, jobs):
    source = context['source']
    require(run['id'] == source['run_id'] and run['run_attempt'] == source['attempt'] and
            run['event'] == source['event'] and run['head_sha'] == source['head_sha'] and
            run['head_branch'] == source['branch'] and run['path'] == '.github/workflows/ci.yml',
            'source run identity mismatch')
    for field in ('repository', 'head_repository'):
        require(run[field]['id'] == context['repository_id'] and
                run[field]['full_name'] == context['repository'], 'source repository mismatch')
    require(run['status'] == 'completed' and run['conclusion'] == 'success', 'source run unsuccessful')
    require(len(jobs) == 15 and len({job['id'] for job in jobs}) == 15 and
            len({job['name'] for job in jobs}) == 15 and {job['name'] for job in jobs} == JOB_NAMES,
            'source job inventory mismatch')
    for job in jobs:
        require(job['run_id'] == source['run_id'] and job['run_attempt'] == source['attempt'] and
                job['head_sha'] == source['head_sha'] and job['status'] == 'completed' and
                job['conclusion'] == 'success', 'source job identity or outcome mismatch')
    return {job['name']: job for job in jobs}


def validate_source_record(context, record, classifier):
    record = classifier.validate_record(record)
    source = context['source']
    require(record['repository'] == context['repository'] and
            record['repository_id'] == context['repository_id'] and
            record['run_id'] == source['run_id'] and record['run_attempt'] == source['attempt'] and
            record['event_name'] == source['event'] and record['mode'] == 'source' and
            record['checkout_sha'] == source['checkout_sha'] and
            record['checkout_tree'] == source['checkout_tree'], 'source record identity mismatch')
    pr = context['pr']
    if pr is None:
        require(record['pull_request'] is None and record['event_ref'] == 'refs/heads/' + source['branch'],
                'source push record mismatch')
    else:
        expected = {'number': pr['number'], 'merged': False, 'merge_sha': pr['merge_sha']}
        for side in ('head', 'base'):
            expected.update({side + '_repo_id': context['repository_id'],
                             side + '_repo': context['repository'], side + '_ref': pr[side + '_ref'],
                             side + '_sha': pr[side + '_sha']})
        require(record['pull_request'] == expected and
                record['event_ref'] == 'refs/pull/' + str(pr['number']) + '/merge',
                'source pull request record mismatch')
    return record


def verify_browser_artifact(name, archive_raw, checkout):
    _, members = ARTIFACTS[name]
    results = []
    with archive_members(archive_raw) as archive:
        for member, (config, count, nested) in members.items():
            require(member in archive.namelist(), 'browser evidence member missing')
            result = assert_summary(json.loads(archive.read(member)), config, count, nested, checkout)
            result.update(artifact=name, member=member)
            results.append(result)
    return results


def collect_source(context, case, client, profile_verifier):
    """Preserve the declared successful source attempt and its authenticated inputs."""
    source = context['source']
    run_id, attempt = source['run_id'], source['attempt']
    prefix = 'repos/' + context['repository'] + '/'
    observation = client.observation

    def api(name, path, paginate=False):
        return client.rest(name, prefix + path, paginate=paginate)

    def contents(path, sha, name):
        obj = api(name + '.api.json', 'contents/' + path + '?ref=' + sha)
        require(obj.get('encoding') == 'base64' and isinstance(obj.get('content'), str), 'contents unavailable')
        raw = base64.b64decode(''.join(obj['content'].split()), validate=True)
        blob = hashlib.sha1(b'blob ' + str(len(raw)).encode('ascii') + b'\0' + raw).hexdigest()
        require(blob == obj.get('sha'), 'content blob mismatch')
        publish_bytes(observation / name, raw)
        return blob

    run = api('run.json', f'actions/runs/{run_id}')
    job_pages = api('jobs.json', f'actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100', True)
    artifact_pages = api('artifacts.json', f'actions/runs/{run_id}/artifacts?per_page=100', True)
    jobs = [job for page in job_pages for job in page['jobs']]
    artifacts = [item for page in artifact_pages for item in page['artifacts']]
    require(job_pages and all(page['total_count'] == len(jobs) for page in job_pages), 'incomplete job pages')
    require(artifact_pages and all(page['total_count'] == len(artifacts) for page in artifact_pages) and
            len({item['id'] for item in artifacts}) == len(artifacts), 'incomplete artifact pages')
    validate_run_jobs(context, run, jobs)

    from importlib.util import module_from_spec, spec_from_file_location
    classifier_path = __import__('pathlib').Path(__file__).resolve().parent.parent / 'classify-ci-event.py'
    spec = spec_from_file_location('ci_evidence_classifier', classifier_path)
    classifier = module_from_spec(spec)
    spec.loader.exec_module(classifier)

    def artifact(name, producer, step):
        selected, provenance = select_attempt_artifact(artifacts, jobs, run, attempt, name, producer, step,
                                                       repository_id=context['repository_id'])
        return get_artifact(case, client, selected, provenance)

    record_archive = artifact(f'ci-event-{run_id}-{attempt}', 'CI Event Classification', 'Upload CI event record')
    record = validate_source_record(context, classifier.archive_record(record_archive.read_bytes()), classifier)
    publish_bytes(observation / 'record.json', canonical(record))
    commit = api('checkout-commit.json', 'git/commits/' + source['checkout_sha'])
    require(commit['sha'] == source['checkout_sha'] and commit['tree']['sha'] == source['checkout_tree'],
            'checkout tree mismatch')
    require(contents('.github/workflows/ci.yml', context['workflow_head_sha'], 'workflow.yml') == context['workflow_blob'],
            'workflow input mismatch')
    require(contents('.github/workflows/ci.yml', record['workflow_sha'], 'record-workflow.yml') == context['workflow_blob'],
            'record workflow mismatch')
    contents('scripts/required-postgres-tests.json', source['checkout_sha'], 'inventory.json')
    artifact('required-postgres-evidence', 'PostgreSQL Migration Parity', 'Upload required PostgreSQL evidence')
    for name, (producer, _) in ARTIFACTS.items():
        artifact(name, producer, UPLOAD_STEPS[producer])
    profile = artifact('asset-worker-profile-signing-evidence-amd64', 'Asset Worker Build & Scan (amd64)',
                       'Upload Worker profile signing evidence')
    expected = {'manifest.json', 'bundle.tar', 'trust.json', 'runtime-closure-amd64.v1.json',
                'runtime-closure-arm64.v1.json', 'attestations.v1.json', 'receipt.json'}
    directory = observation / 'profile'
    directory.mkdir(mode=0o700)
    _fsync_dir(observation)
    with archive_members(profile.read_bytes()) as archive:
        require(set(archive.namelist()) == expected, 'profile archive inventory mismatch')
        for info in archive.infolist():
            mode = info.external_attr >> 16
            require(not info.is_dir() and not stat.S_ISLNK(mode) and
                    (stat.S_IFMT(mode) == 0 or stat.S_ISREG(mode)), 'profile archive member invalid')
            publish_bytes(directory / info.filename, archive.read(info))
    client.closure_cache = {}
    for arch in ('amd64', 'arm64'):
        path = artifact('asset-worker-runtime-closure-' + arch, 'Asset Worker Runtime Closure (' + arch + ')',
                        'Upload native runtime closure')
        with archive_members(path.read_bytes()) as archive:
            require(archive.namelist() == ['runtime-closure.v1.json'], 'native closure archive mismatch')
            closure = archive.read('runtime-closure.v1.json')
            client.closure_cache[arch] = closure
            publish_bytes(observation / ('native-' + arch + '.json'), closure)
    client.download_logs(run_id, attempt)
    final = api('final-run.json', f'actions/runs/{run_id}')
    require(all(final[key] == run[key] for key in ('id', 'run_attempt', 'head_sha', 'head_branch', 'event',
                                                   'path', 'status', 'conclusion', 'repository', 'head_repository')),
            'source run changed during capture')
    client.save_links()
    return observation


def verify_source(context, case, observation, profile_verifier, *, closure_cache=None, verifier_sha256=None):
    """Reauthenticate saved original inputs and run the actual Go verifier offline."""
    from importlib.util import module_from_spec, spec_from_file_location
    from pathlib import Path
    from .capture import next_page, verify_saved_contents

    case, observation = Path(case), Path(observation)
    source = context['source']
    run_id, attempt = source['run_id'], source['attempt']
    repo = context['repository']

    def read(name):
        return json.loads((observation / name).read_bytes())

    classifier_path = Path(__file__).resolve().parent.parent / 'classify-ci-event.py'
    spec = spec_from_file_location('ci_evidence_classifier', classifier_path)
    classifier = module_from_spec(spec)
    spec.loader.exec_module(classifier)
    run, final = read('run.json'), read('final-run.json')
    links = read('rest-pagination.json')

    def pages(name, endpoint, key):
        data = read(name)
        require(isinstance(data, list) and data, 'missing listing')
        path = '/repos/' + repo + '/' + endpoint
        for index in range(len(data)):
            entry = unique([item for item in links if item['path'] == path], 'pagination link')
            path = next_page(entry['link'], repo, context['repository_id'])
            require((path is not None) == (index + 1 < len(data)), 'pagination chain mismatch')
        items = [item for page in data for item in page[key]]
        require(all(page['total_count'] == len(items) for page in data) and
                len({item['id'] for item in items}) == len(items), 'listing incomplete')
        return items

    jobs = pages('jobs.json', f'actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100', 'jobs')
    artifacts = pages('artifacts.json', f'actions/runs/{run_id}/artifacts?per_page=100', 'artifacts')
    by_name = validate_run_jobs(context, run, jobs)
    require(all(final[key] == run[key] for key in ('id', 'run_attempt', 'head_sha', 'head_branch', 'event',
                                                   'path', 'status', 'conclusion', 'repository', 'head_repository')),
            'final source authority mismatch')

    def artifact(name, producer, step):
        selected, provenance = select_attempt_artifact(artifacts, jobs, run, attempt, name, producer, step,
                                                       repository_id=context['repository_id'])
        path = case / 'artifacts' / (str(selected['id']) + '.zip')
        require(not path.is_symlink() and path.is_file(), 'artifact missing')
        raw = path.read_bytes()
        require(selected['digest'] == 'sha256:' + digest(raw), 'artifact digest mismatch')
        stored = json.loads(path.with_suffix('.json').read_bytes())
        require(stored['artifact']['id'] == selected['id'] and stored['artifact']['digest'] == selected['digest'] and
                stored['provenance']['selected_id'] == selected['id'] and
                stored['provenance']['producer']['id'] == provenance['producer']['id'] and
                stored['zip_sha256'] == digest(raw), 'artifact provenance mismatch')
        return raw

    event_raw = artifact(f'ci-event-{run_id}-{attempt}', 'CI Event Classification', 'Upload CI event record')
    record = validate_source_record(context, classifier.archive_record(event_raw), classifier)
    require(record == read('record.json'), 'saved source record mismatch')
    commit = read('checkout-commit.json')
    require(commit['sha'] == source['checkout_sha'] and commit['tree']['sha'] == source['checkout_tree'],
            'checkout commit mismatch')

    verify_saved_contents(observation, repo, context['workflow_head_sha'], '.github/workflows/ci.yml',
                          'workflow.yml', expected_blob=context['workflow_blob'])
    verify_saved_contents(observation, repo, record['workflow_sha'], '.github/workflows/ci.yml',
                          'record-workflow.yml', expected_blob=context['workflow_blob'])
    inventory = json.loads(verify_saved_contents(observation, repo, source['checkout_sha'],
                                               'scripts/required-postgres-tests.json', 'inventory.json'))
    pg_raw = artifact('required-postgres-evidence', 'PostgreSQL Migration Parity', 'Upload required PostgreSQL evidence')
    pg = verify_postgres(inventory, pg_raw)
    checkout_logs = strict_checkout_logs((observation / 'original-logs.zip').read_bytes(), by_name, source['checkout_sha'])
    architecture = check_architecture(by_name)
    browsers = []
    closures = {}
    for name, (producer, _) in ARTIFACTS.items():
        raw = artifact(name, producer, UPLOAD_STEPS[producer])
        browsers.extend(verify_browser_artifact(name, raw, source['checkout_sha']))
        if name.startswith('asset-worker-runtime-closure-'):
            arch = name.rsplit('-', 1)[1]
            if closure_cache is not None and arch in closure_cache:
                closure = closure_cache[arch]
            else:
                with archive_members(raw) as archive:
                    require(archive.namelist() == ['runtime-closure.v1.json'], 'native closure archive mismatch')
                    closure = archive.read('runtime-closure.v1.json')
            obj = json.loads(closure)
            require(obj['schema_version'] == 1 and obj['platform'] == 'linux/' + arch and
                    isinstance(obj['files'], list) and obj['files'], 'native closure invalid')
            require((observation / ('native-' + arch + '.json')).read_bytes() == closure, 'native closure mismatch')
            closures[arch] = closure
    profile_raw = artifact('asset-worker-profile-signing-evidence-amd64', 'Asset Worker Build & Scan (amd64)',
                           'Upload Worker profile signing evidence')
    profile = observation / 'profile'
    expected = {'manifest.json', 'bundle.tar', 'trust.json', 'runtime-closure-amd64.v1.json',
                'runtime-closure-arm64.v1.json', 'attestations.v1.json', 'receipt.json'}
    require({entry.name for entry in profile.iterdir()} == expected, 'profile directory mismatch')
    with archive_members(profile_raw) as archive:
        require(set(archive.namelist()) == expected, 'profile archive mismatch')
        for info in archive.infolist():
            path = profile / info.filename
            mode = info.external_attr >> 16
            require(not info.is_dir() and not stat.S_ISLNK(mode) and
                    (stat.S_IFMT(mode) == 0 or stat.S_ISREG(mode)) and not path.is_symlink() and
                    path.is_file() and path.read_bytes() == archive.read(info), 'profile member mismatch')
    for arch in ('amd64', 'arm64'):
        require((profile / ('runtime-closure-' + arch + '.v1.json')).read_bytes() == closures[arch],
                'profile closure mismatch')
    fd = os.open(str(profile_verifier), os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    try:
        require(stat.S_ISREG(os.fstat(fd).st_mode), 'profile verifier is not a regular file')
        hasher = hashlib.sha256()
        for chunk in iter(lambda: os.read(fd, 1024 * 1024), b''):
            hasher.update(chunk)
        if verifier_sha256 is not None:
            require(hasher.hexdigest() == verifier_sha256, 'profile verifier changed')
        result = subprocess.run(['/proc/self/fd/' + str(fd), str(profile), str(observation / 'native-amd64.json'),
                                 str(observation / 'native-arm64.json'), repo, str(run_id), str(attempt),
                                 source['checkout_sha']], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                close_fds=True, pass_fds=(fd,), timeout=300)
    finally:
        os.close(fd)
    require(result.returncode == 0, 'profile verifier rejected evidence')
    profile_result = json.loads(result.stdout)
    return {'jobs': len(jobs), 'pg_groups': len(pg), 'checkout_logs': checkout_logs,
            'architecture': architecture, 'browser_summaries': browsers, 'profile': profile_result}
