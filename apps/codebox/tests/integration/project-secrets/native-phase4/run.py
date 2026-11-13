#!/usr/bin/env python3
"""Native runner for the Phase 4 channel probe (TEST RUNNER ONLY).

Preflights the machine (native macOS arm64, go + curl on PATH), downloads
the pinned official Microsandbox 0.7.6 runtime into the run's own
directory, builds and runs the Go probe with a hard wall-clock deadline,
filters the report into results.txt, and cleans up everything it created —
on success, on failure, and on signals.

This kit binds NO network ports, starts NO lab, and needs NO --ip flag:
the guest tooling is stdlib-only and every assertion is local to the
host/guest pair. Nothing here touches ~/.microsandbox, an installed msb,
the host trust store, host DNS, or host interfaces; the runner proves the
first of those by fingerprinting ~/.microsandbox before and after.
"""
import os, pathlib, re, shutil, signal, subprocess, sys, tempfile

HERE = pathlib.Path(__file__).resolve().parent
# The kit revision is matched generically: pinning it here once cost a silent
# drop of this whole line when the probe moved to the next revision, which left
# the recorded evidence unbound to the source commit it claims to qualify.
META_PROBE = re.compile(r'^META kit=p4v[0-9]+ source=[0-9a-f]{40} sdk=0\.7\.6 runtime=0\.7\.6 archive_sha256=[0-9a-f]{64} go=go[0-9.]+$')
META_RUNTIME = re.compile(r'^META runtime_archive_sha256=[0-9a-f]{64} runtime_version=0\.7\.6$')
CASE = re.compile(r'^(PASS|FAIL) [a-z0-9-]+$')
PROXY_NOISE = re.compile(r'(?i)(proxy-authorization|authorization|bearer|basic)')
RUNNER_WALL_SECONDS = 1500  # hard ceiling: probe ctx is 20 min, VM max is 16 min

# results.txt may contain exactly two long hex tokens, both in the META
# lines: the source commit and the runtime archive digest. A canary is 48
# lowercase hex characters, so any other long hex run is a leak into the
# recorded evidence and fails the run.
HEX_TOKEN = re.compile(r'\b[0-9a-f]{40,}\b')
ALLOWED_HEX = {
    'bd689185cc8519ff23e5ce4b0c520c621523f43b',
    'a4f1722fee6460d4e2813e62b60dae4c092021d8120b7aaa9c2f4b5679d97d89',
}


def fail(msg):
    print('FAIL runner-preflight ' + msg, file=sys.stderr)
    sys.exit(2)


def host_state_fingerprint(home):
    """A cheap recursive fingerprint of ~/.microsandbox: names, sizes and
    mtimes. If the pinned runtime so much as breathes on the tester's real
    state, the run fails — the probe runs with MSB_HOME pointed at its own
    throwaway directory precisely so this fingerprint must not change."""
    out = []
    if not home.exists():
        return out
    for root, dirs, files in os.walk(home):
        dirs.sort()
        for name in sorted(files):
            p = pathlib.Path(root) / name
            try:
                st = p.stat()
            except OSError:
                continue
            out.append((str(p.relative_to(home)), st.st_size, int(st.st_mtime)))
    return out


