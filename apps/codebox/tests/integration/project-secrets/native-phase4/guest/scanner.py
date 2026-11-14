#!/usr/bin/env python3
"""In-guest leak scanner for the Phase 4 channel probe (TEST ARTIFACT ONLY).

Usage: python3 scan.py <patterns-file>

The patterns file is a JSON document {"patterns": {"A": "<hex>", ...}}
delivered onto the guest tmpfs by the probe. The pattern VALUES never pass
through this process's argv or environment, so the scanner cannot match
itself. It scans, for every pattern:

  - /proc/<pid>/cmdline of every live process (NUL-flattened)
  - /proc/<pid>/environ of every live process (NUL-flattened)
  - regular files under /var/log (bounded: <= 8 MiB each, 64 MiB total)

Prints one JSON line per pattern naming only WHERE it matched:

  {"pattern": "A", "cmdline": [123], "environ": [456], "files": ["/var/log/x"]}

The pattern value is never printed. Processes may exit between listing and
reading; those races are ignored. This process runs as root, so /proc access
is not permission-limited inside the guest.
"""
import json
import os
import sys

MAX_SPECIAL = 1 << 20          # cap for cmdline/environ reads
MAX_FILE = 8 << 20             # cap per /var/log file
MAX_TOTAL = 64 << 20           # cap for the whole /var/log walk


def read_capped(path, cap):
    try:
        with open(path, "rb") as fh:
            return fh.read(cap + 1)
    except OSError:
        return None


def scan_proc(patterns):
    hits = {name: {"cmdline": [], "environ": []} for name in patterns}
    needles = {name: value.encode() for name, value in patterns.items()}
    try:
        pids = [p for p in os.listdir("/proc") if p.isdigit()]
    except OSError:
        return hits
    for pid in pids:
        for special, key in (("cmdline", "cmdline"), ("environ", "environ")):
            data = read_capped("/proc/%s/%s" % (pid, special), MAX_SPECIAL)
            if not data:
                continue
            flat = data.replace(b"\x00", b" ")
            for name, needle in needles.items():
                if needle in flat:
                    hits[name][key].append(int(pid))
    return hits


def scan_varlog(patterns):
    hits = {name: [] for name in patterns}
    needles = {name: value.encode() for name, value in patterns.items()}
    total = 0
    for root, dirs, files in os.walk("/var/log"):
        dirs.sort()
        for name in sorted(files):
            path = os.path.join(root, name)
            try:
                st = os.lstat(path)
            except OSError:
                continue
            if not os.path.isfile(path) or st.st_size > MAX_FILE:
                continue
            if total >= MAX_TOTAL:
                return hits
            total += st.st_size
            data = read_capped(path, MAX_FILE)
            if not data:
                continue
            for pat, needle in needles.items():
                if needle in data:
                    hits[pat].append(path)
    return hits


def main():
    if len(sys.argv) != 2:
        print(json.dumps({"error": "usage: scan.py <patterns-file>"}))
        return 2
    try:
        with open(sys.argv[1], "r") as fh:
            doc = json.load(fh)
        patterns = doc["patterns"]
        assert isinstance(patterns, dict) and patterns
        for name, value in patterns.items():
            assert isinstance(name, str) and isinstance(value, str) and value
    except (OSError, ValueError, KeyError, AssertionError):
        print(json.dumps({"error": "bad-pattern-document"}))
        return 2
    proc = scan_proc(patterns)
    files = scan_varlog(patterns)
    for name in sorted(patterns):
        print(json.dumps({
            "pattern": name,
            "cmdline": sorted(proc[name]["cmdline"]),
            "environ": sorted(proc[name]["environ"]),
            "files": files[name],
        }))
    return 0


if __name__ == "__main__":
    sys.exit(main())
