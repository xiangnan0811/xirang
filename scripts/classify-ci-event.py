#!/usr/bin/env python3
"""Classify CI inputs; metadata is an optimization, never a test result."""
import argparse
import datetime
import io
import json
import os
import re
import stat
import subprocess
import time
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

WORKFLOW = ".github/workflows/ci.yml"
SHA = re.compile(r"[0-9a-f]{40}\Z")
REPO = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
MAX_JSON = 8 * 1024 * 1024
MAX_RECORD = 64 * 1024
MAX_ZIP = 256 * 1024
SOURCE_CHECKS = ('Backend Test & Build', 'Frontend Test & Build', 'Doc Freshness Check')
PENDING = ('queued', 'requested', 'pending', 'waiting', 'in_progress')


class EvidenceError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise EvidenceError(message)


def positive(value):
    return type(value) is int and value > 0


def digest(value):
    return isinstance(value, str) and SHA.fullmatch(value) is not None


def unique_object(pairs):
    obj = {}
    for key, value in pairs:
        require(key not in obj, "duplicate JSON key")
        obj[key] = value
    return obj


def decode(data, limit):
    require(len(data) <= limit, "response exceeds size limit")
    try:
        return json.loads(data, object_pairs_hook=unique_object,
                          parse_constant=lambda _: (_ for _ in ()).throw(EvidenceError("invalid JSON constant")))
    except (ValueError, UnicodeError, RecursionError) as exc:
        raise EvidenceError("invalid JSON") from exc


