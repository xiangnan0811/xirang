"""Authenticated, bounded GitHub transport for one observation generation."""
import ast
import base64
import hashlib
import http.client
import os
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zipfile
from pathlib import Path

from .context import canonical, strict_json
from .store import IntegrityError, _fsync_dir, publish_bytes, publish_part_noreplace

JSON_LIMIT = 8 * 1024 * 1024

# Only bodies proven unused by these upstream stages may be projected out.
_UPSTREAM_EXCLUSIONS = {
    'cli.py': frozenset(('policy', 'verify_observation')),
    'source.py': frozenset(('verify_source',)),
    'snapshot.py': frozenset(('verify_snapshot', 'inventory', 'one')),
}


def projected_module(path, stage, kind):
    """Hash the full module AST except explicitly audited, ordinary bodies."""
    raw = Path(path).read_bytes()
    name = Path(path).name
    excluded = set(_UPSTREAM_EXCLUSIONS.get(name, ())) if stage in ('capture', 'authenticate') else set()
    if name == 'cli.py':
        excluded.add('policy')
    try:
        tree = ast.parse(raw, filename=name)
        found = set()
        policy_found = False
        for node in tree.body:
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in excluded:
                if not isinstance(node, ast.FunctionDef) or node.decorator_list or node.name in found:
                    raise ValueError('unsupported projected definition')
                if node.name == 'policy':
                    allowed = {'dict', 'sorted', 'rules.update', 'context.digest', 'context.canonical'}
                    for item in ast.walk(node):
                        if isinstance(item, (ast.Global, ast.Nonlocal, ast.Delete, ast.AugAssign,
                                             ast.Lambda, ast.Yield, ast.YieldFrom, ast.Await,
                                             ast.Import, ast.ImportFrom, ast.With, ast.Try, ast.Raise)):
                            raise ValueError('policy is not inert')
                        if isinstance(item, ast.Call):
                            call = ast.unparse(item.func)
                            if call not in allowed:
                                raise ValueError('policy call is not audited')
                        if isinstance(item, ast.Name) and isinstance(item.ctx, ast.Store) and item.id != 'rules':
                            raise ValueError('policy writes unexpected name')
                found.add(node.name)
                node.body = [ast.Expr(value=ast.Constant(value='audited body excluded'))]
            elif name == 'cli.py' and isinstance(node, ast.Assign) and any(
                    isinstance(target, ast.Name) and target.id == 'POLICIES' for target in node.targets):
                if policy_found or len(node.targets) != 1 or not isinstance(node.targets[0], ast.Name):
                    raise ValueError('unsupported policy assignment')
                ast.literal_eval(node.value)
                node.value = ast.Constant(value='audited policy data excluded')
                policy_found = True
        if found != excluded or (name == 'cli.py' and not policy_found):
            raise ValueError('missing projected definition')
        # Any other module-scope binding of an excluded name defeats the audit.
        class Bindings(ast.NodeVisitor):
            def visit_FunctionDef(self, node):
                pass
            visit_AsyncFunctionDef = visit_FunctionDef
            def visit_ClassDef(self, node):
                if node.name in excluded or (name == 'cli.py' and node.name == 'POLICIES'):
                    raise ValueError('rebound projected name')
            def visit_Name(self, node):
                if isinstance(node.ctx, (ast.Store, ast.Del)) and (node.id in excluded or
                        (name == 'cli.py' and node.id == 'POLICIES')):
                    raise ValueError('rebound projected name')
            def visit_Import(self, node):
                for item in node.names:
                    bound = item.asname or item.name.split('.')[0]
                    if bound in excluded or (name == 'cli.py' and bound == 'POLICIES'):
                        raise ValueError('rebound projected name')
            visit_ImportFrom = visit_Import
        for node in tree.body:
            if isinstance(node, ast.Assign) and name == 'cli.py' and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name) and node.targets[0].id == 'POLICIES':
                continue
            Bindings().visit(node)
        projection = ast.dump(tree, include_attributes=False).encode('utf-8')
    except (SyntaxError, ValueError, TypeError):
        # Unsupported constructs retain all bytes, never silently reuse a projection.
        projection = b'full\0' + raw
    return hashlib.sha256(canonical({'format': 'audited-ast-v1', 'module': name,
                                     'stage': stage, 'kind': kind,
                                     'ast_sha256': hashlib.sha256(projection).hexdigest()})).hexdigest()


