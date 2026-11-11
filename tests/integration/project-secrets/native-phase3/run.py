#!/usr/bin/env python3
"""Native runner for the Phase 3 qualification kit (TEST RUNNER ONLY).

Preflights the machine, downloads the pinned official Microsandbox 0.7.6
runtime into the run's own directory, creates a one-day temporary CA (never
installed anywhere), starts the own-port DNS/TLS lab, runs the Go probe with
a hard wall-clock deadline, filters the report into results.txt, and cleans
up everything it created — on success, on failure, and on signals.

Nothing here touches ~/.microsandbox, an installed msb, the host trust
store, host DNS, or host interfaces. The only network listeners it starts
are the lab's two own ports (LAN-visible by design; see README).
"""
import argparse, ipaddress, os, pathlib, re, shutil, signal, socket, subprocess, sys, tempfile, time

HERE = pathlib.Path(__file__).resolve().parent
# The kit revision is matched generically: pinning it here once cost a silent
# drop of this whole line when the probe moved to the next revision, which left
# the recorded evidence unbound to the source commit it claims to qualify.
META_PROBE = re.compile(r'^META kit=p3v[0-9]+ source=[0-9a-f]{40} sdk=0\.7\.6 runtime=0\.7\.6 archive_sha256=[0-9a-f]{64} go=go[0-9.]+$')
META_RUNTIME = re.compile(r'^META runtime_archive_sha256=[0-9a-f]{64} runtime_version=0\.7\.6$')
CASE = re.compile(r'^(PASS|FAIL) [a-z0-9-]+$')
PROXY_NOISE = re.compile(r'(?i)(proxy-authorization|authorization|bearer|basic)')
RUNNER_WALL_SECONDS = 900  # hard ceiling: probe ctx is 14 min, VM max is 12 min


def fail(msg):
    print('FAIL runner-preflight ' + msg, file=sys.stderr)
    sys.exit(2)


def port_free(port, kind):
    s = socket.socket(socket.AF_INET, kind)
    try:
        s.bind(('0.0.0.0', port))
        return True
    except OSError:
        return False
    finally:
        s.close()


def ip_is_local(ip):
    try:
        out = subprocess.run(['/sbin/ifconfig'], capture_output=True, text=True, timeout=10).stdout
    except (OSError, subprocess.TimeoutExpired):
        return False
    return re.search(r'inet ' + re.escape(ip) + r'\b', out) is not None


def host_state_fingerprint(home):
    """A cheap recursive fingerprint of ~/.microsandbox: names, sizes and
    mtimes. A top-level listing would miss a runtime mutating inside its own
    subdirectories, which is exactly how the managed runtime stores state."""
    if not home.is_dir():
        return None
    out = []
    for root, dirs, files in os.walk(home):
        dirs.sort()
        for name in sorted(dirs) + sorted(files):
            path = os.path.join(root, name)
            try:
                st = os.lstat(path)
            except OSError:
                out.append((path, 'unreadable'))
                continue
            out.append((os.path.relpath(path, home), st.st_size, int(st.st_mtime)))
    return out


