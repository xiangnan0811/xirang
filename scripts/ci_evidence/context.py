"""Strict request parsing and checkout identity for CI evidence cases."""
import hashlib
import json
import os
import re
import stat
import subprocess
from pathlib import Path

MAX_REQUEST = 64 * 1024
SHA1 = re.compile(r"[0-9a-f]{40}\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
TASK = re.compile(r"[a-z][a-z0-9_-]{0,63}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")


class InputError(ValueError):
    """A request or local path is invalid."""
class StaleError(Exception):
    pass


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"),
                       ensure_ascii=False, allow_nan=False) + "\n").encode("utf-8")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise InputError("duplicate JSON key")
        result[key] = value
    return result


def strict_json(data):
    try:
        return json.loads(data, object_pairs_hook=_pairs,
                          parse_constant=lambda _: (_ for _ in ()).throw(InputError("invalid JSON number")))
    except (UnicodeDecodeError, json.JSONDecodeError, RecursionError) as exc:
        raise InputError("invalid JSON") from exc


def _keys(value, names):
    if not isinstance(value, dict) or set(value) != set(names):
        raise InputError("invalid object keys")


def _positive(value):
    if type(value) is not int or value <= 0:
        raise InputError("expected positive integer")


def _text(value):
    if not isinstance(value, str) or not value or any(ord(ch) < 32 or ord(ch) == 127 for ch in value):
        raise InputError("invalid text")


def _sha(value, regex=SHA1):
    if not isinstance(value, str) or not regex.fullmatch(value):
        raise InputError("invalid digest")


def _run(value):
    _keys(value, ("run_id", "attempt"))
    _positive(value["run_id"])
    _positive(value["attempt"])


def workspace_identity(project_root):
    root = Path(project_root).resolve(strict=True)
    try:
        process = subprocess.run(["git", "rev-parse", "--git-common-dir"], cwd=root,
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                 check=True, close_fds=True)
        common = Path(os.fsdecode(process.stdout.rstrip(b"\n")))
        if not common.is_absolute():
            common = root / common
        common = common.resolve(strict=True)
    except (OSError, subprocess.CalledProcessError) as exc:
        raise InputError("cannot identify workspace") from exc
    return digest(os.fsencode(str(common))), digest(os.fsencode(str(root)))


def agent_path(project_root, candidate, *, leaf="file"):
    """Resolve a path inside .tmp/agent while rejecting all symlink components.

    leaf is 'file', 'directory', or 'new'; for 'new' its parent must exist.
    """
    root = Path(project_root).resolve(strict=True)
    raw = Path(candidate)
    if ".." in raw.parts:
        raise InputError("parent traversal in evidence path")
    if not raw.is_absolute():
        raw = root / raw
    raw = Path(os.path.abspath(raw))
    base = root / ".tmp" / "agent"
    try:
        raw.relative_to(base)
    except ValueError as exc:
        raise InputError("path outside evidence workspace") from exc
    if raw == base:
        raise InputError("invalid evidence path")
    current = root
    try:
        for part in raw.relative_to(root).parts:
            current = current / part
            try:
                mode = current.lstat().st_mode
            except FileNotFoundError:
                if current == raw and leaf == "new":
                    return raw
                raise InputError("path component missing")
            if stat.S_ISLNK(mode):
                raise InputError("symlink in evidence path")
            if current != raw and not stat.S_ISDIR(mode):
                raise InputError("path parent is not a directory")
        if leaf == "new":
            raise InputError("evidence path already exists")
        if leaf == "file" and not stat.S_ISREG(mode):
            raise InputError("expected ordinary file")
        if leaf == "directory" and not stat.S_ISDIR(mode):
            raise InputError("expected directory")
    except OSError as exc:
        raise InputError("cannot inspect evidence path") from exc
    return raw