def publish_download(part, target):
    """Publish a previously fsynced stream, accepting only identical objects."""
    try:
        publish_part_noreplace(part, target)
    except FileExistsError:
        if target.is_symlink() or not target.is_file():
            raise IntegrityError('immutable object conflicts')
        left, right = hashlib.sha256(), hashlib.sha256()
        with Path(part).open('rb') as first, Path(target).open('rb') as second:
            for chunk in iter(lambda: first.read(1024 * 1024), b''):
                left.update(chunk)
            for chunk in iter(lambda: second.read(1024 * 1024), b''):
                right.update(chunk)
        if left.digest() != right.digest():
            raise IntegrityError('immutable object conflicts')
    _fsync_dir(target.parent)


class TransportError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urllib.parse.urlsplit(newurl).scheme != 'https':
            raise TransportError('invalid artifact redirect')
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def next_page(link, repository, repository_id):
    if not link:
        return None
    matches = []
    for part in link.split(','):
        match = re.match(r'\s*<([^>]+)>(.*)', part)
        if not match:
            raise IntegrityError('invalid pagination link')
        rel = re.search(r'(?:^|;)\s*rel\s*=\s*"([^"]*)"', match.group(2))
        if not rel or 'next' not in rel.group(1).split():
            continue
        url = urllib.parse.urlsplit(match.group(1))
        decoded = urllib.parse.unquote(url.path)
        prefixes = ('/repos/' + repository + '/', '/repositories/' + str(repository_id) + '/')
        if (url.scheme != 'https' or url.netloc != 'api.github.com' or
                not url.path.startswith(prefixes) or not decoded.startswith(prefixes) or
                any(part in ('.', '..') for part in decoded.split('/')) or
                '\\' in decoded or url.fragment):
            raise IntegrityError('pagination leaves repository')
        matches.append(urllib.parse.urlunsplit(('', '', url.path, url.query, '')))
    if len(matches) > 1:
        raise IntegrityError('ambiguous next page')
    return matches[0] if matches else None


def verify_saved_contents(observation, repository, ref, repository_path, filename, *, expected_blob=None):
    """Bind saved contents bytes to the exact authenticated GET and Git blob."""
    observation = Path(observation)
    try:
        raw = (observation / filename).read_bytes()
        obj = strict_json((observation / (filename + '.api.json')).read_bytes())
        requests = strict_json((observation / 'requests.json').read_bytes())
        expected_path = '/repos/' + repository + '/contents/' + repository_path + '?ref=' + ref
        if not isinstance(requests, list):
            raise IntegrityError('invalid request provenance')
        matching = [entry for entry in requests if isinstance(entry, dict) and
                    entry.get('method') == 'GET' and entry.get('path') == expected_path]
        if not matching:
            raise IntegrityError('contents request missing')
        for entry in matching:
            name = entry.get('raw')
            if not isinstance(name, str) or re.fullmatch(r'response-[0-9a-f]{32}\.json', name) is None:
                raise IntegrityError('invalid response provenance')
            response = observation / name
            if response.is_symlink() or strict_json(response.read_bytes()) != obj:
                raise IntegrityError('contents response mismatch')
        if (not isinstance(obj, dict) or obj.get('encoding') != 'base64' or
                not isinstance(obj.get('content'), str) or
                base64.b64decode(''.join(obj['content'].split()), validate=True) != raw):
            raise IntegrityError('contents bytes mismatch')
        blob = hashlib.sha1(b'blob ' + str(len(raw)).encode('ascii') + b'\0' + raw).hexdigest()
        if obj.get('sha') != blob or (expected_blob is not None and blob != expected_blob):
            raise IntegrityError('contents blob mismatch')
        return raw
    except (OSError, ValueError, TypeError, KeyError) as exc:
        raise IntegrityError('invalid saved contents') from exc


