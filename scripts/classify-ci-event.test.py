#!/usr/bin/env python3
"""Behavioral regressions for candidate evidence and the classifier CLI."""
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import urllib.error
import zipfile
from unittest import mock

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('ci_event', ROOT / 'scripts/classify-ci-event.py')
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)
A, B, C, D, E, F = (c * 40 for c in 'abcdef')
REPOSITORY = {'id': 10, 'full_name': 'owner/project'}
PREFIX = '/repos/owner/project'


def zip_record(record):
    data = io.BytesIO()
    with zipfile.ZipFile(data, 'w') as archive:
        archive.writestr('record.json', json.dumps(record))
    return data.getvalue()


def payload(merged=False):
    pr = {'number': 7, 'state': 'closed' if merged else 'open', 'merged': merged,
          'mergeable': None if merged else True, 'merge_commit_sha': C,
          'head': {'repo': REPOSITORY, 'ref': 'feature', 'sha': B},
          'base': {'repo': REPOSITORY, 'ref': 'main', 'sha': A}}
    return {'action': 'edited', 'number': 7, 'repository': REPOSITORY,
            'changes': {'title': {'from': 'ci: before'}}, 'pull_request': pr}


def context(merged=False):
    return {'repository': 'owner/project', 'repository_id': 10, 'event_name': 'pull_request',
            'event_ref': 'refs/heads/main' if merged else 'refs/pull/7/merge',
            'event_sha': D if merged else C, 'workflow_sha': F, 'run_id': 400, 'run_attempt': 1,
            'checkout_sha': D if merged else C, 'checkout_tree': E,
            'checkout_parents': [A, B]}


def source_record(merged=False, run_id=200):
    ctx = context(merged)
    return {'schema_version': 1, 'repository': 'owner/project', 'repository_id': 10,
            'workflow_path': ci.WORKFLOW, 'workflow_sha': F, 'run_id': run_id, 'run_attempt': 1,
            'event_name': 'push' if merged else 'pull_request', 'action': None if merged else 'opened',
            'event_ref': 'refs/heads/main' if merged else 'refs/pull/7/merge',
            'event_sha': C, 'checkout_sha': C, 'checkout_tree': E,
            'change_keys': None, 'pull_request': None if merged else ci.event_snapshot(payload(), ctx),
            'mode': 'source', 'reason': 'source-event', 'source_run': None}


def run_record(merged=False, run_id=200):
    return {'id': run_id, 'run_attempt': 1, 'path': ci.WORKFLOW, 'repository': REPOSITORY,
            'head_repository': REPOSITORY, 'head_sha': C if merged else B,
            'head_branch': 'main' if merged else 'feature', 'event': 'push' if merged else 'pull_request',
            'pull_requests': [] if merged else [{'number': 7}], 'status': 'completed', 'conclusion': 'success'}