def parse_context(path, *, project_root, kind):
    """Validate a request and bind it to this repository and workspace."""
    try:
        with open(path, "rb") as request:
            data = request.read(MAX_REQUEST + 1)
    except OSError as exc:
        raise InputError("cannot read request") from exc
    if len(data) > MAX_REQUEST:
        raise InputError("request too large")
    obj = strict_json(data)
    try:
        canonical(obj)
    except (UnicodeError, ValueError, TypeError) as exc:
        raise InputError("invalid request encoding") from exc
    _keys(obj, ("schema_version", "kind", "task_id", "repository", "repository_id",
                "workflow_head_sha", "workflow_blob", "source", "pr", "metadata",
                "title", "expected", "predecessor"))
    if type(obj["schema_version"]) is not int or obj["schema_version"] != 1:
        raise InputError("invalid schema version")
    if kind not in ("source", "snapshot") or obj["kind"] != kind:
        raise InputError("invalid case kind")
    if not isinstance(obj["task_id"], str) or not TASK.fullmatch(obj["task_id"]):
        raise InputError("invalid task id")
    if (not isinstance(obj["repository"], str) or not REPOSITORY.fullmatch(obj["repository"]) or
            any(part in (".", "..") for part in obj["repository"].split("/"))):
        raise InputError("invalid repository")
    _positive(obj["repository_id"])
    _sha(obj["workflow_head_sha"])
    _sha(obj["workflow_blob"])
    source = obj["source"]
    _keys(source, ("run_id", "attempt", "event", "head_sha", "branch", "checkout_sha", "checkout_tree"))
    _positive(source["run_id"])
    _positive(source["attempt"])
    if source["event"] not in ("pull_request", "push"):
        raise InputError("invalid event")
    for key in ("head_sha", "checkout_sha", "checkout_tree"):
        _sha(source[key])
    _text(source["branch"])
    pr = obj["pr"]
    if source["event"] == "push":
        if pr is not None or source["head_sha"] != source["checkout_sha"]:
            raise InputError("invalid push identity")
    else:
        _keys(pr, ("number", "head_ref", "head_sha", "base_ref", "base_sha", "merge_sha"))
        _positive(pr["number"])
        for key in ("head_sha", "base_sha", "merge_sha"):
            _sha(pr[key])
        for key in ("head_ref", "base_ref"):
            _text(pr[key])
        if (pr["head_sha"] != source["head_sha"] or pr["head_ref"] != source["branch"]
                or pr["merge_sha"] != source["checkout_sha"]):
            raise InputError("pull request identity mismatch")
    if kind == "source":
        if obj["metadata"] is not None or obj["title"] is not None or obj["predecessor"] is not None or obj["expected"] != "success":
            raise InputError("invalid source request")
    else:
        if pr is None or obj["expected"] not in ("pending", "success", "failure"):
            raise InputError("invalid snapshot request")
        _run(obj["metadata"])
        _run(obj["title"])
        if len({source["run_id"], obj["metadata"]["run_id"], obj["title"]["run_id"]}) != 3:
            raise InputError("duplicate run id")
        if obj["expected"] == "pending":
            if obj["predecessor"] is not None:
                raise InputError("pending snapshot has predecessor")
        else:
            predecessor = obj["predecessor"]
            _keys(predecessor, ("case", "context_sha256"))
            _text(predecessor["case"])
            if Path(predecessor["case"]).is_absolute():
                raise InputError("predecessor must be workspace relative")
            agent_path(project_root, predecessor["case"], leaf="directory")
            _sha(predecessor["context_sha256"], SHA256)
    repository_key, workspace_key = workspace_identity(project_root)
    return dict(obj, repository_key=repository_key, workspace_key=workspace_key)
