"""Command-line protocol for explicitly named evidence cases."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

from . import capture, context, snapshot, source, store

ROOT = Path(__file__).resolve().parents[2]
STAGES = ('capture', 'authenticate', 'verify', 'finalize')
POLICIES = {
    'capture': {'version': 1, 'name': 'authenticated-attempt-observation'},
    'authenticate': {'version': 1, 'name': 'artifact-origin-producer-window-digest'},
    'verify': {'version': 1, 'name': 'complete-source-or-explicit-snapshot'},
    'finalize': {'version': 1, 'name': 'authoritative-final-observation'},
}




class IncompleteError(Exception):
    pass


class RemoteFailure(Exception):
    pass


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise context.InputError('invalid command arguments')


def classifier():
    spec = importlib.util.spec_from_file_location('ci_evidence_classifier', ROOT / 'scripts/classify-ci-event.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def policy(stage, kind):
    rules = dict(POLICIES[stage], kind=kind)
    if stage == 'capture':
        rules.update(json_limit=capture.JSON_LIMIT, pagination=('same-repository', 'complete'),
                     artifact_redirect='https-without-authorization')
    if stage == 'authenticate':
        rules.update(origin=('repository-id', 'run-id', 'attempt', 'head', 'branch'),
                     producer='unique-successful-upload-window', digest='sha256')
    if stage in ('verify', 'finalize'):
        if kind == 'source':
            rules.update(jobs=sorted(source.JOB_NAMES), postgres=(22, 527, 895),
                         artifacts=source.ARTIFACTS, profile_files=7,
                         browser_retries=0, browser_failures=0)
        else:
            rules.update(protected_jobs=snapshot.NAMES, app_id=15368,
                         source_steps=(14, 17, 6), pending='blocked', success='clean',
                         predecessor='same-source-and-metadata-job-check-ids')
    return context.digest(context.canonical(rules))




def identity(stage, kind, verifier, *, binary_sha256=None):
    paths = [ROOT / 'scripts/ci_evidence/__init__.py', ROOT / 'scripts/ci_evidence/context.py',
             ROOT / 'scripts/ci_evidence/store.py', ROOT / 'scripts/ci_evidence/cli.py',
             ROOT / 'scripts/ci_evidence/capture.py']
    if stage != 'finalize':
        paths.extend((ROOT / 'scripts/ci_evidence' / (kind + '.py'), ROOT / 'scripts/classify-ci-event.py'))
        if kind == 'snapshot':
            paths.append(ROOT / 'scripts/ci_evidence/source.py')
    if stage == 'finalize':
        paths.extend((ROOT / '.github/workflows/ci.yml', ROOT / '.github/workflows/pr-metadata.yml',
                      ROOT / 'scripts/required-postgres-tests.json', ROOT / 'scripts/verify-release-ci.mjs'))
    if stage == 'verify' and kind == 'source':
        if verifier is None:
            raise context.InputError('profile verifier required')
        paths.append(Path(verifier))
        build_files, _ = context.verifier_build_inputs(ROOT)
        paths.extend(build_files)
    entries = []
    for path in paths:
        if not path.is_file() or path.is_symlink():
            raise context.InputError('verifier input unavailable')
        projected = path.name == 'cli.py' or (stage in ('capture', 'authenticate') and path.name in ('source.py', 'snapshot.py'))
        if stage == 'verify' and kind == 'source' and path == Path(verifier) and binary_sha256 is not None:
            sha = binary_sha256
        else:
            sha = capture.projected_module(path, stage, kind) if projected else hashlib.sha256(path.read_bytes()).hexdigest()
        entries.append((str(path.relative_to(ROOT)), sha))
    runtime = {'implementation': sys.implementation.name, 'version': list(sys.version_info[:3]),
               'build': sys.version}
    return context.digest(context.canonical({'files': entries, 'runtime': runtime}))


def adoption_identity(kind, verifier):
    names = ['scripts/ci-evidence.py', 'scripts/classify-ci-event.py', 'scripts/verify-release-ci.mjs',
             'scripts/required-postgres-tests.json', '.github/workflows/ci.yml', '.github/workflows/pr-metadata.yml']
    names += ['scripts/ci_evidence/' + name + '.py' for name in
              ('__init__', 'context', 'store', 'capture', 'source', 'snapshot', 'cli')]
    build_files, _ = context.verifier_build_inputs(ROOT)
    paths = [ROOT / name for name in names] + build_files
    if kind == 'source':
        if verifier is None:
            raise context.InputError('profile verifier required')
        paths.append(Path(verifier))
    files = store.file_hashes(ROOT, [path.relative_to(ROOT).as_posix() for path in paths])
    return {'files': files, 'runtime': {'implementation': sys.implementation.name,
            'version': list(sys.version_info[:3]), 'build': sys.version},
            'policies': {stage: policy(stage, kind) for stage in STAGES}}


def result(kind=None, case_id=None, state='none', verification='not_run', live='not_checked',
           exit_code=0, missing=None, observed_at=None):
    return {'schema_version': 1, 'kind': kind, 'case_id': case_id, 'context_sha256': case_id,
            'state': state, 'verification': verification, 'live_eligibility': live,
            'command_exit': exit_code, 'missing_stages': missing or [], 'observed_at': observed_at}


def read_case(path):
    case = context.agent_path(ROOT, path, leaf='directory')
    store.validate_layout(case)
    raw = (case / 'context.json').read_bytes()
    obj = context.strict_json(raw)
    if not isinstance(obj, dict) or context.canonical(obj) != raw:
        raise store.IntegrityError('invalid case context')
    repository_key, workspace_key = context.workspace_identity(ROOT)
    if obj.get('repository_key') != repository_key or obj.get('workspace_key') != workspace_key:
        raise store.IntegrityError('case workspace mismatch')
    summary = context.strict_json((case / 'summary.json').read_bytes())
    case_id = context.digest(raw)
    if not isinstance(summary, dict) or summary.get('context_sha256') != case_id:
        raise store.IntegrityError('invalid case summary')
    return case, obj, summary, case_id


def current_case():
    state = ROOT / '.agent-state'
    if not state.exists() and not state.is_symlink():
        return None
    if state.is_symlink() or not state.is_dir():
        raise store.IntegrityError('invalid state directory')
    pointer = state / 'current-evidence.json'
    if not pointer.exists() or pointer.is_symlink() or not pointer.is_file():
        raise store.IntegrityError('missing or invalid current pointer')
    data = context.strict_json(pointer.read_bytes())
    if not isinstance(data, dict) or set(data) != {'schema_version', 'repository_key', 'workspace_key', 'task_id', 'case', 'context_sha256'} or data['schema_version'] != 1:
        raise store.IntegrityError('invalid current pointer')
    case, obj, summary, case_id = read_case(data['case'])
    if (data['repository_key'], data['workspace_key'], data['task_id'], data['context_sha256']) != (obj['repository_key'], obj['workspace_key'], obj['task_id'], case_id):
        raise store.IntegrityError('current pointer mismatch')
    return case, obj, summary, case_id


def validate_receipts(case, obj, summary, case_id, verifier=None):
    references = summary.get('receipts')
    if not isinstance(references, dict) or set(references) - set(STAGES):
        raise store.IntegrityError('invalid receipt references')
    dependencies = {}
    stale = False
    for stage in STAGES:
        if stage not in references:
            break
        ref = references[stage]
        if not isinstance(ref, dict) or set(ref) != {'path', 'sha256'}:
            raise store.IntegrityError('invalid receipt reference')
        path = case / 'receipts' / ref['path']
        if path.parent != case / 'receipts' or path.is_symlink():
            raise store.IntegrityError('invalid receipt path')
        raw = path.read_bytes()
        rec = context.strict_json(raw)
        if context.digest(raw) != ref['sha256'] or rec.get('dependencies') != dependencies:
            raise store.IntegrityError('receipt dependency mismatch')
        expected = store.get_receipt(ROOT, case, stage, case_id, rec['inputs'], dependencies,
                                     rec['verifier_sha256'], rec['policy_sha256'])
        if expected is None or expected[1] != ref['sha256']:
            raise store.IntegrityError('receipt unavailable')
        try:
            actual = identity(stage, obj['kind'], verifier)
        except context.InputError:
            actual = None
        if rec['policy_sha256'] != policy(stage, obj['kind']) or rec['verifier_sha256'] != actual:
            stale = True
        if stage == 'finalize':
            authority_name = summary.get('authority_path')
            if not isinstance(authority_name, str):
                raise store.IntegrityError('final authority reference missing')
            authority_path = case / authority_name
            try:
                relative = authority_path.relative_to(ROOT).as_posix()
                if (authority_path.parent.parent != case / 'raw' or authority_path.name != 'authority-final.json' or
                        relative not in rec['inputs']):
                    raise store.IntegrityError('final authority not bound')
                authority = context.strict_json(authority_path.read_bytes())
                if authority.get('live_eligibility') != summary.get('live_eligibility') and summary.get('state') == 'complete':
                    raise store.IntegrityError('final eligibility mismatch')
                try:
                    current_adoption = adoption_identity(obj['kind'], verifier)
                except context.InputError:
                    current_adoption = None
                if authority.get('adoption_identity') != current_adoption:
                    stale = True
            except (OSError, ValueError, TypeError) as exc:
                raise store.IntegrityError('invalid final authority') from exc
        dependencies[stage] = ref['sha256']
    if set(references) != set(dependencies):
        raise store.IntegrityError('receipt sequence incomplete')
    return dependencies, stale


def verifier_for(obj, explicit, summary):
    path = explicit or summary.get('profile_verifier')
    if obj['kind'] != 'source':
        return None
    if path is None:
        raise context.InputError('profile verifier required')
    return context.agent_path(ROOT, path, leaf='file')


def predecessor_result(obj, *, allow_stale=False):
    predecessor = obj.get('predecessor')
    if predecessor is None:
        return None
    case, previous, summary, case_id = read_case(predecessor['case'])
    if case_id != predecessor['context_sha256'] or previous.get('kind') != 'snapshot' or previous.get('expected') != 'pending':
        raise store.IntegrityError('predecessor identity mismatch')
    for key in ('task_id', 'repository', 'repository_id', 'workflow_head_sha', 'workflow_blob', 'source', 'pr', 'metadata', 'repository_key', 'workspace_key'):
        if previous[key] != obj[key]:
            raise store.IntegrityError('predecessor context mismatch')
    deps, stale = validate_receipts(case, previous, summary, case_id)
    if stale and not allow_stale:
        # Reverification below can adopt a new verifier, but not an obsolete capture or authentication.
        for stage in ('capture', 'authenticate'):
            ref = summary['receipts'].get(stage)
            if ref is None:
                raise IncompleteError('predecessor incomplete')
            receipt = context.strict_json((case / 'receipts' / ref['path']).read_bytes())
            if (receipt['verifier_sha256'] != identity(stage, previous['kind'], None) or
                    receipt['policy_sha256'] != policy(stage, previous['kind'])):
                raise IncompleteError('predecessor capture stale')
    if len(deps) != 4 or summary.get('state') != 'complete':
        raise IncompleteError('predecessor incomplete')
    return snapshot.verify_snapshot(previous, case / summary['observation'], classifier())


def verify_observation(case, obj, summary, verifier, *, closure_cache=None, allow_stale_predecessor=False):
    observation = case / summary['observation']
    if observation.is_symlink() or not observation.is_dir() or observation.parent != case / 'raw':
        raise store.IntegrityError('invalid observation')
    binary_sha = hashlib.sha256(Path(verifier).read_bytes()).hexdigest() if obj['kind'] == 'source' else None
    verified_identity = (identity('verify', obj['kind'], verifier, binary_sha256=binary_sha),
                         policy('verify', obj['kind']))
    adoption = adoption_identity(obj['kind'], verifier)
    if (adoption['policies']['verify'] != verified_identity[1] or
            (binary_sha is not None and adoption['files'].get(Path(verifier).relative_to(ROOT).as_posix()) != binary_sha)):
        raise context.StaleError('verification inputs changed before execution')
    require_stage_identity('verify', obj['kind'], verifier, verified_identity)
    try:
        if obj['kind'] == 'source':
            verified = source.verify_source(obj, case, observation, verifier, closure_cache=closure_cache,
                                            verifier_sha256=binary_sha)
        else:
            verified = snapshot.verify_snapshot(obj, observation, classifier(),
                                               predecessor=predecessor_result(obj, allow_stale=allow_stale_predecessor))
    except Exception:
        require_stage_identity('verify', obj['kind'], verifier, verified_identity)
        require_adoption_identity(obj['kind'], verifier, adoption)
        raise
    require_stage_identity('verify', obj['kind'], verifier, verified_identity)
    require_adoption_identity(obj['kind'], verifier, adoption)
    return verified, verified_identity, adoption


def command_verify(args):
    case, obj, summary, case_id = read_case(args.case)
    verifier = verifier_for(obj, args.profile_verifier, {})
    deps, stale = validate_receipts(case, obj, summary, case_id, verifier)
    if len(deps) != 4 or summary.get('state') != 'complete':
        raise IncompleteError('case incomplete')
    verify_observation(case, obj, summary, verifier, allow_stale_predecessor=True)
    if stale:
        return result(obj['kind'], case_id, 'stale', 'passed', 'blocked', exit_code=6,
                      observed_at=summary.get('observed_at'))
    return result(obj['kind'], case_id, 'complete', 'passed', summary.get('live_eligibility', 'not_checked'),
                  observed_at=summary.get('observed_at'))


def command_status(args):
    found = read_case(args.case) if args.case else current_case()
    if found is None:
        return result()
    case, obj, summary, case_id = found
    try:
        verifier = verifier_for(obj, None, summary)
    except context.InputError:
        verifier = None
    deps, stale = validate_receipts(case, obj, summary, case_id, verifier)
    state = 'stale' if stale else summary.get('state', 'incomplete')
    if state not in ('incomplete', 'complete', 'failed', 'stale'):
        raise store.IntegrityError('invalid case state')
    live = 'blocked' if stale else summary.get('live_eligibility', 'not_checked')
    observed_at = summary.get('observed_at')
    try:
        context.require_clean_adoption(ROOT)
    except (context.StaleError, context.InputError):
        state, live = 'stale', 'blocked'
    if args.refresh:
        refreshed, drift = refresh(obj, case, summary)
        observed_at = store.now()
        if state != 'stale':
            live = refreshed
        if drift:
            state, live = 'stale', 'blocked'
    return result(obj['kind'], case_id, state, summary.get('verification', 'not_run'), live,
                  missing=[name for name in STAGES if name not in deps], observed_at=observed_at)


def refresh(obj, case, summary, *, client=None):
    """Read current run, source record, checks and protection without writing."""
    import http.client
    import re
    from urllib.parse import quote

    owned_client = client is None
    try:
        if owned_client:
            token = subprocess.run(['gh', 'auth', 'token'], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                   check=True, close_fds=True).stdout.decode('ascii').strip()
            if not token or not token.isascii() or any(ord(c) < 33 or ord(c) == 127 for c in token):
                return 'unknown', False
            client = object.__new__(capture.Client)
            client.token, client.context = token, obj
            client.connection = http.client.HTTPSConnection('api.github.com', timeout=15)
        try:
            authority = observe_authority(client, obj, allow_nonterminal=True)
            runs = authority['runs']
            prefix = '/repos/' + obj['repository'] + '/'
            branch = obj['pr']['base_ref'] if obj['pr'] else obj['source']['branch']
            protection, _ = client.json('GET', prefix + 'branches/' + quote(branch, safe='') + '/protection')
            required = protection['required_status_checks']
            expected = {(name, 15368) for name in (*snapshot.NAMES, 'PR Title')}
            if (required['strict'] is not True or protection['enforce_admins']['enabled'] is not True or
                    len(required['checks']) != 4 or
                    {(check['context'], check['app_id']) for check in required['checks']} != expected):
                return 'blocked', False
            if obj['pr'] is None:
                branch_obj, _ = client.json('GET', prefix + 'branches/' + quote(branch, safe=''))
                if branch_obj['commit']['sha'] != obj['source']['checkout_sha']:
                    raise context.StaleError('branch head changed')
            elif authority['pr'].get('mergeable_state') != 'clean':
                return 'blocked', False
            if any(run.get('status') != 'completed' or run.get('conclusion') != 'success' for run in runs):
                return 'blocked', False

            def listing(path, key):
                pages, seen = [], set()
                while path is not None:
                    if path in seen:
                        raise store.IntegrityError('pagination cycle')
                    seen.add(path)
                    page, link = client.json('GET', path)
                    pages.append(page)
                    path = capture.next_page(link, obj['repository'], obj['repository_id'])
                items = [item for page in pages for item in page[key]]
                if (not pages or any(page['total_count'] != len(items) for page in pages) or
                        len({item['id'] for item in items}) != len(items)):
                    raise store.IntegrityError('listing incomplete')
                return items

            def jobs_for(run):
                jobs = listing(prefix + f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100", 'jobs')
                if any(job['run_id'] != run['id'] or job['run_attempt'] != run['run_attempt'] or
                       job['head_sha'] != obj['source']['head_sha'] for job in jobs):
                    raise store.IntegrityError('job identity mismatch')
                return jobs

            def record_for(run, jobs):
                artifact_list = listing(prefix + f"actions/runs/{run['id']}/artifacts?per_page=100", 'artifacts')
                artifact, _ = source.select_attempt_artifact(artifact_list, jobs, run, run['run_attempt'],
                    f"ci-event-{run['id']}-{run['run_attempt']}", 'CI Event Classification',
                    'Upload CI event record', repository_id=obj['repository_id'])
                saved = case / 'artifacts' / (str(artifact['id']) + '.zip')
                if saved.is_symlink():
                    raise store.IntegrityError('invalid saved artifact')
                if saved.is_file():
                    raw = saved.read_bytes()
                else:
                    with client.artifact_stream(artifact['id']) as stream:
                        raw = stream.read(256 * 1024 + 1)
                if 'sha256:' + hashlib.sha256(raw).hexdigest() != artifact['digest']:
                    raise store.IntegrityError('source artifact digest mismatch')
                module = classifier()
                record = module.validate_record(module.archive_record(raw))
                if (record['repository'] != obj['repository'] or record['repository_id'] != obj['repository_id'] or
                        record['run_id'] != run['id'] or record['run_attempt'] != run['run_attempt'] or
                        record['event_name'] != run['event']):
                    raise store.IntegrityError('source record identity mismatch')
                content, _ = client.json('GET', prefix + 'contents/.github/workflows/ci.yml?ref=' + record['workflow_sha'])
                import base64
                if content.get('encoding') != 'base64' or not isinstance(content.get('content'), str):
                    raise store.IntegrityError('workflow content unavailable')
                workflow = base64.b64decode(''.join(content['content'].split()), validate=True)
                blob = hashlib.sha1(b'blob ' + str(len(workflow)).encode('ascii') + b'\0' + workflow).hexdigest()
                if blob != obj['workflow_blob'] or content.get('sha') != blob:
                    raise context.StaleError('workflow identity changed')
                return artifact['id'], record

            source_run = runs[0]
            source_jobs = jobs_for(source_run)
            source.validate_run_jobs(obj, source_run, source_jobs)
            source_id, source_record = record_for(source_run, source_jobs)
            source.validate_source_record(obj, source_record, classifier())
            observation = case / summary['observation']
            if obj['kind'] == 'source':
                saved_record = context.strict_json((observation / 'record.json').read_bytes())
                saved_pages = context.strict_json((observation / 'artifacts.json').read_bytes())
                saved_jobs = [j for page in context.strict_json((observation / 'jobs.json').read_bytes()) for j in page['jobs']]
            else:
                rid, attempt = obj['source']['run_id'], obj['source']['attempt']
                saved_record = context.strict_json((observation / f'record-{rid}-{attempt}.json').read_bytes())
                saved_pages = context.strict_json((observation / f'artifacts-{rid}.json').read_bytes())
                saved_jobs = [j for page in context.strict_json((observation / f'jobs-{rid}-{attempt}.json').read_bytes()) for j in page['jobs']]
            saved_artifact, _ = source.select_attempt_artifact([a for page in saved_pages for a in page['artifacts']],
                saved_jobs, source_run, obj['source']['attempt'], f"ci-event-{source_run['id']}-{source_run['run_attempt']}",
                'CI Event Classification', 'Upload CI event record', repository_id=obj['repository_id'])
            if source_record != saved_record or source_id != saved_artifact['id']:
                raise context.StaleError('source pointer changed')

            def check_job(job, run, name, check_id=None):
                match = re.fullmatch(re.escape('https://api.github.com' + prefix + 'check-runs/') + r'([1-9][0-9]*)',
                                     job.get('check_run_url', ''))
                if not match or (check_id is not None and int(match.group(1)) != check_id):
                    raise store.IntegrityError('check URL mismatch')
                check_id = int(match.group(1))
                check, _ = client.json('GET', prefix + 'check-runs/' + str(check_id))
                if (job['name'] != name or job['status'] != 'completed' or job['conclusion'] != 'success' or
                        check['id'] != check_id or check['name'] != name or check['status'] != 'completed' or
                        check['conclusion'] != 'success' or check['head_sha'] != obj['source']['head_sha'] or
                        check['app']['id'] != 15368 or check['check_suite']['id'] != run['check_suite_id']):
                    raise store.IntegrityError('required check mismatch')
                return check_id

            source_checks = {}
            for name in snapshot.NAMES:
                matches = [job for job in source_jobs if job['name'] == name]
                if len(matches) != 1:
                    raise store.IntegrityError('required source job missing')
                source_checks[name] = check_job(matches[0], source_run, name)
            # The head listing may contain the same names in several legitimate suites.
            current = listing(prefix + 'commits/' + obj['source']['head_sha'] + '/check-runs?filter=latest&per_page=100', 'check_runs')
            selected = {}
            names = snapshot.NAMES if obj['pr'] is None else (*snapshot.NAMES, 'PR Title')
            if obj['kind'] == 'snapshot':
                def declared_check(run, jobs, name, expected_id=None):
                    matching_jobs = [job for job in jobs if job['name'] == name]
                    if len(matching_jobs) != 1:
                        raise store.IntegrityError('required job missing or ambiguous')
                    check_id = check_job(matching_jobs[0], run, name, expected_id)
                    matches = [item for item in current if item.get('name') == name and
                               (item.get('check_suite') or {}).get('id') == run['check_suite_id']]
                    if (len(matches) != 1 or matches[0].get('id') != check_id or
                            (matches[0].get('app') or {}).get('id') != 15368 or
                            matches[0].get('head_sha') != obj['source']['head_sha'] or
                            matches[0].get('status') != 'completed' or matches[0].get('conclusion') != 'success'):
                        raise store.IntegrityError('declared required check missing or ambiguous')
                    return matches[0]

                for name in snapshot.NAMES:
                    declared_check(source_run, source_jobs, name, source_checks[name])
                metadata_jobs = jobs_for(runs[1])
                title_jobs = jobs_for(runs[2])
                for name in snapshot.NAMES:
                    selected[name] = declared_check(runs[1], metadata_jobs, name)
                selected['PR Title'] = declared_check(runs[2], title_jobs, 'PR Title')
            else:
                for name in names:
                    matches = [item for item in current if item.get('name') == name and (item.get('app') or {}).get('id') == 15368]
                    if len(matches) != 1:
                        raise store.IntegrityError('current required check missing or ambiguous')
                    selected[name] = matches[0]
            if obj['pr'] is None:
                for name in snapshot.NAMES:
                    if selected[name]['id'] != source_checks[name]:
                        raise context.StaleError('current source check changed')
                    if (selected[name].get('check_suite') or {}).get('id') != source_run['check_suite_id']:
                        raise store.IntegrityError('current source suite mismatch')
                return 'eligible_at_observation', False
            run_candidates = listing(prefix + 'actions/runs?event=pull_request&head_sha=' + obj['source']['head_sha'] + '&per_page=100', 'workflow_runs')
            associated = {}
            for name, check in selected.items():
                matches = [run for run in run_candidates if run.get('check_suite_id') == check['check_suite']['id']]
                if len(matches) != 1:
                    raise store.IntegrityError('required check suite is ambiguous')
                candidate, _ = client.json('GET', prefix + 'actions/runs/' + str(matches[0]['id']))
                if (candidate['id'] != matches[0]['id'] or candidate['run_attempt'] != matches[0]['run_attempt'] or
                        candidate['check_suite_id'] != check['check_suite']['id'] or
                        candidate['head_sha'] != obj['source']['head_sha'] or candidate['head_branch'] != obj['source']['branch'] or
                        candidate['event'] != 'pull_request' or candidate['repository']['id'] != obj['repository_id'] or
                        candidate['repository']['full_name'] != obj['repository'] or
                        candidate['head_repository']['id'] != obj['repository_id'] or
                        candidate['head_repository']['full_name'] != obj['repository'] or
                        candidate['status'] != 'completed' or candidate['conclusion'] != 'success' or
                        candidate['path'] != ('.github/workflows/pr-metadata.yml' if name == 'PR Title' else '.github/workflows/ci.yml')):
                    raise context.StaleError('required run identity changed')
                candidate_jobs = jobs_for(candidate)
                matches = [job for job in candidate_jobs if job['name'] == name]
                if len(matches) != 1:
                    raise store.IntegrityError('required job missing')
                check_job(matches[0], candidate, name, check['id'])
                associated[name] = (candidate, candidate_jobs)
            ci_runs = {associated[name][0]['id'] for name in snapshot.NAMES}
            if len(ci_runs) != 1:
                raise store.IntegrityError('required checks have different source runs')
            current_run, current_jobs = associated[snapshot.NAMES[0]]
            if current_run['id'] == source_run['id']:
                if any(selected[name]['id'] != source_checks[name] for name in snapshot.NAMES):
                    raise context.StaleError('required source check changed')
            else:
                _, current_record = record_for(current_run, current_jobs)
                if (current_record['mode'] != 'metadata' or current_record['source_run'] !=
                        {'run_id': source_run['id'], 'run_attempt': source_run['run_attempt'], 'artifact_id': source_id} or
                        current_record['checkout_sha'] != obj['source']['checkout_sha'] or
                        current_record['checkout_tree'] != obj['source']['checkout_tree'] or
                        current_record['pull_request'] != classifier().snapshot(authority['pr'], obj['pr']['number'])):
                    raise context.StaleError('metadata source pointer changed')
            if obj['kind'] == 'snapshot':
                if (current_run['id'] != obj['metadata']['run_id'] or
                        current_run['run_attempt'] != obj['metadata']['attempt'] or
                        associated['PR Title'][0]['id'] != obj['title']['run_id'] or
                        associated['PR Title'][0]['run_attempt'] != obj['title']['attempt']):
                    raise context.StaleError('required observation changed')
            return 'eligible_at_observation', False
        finally:
            if owned_client:
                client.close()
    except context.StaleError:
        return 'blocked', True
    except (RemoteFailure, IncompleteError):
        return 'blocked', False
    except (capture.TransportError, subprocess.SubprocessError, OSError, UnicodeError):
        return 'unknown', False
    except (ValueError, KeyError, TypeError, RuntimeError):
        return 'blocked', False


def observation_files(case, obj, observation):
    files = []
    for path in observation.rglob('*'):
        if path.is_symlink():
            raise store.IntegrityError('symlink in observation')
        if path.is_file() and '.part.' not in path.name:
            files.append(path)
    run_specs = [(obj['source']['run_id'], obj['source']['attempt'])]
    if obj['kind'] == 'snapshot':
        run_specs.append((obj['metadata']['run_id'], obj['metadata']['attempt']))
    ids = set()
    for run_id, attempt in run_specs:
        if obj['kind'] == 'source':
            run = json.loads((observation / 'run.json').read_bytes())
            jobs = [x for page in json.loads((observation / 'jobs.json').read_bytes()) for x in page['jobs']]
            artifacts = [x for page in json.loads((observation / 'artifacts.json').read_bytes()) for x in page['artifacts']]
            wanted = [(f'ci-event-{run_id}-{attempt}', 'CI Event Classification', 'Upload CI event record'),
                      ('required-postgres-evidence', 'PostgreSQL Migration Parity', 'Upload required PostgreSQL evidence'),
                      ('asset-worker-profile-signing-evidence-amd64', 'Asset Worker Build & Scan (amd64)', 'Upload Worker profile signing evidence')]
            wanted += [(name, producer, source.UPLOAD_STEPS[producer]) for name, (producer, _) in source.ARTIFACTS.items()]
        else:
            run = json.loads((observation / f'run-{run_id}.json').read_bytes())
            jobs = [x for page in json.loads((observation / f'jobs-{run_id}-{attempt}.json').read_bytes()) for x in page['jobs']]
            artifacts = [x for page in json.loads((observation / f'artifacts-{run_id}.json').read_bytes()) for x in page['artifacts']]
            wanted = [(f'ci-event-{run_id}-{attempt}', 'CI Event Classification', 'Upload CI event record')]
        for name, producer, step in wanted:
            artifact, _ = source.select_attempt_artifact(artifacts, jobs, run, attempt, name, producer, step,
                                                          repository_id=obj['repository_id'])
            ids.add(artifact['id'])
    for artifact_id in ids:
        files.extend((case / 'artifacts' / f'{artifact_id}.zip', case / 'artifacts' / f'{artifact_id}.json'))
    files.append(case / 'context.json')
    return store.file_hashes(ROOT, [path.relative_to(ROOT).as_posix() for path in files])


def authenticate_observation(case, obj, observation, files):
    if store.file_hashes(ROOT, files) != files:
        raise store.IntegrityError('capture inputs changed')
    names = ['artifacts.json'] if obj['kind'] == 'source' else [
        'artifacts-' + str(obj[key]['run_id']) + '.json' for key in ('source', 'metadata')]
    advertised = {}
    for filename in names:
        for page in context.strict_json((observation / filename).read_bytes()):
            for item in page['artifacts']:
                if item['id'] in advertised:
                    raise store.IntegrityError('duplicate artifact identity')
                advertised[item['id']] = item
    for name, sha in files.items():
        path = ROOT / name
        if path.parent != case / 'artifacts' or path.suffix != '.zip':
            continue
        artifact_id = int(path.stem)
        sidecar = context.strict_json(path.with_suffix('.json').read_bytes())
        artifact = sidecar['artifact']
        provenance = sidecar['provenance']
        if (artifact['id'] != artifact_id or advertised[artifact_id]['digest'] != artifact['digest'] or
                artifact['digest'] != 'sha256:' + sha or provenance['selected_id'] != artifact_id or
                provenance['digest'] != artifact['digest'] or sidecar['zip_sha256'] != sha):
            raise store.IntegrityError('artifact authentication mismatch')
    return files


def save_summary(case, summary):
    store.publish_bytes(case / 'summary.json', context.canonical(summary), replace=True)


def require_stage_identity(stage, kind, verifier, expected):
    try:
        actual = (identity(stage, kind, verifier), policy(stage, kind))
    except (context.InputError, OSError, ValueError) as exc:
        raise context.StaleError('stage inputs unavailable') from exc
    if actual != expected:
        raise context.StaleError('stage inputs changed')


def require_adoption_identity(kind, verifier, expected):
    try:
        actual = adoption_identity(kind, verifier)
    except (OSError, ValueError) as exc:
        raise context.StaleError('adoption inputs unavailable') from exc
    if actual != expected:
        raise context.StaleError('adoption inputs changed')


def add_stage(case, obj, summary, case_id, stage, files, verifier, dependencies, *, stage_identity, outputs=None):
    require_stage_identity(stage, obj['kind'], verifier, stage_identity)
    verifier_sha, policy_sha = stage_identity
    receipt, sha = store.publish_receipt(ROOT, case, stage, case_id, files, dependencies,
                                         verifier_sha, policy_sha, files if outputs is None else outputs)
    require_stage_identity(stage, obj['kind'], verifier, stage_identity)
    key, _ = store.receipt_key(stage, case_id, files, dependencies, verifier_sha, policy_sha)
    summary['receipts'][stage] = {'path': stage + '-' + key + '.json', 'sha256': sha}
    if stage == 'verify':
        if len(receipt['outputs']) != 1:
            raise store.IntegrityError('invalid verification output')
        summary['verification_path'] = (ROOT / next(iter(receipt['outputs']))).relative_to(case).as_posix()
    summary['missing_stages'] = [name for name in STAGES if name not in summary['receipts']]
    save_summary(case, summary)
    return sha, receipt


def observe_authority(client, obj, *, allow_nonterminal=False):
    prefix = '/repos/' + obj['repository'] + '/'
    specs = [obj['source']]
    if obj['kind'] == 'snapshot':
        specs += [obj['metadata'], obj['title']]
    runs = []
    for spec in specs:
        run, _ = client.json('GET', prefix + 'actions/runs/' + str(spec['run_id']))
        if (run.get('id') != spec['run_id'] or run.get('run_attempt') != spec['attempt'] or
                run.get('head_sha') != obj['source']['head_sha'] or run.get('head_branch') != obj['source']['branch']):
            raise context.StaleError('remote attempt changed')
        expected_path = '.github/workflows/pr-metadata.yml' if obj['kind'] == 'snapshot' and spec is obj['title'] else '.github/workflows/ci.yml'
        if (run.get('event') != obj['source']['event'] or run.get('path') != expected_path or
                any(run.get(side, {}).get('id') != obj['repository_id'] or
                    run.get(side, {}).get('full_name') != obj['repository'] for side in ('repository', 'head_repository'))):
            raise context.StaleError('remote repository or workflow changed')
        if (obj['kind'] == 'snapshot' and obj['expected'] == 'pending' and
                spec is not obj['title'] and run.get('status') == 'completed'):
            raise context.StaleError('pending window closed')
        runs.append(run)
    if obj['kind'] == 'source' and not allow_nonterminal:
        if runs[0].get('status') != 'completed':
            raise IncompleteError('source run incomplete')
        if runs[0].get('conclusion') != 'success':
            raise RemoteFailure('source run failed')
    elif obj['expected'] == 'pending' and any(run.get('status') == 'completed' for run in runs[:2]):
        raise context.StaleError('pending window closed')
    if obj['pr'] is not None:
        pr, _ = client.json('GET', prefix + 'pulls/' + str(obj['pr']['number']))
        if (pr.get('number') != obj['pr']['number'] or pr.get('state') != 'open' or pr.get('merged') is not False or
                pr['head']['sha'] != obj['pr']['head_sha'] or pr['base']['sha'] != obj['pr']['base_sha'] or
                pr.get('merge_commit_sha') != obj['pr']['merge_sha'] or
                pr['head']['ref'] != obj['pr']['head_ref'] or pr['base']['ref'] != obj['pr']['base_ref'] or
                any(pr[side]['repo']['id'] != obj['repository_id'] or
                    pr[side]['repo']['full_name'] != obj['repository'] for side in ('head', 'base'))):
            raise context.StaleError('pull request identity changed')
    else:
        pr = None
    return {'runs': runs, 'pr': pr}


def final_authority(client, obj, observation, verified):
    from urllib.parse import quote
    authority = observe_authority(client, obj)
    prefix = '/repos/' + obj['repository'] + '/'
    if obj['kind'] == 'source':
        final = json.loads((observation / 'final-run.json').read_bytes())
        if any(authority['runs'][0].get(k) != final.get(k) for k in
               ('id', 'run_attempt', 'head_sha', 'head_branch', 'event', 'path', 'status', 'conclusion', 'repository', 'head_repository')):
            raise context.StaleError('source authority changed')
        return authority, 'unknown'
    protection, _ = client.json('GET', prefix + 'branches/' + quote(obj['pr']['base_ref'], safe='') + '/protection')
    authority['protection'] = protection
    required = protection['required_status_checks']
    expected = {(name, 15368) for name in (*snapshot.NAMES, 'PR Title')}
    if (required['strict'] is not True or protection['enforce_admins']['enabled'] is not True or
            {(check['context'], check['app_id']) for check in required['checks']} != expected):
        raise context.StaleError('branch protection changed')
    mode = obj['expected']
    expected_merge = 'clean' if mode == 'success' else 'blocked'
    if authority['pr'].get('mergeable_state') != expected_merge:
        raise context.StaleError('effective merge state changed')
    expected_outcome = 'success' if mode == 'success' else 'failure'
    if mode != 'pending' and any(run['status'] != 'completed' or run['conclusion'] != expected_outcome
                                 for run in authority['runs'][:2]):
        raise context.StaleError('terminal workflow state changed')
    if authority['runs'][2]['status'] != 'completed' or authority['runs'][2]['conclusion'] != 'success':
        raise context.StaleError('title workflow state changed')
    by_run = []
    authority['job_pages'] = []
    for spec in (obj['source'], obj['metadata'], obj['title']):
        path = prefix + f"actions/runs/{spec['run_id']}/attempts/{spec['attempt']}/jobs?per_page=100"
        pages, links, seen = [], [], set()
        while path is not None:
            if path in seen:
                raise store.IntegrityError('job pagination cycle')
            seen.add(path)
            page, link = client.json('GET', path)
            pages.append(page)
            links.append({'path': path, 'link': link})
            path = capture.next_page(link, obj['repository'], obj['repository_id'])
        jobs = [job for page in pages for job in page['jobs']]
        if (not pages or any(page['total_count'] != len(jobs) for page in pages) or
                len({job['id'] for job in jobs}) != len(jobs) or
                any(job['run_id'] != spec['run_id'] or job['run_attempt'] != spec['attempt'] or
                    job['head_sha'] != obj['source']['head_sha'] for job in jobs)):
            raise store.IntegrityError('final job listing incomplete')
        authority['job_pages'].append({'pages': pages, 'links': links})
        by_run.append(jobs)
    groups = {item['name']: item for item in verified['groups']}
    authority['checks'] = []
    waiting = 0
    skipped = 0
    gates = snapshot.inventory((observation / 'workflow.yml').read_bytes())
    for name in snapshot.NAMES:
        group = groups[name]
        selected = []
        for index, prefix_name in ((0, 'source'), (1, 'metadata')):
            matches = [job for job in by_run[index] if job['name'] == name]
            if len(matches) != 1 or matches[0]['id'] != group[prefix_name + '_job_id']:
                raise context.StaleError('protected job changed')
            job = matches[0]
            check_id = group[prefix_name + '_check_id']
            check, _ = client.json('GET', prefix + 'check-runs/' + str(check_id))
            if (job.get('check_run_url') != 'https://api.github.com' + prefix + 'check-runs/' + str(check_id) or
                    check['id'] != check_id or check['name'] != name or check['head_sha'] != obj['source']['head_sha'] or
                    check['app']['id'] != 15368 or check['check_suite']['id'] != authority['runs'][index]['check_suite_id'] or
                    check['status'] != job['status'] or check['conclusion'] != job['conclusion']):
                raise context.StaleError('protected check changed')
            authority['checks'].append(check)
            selected.append(job)
        src_job, meta_job = selected
        if mode == 'pending':
            if meta_job['status'] == 'completed' and meta_job['conclusion'] == 'success':
                raise context.StaleError('metadata verification succeeded early')
            steps = [step for step in meta_job['steps'] if step['name'] == 'Verify source check']
            if len(steps) != 1:
                raise store.IntegrityError('metadata verifier missing')
            if src_job['status'] == 'completed' and src_job['conclusion'] == 'success' and steps[0]['status'] == 'in_progress':
                waiting += 1
        elif (meta_job['status'] != 'completed' or meta_job['conclusion'] != expected_outcome or
              (mode == 'success' and (src_job['status'] != 'completed' or src_job['conclusion'] != 'success'))):
            raise context.StaleError('protected job result changed')
        if mode != 'pending':
            for step_name in gates[name]:
                matches = [step for step in meta_job['steps'] if step['name'] == step_name]
                if len(matches) != 1 or matches[0]['status'] != 'completed' or matches[0]['conclusion'] != 'skipped':
                    raise context.StaleError('metadata source gate changed')
                skipped += 1
    if mode == 'pending' and not waiting:
        raise context.StaleError('pending verifier window closed')
    if mode != 'pending' and skipped != 37:
        raise store.IntegrityError('metadata source gate count mismatch')
    title = [job for job in by_run[2] if job['name'] == 'PR Title']
    if len(title) != 1 or title[0]['id'] != verified['title']['job_id']:
        raise context.StaleError('title job changed')
    title_check, _ = client.json('GET', prefix + 'check-runs/' + str(verified['title']['check_id']))
    authority['checks'].append(title_check)
    if (title[0]['status'] != 'completed' or title[0]['conclusion'] != 'success' or
            title_check['id'] != verified['title']['check_id'] or
            title[0]['check_run_url'] != 'https://api.github.com/repos/' + obj['repository'] + '/check-runs/' + str(title_check['id']) or
            title_check['status'] != 'completed' or title_check['conclusion'] != 'success' or
            title_check['name'] != 'PR Title' or title_check['head_sha'] != obj['source']['head_sha'] or
            title_check['app']['id'] != 15368 or title_check['check_suite']['id'] != authority['runs'][2]['check_suite_id']):
        raise context.StaleError('title check changed')
    return authority, 'eligible_at_observation' if mode == 'success' else 'blocked'




def command_collect(args):
    obj = context.parse_context(args.context, project_root=ROOT, kind=args.command)
    verifier = None
    if args.command == 'source':
        verifier = context.agent_path(ROOT, args.profile_verifier, leaf='file')
        if not os.access(verifier, os.X_OK):
            raise context.InputError('profile verifier not executable')
    case_id = context.digest(context.canonical(obj))
    if args.make_current:
        context.require_clean_adoption(ROOT)
    with store.open_case(ROOT, args.case, obj, resume=args.resume, make_current=args.make_current) as opened:
        case, invocation_id, started = opened
        invocation = case / 'invocations' / invocation_id
        exit_code = 0
        try:
            summary = context.strict_json((case / 'summary.json').read_bytes())
            summary.setdefault('receipts', {})
            if verifier is not None:
                summary['profile_verifier'] = verifier.relative_to(ROOT).as_posix()
            client = capture.Client(case, obj, invocation_id)
            try:
                capture_identity = (identity('capture', obj['kind'], verifier), policy('capture', obj['kind']))
                observe_authority(client, obj)
                previous = summary.get('observation')
                reuse = False
                if previous and 'capture' in summary['receipts']:
                    deps, _ = validate_receipts(case, obj, summary, case_id, verifier)
                    reuse = 'capture' in deps
                    invalid = False
                    for stage in STAGES:
                        ref = summary['receipts'].get(stage)
                        if ref is None:
                            break
                        receipt = context.strict_json((case / 'receipts' / ref['path']).read_bytes())
                        if (receipt['verifier_sha256'] != identity(stage, obj['kind'], verifier) or
                                receipt['policy_sha256'] != policy(stage, obj['kind'])):
                            invalid = True
                        if invalid:
                            del summary['receipts'][stage]
                            if stage == 'capture':
                                reuse = False
                    if invalid:
                        summary['state'] = 'incomplete'
                        summary['verification'] = 'not_run'
                        summary['live_eligibility'] = 'not_checked'
                        save_summary(case, summary)
                if reuse:
                    observation = case / previous
                else:
                    if obj['kind'] == 'source':
                        observation = source.collect_source(obj, case, client, verifier)
                    else:
                        observation = snapshot.collect_snapshot(obj, case, client, classifier())
                    summary['observation'] = observation.relative_to(case).as_posix()
                    summary['receipts'] = {}
                    summary['state'] = 'incomplete'
                    summary['verification'] = 'not_run'
                    files = observation_files(case, obj, observation)
                    add_stage(case, obj, summary, case_id, 'capture', files, verifier, {},
                              stage_identity=capture_identity)
                capture_ref = summary['receipts']['capture']
                capture_receipt = context.strict_json((case / 'receipts' / capture_ref['path']).read_bytes())
                capture_identity = (capture_receipt['verifier_sha256'], capture_receipt['policy_sha256'])
                files = capture_receipt['inputs']
                dependencies = {'capture': summary['receipts']['capture']['sha256']}
                if 'authenticate' not in summary['receipts']:
                    auth_identity = (identity('authenticate', obj['kind'], verifier), policy('authenticate', obj['kind']))
                    files = authenticate_observation(case, obj, observation, files)
                    sha, auth_receipt = add_stage(case, obj, summary, case_id, 'authenticate', files, verifier,
                                                  dict(dependencies), stage_identity=auth_identity, outputs={})
                    dependencies['authenticate'] = sha
                else:
                    dependencies['authenticate'] = summary['receipts']['authenticate']['sha256']
                    auth_ref = summary['receipts']['authenticate']
                    auth_receipt = context.strict_json((case / 'receipts' / auth_ref['path']).read_bytes())
                auth_identity = (auth_receipt['verifier_sha256'], auth_receipt['policy_sha256'])
                if 'verify' not in summary['receipts']:
                    verified, verify_identity, verified_adoption = verify_observation(
                        case, obj, summary, verifier,
                        closure_cache=getattr(client, 'closure_cache', None) if not reuse else None)
                    verification_path = client.observation / 'verification.json'
                    store.publish_bytes(verification_path, context.canonical(verified))
                    summary['verification_path'] = verification_path.relative_to(case).as_posix()
                    outputs = store.file_hashes(ROOT, [verification_path.relative_to(ROOT).as_posix()])
                    require_adoption_identity(obj['kind'], verifier, verified_adoption)
                    sha, verify_receipt = add_stage(case, obj, summary, case_id, 'verify', files, verifier,
                                                    dict(dependencies), stage_identity=verify_identity, outputs=outputs)
                    if len(verify_receipt['outputs']) != 1:
                        raise store.IntegrityError('invalid verification output')
                    verification_path = ROOT / next(iter(verify_receipt['outputs']))
                    summary['verification_path'] = verification_path.relative_to(case).as_posix()
                    verified = context.strict_json(verification_path.read_bytes())
                    dependencies['verify'] = sha
                else:
                    verify_ref = summary['receipts']['verify']
                    receipt = context.strict_json((case / 'receipts' / verify_ref['path']).read_bytes())
                    verify_identity = (receipt['verifier_sha256'], receipt['policy_sha256'])
                    require_stage_identity('verify', obj['kind'], verifier, verify_identity)
                    verified_adoption = adoption_identity(obj['kind'], verifier)
                    if verified_adoption['policies']['verify'] != verify_identity[1]:
                        raise context.StaleError('verification policy changed')
                    verification_path = case / summary['verification_path']
                    if verification_path.relative_to(ROOT).as_posix() not in receipt['outputs']:
                        raise store.IntegrityError('verification output not bound to receipt')
                    verified = context.strict_json(verification_path.read_bytes())
                    dependencies['verify'] = verify_ref['sha256']
                final_identity = (identity('finalize', obj['kind'], verifier), policy('finalize', obj['kind']))
                authority, eligibility = final_authority(client, obj, observation, verified)
                if obj['kind'] == 'source' or (obj['kind'] == 'snapshot' and obj['expected'] == 'success'):
                    eligibility, drift = refresh(obj, case, summary, client=client)
                    if drift:
                        raise context.StaleError('current required observation changed')
                    if obj['kind'] == 'snapshot' and eligibility == 'unknown':
                        raise capture.TransportError('current required checks unavailable')
                    if obj['kind'] == 'snapshot' and eligibility != 'eligible_at_observation':
                        raise store.IntegrityError('current required checks blocked')
                authority['live_eligibility'] = eligibility
                for stage, bound in (('capture', capture_identity), ('authenticate', auth_identity),
                                     ('verify', verify_identity), ('finalize', final_identity)):
                    require_stage_identity(stage, obj['kind'], verifier, bound)
                require_adoption_identity(obj['kind'], verifier, verified_adoption)
                authority['adoption_identity'] = verified_adoption
                authority_path = client.observation / 'authority-final.json'
                store.publish_bytes(authority_path, context.canonical(authority))
                authority_requests = client.observation / 'authority-requests.json'
                store.publish_bytes(authority_requests, context.canonical(client.requests))
                final_files = dict(files)
                final_files.update(store.file_hashes(ROOT, [verification_path.relative_to(ROOT).as_posix()]))
                authority_files = [path for path in client.observation.iterdir()
                                   if path.is_file() and (path.name.startswith('response-') or path in (authority_path, authority_requests))]
                final_files.update(store.file_hashes(ROOT, [path.relative_to(ROOT).as_posix() for path in authority_files]))
                summary['state'] = 'incomplete'
                summary['authority_path'] = authority_path.relative_to(case).as_posix()
                _, final_receipt = add_stage(case, obj, summary, case_id, 'finalize', final_files, verifier,
                                             dependencies, stage_identity=final_identity)
                for stage, bound in (('capture', capture_identity), ('authenticate', auth_identity),
                                     ('verify', verify_identity), ('finalize', final_identity)):
                    require_stage_identity(stage, obj['kind'], verifier, bound)
                require_adoption_identity(obj['kind'], verifier, verified_adoption)
                summary['state'] = 'complete'
                summary['verification'] = 'passed'
                summary['live_eligibility'] = eligibility
                summary['observed_at'] = final_receipt['observed_at']
                summary['missing_stages'] = []
                save_summary(case, summary)
                return result(obj['kind'], case_id, 'complete', 'passed', summary['live_eligibility'], observed_at=summary['observed_at'])
            finally:
                client.close()
        except Exception as exc:
            exit_code, _ = error_details(exc)
            raise
        finally:
            record = {'schema_version': 1, 'command': args.command, 'resume': args.resume,
                      'make_current': args.make_current, 'started_at': started, 'ended_at': store.now(),
                      'exit': exit_code, 'stderr': '' if exit_code == 0 else error_code(exit_code)}
            store.publish_bytes(invocation / 'command.json', context.canonical(record), replace=True)


def error_code(code):
    return {2: 'E_INPUT', 3: 'E_TRANSPORT', 4: 'E_INTEGRITY', 5: 'E_VERIFY',
            6: 'E_STALE', 7: 'E_BUSY', 8: 'E_INCOMPLETE', 9: 'E_REMOTE_FAILURE'}.get(code, 'E_VERIFY')


def error_details(exc):
    if isinstance(exc, context.InputError):
        return 2, 'invalid input'
    if isinstance(exc, capture.TransportError):
        return 3, 'transport unavailable'
    if isinstance(exc, store.IntegrityError):
        return 4, 'evidence integrity failure'
    if isinstance(exc, context.StaleError):
        return 6, 'observation identity changed'
    if isinstance(exc, store.BusyError):
        return 7, 'evidence writer busy'
    if isinstance(exc, IncompleteError):
        return 8, 'evidence incomplete'
    if isinstance(exc, RemoteFailure):
        return 9, 'remote source failed'
    return 5, 'evidence verification failed'


def main(argv=None):
    parser = Parser(prog='ci-evidence.py')
    sub = parser.add_subparsers(dest='command', required=True, parser_class=Parser)
    for name in ('source', 'snapshot'):
        command = sub.add_parser(name)
        command.add_argument('--context', required=True)
        command.add_argument('--case', required=True)
        if name == 'source':
            command.add_argument('--profile-verifier', required=True)
        command.add_argument('--resume', action='store_true')
        command.add_argument('--make-current', action='store_true')
    verify = sub.add_parser('verify')
    verify.add_argument('--case', required=True)
    verify.add_argument('--profile-verifier')
    status = sub.add_parser('status')
    status.add_argument('--case')
    status.add_argument('--refresh', action='store_true')
    args = None
    error_reported = False
    try:
        args = parser.parse_args(argv)
        if args.command == 'verify':
            output = command_verify(args)
        elif args.command == 'status':
            output = command_status(args)
        else:
            output = command_collect(args)
    except Exception as exc:
        code, message = error_details(exc)
        print(error_code(code) + ': ' + message, file=sys.stderr)
        error_reported = True
        kind, case_id, missing, observed = None, None, [], None
        try:
            if args is not None and getattr(args, 'case', None):
                _, obj, summary, case_id = read_case(args.case)
                kind = obj.get('kind')
                missing = summary.get('missing_stages', [])
                observed = summary.get('observed_at')
        except Exception:
            pass
        state = 'stale' if code == 6 else 'incomplete' if code in (7, 8) else 'failed'
        output = result(kind, case_id, state=state, verification='failed', exit_code=code,
                        missing=missing, observed_at=observed)
    if output['command_exit'] and not error_reported:
        print(error_code(output['command_exit']) + ': observation identity changed', file=sys.stderr)
    sys.stdout.write(json.dumps(output, sort_keys=True, separators=(',', ':'), ensure_ascii=False) + '\n')
    return output['command_exit']