class Client:
    def __init__(self, case, context, invocation, *, token=None, connection=None):
        self.case = Path(case)
        self.context = context
        self.links = []
        self.requests = []
        self.observation = self.case / 'raw' / invocation
        self.observation.mkdir(mode=0o700, exist_ok=False)
        _fsync_dir(self.observation.parent)
        if token is None:
            try:
                result = subprocess.run(['gh', 'auth', 'token'], stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, close_fds=True)
                if result.returncode:
                    raise TransportError('authentication unavailable')
                token = result.stdout.decode('ascii').strip()
            except (OSError, UnicodeError) as exc:
                raise TransportError('authentication unavailable') from exc
        if not token or not token.isascii() or any(ord(c) < 33 or ord(c) == 127 for c in token):
            raise TransportError('invalid authentication')
        self.token = token
        self.connection = connection or http.client.HTTPSConnection('api.github.com', timeout=30)

    def close(self):
        self.connection.close()

    def json(self, method, path, payload=None):
        headers = {'Authorization': 'Bearer ' + self.token,
                   'Accept': 'application/vnd.github+json',
                   'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'xirang-ci-evidence'}
        body = None
        if payload is not None:
            body = canonical(payload)
            headers['Content-Type'] = 'application/json'
        try:
            self.connection.request(method, path, body=body, headers=headers)
            response = self.connection.getresponse()
            raw = response.read(JSON_LIMIT + 1)
            if response.status < 200 or response.status >= 300 or len(raw) > JSON_LIMIT:
                raise TransportError('GitHub response unavailable')
            if hasattr(self, 'observation'):
                filename = 'response-' + uuid.uuid4().hex + '.json'
                publish_bytes(self.observation / filename, raw)
                self.requests.append({'method': method, 'path': path, 'raw': filename,
                                      'link': response.getheader('Link')})
            try:
                return strict_json(raw), response.getheader('Link')
            except ValueError as exc:
                raise IntegrityError('invalid GitHub JSON') from exc
        except (OSError, http.client.HTTPException) as exc:
            raise TransportError('GitHub request failed') from exc

    def rest(self, name, path, *, paginate=False):
        if not path.startswith('/'):
            path = '/' + path
        pages, seen = [], set()
        while True:
            if path in seen:
                raise IntegrityError('pagination cycle')
            seen.add(path)
            value, link = self.json('GET', path)
            self.links.append({'path': path, 'link': link})
            pages.append(value)
            if not paginate:
                break
            path = next_page(link, self.context['repository'], self.context['repository_id'])
            if path is None:
                break
        result = pages if paginate else pages[0]
        publish_bytes(self.observation / name, canonical(result))
        return result

    def save_links(self):
        publish_bytes(self.observation / 'rest-pagination.json', canonical(self.links))
        publish_bytes(self.observation / 'requests.json', canonical(self.requests))

    def resource_stream(self, path):
        url = 'https://api.github.com/repos/' + self.context['repository'] + '/' + path
        req = urllib.request.Request(url, headers={'Authorization': 'Bearer ' + self.token,
                                                   'Accept': 'application/vnd.github+json',
                                                   'User-Agent': 'xirang-ci-evidence'})
        try:
            try:
                response = urllib.request.build_opener(NoRedirect()).open(req, timeout=60)
            except urllib.error.HTTPError as exc:
                try:
                    if exc.code not in (301, 302, 303, 307, 308):
                        raise TransportError('artifact request failed') from exc
                    location = exc.headers.get('Location')
                    if not location or urllib.parse.urlsplit(location).scheme != 'https':
                        raise TransportError('invalid artifact redirect') from exc
                finally:
                    exc.close()
                response = urllib.request.build_opener(HTTPSRedirect()).open(
                    urllib.request.Request(location, headers={'User-Agent': 'xirang-ci-evidence'}), timeout=60)
            return response
        except (OSError, urllib.error.URLError) as exc:
            raise TransportError('artifact download failed') from exc

    def artifact_stream(self, artifact_id):
        return self.resource_stream('actions/artifacts/' + str(artifact_id) + '/zip')

    def download_logs(self, run_id, attempt):
        target = self.observation / 'original-logs.zip'
        temporary = target.with_name(target.name + '.part.' + uuid.uuid4().hex)
        fd = os.open(str(temporary), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, 'wb') as output, self.resource_stream(
                    f'actions/runs/{run_id}/attempts/{attempt}/logs') as response:
                for data in iter(lambda: response.read(1024 * 1024), b''):
                    output.write(data)
                output.flush()
                os.fsync(output.fileno())
            with zipfile.ZipFile(temporary) as archive:
                if archive.testzip() is not None:
                    raise IntegrityError('invalid attempt logs')
            publish_download(temporary, target)
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass
        return target


def _check_archive(path, artifact):
    try:
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            if len(names) != len(set(names)):
                raise IntegrityError('duplicate archive members')
            if artifact.get('name', '').startswith('asset-worker-runtime-closure-'):
                if names != ['runtime-closure.v1.json']:
                    raise IntegrityError('invalid closure archive')
            elif archive.testzip() is not None:
                raise IntegrityError('invalid artifact archive')
    except (OSError, zipfile.BadZipFile) as exc:
        raise IntegrityError('invalid artifact archive') from exc


def get_artifact(case, client, artifact, provenance):
    """Reuse only a digest-verified, producer-authenticated complete archive."""
    if type(artifact.get('id')) is not int or artifact['id'] <= 0:
        raise IntegrityError('invalid artifact id')
    artifact_id = artifact['id']
    path = Path(case) / 'artifacts' / (str(artifact_id) + '.zip')
    metadata = path.with_suffix('.json')
    expected = artifact.get('digest')
    if not isinstance(expected, str) or not re.fullmatch(r'sha256:[0-9a-f]{64}', expected):
        raise IntegrityError('invalid artifact digest')
    if (not isinstance(provenance, dict) or provenance.get('selected_id') != artifact_id or
            provenance.get('digest') != expected or not isinstance(provenance.get('producer'), dict) or
            not isinstance(provenance.get('upload_step'), dict)):
        raise IntegrityError('artifact provenance mismatch')
    if path.is_symlink() or metadata.is_symlink():
        raise IntegrityError('invalid artifact path')
    if path.exists():
        hasher = hashlib.sha256()
        with path.open('rb') as stream:
            for data in iter(lambda: stream.read(1024 * 1024), b''):
                hasher.update(data)
        if 'sha256:' + hasher.hexdigest() != expected:
            raise IntegrityError('saved artifact digest mismatch')
        _check_archive(path, artifact)
        _fsync_dir(path.parent)
    else:
        part = path.with_name(path.name + '.part.' + uuid.uuid4().hex)
        hasher = hashlib.sha256()
        try:
            fd = os.open(str(part), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, 'wb') as dest, client.artifact_stream(artifact_id) as source:
                for data in iter(lambda: source.read(1024 * 1024), b''):
                    hasher.update(data)
                    dest.write(data)
                dest.flush()
                os.fsync(dest.fileno())
            if 'sha256:' + hasher.hexdigest() != expected:
                raise IntegrityError('artifact digest mismatch')
            _check_archive(part, artifact)
            publish_download(part, path)
        finally:
            try:
                part.unlink()
            except FileNotFoundError:
                pass
    record = {'artifact': artifact, 'provenance': provenance, 'zip_sha256': expected[7:]}
    if metadata.exists():
        old = strict_json(metadata.read_bytes())
        if (not isinstance(old, dict) or old.get('zip_sha256') != expected[7:] or
                old.get('artifact', {}).get('id') != artifact_id or
                old.get('artifact', {}).get('digest') != expected or
                old.get('provenance', {}).get('selected_id') != artifact_id or
                old.get('provenance', {}).get('digest') != expected or
                old.get('provenance', {}).get('producer', {}).get('id') != provenance['producer']['id']):
            raise IntegrityError('saved artifact provenance mismatch')
        _fsync_dir(metadata.parent)
    else:
        publish_bytes(metadata, canonical(record))
    return path