def default_dns_upstream():
    # Read the host's configured resolver to relay guest package queries.
    # Reading /etc/resolv.conf never modifies host DNS.
    try:
        for line in pathlib.Path('/etc/resolv.conf').read_text().splitlines():
            parts = line.split()
            if len(parts) == 2 and parts[0] == 'nameserver':
                return parts[1]
    except OSError:
        pass
    return ''


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--ip', required=True, help='LAN IPv4 of THIS machine, e.g. 192.168.1.42')
    p.add_argument('--tls', type=int, default=19443, help='own TLS port for the lab (LAN-visible during the run)')
    p.add_argument('--dns', type=int, default=15356, help='own DNS port for the lab (LAN-visible during the run)')
    p.add_argument('--dns-upstream', default=None, help='resolver for guest package fetches (default: first host nameserver, else 1.1.1.1)')
    a = p.parse_args()

    # --- preflight -------------------------------------------------------
    if sys.platform != 'darwin' or os.uname().machine != 'arm64':
        fail('this qualification requires native macOS arm64')
    try:
        ipaddress.IPv4Address(a.ip)
    except ValueError:
        fail('--ip must be a LAN IPv4 address')
    if ipaddress.IPv4Address(a.ip).is_loopback:
        fail('--ip must be the LAN address, not loopback: the VM must reach this machine')
    if not ip_is_local(a.ip):
        fail('--ip ' + a.ip + ' is not assigned to any interface on this machine')
    for tool in ('go', 'curl', 'openssl'):
        if shutil.which(tool) is None:
            fail(tool + ' not found on PATH')
    for port, kind in ((a.tls, socket.SOCK_STREAM), (a.dns, socket.SOCK_DGRAM)):
        if not port_free(port, kind):
            fail('port ' + str(port) + ' is already in use; refusing to disturb its owner')
    upstream = a.dns_upstream or default_dns_upstream() or '1.1.1.1'
    try:
        ipaddress.IPv4Address(upstream)
    except ValueError:
        fail('--dns-upstream must be an IPv4 resolver address')
    home = pathlib.Path.home() / '.microsandbox'
    before = host_state_fingerprint(home)

    # The vendored codebox sources must match the pinned commit byte for
    # byte (package clause aside) or the run qualifies nothing.
    v = subprocess.run([sys.executable, str(HERE / 'verify_vendored.py')], capture_output=True, text=True)
    if v.returncode != 0:
        fail('verify_vendored.py failed: ' + (v.stderr or v.stdout).strip()[:400])
    print(v.stdout.strip())

    run_dir = pathlib.Path(tempfile.mkdtemp(prefix='jcp3-', dir='/tmp'))
    os.chmod(run_dir, 0o700)
    msb_home = run_dir / 'msb-home'
    msb_home.mkdir()
    results = []
    procs = []
    probe_bin = run_dir / 'probe'
    env = dict(os.environ)
    env['MSB_HOME'] = str(msb_home)
    env['MSB_PATH'] = str(run_dir / 'runtime' / 'msb')
    # The guest resolves ONLY the lab DNS; public names are relayed to the
    # upstream resolver for the guest's own package installs.
    env['P3_DNS_UPSTREAM'] = upstream

    def cleanup():
        for proc in procs:
            if proc.poll() is None:
                proc.terminate()
        for proc in procs:
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
        # Remove ONLY our own VM, identified by the name file we wrote.
        # The probe binary may not exist yet on an early failure (runtime
        # download, openssl, lab startup, build): an unguarded call would raise
        # FileNotFoundError, mask the real error, and skip the directory
        # removal below — leaving the throwaway CA key and runtime behind.
        if probe_bin.exists():
            try:
                subprocess.run([str(probe_bin), '-cleanup', '-dir', str(run_dir)], env=env,
                               capture_output=True, timeout=60)
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

        # --- one-day temporary CA + server certificate --------------------
        openssl = shutil.which('openssl')
        ca_key, ca_crt = run_dir / 'ca.key', run_dir / 'ca.crt'
        srv_key, srv_csr, srv_crt = run_dir / 'srv.key', run_dir / 'srv.csr', run_dir / 'srv.crt'
        ext = run_dir / 'srv.ext'
        ext.write_text('subjectAltName=DNS:p3lab.test,DNS:evil.test,IP:' + a.ip + '\n')
        def ossl(*args):
            subprocess.run([openssl] + list(args), check=True, capture_output=True, timeout=30)
        ossl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
             '-subj', '/CN=Codebox Phase3 Temporary CA', '-keyout', str(ca_key), '-out', str(ca_crt))
        ossl('req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=p3lab.test',
             '-keyout', str(srv_key), '-out', str(srv_csr))
        ossl('x509', '-req', '-in', str(srv_csr), '-CA', str(ca_crt), '-CAkey', str(ca_key),
             '-CAcreateserial', '-days', '1', '-extfile', str(ext), '-out', str(srv_crt))
        os.chmod(ca_key, 0o600)
        os.chmod(srv_key, 0o600)

        # --- lab ------------------------------------------------------------
        lab = subprocess.Popen([sys.executable, str(HERE / 'lab.py'), '--dir', str(run_dir),
                                '--ip', a.ip, '--tls', str(a.tls), '--dns', str(a.dns),
                                '--dns-upstream', upstream],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        procs.append(lab)
        deadline = time.time() + 20
        while not (run_dir / 'lab-ready').exists():
            if lab.poll() is not None:
                fail('lab exited during startup')
            if time.time() > deadline:
                fail('lab did not become ready in 20s')
            time.sleep(0.1)

        # --- probe: build once (for emergency cleanup), then go run --------
        build = subprocess.run(['go', 'build', '-o', str(probe_bin), '.'], cwd=HERE, env=env,
                               capture_output=True, text=True, timeout=300)
        if build.returncode != 0:
            fail('probe build failed: ' + (build.stderr or '')[:400])
        probe = subprocess.Popen(['go', 'run', '.', '-dir', str(run_dir), '-ip', a.ip,
                                  '-tls', str(a.tls), '-dns', str(a.dns)],
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
