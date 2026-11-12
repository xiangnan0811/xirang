"""Verification of a declared pull request observation."""
import re
import base64
import hashlib
import json
from urllib.parse import quote

from .capture import get_artifact
from .context import canonical
from .source import select_attempt_artifact
from .store import publish_bytes

NAMES = ('Backend Test & Build', 'Frontend Test & Build', 'Doc Freshness Check')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def one(items, message):
    require(len(items) == 1, message)
    return items[0]


def inventory(raw):
    # This deliberately accepts only the fixed, authenticated workflow's indentation
    # and scalar step names; unfamiliar structure is rejected instead of guessed.
    text = raw.decode('utf-8')
    jobs = re.split(r'(?m)^  (?=[A-Za-z0-9_-]+:\s*$)', text.split('\njobs:\n', 1)[1])
    gates = ("${{ needs.classify.result != 'success' || needs.classify.outputs.mode != 'metadata' }}",
             "${{ always() && (needs.classify.result != 'success' || needs.classify.outputs.mode != 'metadata') }}")
    result = {}
    for name, count in zip(NAMES, (14, 17, 6)):
        job = one([j for j in jobs if re.search(r'(?m)^    name: ' + re.escape(name) + r'\s*$', j)], 'workflow job missing or ambiguous')
        steps = re.split(r'(?m)^      - name: ', job)[1:]
        selected = []
        for step in steps:
            step_name = step.split('\n', 1)[0].strip()
            conditions = re.findall(r'(?m)^        if: (.*)$', step)
            require(len(conditions) <= 1, 'duplicate step condition')
            if conditions and ('needs.classify.result' in conditions[0] or 'needs.classify.outputs.mode' in conditions[0]):
                cond = conditions[0]
                if "!= 'success'" in cond or "!= 'metadata'" in cond:
                    require(cond in gates, 'unknown source gate')
                    if cond == gates[1]:
                        require(name == NAMES[1] and step_name in ('Upload browser execution evidence', 'Check frontend coverage'), 'unexpected always source gate')
                    selected.append(step_name)
        require(len(selected) == count and len(set(selected)) == count, 'source step inventory mismatch')
        result[name] = selected
    return result


