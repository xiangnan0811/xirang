#!/usr/bin/env python3
"""Capture and verify an explicitly identified CI evidence case."""
import sys

sys.dont_write_bytecode = True
from ci_evidence.cli import main

if __name__ == '__main__':
    raise SystemExit(main())
