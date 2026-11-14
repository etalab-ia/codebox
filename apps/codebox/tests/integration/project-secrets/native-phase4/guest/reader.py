#!/usr/bin/env python3
"""Memory-only stdin consumer for the Phase 4 channel probe (TEST ARTIFACT).

Reads the exact bytes delivered on stdin and prints their SHA256 digest.
Nothing is written to any file, so a successful match proves a value moved
host -> guest -> process memory without touching argv, environ, or disk.
"""
import hashlib
import sys

data = sys.stdin.buffer.read()
print(hashlib.sha256(data).hexdigest())