class FakeAPI:
    def __init__(self, merged=False):
        self.merged = merged
        self.event = payload(merged)
        self.ctx = context(merged)
        self.pr = copy.deepcopy(self.event['pull_request'])
        if merged:
            self.pr['base']['sha'] = D  # main advanced after the merge
        self.run = run_record(merged)
        self.record = source_record(merged)
        self.runs = [self.run]
        self.records = {200: self.record}
        self.run_objects = {200: self.run}
        self.artifacts = {200: 1200}
        self.expired = False
        self.fail = None
        self.changed_pr = None
        self.pr_reads = 0
        self.changed_ref = False
        self.ref_reads = 0
        self.zip_override = None
        self.producer_status = 'completed'
        self.producer_conclusion = 'success'
        self.attempt_change = False
        self.run_reads = {}

    def get_json(self, path):
        if self.fail:
            raise self.fail
        if path == PREFIX + '/pulls/7':
            self.pr_reads += 1
            return copy.deepcopy(self.changed_pr if self.pr_reads > 1 and self.changed_pr else self.pr)
        if path == PREFIX + '/git/ref/pull/7/merge':
            self.ref_reads += 1
            return {'ref': 'refs/pull/7/merge', 'object': {'type': 'commit',
                    'sha': D if self.changed_ref and self.ref_reads > 1 else C}}
        if path == PREFIX + '/git/commits/' + C:
            return {'sha': C, 'tree': {'sha': E}, 'parents': [{'sha': A}, {'sha': B}]}
        if '/actions/workflows/ci.yml/runs?' in path:
            return {'total_count': len(self.runs), 'workflow_runs': copy.deepcopy(self.runs)}
        if '/artifacts?' in path:
            run_id = int(path.split('/runs/')[1].split('/')[0])
            run = self.run_objects[run_id]
            artifact = {'id': self.artifacts[run_id], 'name': 'ci-event-%d-%d' % (run_id, run['run_attempt']),
                        'expired': self.expired, 'created_at': '2026-01-01T00:00:01Z',
                        'workflow_run': {'id': run_id, 'repository_id': 10, 'head_repository_id': 10,
                                         'head_sha': run['head_sha'], 'head_branch': run['head_branch']}}
            return {'total_count': 1, 'artifacts': [artifact]}
        if '/jobs?' in path:
            job = {'name': 'CI Event Classification', 'status': self.producer_status,
                   'conclusion': self.producer_conclusion, 'started_at': '2026-01-01T00:00:00Z',
                   'completed_at': '2026-01-01T00:00:02Z' if self.producer_status == 'completed' else None}
            return {'total_count': 1, 'jobs': [job]}
        if '/actions/runs/' in path:
            run_id = int(path.rsplit('/', 1)[1])
            self.run_reads[run_id] = self.run_reads.get(run_id, 0) + 1
            run = copy.deepcopy(self.run_objects[run_id])
            if self.attempt_change and self.run_reads[run_id] > 1:
                run['run_attempt'] = 2
            return run
        raise AssertionError('unexpected path ' + path)

    def get_artifact(self, artifact_id):
        if self.zip_override is not None:
            return self.zip_override
        run_id = next(k for k, v in self.artifacts.items() if v == artifact_id)
        return zip_record(self.records[run_id])

    def classify(self):
        return ci.classify_event(self.event, self.ctx, self)

    def add_metadata(self):
        run = run_record(run_id=300)
        record = copy.deepcopy(self.record)
        record.update(run_id=300, action='edited', mode='metadata', reason='unchanged-open-candidate',
                      change_keys=['title'], source_run={'run_id': 200, 'run_attempt': 1, 'artifact_id': 1200})
        self.runs.insert(0, run)
        self.run_objects[300] = run
        self.records[300] = record
        self.artifacts[300] = 1300