def read_bounded(stream, limit):
    data = stream.read(limit + 1)
    require(len(data) <= limit, "response exceeds size limit")
    return data


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        require(urllib.parse.urlsplit(newurl).scheme == "https", "non-HTTPS artifact redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class GitHubAPI:
    def __init__(self, token):
        self.token = token

    def request(self, path):
        require(path.startswith('/repos/') and not path.startswith('//'), "invalid API path")
        return urllib.request.Request('https://api.github.com' + path, headers={
            'Authorization': 'Bearer ' + self.token,
            'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28',
        })

    def get_json(self, path):
        with urllib.request.build_opener(NoRedirect()).open(self.request(path), timeout=15) as response:
            return decode(read_bounded(response, MAX_JSON), MAX_JSON)

    def get_artifact(self, artifact_id):
        request = self.request(self.prefix + '/actions/artifacts/%d/zip' % artifact_id)
        try:
            with urllib.request.build_opener(NoRedirect()).open(request, timeout=15):
                pass
        except urllib.error.HTTPError as exc:
            try:
                require(exc.code == 302, "artifact download did not redirect")
                location = exc.headers.get('Location')
            finally:
                exc.close()
        else:
            raise EvidenceError("artifact download did not redirect")
        require(isinstance(location, str) and urllib.parse.urlsplit(location).scheme == 'https', "invalid artifact location")
        # This opener receives a fresh request without credentials, including on subsequent redirects.
        with urllib.request.build_opener(HTTPSRedirect()).open(urllib.request.Request(location), timeout=15) as response:
            return read_bounded(response, MAX_ZIP)


def archive_record(data):
    require(isinstance(data, bytes) and len(data) <= MAX_ZIP, "invalid artifact size")
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            members = archive.infolist()
            require(len(members) == 1 and members[0].filename == 'record.json', "invalid artifact members")
            member = members[0]
            mode = member.external_attr >> 16
            require(not member.is_dir() and (stat.S_IFMT(mode) in (0, stat.S_IFREG)), "artifact is not a regular file")
            require(member.file_size <= MAX_RECORD, "record exceeds size limit")
            with archive.open(member) as stream:
                return decode(read_bounded(stream, MAX_RECORD), MAX_RECORD)
    except (zipfile.BadZipFile, RuntimeError, EOFError, OSError, NotImplementedError) as exc:
        raise EvidenceError("invalid artifact archive") from exc


def timestamp(value):
    require(isinstance(value, str), "missing artifact timestamp")
    try:
        result = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
        require(result.tzinfo is not None, "timestamp lacks timezone")
        return result
    except ValueError as exc:
        raise EvidenceError("invalid artifact timestamp") from exc


def repo_identity(repo):
    require(isinstance(repo, dict) and positive(repo.get('id')) and isinstance(repo.get('full_name'), str)
            and REPO.fullmatch(repo['full_name']), "invalid repository identity")
    return repo['id'], repo['full_name']


def snapshot(pr, number):
    require(isinstance(pr, dict) and pr.get('number') == number and positive(number), "invalid PR identity")
    result = {'number': number}
    for side in ('head', 'base'):
        obj = pr.get(side)
        require(isinstance(obj, dict), "missing PR side")
        repo_id, repo_name = repo_identity(obj.get('repo'))
        require(isinstance(obj.get('ref'), str) and obj['ref'] and digest(obj.get('sha')), "invalid PR ref")
        result.update({side + '_repo_id': repo_id, side + '_repo': repo_name,
                       side + '_ref': obj['ref'], side + '_sha': obj['sha']})
    require(type(pr.get('merged')) is bool, "invalid PR merged state")
    merge = pr.get('merge_commit_sha')
    require(merge is None or digest(merge), "invalid PR merge SHA")
    result.update(merged=pr['merged'], merge_sha=merge)
    return result


def event_snapshot(event, context):
    try:
        number = event.get('number')
        pr = snapshot(event.get('pull_request'), number)
        require((pr['base_repo_id'], pr['base_repo']) == (context['repository_id'], context['repository']), "wrong base repository")
        if not pr['merged']:
            require(context['checkout_parents'] == [pr['base_sha'], pr['head_sha']], "unconfirmed merge parents")
            pr['merge_sha'] = context['checkout_sha']
        return pr
    except EvidenceError:
        return None


def validate_record(record):
    fields = {'schema_version', 'repository', 'repository_id', 'workflow_path', 'workflow_sha', 'run_id',
              'run_attempt', 'event_name', 'action', 'event_ref', 'event_sha', 'checkout_sha', 'checkout_tree',
              'change_keys', 'pull_request', 'mode', 'reason', 'source_run'}
    require(isinstance(record, dict) and set(record) == fields and record['schema_version'] == 1, "invalid record schema")
    require(isinstance(record['repository'], str) and REPO.fullmatch(record['repository']) and positive(record['repository_id']), "invalid record repository")
    require(record['workflow_path'] == WORKFLOW and all(digest(record[k]) for k in ('workflow_sha', 'event_sha', 'checkout_sha', 'checkout_tree')), "invalid record SHA")
    require(record['event_sha'] == record['checkout_sha'] and positive(record['run_id']) and positive(record['run_attempt']), "invalid record run")
    require(isinstance(record['event_name'], str) and isinstance(record['event_ref'], str) and record['event_ref'], "invalid record event")
    require(record['action'] is None or isinstance(record['action'], str), "invalid action")
    keys = record['change_keys']
    require(keys is None or (isinstance(keys, list) and all(isinstance(k, str) for k in keys) and len(set(keys)) == len(keys)), "invalid change keys")
    pr = record['pull_request']
    if pr is not None:
        expected = {'number', 'head_repo_id', 'head_repo', 'head_ref', 'head_sha', 'base_repo_id', 'base_repo', 'base_ref', 'base_sha', 'merged', 'merge_sha'}
        require(isinstance(pr, dict) and set(pr) == expected and positive(pr['number']) and type(pr['merged']) is bool, "invalid record PR")
        for side in ('head', 'base'):
            require(positive(pr[side + '_repo_id']) and isinstance(pr[side + '_repo'], str) and REPO.fullmatch(pr[side + '_repo'])
                    and isinstance(pr[side + '_ref'], str) and pr[side + '_ref'] and digest(pr[side + '_sha']), "invalid record PR side")
        require(pr['merge_sha'] is None or digest(pr['merge_sha']), "invalid record merge")
    if record['event_name'] == 'push':
        require(record['action'] is None and keys is None and pr is None, "invalid push record")
    require(record['mode'] in ('source', 'metadata'), "invalid mode")
    require(record['reason'] in ('source-event', 'unknown-changes', 'candidate-changed', 'evidence-unavailable', 'unchanged-open-candidate', 'unchanged-merged-source'), "invalid reason")
    pointer = record['source_run']
    if record['mode'] == 'source':
        require(pointer is None and record['reason'] in ('source-event', 'unknown-changes', 'candidate-changed', 'evidence-unavailable'), "invalid source pointer")
    else:
        require(record['reason'] in ('unchanged-open-candidate', 'unchanged-merged-source') and isinstance(pointer, dict)
                and set(pointer) == {'run_id', 'run_attempt', 'artifact_id'} and all(positive(v) for v in pointer.values()), "invalid metadata pointer")
    return record


def run_identity(run, repository, repository_id):
    require(isinstance(run, dict) and positive(run.get('id')) and positive(run.get('run_attempt')), "invalid run")
    require(run.get('path') == WORKFLOW and repo_identity(run.get('repository')) == (repository_id, repository), "wrong run workflow or repository")
    repo_identity(run.get('head_repository'))
    require(digest(run.get('head_sha')), "invalid run head")


def read_candidate_record(run, api):
    require(isinstance(run, dict), "invalid run")
    repository_id, repository = repo_identity(run.get('repository'))
    run_identity(run, repository, repository_id)
    run_id, attempt = run['id'], run['run_attempt']
    prefix = '/repos/' + repository
    authoritative = api.get_json(prefix + '/actions/runs/%d' % run_id)
    run_identity(authoritative, repository, repository_id)
    require(all(authoritative.get(k) == run.get(k) for k in ('id', 'run_attempt', 'event', 'head_sha', 'head_branch', 'path', 'head_repository')), "run changed")
    listing = api.get_json(prefix + '/actions/runs/%d/artifacts?per_page=100' % run_id)
    require(isinstance(listing, dict) and isinstance(listing.get('artifacts'), list) and listing.get('total_count') == len(listing['artifacts']), "ambiguous artifact listing")
    name = 'ci-event-%d-%d' % (run_id, attempt)
    matches = [a for a in listing['artifacts'] if isinstance(a, dict) and a.get('name') == name and a.get('expired') is False]
    require(len(matches) == 1, "missing or ambiguous record artifact")
    artifact = matches[0]
    require(positive(artifact.get('id')), "invalid artifact ID")
    origin = artifact.get('workflow_run')
    require(isinstance(origin, dict) and origin.get('id') == run_id and origin.get('repository_id') == repository_id
            and origin.get('head_repository_id') == run['head_repository']['id'] and origin.get('head_sha') == run['head_sha']
            and origin.get('head_branch') == run.get('head_branch'), "artifact origin mismatch")
    jobs = api.get_json(prefix + '/actions/runs/%d/attempts/%d/jobs?per_page=100' % (run_id, attempt))
    require(isinstance(jobs, dict) and isinstance(jobs.get('jobs'), list) and jobs.get('total_count') == len(jobs['jobs']), "ambiguous jobs listing")
    producers = [j for j in jobs['jobs'] if isinstance(j, dict) and j.get('name') == 'CI Event Classification']
    require(len(producers) == 1, "missing or ambiguous producer")
    job = producers[0]
    status = job.get('status')
    require(status in ('in_progress', 'completed'), "invalid producer status")
    created = timestamp(artifact.get('created_at'))
    start = timestamp(job.get('started_at'))
    if status == 'completed':
        end = timestamp(job.get('completed_at'))
    else:
        require(job.get('completed_at') is None, "invalid producer completion time")
        end = datetime.datetime.now(datetime.timezone.utc)
    require(start <= created <= end, "artifact outside producer attempt")
    record = validate_record(archive_record(api.get_artifact(artifact['id'])))
    current = api.get_json(prefix + '/actions/runs/%d' % run_id)
    run_identity(current, repository, repository_id)
    require(all(current.get(k) == authoritative.get(k) for k in ('id', 'run_attempt', 'event', 'head_sha', 'head_branch', 'path', 'head_repository')), "run changed during download")
    require(record['run_id'] == run_id and record['run_attempt'] == attempt and record['repository'] == repository
            and record['repository_id'] == repository_id and record['event_name'] == run['event'], "record origin mismatch")
    if record['event_name'] == 'pull_request' and record['pull_request'] is not None:
        pr = record['pull_request']
        associations = run.get('pull_requests')
        require(run['head_sha'] == pr['head_sha'] and repo_identity(run['head_repository']) == (pr['head_repo_id'], pr['head_repo'])
                and run.get('head_branch') == pr['head_ref'] and isinstance(associations, list)
                and len(associations) == 1 and isinstance(associations[0], dict)
                and associations[0].get('number') == pr['number'], "record PR run mismatch")
    if record['event_name'] == 'push':
        require(run['head_sha'] == record['event_sha'] and run.get('head_branch') == 'main'
                and run['head_repository']['id'] == repository_id, "record push run mismatch")
    return {'record': record, 'artifact_id': artifact['id']}


def runs(api, repository, query):
    data = api.get_json('/repos/' + repository + '/actions/workflows/ci.yml/runs?' + query + '&per_page=100')
    require(isinstance(data, dict) and isinstance(data.get('workflow_runs'), list)
            and type(data.get('total_count')) is int and data['total_count'] == len(data['workflow_runs']), "ambiguous run listing")
    require(all(isinstance(r, dict) and positive(r.get('id')) for r in data['workflow_runs']), "invalid run listing")
    return sorted(data['workflow_runs'], key=lambda r: r['id'], reverse=True)


def select_run(api, context, pr, merged):
    if merged:
        query = 'event=push&branch=main&head_sha=' + pr['merge_sha']
    else:
        query = 'event=pull_request&head_sha=' + pr['head_sha']
    for run in runs(api, context['repository'], query):
        if run['id'] >= context['run_id']:
            continue
        run_identity(run, context['repository'], context['repository_id'])
        if merged:
            require(run.get('event') == 'push' and run.get('head_branch') == 'main' and run.get('head_sha') == pr['merge_sha']
                    and repo_identity(run['head_repository']) == (context['repository_id'], context['repository']), "ambiguous push run")
        else:
            require(run.get('event') == 'pull_request' and run.get('head_sha') == pr['head_sha'], "ambiguous PR run")
            associations = run.get('pull_requests')
            require(isinstance(associations, list), "missing PR association")
            numbers = [p.get('number') for p in associations if isinstance(p, dict) and positive(p.get('number'))]
            require(len(numbers) == len(associations), "ambiguous PR association")
            if numbers and pr['number'] not in numbers:
                continue
            require(numbers == [pr['number']] and repo_identity(run['head_repository']) == (pr['head_repo_id'], pr['head_repo'])
                    and run.get('head_branch') == pr['head_ref'], "ambiguous PR run")
        return run
    raise EvidenceError("no applicable source run")


def resolve_source(run, api, context):
    item = read_candidate_record(run, api)
    record = item['record']
    if record['mode'] == 'source':
        return record, {'run_id': run['id'], 'run_attempt': run['run_attempt'], 'artifact_id': item['artifact_id']}
    pointer = record['source_run']
    require(pointer['run_id'] < run['id'] and pointer['run_id'] < context['run_id'], "invalid source lineage")
    source_run = api.get_json('/repos/' + context['repository'] + '/actions/runs/%d' % pointer['run_id'])
    run_identity(source_run, context['repository'], context['repository_id'])
    require(source_run.get('run_attempt') == pointer['run_attempt'], "source attempt changed")
    source = read_candidate_record(source_run, api)
    require(source['artifact_id'] == pointer['artifact_id'] and source['record']['mode'] == 'source', "invalid source artifact")
    require(record['pull_request'] == source['record']['pull_request'] and record['checkout_sha'] == source['record']['checkout_sha']
            and record['checkout_tree'] == source['record']['checkout_tree'], "metadata lineage mismatch")
    return source['record'], pointer


def commit_identity(api, repository, sha):
    obj = api.get_json('/repos/' + repository + '/git/commits/' + sha)
    require(isinstance(obj, dict) and obj.get('sha') == sha and isinstance(obj.get('tree'), dict)
            and digest(obj['tree'].get('sha')) and isinstance(obj.get('parents'), list), "invalid commit")
    parents = [p.get('sha') for p in obj['parents'] if isinstance(p, dict)]
    require(len(parents) == len(obj['parents']) and all(digest(p) for p in parents), "invalid commit parents")
    return obj['tree']['sha'], parents


def merge_ref(api, repository, number):
    ref = api.get_json('/repos/' + repository + '/git/ref/pull/%d/merge' % number)
    require(isinstance(ref, dict) and ref.get('ref') == 'refs/pull/%d/merge' % number
            and isinstance(ref.get('object'), dict) and ref['object'].get('type') == 'commit'
            and digest(ref['object'].get('sha')), "invalid merge ref")
    return ref['object']['sha']


def classify_event(event, context, api):
    source = lambda reason: {'mode': 'source', 'reason': reason, 'source_run': None}
    if context['event_name'] != 'pull_request' or event.get('action') != 'edited':
        return source('source-event')
    changes = event.get('changes')
    if not isinstance(changes, dict) or not changes or any(k not in ('title', 'body') for k in changes):
        return source('unknown-changes')
    for key, value in changes.items():
        if not isinstance(value, dict) or set(value) != {'from'} or not (isinstance(value['from'], str) or (key == 'body' and value['from'] is None)):
            return source('unknown-changes')
    try:
        event_pr = event_snapshot(event, context)
        require(event_pr is not None, "event PR identity unavailable")
        require(repo_identity(event.get('repository')) == (context['repository_id'], context['repository']), "event repository mismatch")
        prefix = '/repos/' + context['repository']
        live_obj = api.get_json(prefix + '/pulls/%d' % event_pr['number'])
        live = snapshot(live_obj, event_pr['number'])
        identity = ('number', 'head_repo_id', 'head_repo', 'head_ref', 'head_sha', 'base_repo_id', 'base_repo', 'base_ref')
        if any(event_pr[k] != live[k] for k in identity) or event_pr['merged'] != live['merged']:
            return source('candidate-changed')
        if live['merged']:
            if event['pull_request'].get('state') != 'closed' or live_obj.get('state') != 'closed' or not digest(live['merge_sha']) or event_pr['merge_sha'] != live['merge_sha']:
                return source('candidate-changed')
            tree, _ = commit_identity(api, context['repository'], live['merge_sha'])
            run = select_run(api, context, live, True)
            record, pointer = resolve_source(run, api, context)
            require(record['event_name'] == 'push' and record['event_ref'] == 'refs/heads/main'
                    and record['event_sha'] == live['merge_sha'] and record['checkout_sha'] == live['merge_sha']
                    and record['checkout_tree'] == tree, "merged source identity mismatch")
            later_obj = api.get_json(prefix + '/pulls/%d' % live['number'])
            later = snapshot(later_obj, live['number'])
            require(later_obj.get('state') == 'closed' and all(later[k] == live[k] for k in identity + ('merged', 'merge_sha')), "PR changed during lookup")
            return {'mode': 'metadata', 'reason': 'unchanged-merged-source', 'source_run': pointer}
        if event['pull_request'].get('state') != 'open' or live_obj.get('state') != 'open' or live_obj.get('mergeable') is not True or event_pr['base_sha'] != live['base_sha']:
            return source('candidate-changed')
        candidate = merge_ref(api, context['repository'], live['number'])
        if not (candidate == live['merge_sha'] == context['event_sha'] == context['checkout_sha']):
            return source('candidate-changed')
        tree, parents = commit_identity(api, context['repository'], candidate)
        if tree != context['checkout_tree'] or parents != [live['base_sha'], live['head_sha']]:
            return source('candidate-changed')
        run = select_run(api, context, live, False)
        record, pointer = resolve_source(run, api, context)
        require(record['event_name'] == 'pull_request' and record['pull_request'] == live
                and record['event_ref'] == context['event_ref'] and record['event_sha'] == candidate
                and record['checkout_sha'] == candidate and record['checkout_tree'] == tree, "source candidate mismatch")
        later_obj = api.get_json(prefix + '/pulls/%d' % live['number'])
        later = snapshot(later_obj, live['number'])
        require(later == live and later_obj.get('state') == 'open' and later_obj.get('mergeable') is True
                and merge_ref(api, context['repository'], live['number']) == candidate, "candidate changed during lookup")
        return {'mode': 'metadata', 'reason': 'unchanged-open-candidate', 'source_run': pointer}
    except (EvidenceError, urllib.error.URLError, TimeoutError, OSError):
        print('CI event evidence unavailable', file=sys.stderr)
        return source('evidence-unavailable')


def execution_inputs(env):
    def integer(name):
        value = env[name]
        if not re.fullmatch(r'[1-9][0-9]*', value):
            raise ValueError('invalid ' + name)
        return int(value)
    def git(*args):
        return subprocess.run(['git', *args], check=True, stdout=subprocess.PIPE, text=True).stdout.strip()
    context = {'repository': env['GITHUB_REPOSITORY'], 'repository_id': integer('GITHUB_REPOSITORY_ID'),
               'event_name': env['GITHUB_EVENT_NAME'], 'event_ref': env['GITHUB_REF'], 'event_sha': env['GITHUB_SHA'],
               'workflow_sha': env['CI_WORKFLOW_SHA'], 'run_id': integer('GITHUB_RUN_ID'), 'run_attempt': integer('GITHUB_RUN_ATTEMPT'),
               'checkout_sha': git('rev-parse', 'HEAD'), 'checkout_tree': git('rev-parse', 'HEAD^{tree}'),
               'checkout_parents': git('show', '-s', '--format=%P', 'HEAD').split()}
    require(REPO.fullmatch(context['repository']) and context['event_name'] and context['event_ref']
            and all(digest(context[k]) for k in ('event_sha', 'workflow_sha', 'checkout_sha', 'checkout_tree'))
            and all(digest(p) for p in context['checkout_parents']) and context['checkout_sha'] == context['event_sha'], "invalid environment or checkout")
    with open(env['GITHUB_EVENT_PATH'], 'rb') as stream:
        event = decode(read_bounded(stream, MAX_JSON), MAX_JSON)
    require(isinstance(event, dict), "event must be an object")
    require(repo_identity(event.get('repository')) == (context['repository_id'], context['repository']), "invalid event repository")
    if context['event_name'] == 'pull_request':
        require(isinstance(event.get('action'), str) and event['action'], "missing PR action")
    else:
        require(context['event_name'] != 'push' or event.get('action') is None, "invalid push action")
    return event, context


def event_record(event, context, result):
    change_keys = sorted(event['changes']) if context['event_name'] == 'pull_request' and isinstance(event.get('changes'), dict) else None
    pr = event_snapshot(event, context) if context['event_name'] == 'pull_request' else None
    record = {'schema_version': 1, 'repository': context['repository'], 'repository_id': context['repository_id'],
              'workflow_path': WORKFLOW, 'workflow_sha': context['workflow_sha'], 'run_id': context['run_id'],
              'run_attempt': context['run_attempt'], 'event_name': context['event_name'],
              'action': event.get('action') if context['event_name'] == 'pull_request' else None,
              'event_ref': context['event_ref'], 'event_sha': context['event_sha'], 'checkout_sha': context['checkout_sha'],
              'checkout_tree': context['checkout_tree'], 'change_keys': change_keys, 'pull_request': pr, **result}
    return validate_record(record)


def verify_source_check(event, context, metadata_record, job_name, api, *,
                        timeout_seconds=21000, poll_seconds=60,
                        monotonic=time.monotonic, sleep=time.sleep):
    require(job_name in SOURCE_CHECKS, 'invalid source check name')
    require(timeout_seconds > 0 and poll_seconds > 0, 'invalid polling interval')
    deadline = monotonic() + timeout_seconds
    prefix = '/repos/' + context['repository']
    metadata_record = validate_record(metadata_record)
    require(metadata_record['mode'] == 'metadata', 'record is not metadata')
    pointer = metadata_record['source_run']
    require(pointer['run_id'] < context['run_id'], 'invalid source lineage')

    def remaining():
        value = deadline - monotonic()
        require(value > 0, 'source verification timed out')
        return value

    def source_identity():
        fresh = event_record(event, context, classify_event(event, context, api))
        require(fresh == metadata_record, 'metadata classification changed')
        run = api.get_json(prefix + '/actions/runs/%d' % pointer['run_id'])
        run_identity(run, context['repository'], context['repository_id'])
        require(run['id'] == pointer['run_id'] and run['run_attempt'] == pointer['run_attempt'], 'source run changed')
        record, resolved = resolve_source(run, api, context)
        require(record['mode'] == 'source' and resolved == pointer, 'source pointer changed')
        pr = metadata_record['pull_request']
        require(pr is not None, 'missing PR snapshot')
        if pr['merged']:
            require(run.get('event') == 'push' and run.get('head_branch') == 'main'
                    and run['head_sha'] == pr['merge_sha'] and record['checkout_sha'] == pr['merge_sha'], 'wrong merged source')
        else:
            require(run.get('event') == 'pull_request', 'wrong PR source')
        require(positive(run.get('check_suite_id')), 'invalid check suite')
        return run

    def state(obj, kind):
        require(isinstance(obj, dict), 'invalid ' + kind)
        status, conclusion = obj.get('status'), obj.get('conclusion')
        require(status in PENDING or status == 'completed', 'invalid ' + kind + ' status')
        if status == 'completed':
            require(conclusion == 'success', kind + ' did not succeed')
            return True
        require(conclusion is None, 'invalid ' + kind + ' conclusion')
        return False

    def list_job(run_done):
        data = api.get_json(prefix + '/actions/runs/%d/attempts/%d/jobs?per_page=100'
                            % (pointer['run_id'], pointer['run_attempt']))
        require(isinstance(data, dict) and isinstance(data.get('jobs'), list)
                and type(data.get('total_count')) is int and data['total_count'] == len(data['jobs'])
                and all(isinstance(j, dict) and isinstance(j.get('name'), str) for j in data['jobs']), 'ambiguous jobs listing')
        jobs = [j for j in data['jobs'] if j['name'] == job_name]
        require(len(jobs) <= 1 and (jobs or not run_done), 'missing or ambiguous source job')
        return jobs[0] if jobs else None

    def job_identity(job, run, fixed_id=None):
        require(isinstance(job, dict) and positive(job.get('id')) and (fixed_id is None or job['id'] == fixed_id)
                and job.get('run_id') == pointer['run_id'] and job.get('run_attempt') == pointer['run_attempt']
                and job.get('name') == job_name and job.get('head_sha') == run['head_sha'], 'source job identity mismatch')
        url = job.get('check_run_url')
        match = re.fullmatch(re.escape('https://api.github.com' + prefix + '/check-runs/') + r'([1-9][0-9]*)', url) if isinstance(url, str) else None
        require(match is not None, 'invalid check URL')
        return int(match.group(1))

    def check_identity(check, run, check_id):
        require(isinstance(check, dict) and check.get('id') == check_id and check.get('name') == job_name
                and check.get('head_sha') == run['head_sha'] and isinstance(check.get('app'), dict)
                and check['app'].get('id') == 15368 and isinstance(check.get('check_suite'), dict)
                and check['check_suite'].get('id') == run['check_suite_id'], 'source check identity mismatch')
        return state(check, 'source check')

    try:
        remaining()
        initial = source_identity()
        fixed_run = {k: initial.get(k) for k in ('id', 'run_attempt', 'event', 'head_sha', 'head_branch', 'path', 'head_repository', 'check_suite_id')}
        job_id = check_id = None
        last_status = None
        while True:
            remaining()
            run = api.get_json(prefix + '/actions/runs/%d' % pointer['run_id'])
            run_identity(run, context['repository'], context['repository_id'])
            require(all(run.get(k) == v for k, v in fixed_run.items()), 'source run changed')
            run_done = state(run, 'source run')
            job = list_job(run_done) if job_id is None else api.get_json(prefix + '/actions/jobs/%d' % job_id)
            first_seen = job is not None and job_id is None
            if job is None:
                job_done = False
            else:
                current_check = job_identity(job, run, job_id)
                if job_id is None:
                    job_id, check_id = job['id'], current_check
                require(check_id == current_check, 'source check changed')
                job_done = state(job, 'source job')
            status = (run.get('status'), job.get('status') if job else 'not registered')
            changed = status != last_status
            if changed:
                print('CI source verification: run=%s job=%s' % status, file=sys.stderr)
                last_status = status
            check_done = False
            if job is not None and (first_seen or changed or (job_done and run_done)):
                check_done = check_identity(api.get_json(prefix + '/check-runs/%d' % check_id), run, check_id)
            if job_done and run_done and check_done:
                remaining()
                final_run = source_identity()
                require(all(final_run.get(k) == v for k, v in fixed_run.items()) and state(final_run, 'source run'), 'source run changed')
                final_job = list_job(True)
                require(job_identity(final_job, final_run, job_id) == check_id and state(final_job, 'source job'), 'source job changed')
                require(check_identity(api.get_json(prefix + '/check-runs/%d' % check_id), final_run, check_id), 'source check changed')
                final_run = api.get_json(prefix + '/actions/runs/%d' % pointer['run_id'])
                run_identity(final_run, context['repository'], context['repository_id'])
                require(all(final_run.get(k) == v for k, v in fixed_run.items()) and state(final_run, 'source run'), 'source run changed')
                remaining()
                return {'source_run_id': pointer['run_id'], 'source_run_attempt': pointer['run_attempt'],
                        'source_artifact_id': pointer['artifact_id'], 'source_job_id': job_id,
                        'source_check_id': check_id, 'source_job_name': job_name, 'source_conclusion': 'success'}
            sleep(min(poll_seconds, remaining()))
    except (urllib.error.URLError, TimeoutError, OSError, ValueError, KeyError, TypeError) as exc:
        if isinstance(exc, EvidenceError):
            raise
        raise EvidenceError('source evidence unavailable') from exc


def main():
    parser = argparse.ArgumentParser()
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--record')
    group.add_argument('--source-check', choices=SOURCE_CHECKS)
    args = parser.parse_args()
    env = os.environ
    event, context = execution_inputs(env)
    api = GitHubAPI(env.get('GH_TOKEN', ''))
    api.prefix = '/repos/' + context['repository']
    if args.source_check:
        record = validate_record(decode(env['CI_METADATA_RECORD'].encode('utf-8'), MAX_RECORD))
        print(json.dumps(verify_source_check(event, context, record, args.source_check, api), separators=(',', ':')))
        return 0
    result = classify_event(event, context, api)
    record = event_record(event, context, result)
    print(json.dumps({'action': record['action'], 'changes': event.get('changes'), 'pull_request': record['pull_request'],
                      'event_ref': context['event_ref'], 'event_sha': context['event_sha'], 'checkout_sha': context['checkout_sha'],
                      'checkout_tree': context['checkout_tree']}, separators=(',', ':')))
    path = Path(args.record)
    path.parent.mkdir(parents=True, exist_ok=True)
    encoded = (json.dumps(record, sort_keys=True, separators=(',', ':')) + '\n').encode('utf-8')
    require(len(encoded) <= MAX_RECORD, 'record exceeds size limit')
    path.write_bytes(encoded)
    with open(env['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
        output.write('mode=%s\nreason=%s\nrecord=%s\n' % (result['mode'], result['reason'], encoded.decode('utf-8').rstrip('\n')))
    return 0


def cli():
    try:
        return main()
    except (EvidenceError, KeyError, OSError, subprocess.CalledProcessError, ValueError) as exc:
        if '--source-check' in sys.argv:
            message = str(exc) if isinstance(exc, EvidenceError) else 'invalid input or unavailable evidence'
            print('CI source verification failed: ' + message.replace('\n', ' ')[:300], file=sys.stderr)
        else:
            print('CI event classification failed: ' + str(exc).replace('\n', ' ')[:300], file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(cli())