def verifier_build_inputs(root):
    """Find local Go packages and embedded files used to build the verifier."""
    import re
    import shlex
    root = Path(root)
    backend = root / 'backend'
    try:
        module = re.search(r'(?m)^module\s+(\S+)\s*$', (backend / 'go.mod').read_text()).group(1)
        pending = [backend / 'cmd/verify-ci-profile']
        visited, files, specs = set(), {backend / 'go.mod', backend / 'go.sum'}, []
        while pending:
            directory = pending.pop()
            if directory in visited:
                continue
            if directory.is_symlink() or not directory.is_dir():
                raise InputError('verifier package unavailable')
            directory.relative_to(backend)
            visited.add(directory)
            specs.append(':(glob)' + directory.relative_to(root).as_posix() + '/*.go')
            for path in directory.glob('*.go'):
                if path.name.endswith('_test.go'):
                    continue
                if path.is_symlink() or not path.is_file():
                    raise InputError('invalid verifier source')
                files.add(path)
                text = path.read_text(encoding='utf-8')
                pattern = r'(?m)^\s*(?:import\s+)?(?:[.\w]+\s+)?["`](' + re.escape(module) + r'/[^"`]+)["`]'
                for imported in re.findall(pattern, text):
                    relative = imported[len(module) + 1:]
                    if '..' in Path(relative).parts:
                        raise InputError('invalid verifier import')
                    pending.append(backend / relative)
                for directive in re.findall(r'(?m)^\s*//go:embed\s+(.+)$', text):
                    for pattern in shlex.split(directive):
                        pattern = pattern.removeprefix('all:')
                        if Path(pattern).is_absolute() or '..' in Path(pattern).parts:
                            raise InputError('invalid embedded input')
                        specs.append(':(glob)' + (directory / pattern).relative_to(root).as_posix())
                        matches = list(directory.glob(pattern))
                        if not matches:
                            raise InputError('embedded verifier input unavailable')
                        for match in matches:
                            if match.is_symlink():
                                raise InputError('invalid embedded input')
                            if match.is_dir():
                                files.update(item for item in match.rglob('*') if item.is_file() and not item.is_symlink())
                            elif match.is_file():
                                files.add(match)
        return sorted(files), specs
    except (OSError, ValueError, AttributeError) as exc:
        raise InputError('cannot identify verifier build inputs') from exc


def require_clean_adoption(root):
    """Reject relevant staged, unstaged, untracked or ignored build inputs."""
    try:
        _, build_specs = verifier_build_inputs(root)
    except InputError as exc:
        raise StaleError('adoption build inputs unavailable') from exc
    required = ('scripts/ci-evidence.py', 'scripts/classify-ci-event.py',
                'scripts/verify-release-ci.mjs', 'scripts/required-postgres-tests.json',
                '.github/workflows/ci.yml', '.github/workflows/pr-metadata.yml')
    if any(not (Path(root) / name).is_file() or (Path(root) / name).is_symlink() for name in required):
        raise StaleError('adoption input unavailable')
    specs = list(required) + [':(glob)scripts/ci_evidence/*.py', 'backend/go.mod', 'backend/go.sum'] + build_specs
    for args in (['status', '--porcelain=v1', '-z', '--untracked-files=all'],
                 ['ls-files', '--others', '--ignored', '--exclude-standard', '-z']):
        pathspecs = specs + [':(exclude,glob)**/*_test.go'] if args[0] == 'status' else specs
        try:
            result = subprocess.run(['git', *args, '--', *pathspecs], cwd=root, stdout=subprocess.PIPE,
                                    stderr=subprocess.DEVNULL, check=True, close_fds=True,
                                    env=dict(os.environ, GIT_OPTIONAL_LOCKS='0'))
        except (OSError, subprocess.CalledProcessError) as exc:
            raise InputError('cannot inspect adoption inputs') from exc
        if args[0] == 'status':
            dirty = bool(result.stdout)
        else:
            dirty = any(path and (path.endswith(b'/') or not path.rsplit(b'/', 1)[-1].endswith(b'_test.go'))
                        for path in result.stdout.split(b'\0'))
        if dirty:
            raise StaleError('adoption inputs have local changes')
