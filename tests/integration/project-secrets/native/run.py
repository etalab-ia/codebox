#!/usr/bin/env python3
"""Native-only orchestrator. All runtime, CA, state and logs are temporary."""
import argparse, ipaddress, os, pathlib, platform, re, shutil, signal, socket, subprocess, sys, tempfile, time

HERE=pathlib.Path(__file__).resolve().parent

def main():
    p=argparse.ArgumentParser();p.add_argument('--ip',required=True);p.add_argument('--tls',type=int,default=18443);p.add_argument('--dns',type=int,default=15354);a=p.parse_args()
    ipaddress.IPv4Address(a.ip)
    if platform.system()!='Darwin' or platform.machine()!='arm64': p.error('native darwin/arm64 required; no Cloud native pass')
    for cmd in ('go','curl','openssl'):
        if not shutil.which(cmd): p.error('missing '+cmd)
    if not 1024<a.tls<65536 or not 1024<a.dns<65536: p.error('ports must be nonprivileged')
    # Reject occupied ports without touching their owners. The lab binds again
    # without reuse and is the final authority if another process wins the race.
    for kind,port in ((socket.SOCK_STREAM,a.tls),(socket.SOCK_DGRAM,a.dns)):
        with socket.socket(socket.AF_INET,kind) as s: s.bind(('0.0.0.0',port))
    # Require caller-specified LAN address actually assigned to this host.
    with socket.socket(socket.AF_INET,socket.SOCK_STREAM) as s: s.bind((a.ip,0))
    # macOS runtime Unix sockets have a <104-byte pathname limit. Ignore
    # the default long /var/folders TMPDIR; keep the owned state root short.
    directory=pathlib.Path(tempfile.mkdtemp(dir='/tmp',prefix='jcp0-'));directory.chmod(0o700)
    if len(os.fsencode(str(directory/'msb-home'))) > 40:
        raise RuntimeError('owned MSB_HOME exceeds conservative short-path budget')
    print('RUN_DIR='+str(directory),flush=True)
    env=os.environ.copy();env.update(MSB_HOME=str(directory/'msb-home'),MSB_PATH=str(directory/'runtime'/'msb'),MSB_LIBKRUNFW_PATH=str(directory/'runtime'/'libkrunfw.5.dylib'))
    (directory/'msb-home').mkdir(mode=0o700)
    lab=probe=None;rc=1
    def interrupted(sig,frame): raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM,interrupted)
    try:
        subprocess.run([sys.executable,str(HERE/'runtime-download.py'),str(directory/'runtime')],check=True,timeout=220,env=env)
        # Temp CA, never security add-trusted-cert or host trust mutation.
        (directory/'ca.cnf').write_text('[req]\ndistinguished_name=dn\nx509_extensions=ca_ext\n[dn]\nCN=Codebox Phase0 Temporary CA\n[ca_ext]\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\n')
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(directory/'ca.key'),'-out',str(directory/'ca.crt'),'-subj','/CN=Codebox Phase0 Temporary CA','-days','1','-config',str(directory/'ca.cnf')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=30)
        subprocess.run(['openssl','req','-newkey','rsa:2048','-nodes','-keyout',str(directory/'srv.key'),'-out',str(directory/'srv.csr'),'-subj','/CN=p02lab.test'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=30)
        (directory/'srv.ext').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:p02lab.test,DNS:evil.test,IP:'+a.ip+'\n')
        subprocess.run(['openssl','x509','-req','-in',str(directory/'srv.csr'),'-CA',str(directory/'ca.crt'),'-CAkey',str(directory/'ca.key'),'-CAcreateserial','-out',str(directory/'srv.crt'),'-days','1','-extfile',str(directory/'srv.ext')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=30)
        with open(directory/'lab.log','w') as log:
            lab=subprocess.Popen([sys.executable,str(HERE/'lab.py'),'--dir',str(directory),'--ip',a.ip,'--tls',str(a.tls),'--dns',str(a.dns)],stdout=log,stderr=log,start_new_session=True)
        deadline=time.monotonic()+10
        while not (directory/'lab-ready').exists():
            if lab.poll() is not None or time.monotonic()>deadline: raise RuntimeError('lab port bind/start failed; see lab.log')
            time.sleep(.1)
        # Build time/downloads have their own deadline, VM not started yet.
        subprocess.run(['go','build','-o',str(directory/'probe'),'.'],cwd=HERE,env=env,check=True,timeout=240)
        with open(directory/'native-private.log','w') as err, open(directory/'native-stdout-private.log','w') as out:
            # Raw SDK diagnostics are private temporary data, not a report.
            probe=subprocess.Popen(['go','run','.','--dir',str(directory),'--ip',a.ip,'--tls',str(a.tls),'--dns',str(a.dns)],cwd=HERE,env=env,stdout=out,stderr=err,start_new_session=True)
            try: rc=probe.wait(timeout=540)
            except subprocess.TimeoutExpired: raise RuntimeError('native probe deadline exceeded')
        raw=(directory/'native-stdout-private.log').read_text(errors='replace')
        lines=[s for s in raw.splitlines() if re.fullmatch(r'(?:PASS|FAIL) [a-z0-9-]+',s) or re.fullmatch(r'META kit=v3 source=[0-9a-f]{40} sdk=0\.7\.14 runtime=0\.7\.14 archive_sha256=[0-9a-f]{64} go=go[0-9.]+',s)]
        (directory/'results.txt').write_text('\n'.join(lines)+'\n')
        print((directory/'results.txt').read_text(),end='',flush=True)
    except (Exception,KeyboardInterrupt):
        print('FAIL orchestration (diagnostics retained privately in RUN_DIR)',flush=True)
        rc=1
    finally:
        # Every step here is best-effort: a process that already exited, or one
        # that ignores SIGTERM, must not abort the block before the VM cleanup
        # below. An unguarded killpg raises ProcessLookupError on that race,
        # and an unguarded wait raises TimeoutExpired.
        if probe and probe.poll() is None:
            try:
                os.killpg(probe.pid,signal.SIGTERM)
                try: probe.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(probe.pid,signal.SIGKILL);probe.wait()
            except (ProcessLookupError,subprocess.TimeoutExpired,OSError):
                print('Note: the native probe process could not be stopped cleanly.',flush=True)
        if (directory/'probe').exists() and (directory/'vm-name').exists():
            try:
                c=subprocess.run([str(directory/'probe'),'--dir',str(directory),'--cleanup'],env=env,capture_output=True,text=True,timeout=45)
                with open(directory/'native-private.log','a') as log: log.write(c.stderr)
                print(c.stdout,end='');rc=rc or c.returncode
            except Exception: print('FAIL emergency-cleanup; retain RUN_DIR and use README cleanup command');rc=1
        if lab and lab.poll() is None:
            try:
                os.killpg(lab.pid,signal.SIGTERM);lab.wait(timeout=10)
            except (ProcessLookupError,subprocess.TimeoutExpired,OSError):
                print('Note: the lab process could not be stopped cleanly; stop only the lab.py PID whose --dir is this RUN_DIR.',flush=True)
        print('Retained private RUN_DIR. Copy results.txt only; cleanup command is in README.',flush=True)
    return rc
if __name__=='__main__': sys.exit(main())