class ClassificationTest(unittest.TestCase):
    def test_open_metadata_changes(self):
        for changes in ({'title': {'from': 'before'}}, {'body': {'from': None}},
                        {'title': {'from': ''}, 'body': {'from': 'old'}}):
            with self.subTest(changes=changes):
                api = FakeAPI()
                api.event['changes'] = changes
                self.assertEqual(api.classify(), {'mode': 'metadata', 'reason': 'unchanged-open-candidate',
                                                 'source_run': {'run_id': 200, 'run_attempt': 1, 'artifact_id': 1200}})

    def test_merged_metadata_with_main_advanced(self):
        api = FakeAPI(merged=True)
        self.assertEqual(api.classify()['reason'], 'unchanged-merged-source')

    def test_full_events_never_use_api(self):
        for name, action in [('push', None), ('pull_request', 'opened'), ('pull_request', 'synchronize'),
                             ('pull_request', 'reopened'), ('other', 'edited')]:
            api = FakeAPI()
            api.ctx['event_name'] = name
            api.event['action'] = action
            api.fail = AssertionError('API called for full event')
            self.assertEqual(api.classify()['reason'], 'source-event')

    def test_unknown_changes(self):
        for changes in (None, {}, [], {'base': {'from': 'main'}}, {'title': {'from': 'x'}, 'base': {'from': 'x'}},
                        {'title': {}}, {'title': {'from': None}}, {'body': {'from': 1}},
                        {'body': {'from': '', 'extra': 1}}):
            api = FakeAPI()
            api.event['changes'] = changes
            self.assertEqual(api.classify()['reason'], 'unknown-changes')

    def test_candidate_differences_and_races_fail_closed(self):
        cases = [lambda a: a.pr['head'].__setitem__('sha', D),
                 lambda a: a.pr['base'].__setitem__('sha', D),
                 lambda a: a.pr['base'].__setitem__('ref', 'other'),
                 lambda a: a.pr['head']['repo'].__setitem__('id', 11),
                 lambda a: a.pr.__setitem__('merge_commit_sha', D),
                 lambda a: a.ctx.__setitem__('checkout_tree', D),
                 lambda a: a.ctx.__setitem__('event_sha', D),
                 lambda a: setattr(a, 'changed_ref', True),
                 lambda a: setattr(a, 'changed_pr', dict(a.pr, mergeable=False))]
        for change in cases:
            api = FakeAPI()
            change(api)
            self.assertEqual(api.classify()['mode'], 'source')

    def test_merged_identity_and_snapshot_races_fail_closed(self):
        mutations = [lambda a: a.pr.__setitem__('merge_commit_sha', D),
                     lambda a: a.pr.__setitem__('merged', False),
                     lambda a: a.pr['head'].__setitem__('sha', D),
                     lambda a: a.pr['base'].__setitem__('ref', 'other'),
                     lambda a: a.record.__setitem__('checkout_tree', D),
                     lambda a: a.record.__setitem__('event_sha', D),
                     lambda a: setattr(a, 'changed_pr', dict(a.pr, state='open'))]
        for mutate in mutations:
            api = FakeAPI(merged=True)
            mutate(api)
            self.assertEqual(api.classify()['mode'], 'source')

    def test_newer_ambiguous_or_unrelated_runs(self):
        api = FakeAPI()
        newer = run_record(run_id=300)
        newer['pull_requests'] = []
        api.runs.insert(0, newer)
        self.assertEqual(api.classify()['mode'], 'source')
        api = FakeAPI()
        newer = run_record(run_id=300)
        newer['pull_requests'] = [{'number': 8}]
        api.runs.insert(0, newer)
        self.assertEqual(api.classify()['mode'], 'metadata')

    def test_json_and_archive_limits(self):
        with self.assertRaises(ci.EvidenceError):
            ci.decode(b'{"a":1,"a":2}', ci.MAX_JSON)
        with self.assertRaises(ci.EvidenceError):
            ci.decode(b'x' * (ci.MAX_JSON + 1), ci.MAX_JSON)
        raw = io.BytesIO()
        with zipfile.ZipFile(raw, 'w') as archive:
            archive.writestr('record.json', 'x' * (ci.MAX_RECORD + 1))
        with self.assertRaises(ci.EvidenceError):
            ci.archive_record(raw.getvalue())

    def test_artifact_provenance_is_authoritative(self):
        for field, value in [('id', 999), ('repository_id', 999), ('head_repository_id', 999), ('head_sha', D), ('head_branch', 'other')]:
            api = FakeAPI()
            original = api.get_json
            def altered(path):
                result = original(path)
                if '/artifacts?' in path:
                    result['artifacts'][0]['workflow_run'][field] = value
                return result
            api.get_json = altered
            self.assertEqual(api.classify()['mode'], 'source')

    def test_source_run_conclusion_does_not_become_pass_certificate(self):
        for merged in (False, True):
            for status, conclusion in [('completed', 'success'), ('completed', 'failure'),
                                       ('completed', 'cancelled'), ('queued', None), ('in_progress', None)]:
                for producer_conclusion in ('success', 'failure', 'cancelled'):
                    with self.subTest(merged=merged, status=status, conclusion=conclusion,
                                      producer_conclusion=producer_conclusion):
                        api = FakeAPI(merged=merged)
                        api.run.update(status=status, conclusion=conclusion)
                        api.producer_conclusion = producer_conclusion
                        result = api.classify()
                        self.assertEqual(result['mode'], 'metadata')
                        self.assertEqual(set(result), {'mode', 'reason', 'source_run'})
                        self.assertEqual(result['source_run'], {'run_id': 200, 'run_attempt': 1, 'artifact_id': 1200})

    def test_failed_metadata_producer_can_reference_source_record(self):
        api = FakeAPI()
        api.add_metadata()
        api.producer_conclusion = 'failure'
        self.assertEqual(api.classify()['source_run'], {'run_id': 200, 'run_attempt': 1, 'artifact_id': 1200})

    def test_producer_timestamp_and_status_are_required(self):
        mutations = [lambda job: job.__setitem__('started_at', '2026-01-01T00:00:02Z'),
                     lambda job: job.__setitem__('completed_at', '2026-01-01T00:00:00Z'),
                     lambda job: job.__setitem__('completed_at', None),
                     lambda job: job.__setitem__('status', 'queued')]
        for mutate in mutations:
            api = FakeAPI()
            original = api.get_json
            def altered(path):
                result = original(path)
                if '/jobs?' in path:
                    mutate(result['jobs'][0])
                return result
            api.get_json = altered
            self.assertEqual(api.classify()['reason'], 'evidence-unavailable')

    def test_metadata_pointer_only_one_hop(self):
        api = FakeAPI()
        api.add_metadata()
        self.assertEqual(api.classify()['source_run']['run_id'], 200)
        for mutation in ('cycle', 'attempt', 'artifact', 'cross_pr', 'wrong_repo', 'wrong_workflow'):
            api = FakeAPI()
            api.add_metadata()
            if mutation == 'cycle':
                api.records[300]['source_run']['run_id'] = 300
            elif mutation == 'attempt':
                api.records[300]['source_run']['run_attempt'] = 2
            elif mutation == 'artifact':
                api.records[300]['source_run']['artifact_id'] = 999
            elif mutation == 'cross_pr':
                api.records[200]['pull_request']['number'] = 8
            elif mutation == 'wrong_repo':
                api.records[200]['repository'] = 'other/repo'
            else:
                api.records[200]['workflow_path'] = '.github/workflows/other.yml'
            self.assertEqual(api.classify()['mode'], 'source', mutation)

    def test_latest_missing_evidence_never_uses_older_success(self):
        for newest_mode in ('source', 'metadata'):
            api = FakeAPI()
            api.add_metadata()
            if newest_mode == 'source':
                api.records[300] = source_record(run_id=300)
            # A missing artifact is represented by an empty authoritative listing.
            original = api.get_json
            def missing(path):
                if '/runs/300/artifacts?' in path:
                    return {'total_count': 0, 'artifacts': []}
                return original(path)
            api.get_json = missing
            self.assertEqual(api.classify()['mode'], 'source')
            self.assertNotIn(200, api.run_reads)

    def test_evidence_errors_fail_closed(self):
        failures = [urllib.error.HTTPError('https://api.github.com', 403, 'forbidden', {}, None),
                    urllib.error.HTTPError('https://api.github.com', 404, 'missing', {}, None),
                    urllib.error.HTTPError('https://api.github.com', 410, 'gone', {}, None),
                    TimeoutError('timeout')]
        for failure in failures:
            api = FakeAPI()
            api.fail = failure
            self.assertEqual(api.classify()['reason'], 'evidence-unavailable')
        for mutation in ('expired', 'attempt', 'invalid_producer_status', 'oversize', 'duplicate', 'extra', 'path', 'symlink', 'malformed'):
            api = FakeAPI()
            if mutation == 'expired':
                api.expired = True
            elif mutation == 'attempt':
                api.attempt_change = True
            elif mutation == 'invalid_producer_status':
                api.producer_status = 'queued'
            elif mutation == 'oversize':
                api.zip_override = b'x' * (ci.MAX_ZIP + 1)
            elif mutation == 'malformed':
                api.zip_override = b'not a zip'
            else:
                raw = io.BytesIO()
                with zipfile.ZipFile(raw, 'w') as archive:
                    if mutation == 'duplicate':
                        archive.writestr('record.json', '{"schema_version":1,"schema_version":1}')
                    elif mutation == 'extra':
                        archive.writestr('record.json', '{}')
                        archive.writestr('extra', '{}')
                    elif mutation == 'path':
                        archive.writestr('../record.json', '{}')
                    else:
                        member = zipfile.ZipInfo('record.json')
                        member.create_system = 3
                        member.external_attr = (0o120777 << 16)
                        archive.writestr(member, '{}')
                api.zip_override = raw.getvalue()
            self.assertEqual(api.classify()['mode'], 'source', mutation)

    def test_redirect_never_forwards_token(self):
        calls = []
        redirect_body = io.BytesIO(b'redirect')
        class Opener:
            def open(self, request, timeout):
                calls.append(request)
                if len(calls) == 1:
                    raise urllib.error.HTTPError(request.full_url, 302, 'redirect',
                                                 {'Location': 'https://storage.example/object'}, redirect_body)
                return io.BytesIO(b'archive')
        api = ci.GitHubAPI('secret')
        api.prefix = PREFIX
        with mock.patch.object(ci.urllib.request, 'build_opener', return_value=Opener()):
            self.assertEqual(api.get_artifact(1), b'archive')
        self.assertTrue(redirect_body.closed)
        self.assertEqual(calls[0].get_header('Authorization'), 'Bearer secret')
        self.assertIsNone(calls[1].get_header('Authorization'))
        handler = ci.HTTPSRedirect()
        redirected = handler.redirect_request(calls[1], None, 302, 'redirect', {}, 'https://other.example/object')
        self.assertIsNone(redirected.get_header('Authorization'))
        with self.assertRaises(ci.EvidenceError):
            handler.redirect_request(calls[1], None, 302, 'redirect', {}, 'http://other.example/object')


