#!/usr/bin/env python3
"""Download only project-pinned official darwin/arm64 runtime, verify before extract."""
import hashlib, os, pathlib, subprocess, sys, tarfile
URL='https://github.com/superradcompany/microsandbox/releases/download/v0.7.13/microsandbox-darwin-aarch64.tar.gz'
SHA='5eb53ca1d6beb36caa64d19602284b9ce37872eff340a8db0542ab91b5808c96'
def install(directory):
    root=pathlib.Path(directory);root.mkdir(mode=0o700)
    archive=root/'official.tar.gz'
    subprocess.run(['curl','--fail','--location','--silent','--show-error','--max-time','180',URL,'-o',str(archive)],check=True,timeout=190)
    if hashlib.sha256(archive.read_bytes()).hexdigest()!=SHA: raise RuntimeError('runtime archive SHA256 mismatch')
    with tarfile.open(archive) as tf:
        # Release layout inspected by pinned just-code installer: archive has
        # top-level msb and libkrunfw.5.dylib. Extract only these regular files.
        for name in ('msb','libkrunfw.5.dylib'):
            members=[m for m in tf.getmembers() if pathlib.PurePosixPath(m.name).name==name and m.isfile()]
            if len(members)!=1: raise RuntimeError('ambiguous/missing runtime member '+name)
            data=tf.extractfile(members[0]).read();p=root/name;p.write_bytes(data);p.chmod(0o700 if name=='msb' else 0o600)
    (root/'libkrunfw.dylib').symlink_to('libkrunfw.5.dylib')
    version=subprocess.run([str(root/'msb'),'--version'],capture_output=True,text=True,check=True,timeout=15).stdout.strip()
    if version!='msb 0.7.13': raise RuntimeError('runtime version mismatch')
    print('META runtime_archive_sha256='+SHA+' runtime_version=0.7.13',flush=True)
if __name__=='__main__': install(sys.argv[1])
