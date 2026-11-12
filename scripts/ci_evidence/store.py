"""Durable case objects, receipts, and the checkout-wide writer lock."""
import contextlib
import datetime
import errno
import hashlib
import os
import stat
import subprocess
import uuid
from pathlib import Path

try:
    import fcntl
except ImportError:
    fcntl = None

from .context import InputError, StaleError, agent_path, canonical, digest, require_clean_adoption, strict_json, workspace_identity


class IntegrityError(ValueError):
    pass


class BusyError(Exception):
    pass


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def _fsync_dir(path):
    fd = os.open(str(path), os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def publish_part_noreplace(part, path):
    """Atomically publish an existing part without replacing any destination."""
    import ctypes
    import sys
    if sys.platform.startswith('linux'):
        libc = ctypes.CDLL(None, use_errno=True)
        rename = getattr(libc, 'renameat2', None)
        if rename is not None:
            rename.argtypes = (ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint)
            rename.restype = ctypes.c_int
            if rename(-100, os.fsencode(part), -100, os.fsencode(path), 1) == 0:
                return
            error = ctypes.get_errno()
            if error in (errno.ENOSYS, errno.EINVAL, errno.EOPNOTSUPP):
                raise InputError('atomic no-replace rename unavailable')
            raise OSError(error, os.strerror(error), str(path))
    raise InputError('atomic no-replace rename unavailable')


def publish_bytes(path, data, *, replace=False):
    """Publish bytes durably; immutable paths accept only identical old bytes."""
    path = Path(path)
    part = path.with_name(path.name + ".part." + uuid.uuid4().hex)
    fd = os.open(str(part), os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if replace:
            os.replace(str(part), str(path))
        else:
            try:
                publish_part_noreplace(part, path)
            except FileExistsError:
                if path.is_symlink() or not path.is_file() or path.read_bytes() != data:
                    raise IntegrityError("immutable object conflicts")
                part.unlink()
        _fsync_dir(path.parent)
    finally:
        try:
            part.unlink()
        except FileNotFoundError:
            pass


def _state(root):
    path = root / ".agent-state"
    try:
        path.mkdir(mode=0o700)
        _fsync_dir(root)
    except FileExistsError:
        pass
    mode = path.lstat().st_mode
    if stat.S_ISLNK(mode) or not stat.S_ISDIR(mode) or stat.S_IMODE(mode) != 0o700:
        raise InputError("invalid evidence state directory")
    return path


@contextlib.contextmanager
def checkout_lock(root):
    if fcntl is None:
        raise InputError("POSIX file locking unavailable")
    root = Path(root).resolve(strict=True)
    state = _state(root)
    flags = os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(str(state / "evidence.lock"), flags, 0o600)
    except OSError as exc:
        raise InputError("invalid evidence lock") from exc
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise InputError("invalid evidence lock")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            if exc.errno in (errno.EACCES, errno.EAGAIN):
                raise BusyError("evidence writer busy") from exc
            raise
        yield state
    finally:
        os.close(fd)


def _initial_summary(context_sha):
    return {"schema_version": 1, "context_sha256": context_sha,
            "state": "incomplete", "verification": "not_run",
            "live_eligibility": "not_checked", "missing_stages":
            ["capture", "authenticate", "verify", "finalize"], "observed_at": None,
            "receipts": {}}


def validate_layout(case):
    case = Path(case)
    try:
        mode = case.lstat().st_mode
        if not stat.S_ISDIR(mode) or stat.S_IMODE(mode) != 0o700:
            raise IntegrityError('invalid case directory')
        for name in ('raw', 'artifacts', 'receipts', 'invocations'):
            mode = (case / name).lstat().st_mode
            if not stat.S_ISDIR(mode) or stat.S_IMODE(mode) != 0o700:
                raise IntegrityError('invalid case directory member')
        for name in ('context.json', 'summary.json'):
            if not stat.S_ISREG((case / name).lstat().st_mode):
                raise IntegrityError('invalid case file')
    except OSError as exc:
        raise IntegrityError('incomplete case layout') from exc


def record_interruptions(case, recovery_id):
    """Record abandoned commands after acquiring the shared writer lock."""
    import re
    for directory in (case / 'invocations').iterdir():
        if directory.name == recovery_id:
            continue
        if directory.is_symlink() or not directory.is_dir() or re.fullmatch(r'[0-9a-f]{32}', directory.name) is None:
            raise IntegrityError('invalid invocation directory')
        command = directory / 'command.json'
        if command.is_symlink() or (command.exists() and not command.is_file()):
            raise IntegrityError('invalid invocation command')
        missing = not command.exists()
        command_sha = None
        if not missing:
            raw = command.read_bytes()
            obj = strict_json(raw)
            if (not isinstance(obj, dict) or canonical(obj) != raw or
                    set(obj) != {'schema_version', 'command', 'resume', 'make_current', 'started_at',
                                 'ended_at', 'exit', 'stderr'} or obj.get('schema_version') != 1):
                raise IntegrityError('invalid invocation command')
            if obj['exit'] is not None or obj['ended_at'] is not None:
                if obj['exit'] is None or obj['ended_at'] is None:
                    raise IntegrityError('inconsistent invocation outcome')
                continue
            command_sha = digest(raw)
        prior = list(directory.glob('interruption-*.json'))
        if prior:
            for item in prior:
                if item.is_symlink() or not item.is_file():
                    raise IntegrityError('invalid interruption record')
                sidecar_raw = item.read_bytes()
                record = strict_json(sidecar_raw)
                if (not isinstance(record, dict) or canonical(record) != sidecar_raw or
                        set(record) != {'schema_version', 'kind', 'invocation', 'recovery_invocation',
                                        'command_sha256', 'observed_at', 'actual_exit', 'ended_at'} or
                        record.get('schema_version') != 1 or record.get('kind') != ('missing_command' if missing else 'interrupted') or
                        record.get('invocation') != directory.name or
                        not isinstance(record.get('recovery_invocation'), str) or
                        re.fullmatch(r'[0-9a-f]{32}', record['recovery_invocation']) is None or
                        item.name != 'interruption-' + record['recovery_invocation'] + '.json' or
                        record.get('command_sha256') != command_sha or record.get('actual_exit') is not None or
                        record.get('ended_at') is not None or not isinstance(record.get('observed_at'), str)):
                    raise IntegrityError('invalid interruption record')
            continue
        sidecar = {'schema_version': 1, 'kind': 'missing_command' if missing else 'interrupted', 'invocation': directory.name,
                   'recovery_invocation': recovery_id, 'command_sha256': command_sha,
                   'observed_at': now(), 'actual_exit': None, 'ended_at': None}
        publish_bytes(directory / ('interruption-' + recovery_id + '.json'), canonical(sidecar))


@contextlib.contextmanager
def open_case(project_root, case_dir, context, *, resume, make_current):
    root = Path(project_root).resolve(strict=True)
    raw = canonical(context)
    context_sha = digest(raw)
    repository_key, workspace_key = workspace_identity(root)
    if context.get("repository_key") != repository_key or context.get("workspace_key") != workspace_key:
        raise InputError("workspace identity mismatch")
    try:
        tracked = subprocess.run(['git', 'ls-files', '--', '.agent-state'], cwd=root,
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True, close_fds=True).stdout
    except (OSError, subprocess.CalledProcessError) as exc:
        raise InputError('cannot inspect evidence state') from exc
    if tracked:
        raise InputError('evidence state contains tracked files')
    with checkout_lock(root) as state:
        case = agent_path(root, case_dir, leaf="directory" if resume else "new")
        if resume:
            validate_layout(case)
            try:
                existing = (case / "context.json").read_bytes()
            except OSError as exc:
                raise IntegrityError("case context unavailable") from exc
            if existing != raw:
                raise IntegrityError("case context mismatch")
        else:
            case.mkdir(mode=0o700)
            _fsync_dir(case.parent)
            for name in ("raw", "artifacts", "receipts", "invocations"):
                (case / name).mkdir(mode=0o700)
            _fsync_dir(case)
            publish_bytes(case / "context.json", raw)
            publish_bytes(case / "summary.json", canonical(_initial_summary(context_sha)))
        invocation_id = uuid.uuid4().hex
        invocation = case / 'invocations' / invocation_id
        invocation.mkdir(mode=0o700)
        _fsync_dir(invocation.parent)
        started = now()
        initial = {'schema_version': 1, 'command': context['kind'], 'resume': resume,
                   'make_current': make_current, 'started_at': started, 'ended_at': None,
                   'exit': None, 'stderr': ''}
        publish_bytes(invocation / 'command.json', canonical(initial))
        try:
            if resume:
                record_interruptions(case, invocation_id)
            if make_current:
                require_clean_adoption(root)
                pointer = {"schema_version": 1, "repository_key": repository_key,
                           "workspace_key": workspace_key, "task_id": context["task_id"],
                           "case": case.relative_to(root).as_posix(), "context_sha256": context_sha}
                publish_bytes(state / "current-evidence.json", canonical(pointer), replace=True)
        except Exception as exc:
            exit_code = 2 if isinstance(exc, InputError) else 4 if isinstance(exc, IntegrityError) else 6 if isinstance(exc, StaleError) else 5
            initial.update(ended_at=now(), exit=exit_code,
                           stderr={2: 'E_INPUT', 4: 'E_INTEGRITY', 5: 'E_VERIFY', 6: 'E_STALE'}[exit_code])
            publish_bytes(invocation / 'command.json', canonical(initial), replace=True)
            raise
        yield case, invocation_id, started


def receipt_key(stage, context_sha256, inputs, dependencies, verifier_sha256, policy_sha256):
    identity = {"stage": stage, "context_sha256": context_sha256,
                "inputs": inputs, "dependencies": dependencies,
                "verifier_sha256": verifier_sha256, "policy_version": 1,
                "policy_sha256": policy_sha256}
    return digest(canonical(identity)), identity


def file_hashes(root, paths):
    root = Path(root).resolve(strict=True)
    result = {}
    for name in paths:
        path = Path(name)
        if path.is_absolute() or ".." in path.parts:
            raise IntegrityError("invalid receipt path")
        candidate = root / path
        try:
            current = root
            for component in path.parts:
                current = current / component
                if current.is_symlink():
                    raise IntegrityError('symlink in receipt path')
            resolved = candidate.resolve(strict=True)
            resolved.relative_to(root)
            if candidate.is_symlink() or not resolved.is_file():
                raise IntegrityError("invalid receipt object")
            hasher = hashlib.sha256()
            with resolved.open("rb") as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    hasher.update(chunk)
            result[path.as_posix()] = hasher.hexdigest()
        except (OSError, ValueError) as exc:
            raise IntegrityError("receipt object unavailable") from exc
    return result


def get_receipt(root, case, stage, context_sha256, inputs, dependencies, verifier_sha256, policy_sha256):
    key, identity = receipt_key(stage, context_sha256, inputs, dependencies, verifier_sha256, policy_sha256)
    path = Path(case) / "receipts" / (stage + "-" + key + ".json")
    if not path.exists() and not path.is_symlink():
        return None
    try:
        raw = path.read_bytes()
        obj = strict_json(raw)
        if (canonical(obj) != raw or not isinstance(obj, dict) or
                set(obj) != set(identity) | {"schema_version", "outputs", "result", "observed_at"} or
                any(obj.get(k) != v for k, v in identity.items()) or
                obj.get("schema_version") != 1 or obj.get("result") != "passed" or
                not isinstance(obj.get("observed_at"), str) or not isinstance(obj.get("outputs"), dict) or
                file_hashes(root, obj["inputs"]) != obj["inputs"] or
                file_hashes(root, obj["outputs"]) != obj["outputs"]):
            raise IntegrityError("receipt mismatch")
        return obj, digest(raw)
    except (OSError, ValueError, TypeError) as exc:
        raise IntegrityError("invalid receipt") from exc


def publish_receipt(root, case, stage, context_sha256, inputs, dependencies, verifier_sha256, policy_sha256, outputs):
    existing = get_receipt(root, case, stage, context_sha256, inputs, dependencies, verifier_sha256, policy_sha256)
    if existing is not None:
        _fsync_dir(Path(case) / 'receipts')
        _fsync_dir(Path(case))
        return existing
    key, identity = receipt_key(stage, context_sha256, inputs, dependencies, verifier_sha256, policy_sha256)
    if file_hashes(root, inputs) != inputs or file_hashes(root, outputs) != outputs:
        raise IntegrityError("receipt input or output mismatch")
    obj = dict(identity, schema_version=1, outputs=outputs, result="passed", observed_at=now())
    raw = canonical(obj)
    publish_bytes(Path(case) / "receipts" / (stage + "-" + key + ".json"), raw)
    return obj, digest(raw)