class CLITest(unittest.TestCase):
    def setUp(self):
        scratch = ROOT / '.tmp/agent'
        scratch.mkdir(parents=True, exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(prefix='ci-event-test-', dir=scratch)
        self.addCleanup(self.tmp.cleanup)
        self.directory = Path(self.tmp.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_') and not k.startswith('GITHUB_')}
        self.env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null',
                        GIT_AUTHOR_NAME='Test', GIT_AUTHOR_EMAIL='test@example.invalid',
                        GIT_COMMITTER_NAME='Test', GIT_COMMITTER_EMAIL='test@example.invalid')
        self.git('init', '-q')
        (self.directory / 'fixture').write_text('input')
        self.git('add', 'fixture')
        self.git('-c', 'core.hooksPath=/dev/null', 'commit', '-qm', 'fixture')
        sha = self.git('rev-parse', 'HEAD')
        self.event = self.directory / 'event.json'
        self.event.write_text(json.dumps({'repository': REPOSITORY}))
        self.output = self.directory / 'output'
        self.record = self.directory / 'record.json'
        self.env.update(GITHUB_REPOSITORY='owner/project', GITHUB_REPOSITORY_ID='10', GITHUB_EVENT_NAME='push',
                        GITHUB_REF='refs/heads/main', GITHUB_SHA=sha, CI_WORKFLOW_SHA=sha,
                        GITHUB_RUN_ID='400', GITHUB_RUN_ATTEMPT='1', GITHUB_EVENT_PATH=str(self.event),
                        GITHUB_OUTPUT=str(self.output), GH_TOKEN='unused')

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.directory, env=self.env, check=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True).stdout.strip()

    def invoke(self):
        return subprocess.run([sys.executable, str(ROOT / 'scripts/classify-ci-event.py'), '--record', str(self.record)],
                              cwd=self.directory, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

    def test_real_checkout_record_and_output(self):
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        record = json.loads(self.record.read_text())
        self.assertEqual(record['checkout_sha'], self.env['GITHUB_SHA'])
        self.assertEqual(record['mode'], 'source')
        self.assertEqual(self.output.read_text(), 'mode=source\nreason=source-event\n')

    def test_real_merge_parents_record_candidate(self):
        branch = self.git('branch', '--show-current')
        self.git('checkout', '-qb', 'feature')
        (self.directory / 'head').write_text('head')
        self.git('add', 'head')
        self.git('-c', 'core.hooksPath=/dev/null', 'commit', '-qm', 'head')
        head = self.git('rev-parse', 'HEAD')
        self.git('checkout', '-q', branch)
        (self.directory / 'base').write_text('base')
        self.git('add', 'base')
        self.git('-c', 'core.hooksPath=/dev/null', 'commit', '-qm', 'base')
        base = self.git('rev-parse', 'HEAD')
        self.git('-c', 'core.hooksPath=/dev/null', 'merge', '--no-ff', '-qm', 'merge', 'feature')
        merge = self.git('rev-parse', 'HEAD')
        event = payload()
        event['action'] = 'opened'
        event['pull_request']['head']['sha'] = head
        event['pull_request']['base']['sha'] = base
        event['pull_request']['merge_commit_sha'] = None
        self.event.write_text(json.dumps(event))
        self.env.update(GITHUB_EVENT_NAME='pull_request', GITHUB_REF='refs/pull/7/merge', GITHUB_SHA=merge)
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(self.record.read_text())['pull_request']['merge_sha'], merge)
        event['pull_request']['head']['sha'] = 'a' * 40
        self.event.write_text(json.dumps(event))
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(json.loads(self.record.read_text())['pull_request'])

    def test_invalid_inputs_and_write_failures(self):
        for failure in ('head', 'json', 'record', 'output'):
            with self.subTest(failure=failure):
                if failure == 'head':
                    self.env['GITHUB_SHA'] = 'a' * 40
                elif failure == 'json':
                    self.event.write_text('{broken')
                elif failure == 'record':
                    self.record = self.directory / 'missing-parent-file' / 'record.json'
                    self.record.parent.write_text('not a directory')
                else:
                    self.env['GITHUB_OUTPUT'] = str(self.directory)
                result = self.invoke()
                self.assertNotEqual(result.returncode, 0)
                if self.output.exists():
                    self.assertNotIn('mode=metadata', self.output.read_text())
                # Reset shared fixture between subtests.
                self.env['GITHUB_SHA'] = self.git('rev-parse', 'HEAD')
                self.env['GITHUB_OUTPUT'] = str(self.output)
                self.event.write_text(json.dumps({'repository': REPOSITORY}))
                self.record = self.directory / 'record.json'

    def test_existing_title_validator(self):
        validator = str(ROOT / 'scripts/check-pr-title.sh')
        for title, expected in [('ci: isolate pull request metadata validation', 0), ('Round29 invalid title', 1)]:
            result = subprocess.run(['bash', validator, title], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertEqual(result.returncode, expected)


if __name__ == '__main__':
    unittest.main()