def verify_snapshot(context, directory, classifier, *, predecessor=None):
    """Verify one captured observation without network access or mutations."""
    from pathlib import Path
    import hashlib
    import json
    from .capture import next_page, verify_saved_contents
    from .source import select_attempt_artifact

    directory = Path(directory)
    REPO, REPO_ID = context['repository'], context['repository_id']
    source, metadata, title = context['source'], context['metadata'], context['title']
    sr, sa = source['run_id'], source['attempt']
    mr, ma = metadata['run_id'], metadata['attempt']
    tr, ta = title['run_id'], title['attempt']
    mode = context['expected']
    head, base = context['pr']['head_sha'], context['pr']['base_sha']
    checkout, tree = source['checkout_sha'], source['checkout_tree']
    blob, pr_number = context['workflow_blob'], context['pr']['number']

    raw = verify_saved_contents(directory, REPO, context['workflow_head_sha'],
                                '.github/workflows/ci.yml', 'workflow.yml', expected_blob=blob)
    sha256 = hashlib.sha256(raw).hexdigest()
    steps = inventory(raw)

    def blob_for_record(sha):
        require(re.fullmatch(r'[0-9a-f]{40}', sha) is not None, 'invalid workflow commit')
        verify_saved_contents(directory, REPO, sha, '.github/workflows/ci.yml',
                              'workflow-record-' + sha + '.yml', expected_blob=blob)
        return blob

    def read(name):
        return json.loads((directory / name).read_text())

    pr = read('pr.json')
    expected_snapshot = classifier.snapshot(pr, pr_number)
    require(pr['state'] == 'open' and pr['merged'] is False and pr['mergeable'] is True and pr['merge_commit_sha'] == checkout, 'PR state mismatch')
    require(pr['head']['sha'] == head and pr['base']['sha'] == base and pr['head']['ref'] == context['pr']['head_ref'] and pr['base']['ref'] == context['pr']['base_ref'], 'PR ref mismatch')
    require(all(pr[side]['repo']['id'] == REPO_ID and pr[side]['repo']['full_name'] == REPO for side in ('head', 'base')), 'PR repository mismatch')
    protection = read('protection.json')
    checks = protection['required_status_checks']
    require(checks['strict'] is True and protection['enforce_admins']['enabled'] is True and len(checks['checks']) == 4 and {(x['context'], x['app_id']) for x in checks['checks']} == {(x, 15368) for x in (*NAMES, 'PR Title')}, 'branch protection mismatch')
    links = read('rest-pagination.json')
    def pages(name, url, key):
        data = read(name)
        require(isinstance(data, list) and data, 'missing REST pages')
        current = '/repos/' + REPO + '/' + url
        for index in range(len(data)):
            entries = [entry for entry in links if entry['path'] == current]
            require(len(entries) == 1, 'missing REST pagination entry')
            current = next_page(entries[0]['link'], REPO, REPO_ID)
            require((current is not None) == (index + 1 < len(data)), 'REST pagination chain incomplete')
        items = [item for page in data for item in page[key]]
        require(all(page['total_count'] == len(items) for page in data) and len({item['id'] for item in items}) == len(items), 'REST listing incomplete or duplicate')
        return items
    runs = {}
    jobs = {}
    for rid, attempt in ((sr, sa), (mr, ma), (tr, ta)):
        run = read(f'run-{rid}.json')
        require(run['id'] == rid and run['run_attempt'] == attempt and run['head_sha'] == head and run['head_branch'] == context['source']['branch'] and run['event'] == 'pull_request' and run['repository']['id'] == REPO_ID and run['head_repository']['id'] == REPO_ID and run['repository']['full_name'] == REPO and run['head_repository']['full_name'] == REPO, 'run identity mismatch')
        require(run['path'] == ('.github/workflows/pr-metadata.yml' if rid == tr else '.github/workflows/ci.yml'), 'workflow mismatch')
        final_file = directory / f'run-final-{rid}.json'
        require(final_file.is_file(), 'missing final run')
        final = read(final_file.name)
        require(all(final[k] == run[k] for k in ('id', 'run_attempt', 'head_sha', 'head_branch', 'event', 'path', 'repository', 'head_repository')), 'final run identity mismatch')
        runs[rid] = (run, final)
        joblist = pages(f'jobs-{rid}-{attempt}.json', f'actions/runs/{rid}/attempts/{attempt}/jobs?per_page=100', 'jobs')
        require(all(j['run_id'] == rid and j['run_attempt'] == attempt and j['head_sha'] == head for j in joblist), 'job identity mismatch')
        jobs[rid] = joblist
    require(mode == 'pending' or len(jobs[sr]) == 15, 'unexpected terminal source job inventory')
    require(all(len({j['name'] for j in jobs[rid]}) == len(jobs[rid]) for rid in (sr, mr, tr)), 'duplicate attempt job name')
    pages(f'artifacts-{tr}.json', f'actions/runs/{tr}/artifacts?per_page=100', 'artifacts')
    head_checks = pages(f'checks-{head}.json', f'commits/{head}/check-runs?filter=all&per_page=100', 'check_runs')
    records = {}
    artifacts = {}
    for rid, attempt in ((sr, sa), (mr, ma)):
        listing = pages(f'artifacts-{rid}.json', f'actions/runs/{rid}/artifacts?per_page=100', 'artifacts')
        artifact, provenance = select_attempt_artifact(listing, jobs[rid], runs[rid][0], attempt, f'ci-event-{rid}-{attempt}', 'CI Event Classification', 'Upload CI event record', repository_id=REPO_ID)
        archive = (directory.parent.parent / 'artifacts' / f'{artifact["id"]}.zip').read_bytes()
        require(artifact['digest'] == 'sha256:' + hashlib.sha256(archive).hexdigest(), 'record digest mismatch')
        record = classifier.validate_record(classifier.archive_record(archive))
        require(record == read(f'record-{rid}-{attempt}.json'), 'saved record mismatch')
        require(record['repository'] == REPO and record['repository_id'] == REPO_ID and record['run_id'] == rid and record['run_attempt'] == attempt and record['event_name'] == 'pull_request' and record['event_ref'] == 'refs/pull/' + str(pr_number) + '/merge' and record['checkout_sha'] == checkout and record['checkout_tree'] == tree and blob_for_record(record['workflow_sha']) == blob and record['pull_request'] == expected_snapshot, 'record identity mismatch')
        records[rid] = record
        artifacts[rid] = artifact['id']
    require(records[sr]['mode'] == 'source' and records[sr]['reason'] == 'source-event' , 'source record mismatch')
    require(records[mr]['mode'] == 'metadata' and records[mr]['reason'] == 'unchanged-open-candidate' and records[mr]['action'] == 'edited' and records[mr]['change_keys'] == ['body'] and records[mr]['source_run'] == {'run_id': sr, 'run_attempt': sa, 'artifact_id': artifacts[sr]}, 'metadata source pointer mismatch')
    direct = []
    grouped = {}
    for entry in read('graphql.json'):
        grouped.setdefault(entry['surface'], []).append(entry)
    require(set(grouped) == {'head', 'merge', 'direct'}, 'GraphQL surfaces missing')
    for surface, entries in grouped.items():
        cursor = None
        for index, entry in enumerate(entries):
            require(entry['after'] == cursor and not entry['response'].get('errors'), 'GraphQL cursor or errors')
            node = entry['response']['data']['repository']['pullRequest']
            require(node['headRefOid'] == head and node['baseRefOid'] == base and node['mergeable'] == 'MERGEABLE' and node['state'] == 'OPEN' and node['merged'] is False, 'GraphQL PR mismatch')
            expected_state = 'CLEAN' if mode == 'success' else 'BLOCKED'
            require(node['mergeStateStatus'] == expected_state, 'effective merge state mismatch')
            container = node if surface == 'direct' else node['commits']['nodes'][0]['commit'] if surface == 'head' else node['potentialMergeCommit']
            if surface == 'head':
                require(container['oid'] == head, 'GraphQL head mismatch')
            if surface == 'merge' and container is not None:
                require(container['oid'] == checkout, 'GraphQL merge mismatch')
            contexts = ((container or {}).get('statusCheckRollup') or {}).get('contexts')
            if contexts is None:
                require(len(entries) == 1, 'missing GraphQL contexts')
                continue
            page = contexts['pageInfo']
            if surface == 'direct':
                direct.extend(contexts['nodes'])
            cursor = page['endCursor'] if page['hasNextPage'] else None
            require((cursor is not None) == (index + 1 < len(entries)), 'incomplete GraphQL cursor chain')
    def job_check(rid, name):
        job = one([j for j in jobs[rid] if j['name'] == name], 'job missing or ambiguous')
        match = re.fullmatch(r'https://api\.github\.com/repos/' + re.escape(REPO) + r'/check-runs/([0-9]+)', job['check_run_url'])
        require(match is not None, 'invalid job check URL')
        check_id = int(match.group(1))
        run = runs[rid][0]
        suite = run['check_suite_id']
        listing = pages(f'suite-{suite}.json', f'check-suites/{suite}/check-runs?filter=all&per_page=100', 'check_runs')
        check = one([c for c in listing if c['id'] == check_id], 'check missing or ambiguous')
        require(check['name'] == name and check['head_sha'] == head and check['check_suite']['id'] == suite and check['app']['id'] == 15368 and check['status'] == job['status'] and check['conclusion'] == job['conclusion'], 'check identity or outcome mismatch')
        head_check = one([c for c in head_checks if c['id'] == check_id], 'head check missing or ambiguous')
        require(all(head_check[k] == check[k] for k in ('name', 'head_sha', 'status', 'conclusion', 'check_suite', 'app')), 'head check mismatch')
        return job, check
    title_job, title_check = job_check(tr, 'PR Title')
    require(all(r['status'] == 'completed' and r['conclusion'] == 'success' for r in (*runs[tr], title_job, title_check)), 'title unsuccessful')
    waiting = []
    groups = []
    skipped = 0
    for name in NAMES:
        source_job, source_check = job_check(sr, name)
        meta_job, meta_check = job_check(mr, name)
        for check in (meta_check, title_check):
            context = one([c for c in direct if c.get('__typename') == 'CheckRun' and c.get('databaseId') == check['id']], 'required direct check missing or ambiguous')
            require(context['name'] == check['name'] and context['isRequired'] is True and context['checkSuite']['databaseId'] == check['check_suite']['id'] and context['checkSuite']['app']['databaseId'] == 15368 and context['status'].lower() == check['status'] and (context['conclusion'] or '').lower() == (check['conclusion'] or ''), 'required direct check mismatch')
        group = {'name': name, 'source_job_id': source_job['id'], 'source_check_id': source_check['id'], 'source_status': source_job['status'], 'source_conclusion': source_job['conclusion'], 'metadata_job_id': meta_job['id'], 'metadata_check_id': meta_check['id'], 'metadata_status': meta_job['status'], 'metadata_conclusion': meta_job['conclusion']}
        groups.append(group)
        if mode == 'pending':
            require(not (meta_job['status'] == 'completed' and meta_job['conclusion'] == 'success') and not (meta_check['status'] == 'completed' and meta_check['conclusion'] == 'success'), 'metadata verifier succeeded prematurely')
            verify = one([s for s in meta_job['steps'] if s['name'] == 'Verify source check'], 'missing verifier step')
            if source_job['status'] == 'completed' and source_job['conclusion'] == 'success' and source_check['status'] == 'completed' and source_check['conclusion'] == 'success' and verify['status'] == 'in_progress':
                waiting.append(group)
        else:
            expected = 'success' if mode == 'success' else 'failure'
            require(meta_job['status'] == meta_check['status'] == 'completed' and meta_job['conclusion'] == meta_check['conclusion'] == expected, 'metadata verification result mismatch')
            if mode == 'success':
                require(source_job['status'] == source_check['status'] == 'completed' and source_job['conclusion'] == source_check['conclusion'] == 'success', 'source check unsuccessful')
            for step_name in steps[name]:
                step = one([s for s in meta_job['steps'] if s['name'] == step_name], 'source step missing or ambiguous')
                require(step['status'] == 'completed' and step['conclusion'] == 'skipped', 'metadata source step was not skipped')
                skipped += 1
    if mode == 'pending':
        require(all(r['status'] != 'completed' for rid in (sr, mr) for r in runs[rid]), 'workflow completed during pending snapshot')
        require(not any(j['status'] == 'completed' and j['conclusion'] != 'success' for j in jobs[sr]), 'source has known unsuccessful job')
        require(waiting, 'no verifier waiting for whole source workflow')
    else:
        expected = 'success' if mode == 'success' else 'failure'
        require(all(r['status'] == 'completed' and r['conclusion'] == expected for rid in (sr, mr) for r in runs[rid]), 'workflow result mismatch')
        require(skipped == 37, 'skip count mismatch')
    result = {'mode': mode, 'effective_state': 'CLEAN' if mode == 'success' else 'BLOCKED', 'workflow': {'head': head, 'blob': blob, 'sha256': sha256}, 'source': {'run': sr, 'attempt': sa, 'artifact': artifacts[sr]}, 'metadata': {'run': mr, 'attempt': ma, 'artifact': artifacts[mr]}, 'title': {'run': tr, 'attempt': ta, 'job_id': title_job['id'], 'check_id': title_check['id']}, 'groups': groups, 'waiting_on_whole_run': waiting, 'skip_counts': {name: len(steps[name]) for name in NAMES} if mode != 'pending' else {}, 'skipped': skipped}
    if mode != 'pending':
        require(isinstance(predecessor, dict) and predecessor.get('mode') == 'pending',
                'missing pending predecessor')
        old = {item['name']: item for item in predecessor['groups']}
        require(set(old) == set(NAMES), 'predecessor group inventory mismatch')
        for group in groups:
            require(all(group[key] == old[group['name']][key] for key in
                        ('source_job_id', 'source_check_id', 'metadata_job_id', 'metadata_check_id')),
                    'predecessor job or check identity mismatch')
    return result


