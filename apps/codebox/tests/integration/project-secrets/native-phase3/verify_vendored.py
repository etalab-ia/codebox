#!/usr/bin/env python3
"""Verify the vendored just-code sources are byte-identical to the pinned
commit, with only the package clause renamed.

The kit's qualification rests on testing the REAL renderer/planner/publisher,
not a reimplementation: jcvendor/project_secret_guest_files.go and
jcvendor/project_secret_bindings.go must match
internal/justcode/<name> at SOURCE_COMMIT exactly, except the first line
(`package justcode` upstream, `package jcvendor` in the kit).

This check fetches the upstream files over the network (the run needs the
network anyway for the runtime download and the guest's own packages) and
aborts the run on any drift. It never writes outside the kit directory.
"""
import pathlib, sys, urllib.request

SOURCE_COMMIT = "f483a8003d7745dfe3cdea7363d5f366af51eebc"
FILES = ("project_secret_guest_files.go", "project_secret_bindings.go")
HERE = pathlib.Path(__file__).resolve().parent


def fetch(name):
    url = (
        "https://raw.githubusercontent.com/etalab-ia/just-code/"
        + SOURCE_COMMIT + "/internal/justcode/" + name
    )
    with urllib.request.urlopen(url, timeout=30) as response:
        return response.read().decode("utf-8")


def main():
    for name in FILES:
        upstream = fetch(name)
        vendored = (HERE / "jcvendor" / name).read_text(encoding="utf-8")
        upstream_lines = upstream.splitlines(keepends=True)
        vendored_lines = vendored.splitlines(keepends=True)
        if upstream_lines[0].rstrip("\n") != "package justcode":
            raise SystemExit("upstream first line changed shape for " + name + ": " + upstream_lines[0])
        if vendored_lines[0].rstrip("\n") != "package jcvendor":
            raise SystemExit("vendored first line is not the renamed package clause for " + name)
        if upstream_lines[1:] != vendored_lines[1:]:
            raise SystemExit(
                "VENDORED DRIFT in " + name + ": the kit no longer matches just-code@"
                + SOURCE_COMMIT + " beyond the package clause; re-vendor or re-qualify"
            )
        print("VENDORED-OK " + name + " == just-code@" + SOURCE_COMMIT[:12] + " (package clause renamed only)", flush=True)


if __name__ == "__main__":
    sys.exit(main())
