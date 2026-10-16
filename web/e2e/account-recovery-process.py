#!/usr/bin/env python3
"""Private JSON transport for the isolated recovery fixture; never print raw CLI IO.

Input is read by this parent only. The CLI child gets a controlling PTY, not the
transport pipe. No password is ever placed in child argv, environment or files.
"""
import errno
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import sqlite3
import sys
import termios
import time


def main():
    request = json.load(sys.stdin)
    runtime = Path(request["runtime"])
    root = Path(__file__).resolve().parents[2] / ".tmp" / "agent"
    if runtime.is_symlink() or runtime.resolve().parent != root.resolve() or not runtime.name.startswith("r19-browser-"):
        raise ValueError("unsafe runtime")
    if not (runtime / "owner").is_file():
        raise ValueError("missing ownership marker")
    if request["mode"] == "sql":
        database = runtime / "xirang.db"
        if database.is_symlink() or not database.is_file():
            raise ValueError("unsafe database")
        connection = sqlite3.connect(f"file:{database}?mode=rw", uri=True)
        connection.row_factory = sqlite3.Row
        try:
            cursor = connection.execute(request["sql"], request.get("parameters", []))
            rows = [dict(row) for row in cursor.fetchall()]
            connection.commit()
            print(json.dumps(rows))
        finally:
            connection.close()
        return
    child, master = pty.fork()
    if child == 0:
        if request.get("redirect"):
            null = os.open(os.devnull, os.O_RDONLY)
            os.dup2(null, 0)
            os.close(null)
        os.chdir(runtime)
        os.execve(request["binary"], [request["binary"], *request["args"]], request["env"])
    transcript = bytearray()
    prompts = [(b"XIRANG_BREAK_GLASS_CONFIRMATION:", request["confirmation"])]
    if request.get("password") is not None:
        prompts += [(b"New password:", request["password"]), (b"Repeat new password:", request["password"])]
    sent = 0
    offset = 0
    status = None
    deadline = time.monotonic() + 45
    try:
        while time.monotonic() < deadline:
            readable, _, _ = select.select([master], [], [], 0.02)
            if readable:
                try:
                    chunk = os.read(master, 65536)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
                    chunk = b""
                transcript.extend(chunk)
            if sent < len(prompts):
                prompt, secret = prompts[sent]
                at = transcript.find(prompt, offset)
                # The prompt precedes ReadPassword's termios call. Never race
                # the noecho transition: do not send until the terminal proves it.
                if at >= 0 and not (termios.tcgetattr(master)[3] & termios.ECHO):
                    os.write(master, secret.encode() + b"\n")
                    offset = at + len(prompt)
                    sent += 1
            observed, code = os.waitpid(child, os.WNOHANG)
            if observed:
                status = os.waitstatus_to_exitcode(code)
                break
        if status is None:
            os.kill(child, signal.SIGKILL)
            os.waitpid(child, 0)
            raise ValueError("CLI deadline")
        # Drain the final fixed outcome without exposing raw bytes.
        while select.select([master], [], [], 0)[0]:
            try:
                chunk = os.read(master, 65536)
            except OSError:
                break
            if not chunk:
                break
            transcript.extend(chunk)
        text = transcript.decode(errors="replace")
        secrets = [request["confirmation"], request.get("password")]
        if any(secret and secret in text for secret in secrets):
            raise ValueError("terminal echoed private input")
        operation = re.search(r"operation_id=([0-9a-f]{32})", text)
        print(json.dumps({"exit": status, "operationId": operation[1] if operation else None,
                          "committed": "Recovery committed:" in text,
                          "rejected": "Recovery rejected before credential writes" in text,
                          "terminalRejected": "Terminal input rejected" in text,
                          "prompts": sent}))
    finally:
        os.close(master)
        if status is None:
            try:
                os.kill(child, signal.SIGKILL)
                os.waitpid(child, 0)
            except ProcessLookupError:
                pass


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Neither exceptions nor tracebacks may contain transport values/SQL IO.
        print('{"error":"isolated fixture process failed"}')
        sys.exit(1)