def collect_snapshot(context, case, client, classifier):
    """Capture a single explicitly identified PR observation into this generation."""
    repository = context['repository']
    repo_id = context['repository_id']
    pr_number = context['pr']['number']
    prefix = 'repos/' + repository + '/'
    observation = client.observation

    def api(name, path, paginate=False):
        return client.rest(name, prefix + path, paginate=paginate)

    def workflow(sha, filename):
        content = api(filename + '.api.json', 'contents/.github/workflows/ci.yml?ref=' + sha)
        require(content.get('encoding') == 'base64' and isinstance(content.get('content'), str),
                'workflow content missing')
        try:
            raw = base64.b64decode(''.join(content['content'].split()), validate=True)
        except (ValueError, TypeError) as exc:
            raise ValueError('invalid workflow content') from exc
        actual = hashlib.sha1(b'blob ' + str(len(raw)).encode('ascii') + b'\0' + raw).hexdigest()
        require(content.get('sha') == actual == context['workflow_blob'], 'workflow blob mismatch')
        publish_bytes(observation / filename, raw)

    pr = api('pr.json', 'pulls/' + str(pr_number))
    workflow(context['workflow_head_sha'], 'workflow.yml')
    api('protection.json', 'branches/' + quote(context['pr']['base_ref'], safe='') + '/protection')
    head = context['pr']['head_sha']
    api('ci-runs.json', 'actions/workflows/ci.yml/runs?event=pull_request&head_sha=' + head + '&per_page=100', True)
    api('title-runs.json', 'actions/workflows/pr-metadata.yml/runs?event=pull_request&head_sha=' + head + '&per_page=100', True)

    owner, repo = repository.split('/')
    gql_base = 'headRefOid baseRefOid mergeable mergeStateStatus isDraft state merged mergeCommit { oid }'
    fields = ('pageInfo { hasNextPage endCursor } nodes { __typename ... on CheckRun { databaseId name status '
              'conclusion detailsUrl isRequired(pullRequestNumber: ' + str(pr_number) + ') '
              'checkSuite { databaseId app { databaseId slug } } } ... on StatusContext { context state targetUrl '
              'isRequired(pullRequestNumber: ' + str(pr_number) + ') } }')
    graphql = []
    for surface in ('head', 'merge', 'direct'):
        cursor = None
        seen = set()
        while True:
            require(cursor not in seen, 'GraphQL cursor cycle')
            seen.add(cursor)
            after = '' if cursor is None else ', after: ' + json.dumps(cursor)
            rollup = 'statusCheckRollup { state contexts(first: 100' + after + ') { ' + fields + ' } }'
            selection = ('commits(last: 1) { nodes { commit { oid ' + rollup + ' } } }' if surface == 'head'
                         else 'potentialMergeCommit { oid ' + rollup + ' }' if surface == 'merge' else rollup)
            query = ('query { repository(owner: ' + json.dumps(owner) + ', name: ' + json.dumps(repo) +
                     ') { pullRequest(number: ' + str(pr_number) + ') { ' + gql_base + ' ' + selection + ' } } }')
            response, _ = client.json('POST', '/graphql', {'query': query})
            graphql.append({'surface': surface, 'after': cursor, 'response': response})
            require(not response.get('errors'), 'GraphQL response failed')
            node = response['data']['repository']['pullRequest']
            container = (node['commits']['nodes'][0]['commit'] if surface == 'head'
                         else node.get('potentialMergeCommit') if surface == 'merge' else node)
            page = (((container or {}).get('statusCheckRollup') or {}).get('contexts') or {}).get('pageInfo') or {}
            if not page.get('hasNextPage'):
                break
            cursor = page.get('endCursor')
            require(isinstance(cursor, str) and cursor, 'GraphQL cursor missing')
    publish_bytes(observation / 'graphql.json', canonical(graphql))
    shas = {head}
    for entry in graphql:
        merge = entry['response']['data']['repository']['pullRequest'].get('potentialMergeCommit') or {}
        if merge.get('oid'):
            shas.add(merge['oid'])
    if pr.get('merged') and pr.get('merge_commit_sha'):
        shas.add(pr['merge_commit_sha'])
    for sha in sorted(shas):
        api('checks-' + sha + '.json', 'commits/' + sha + '/check-runs?filter=all&per_page=100', True)

    specs = (context['source'], context['metadata'], context['title'])
    runs = {}
    for spec in specs:
        run_id, attempt = spec['run_id'], spec['attempt']
        run = api(f'run-{run_id}.json', f'actions/runs/{run_id}')
        require(run['id'] == run_id and run['run_attempt'] == attempt, 'run attempt mismatch')
        runs[run_id] = run
    for suite in sorted({run['check_suite_id'] for run in runs.values()}):
        api(f'suite-{suite}.json', f'check-suites/{suite}/check-runs?filter=all&per_page=100', True)

    workflow_commits = set()
    for spec in specs:
        run_id, attempt = spec['run_id'], spec['attempt']
        run = runs[run_id]
        job_pages = api(f'jobs-{run_id}-{attempt}.json', f'actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100', True)
        artifact_pages = api(f'artifacts-{run_id}.json', f'actions/runs/{run_id}/artifacts?per_page=100', True)
        jobs = [job for page in job_pages for job in page['jobs']]
        artifacts = [artifact for page in artifact_pages for artifact in page['artifacts']]
        require(job_pages and all(page['total_count'] == len(jobs) for page in job_pages) and
                len({job['id'] for job in jobs}) == len(jobs), 'job listing incomplete')
        require(artifact_pages and all(page['total_count'] == len(artifacts) for page in artifact_pages) and
                len({item['id'] for item in artifacts}) == len(artifacts), 'artifact listing incomplete')
        if run['path'] == '.github/workflows/ci.yml':
            artifact, provenance = select_attempt_artifact(artifacts, jobs, run, attempt,
                f'ci-event-{run_id}-{attempt}', 'CI Event Classification', 'Upload CI event record',
                repository_id=repo_id)
            archive = get_artifact(case, client, artifact, provenance).read_bytes()
            record = classifier.validate_record(classifier.archive_record(archive))
            publish_bytes(observation / f'record-{run_id}-{attempt}.json', canonical(record))
            workflow_commits.add(record['workflow_sha'])
        api(f'run-final-{run_id}.json', f'actions/runs/{run_id}')
    for sha in sorted(workflow_commits):
        workflow(sha, 'workflow-record-' + sha + '.yml')
    client.save_links()
    return observation