def main():
    # --- preflight --------------------------------------------------------
    if sys.platform != 'darwin' or os.uname().machine != 'arm64':
        fail('this qualification requires native macOS arm64')
    for tool in ('go', 'curl'):
        if shutil.which(tool) is None:
            fail(tool + ' not found on PATH')
    home = pathlib.Path.home() / '.microsandbox'
    before = host_state_fingerprint(home)

    run_dir = pathlib.Path(tempfile.mkdtemp(prefix='jcp4-', dir='/tmp'))
    os.chmod(run_dir, 0o700)
    msb_home = run_dir / 'msb-home'
    msb_home.mkdir()
    results = []
    procs = []
    probe_bin = run_dir / 'probe'
    env = dict(os.environ)
    env['MSB_HOME'] = str(msb_home)
    env['MSB_PATH'] = str(run_dir / 'runtime' / 'msb')

    def cleanup():
        for proc in procs:
            if proc.poll() is None:
                proc.terminate()
        for proc in procs:
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
        # Remove ONLY our own VMs and snapshot, identified by the name files
        # we wrote (ownership prefix checked again inside the probe). The
        # probe binary may not exist yet on an early failure (runtime
        # download, build): guard or the exception masks the real error and
        # skips the directory removal below.
        if probe_bin.exists():
            try:
                subprocess.run([str(probe_bin), '-cleanup', '-dir', str(run_dir)], env=env,
                               capture_output=True, timeout=90)
            except (OSError, subprocess.SubprocessError):
                pass
        shutil.rmtree(run_dir, ignore_errors=True)

    def on_signal(signum, _frame):
        cleanup()
        sys.exit(128 + signum)

    signal.signal(signal.SIGINT, on_signal)
    signal.signal(signal.SIGTERM, on_signal)

    ok = False
    try:
        # --- pinned runtime into our own directory ------------------------
        dl = subprocess.run([sys.executable, str(HERE / 'runtime-download.py'), str(run_dir / 'runtime')],
                            capture_output=True, text=True, timeout=240)
        if dl.returncode != 0:
            fail('runtime download/verification failed: ' + (dl.stderr or dl.stdout).strip()[:400])
        results.extend(l for l in dl.stdout.splitlines() if META_RUNTIME.match(l))

        # --- probe: build once, then run the binary -----------------------
        build = subprocess.run(['go', 'build', '-o', str(probe_bin), '.'], cwd=HERE, env=env,
                               capture_output=True, text=True, timeout=300)
        if build.returncode != 0:
            fail('probe build failed: ' + (build.stderr or '')[:400])
        probe = subprocess.Popen([str(probe_bin), '-dir', str(run_dir)],
                                 cwd=HERE, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        procs.append(probe)
        try:
            out, err = probe.communicate(timeout=RUNNER_WALL_SECONDS)
        except subprocess.TimeoutExpired:
            probe.kill()
            out, err = probe.communicate()
            results.append('FAIL runner-deadline')
        for line in out.splitlines():
            if CASE.match(line) or META_PROBE.match(line):
                results.append(line)
            elif PROXY_NOISE.search(line):
                results.append('FAIL unfiltered-sensitive-token')
        if err.strip():
            # Diagnostics go to stderr, already canary-scrubbed by the probe;
            # keep only the DIAGNOSTIC records for evidence.
            for line in err.splitlines():
                if line.startswith('DIAGNOSTIC '):
                    results.append(line)

        # --- host-state budget: ~/.microsandbox untouched -------------------
        after = host_state_fingerprint(home)
        if before != after:
            results.append('FAIL host-msb-state-touched')

        # --- the run must be bound to what actually ran --------------------
        # Without the probe's own META line the transcript names no source
        # commit and no kit revision: a run of a different probe would be
        # indistinguishable from this one.
        if not any(META_PROBE.match(r) for r in results):
            results.append('FAIL probe-meta-line-missing')

        # --- the recorded evidence must not contain a secret ---------------
        # Case names are fixed literals and diagnostics are probe-scrubbed;
        # this is the independent runner-side check that no 40+ hex run other
        # than the two pinned constants reached the transcript.
        report_so_far = '\n'.join(results)
        for token in HEX_TOKEN.findall(report_so_far):
            if token not in ALLOWED_HEX:
                results.append('FAIL results-sensitive-token')
                break

        ok = probe.returncode == 0 and all(not r.startswith('FAIL') for r in results)
    finally:
        report = '\n'.join(results) + '\n'
        (HERE / 'results.txt').write_text(report)
        sys.stdout.write(report)
        cleanup()
    if not ok:
        sys.exit(1)


if __name__ == '__main__':
    main()
